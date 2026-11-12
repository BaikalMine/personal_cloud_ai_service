[CmdletBinding()]
param([switch]$Execute,[Parameter(Mandatory)][ValidatePattern('^sha256:[a-f0-9]{64}$')][string]$ImageId)
$ErrorActionPreference = 'Stop'
if (!$Execute) { throw 'Explicit -Execute is required.' }
$root = 'C:/ai-access-gateway'
$override = Join-Path $root 'docker-compose.override.yml'
$baseline = 'sha256:0105ffa0dec778c217269d957378459af5bcf87f17a4425a064f3147a9f7101b'
$app = 'ai-access-gateway'
$protected = @('ai-gateway-postgres','ai-gateway-harness','ai-gateway-harness-users','ai-gateway-harness-ultra','ai-gateway-harness-retention','ai-content-moderator')
function Docker([string[]]$Arguments) {
    $out = & docker.exe @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Docker operation failed: $($Arguments[0])" }
    return ($out -join "`n")
}
function AssertIdle {
    $sql = "SELECT (SELECT count(*) FROM gpu_work WHERE state NOT IN ('released','cancelled'))+(SELECT count(*) FROM gpu_executor_permits WHERE state<>'closed')+(SELECT count(*) FROM quick_generation_mining_leases)+(SELECT count(*) FROM generation_jobs WHERE state NOT IN ('completed','failed','cancelled','expired'))+(SELECT count(*) FROM prompt_assistant_jobs WHERE state IN ('queued','running'))+(SELECT count(*) FROM gpu_inference_requests WHERE state IN ('queued','running'));"
    if ((Docker @('exec','ai-gateway-postgres','psql','-U','ai_gateway','-d','ai_gateway','-At','-c',$sql)).Trim() -ne '0') { throw 'GPU work active; deployment deferred.' }
}
function WaitHealthy([string]$Expected) {
    $end = [DateTime]::UtcNow.AddMinutes(2)
    do {
        $state = Docker @('inspect','--format','{{.Image}} {{.State.Health.Status}}',$app)
        if ($state -eq "$Expected healthy") { return }
        Start-Sleep -Seconds 2
    } while ([DateTime]::UtcNow -lt $end)
    throw 'Gateway did not become healthy with the expected image.'
}
function AssertNoPendingResume {
    $null = Docker @('exec',$app,'sh','-ec','test -n "$MEDIA_SPOOL_DIR"; test -d "$MEDIA_SPOOL_DIR"; test ! -e "$MEDIA_SPOOL_DIR/manual-mining-resume.json"')
}
$configHash = (Get-FileHash -LiteralPath $override).Hash
$config = Get-Content -Raw -LiteralPath $override | ConvertFrom-Json -AsHashtable
if ($config.services.app.image -ne $baseline -or (Docker @('inspect','--format','{{.Image}} {{.State.Health.Status}}',$app)) -ne "$baseline healthy") { throw 'Unexpected Gateway baseline.' }
if ((Docker @('image','inspect','--format','{{.Id}}',$ImageId)) -ne $ImageId) { throw 'Candidate image missing.' }
AssertIdle
AssertNoPendingResume
$miners = @(Get-CimInstance Win32_Process | Where-Object { $_.Name -match 'miner.*\.exe$' } | Select-Object Name,ProcessId,CreationDate)
if ($miners.Count -eq 0) { throw 'No running miner was found to verify.' }
$protectedBefore = Docker (@('inspect','--format','{{.Id}} {{.State.StartedAt}}') + $protected)
$backup = Join-Path $root ('backups/manual-mining-resume-' + [DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss-fff'))
$null = New-Item -ItemType Directory -Path $backup
$acl = [Security.AccessControl.DirectorySecurity]::new()
$acl.SetAccessRuleProtection($true, $false)
foreach ($sid in @([Security.Principal.WindowsIdentity]::GetCurrent().User.Value,'S-1-5-18','S-1-5-32-544')) {
    $rule = [Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($sid),'FullControl','ContainerInherit,ObjectInherit','None','Allow')
    $acl.AddAccessRule($rule)
}
Set-Acl -LiteralPath $backup -AclObject $acl
$old = Join-Path $backup 'docker-compose.override.yml'
Copy-Item -LiteralPath $override -Destination $old
$next = Join-Path $backup 'next.json'
$config.services.app.image = $ImageId
[IO.File]::WriteAllText($next, ($config | ConvertTo-Json -Depth 100), [Text.UTF8Encoding]::new($false))
$changed = $false
$recreated = $false
try {
    if ((Get-FileHash -LiteralPath $override).Hash -ne $configHash) { throw 'Compose configuration changed concurrently.' }
    [IO.File]::Replace($next,$override,[NullString]::Value)
    $changed = $true
    $null = Docker @('compose','--project-directory',$root,'config','-q')
    AssertIdle
    AssertNoPendingResume
    $recreated = $true
    $null = Docker @('compose','--project-directory',$root,'up','-d','--no-deps','--pull','never','app')
    WaitHealthy $ImageId
    $worker = & docker.exe logs $app 2>&1 | Select-String -SimpleMatch 'manual mining auto-resume enabled; idle check interval=10m'
    if ($LASTEXITCODE -ne 0 -or !$worker) { throw 'Auto-resume worker did not report being enabled.' }
    AssertNoPendingResume
    if ((Docker (@('inspect','--format','{{.Id}} {{.State.StartedAt}}') + $protected)) -cne $protectedBefore) { throw 'An unrelated service changed.' }
    foreach ($miner in $miners) {
        $now = Get-CimInstance Win32_Process -Filter "ProcessId = $($miner.ProcessId)"
        if (!$now -or $now.CreationDate -ne $miner.CreationDate -or $now.Name -ne $miner.Name) { throw 'An existing miner process changed.' }
        Write-Output "Unchanged miner: $($miner.Name) PID $($miner.ProcessId)"
    }
    Write-Output "Gateway healthy; auto-resume enabled; no pending restart. Backup: $backup"
} catch {
    if ($changed) {
        $restore = Join-Path $backup 'restore.json'
        Copy-Item -LiteralPath $old -Destination $restore
        [IO.File]::Replace($restore,$override,[NullString]::Value)
        if ($recreated) {
            $null = Docker @('compose','--project-directory',$root,'up','-d','--no-deps','--pull','never','app')
            WaitHealthy $baseline
        }
    }
    throw
}
