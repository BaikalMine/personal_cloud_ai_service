param([Parameter(Mandatory)][string]$ImageId)
$ErrorActionPreference = 'Stop'
$root = 'C:\ai-access-gateway'
$override = Join-Path $root 'docker-compose.override.yml'
$backup = Join-Path $root 'backups/assistant-image-20261010-r1'
$image = 'ai-gateway-ready:20261010-assistant-image-r1'
$baseline = 'ai-gateway-ready:20261010-harness-rea-r1'
function Docker([string[]]$Arguments) {
    $result = & docker.exe @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Docker operation failed: $($Arguments[0])" }
    return $result
}
function AssertIdle {
    $sql = "SELECT (SELECT count(*) FROM gpu_work WHERE state NOT IN ('released','cancelled'))+(SELECT count(*) FROM gpu_executor_permits WHERE state<>'closed')+(SELECT count(*) FROM quick_generation_mining_leases);"
    if ((Docker @('exec', 'ai-gateway-postgres', 'psql', '-U', 'ai_gateway', '-d', 'ai_gateway', '-At', '-c', $sql)).Trim() -ne '0') {
        throw 'GPU work active; deployment deferred.'
    }
}
function WaitGateway {
    for ($i = 0; $i -lt 45; $i++) {
        if ((Docker @('inspect', '--format', '{{.State.Health.Status}}', 'ai-access-gateway')).Trim() -eq 'healthy') { return }
        Start-Sleep -Seconds 2
    }
    throw 'Gateway readiness failed.'
}
$overlay = Get-Content -LiteralPath $override -Raw | ConvertFrom-Json
if ($overlay.services.app.image -ne $baseline) { throw 'Production baseline changed.' }
if ((Docker @('inspect', '--format', '{{.Image}}', 'ai-access-gateway')).Trim() -ne
    'sha256:271220d2b08ca4ab9de7751e56f7a9198d5853900c67f055ede41cdef54568cc') { throw 'Running baseline changed.' }
if ((Docker @('image', 'inspect', '--format', '{{.Id}}', $image)).Trim() -ne $ImageId) { throw 'Candidate image changed.' }
AssertIdle
if (Test-Path -LiteralPath $backup) { throw 'Backup already exists.' }
$null = New-Item -ItemType Directory -Path $backup
Set-Acl -LiteralPath $backup -AclObject (Get-Acl -LiteralPath $root)
Copy-Item -LiteralPath $override -Destination (Join-Path $backup 'docker-compose.override.yml')
$protected = @(Get-CimInstance Win32_Process | Where-Object {
    $_.Name -match 'miner|rigel|t-rex|xmrig|ollama' -or $_.Name -in @('python.exe', 'inference-worker.exe')
} | Select-Object ProcessId, CreationDate)
$envHash = (Get-FileHash -LiteralPath (Join-Path $root '.env')).Hash
$harnessId = (Docker @('inspect', '--format', '{{.Id}}', 'ai-gateway-harness')).Trim()
try {
    AssertIdle
    $overlay.services.app.image = $image
    [IO.File]::WriteAllText($override, ($overlay | ConvertTo-Json -Depth 100), [Text.UTF8Encoding]::new($false))
    $null = Docker @('compose', '--project-directory', $root, 'config', '-q')
    AssertIdle
    $null = Docker @('compose', '--project-directory', $root, 'up', '-d', '--no-deps', 'app')
    WaitGateway
} catch {
    $failure = $_
    Copy-Item -LiteralPath (Join-Path $backup 'docker-compose.override.yml') -Destination $override -Force
    $null = Docker @('compose', '--project-directory', $root, 'up', '-d', '--no-deps', 'app')
    throw $failure
}
$now = @(Get-CimInstance Win32_Process | Select-Object ProcessId, CreationDate)
foreach ($process in $protected) {
    if (!($now | Where-Object { $_.ProcessId -eq $process.ProcessId -and $_.CreationDate -eq $process.CreationDate })) {
        throw 'Unrelated host process changed during deployment.'
    }
}
if ((Get-FileHash -LiteralPath (Join-Path $root '.env')).Hash -ne $envHash -or
    (Docker @('inspect', '--format', '{{.Id}}', 'ai-gateway-harness')).Trim() -ne $harnessId) {
    throw 'Unrelated environment or Harness changed.'
}
'Gateway image preparation updated; mining, ComfyUI, Harness and native worker preserved.'
