param([string] $TrainingBackupTool)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'training-backup-common.ps1')

foreach ($name in @('backup.ps1', 'restore.ps1', 'training-backup-common.ps1', 'test-training-backup.ps1')) {
    $tokens = $null
    $errors = $null
    [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot $name), [ref] $tokens, [ref] $errors) | Out-Null
    if (@($errors).Count -ne 0) { throw "Syntax error in ${name}: $($errors[0].Message)" }
}

$source = @'
{
 "jobs":[{"gateway_id":"gateway-001","agent_id":"agent-001","owner":"alice","artifact":"Trained/Krea2/alice/portrait.safetensors","profile_id":"raw"}],
 "files":[{"path":"Trained/Krea2/alice/portrait.safetensors","bytes":12,"sha256":"fixture-hash"}]
}
'@
$rowSource = @'
{"gateway_id":"gateway-001","agent_id":"agent-001","owner":"alice","profile_id":"raw","user_id":17,"artifact_name":"Trained\\Krea2\\alice\\portrait.safetensors","artifact_bytes":12}
'@
$manifest = $source | ConvertFrom-Json
$row = $rowSource | ConvertFrom-Json
$result = @(Assert-TrainingBackupInventory $manifest @($row))
if ($result.Count -ne 1 -or $result[0].UserID -ne 17 -or $result[0].SHA256 -ne 'fixture-hash') {
    throw 'Owner/file linkage was not preserved.'
}
foreach ($mode in @('missing', 'agent', 'owner', 'profile', 'size', 'name', 'file', 'duplicate')) {
    $manifest = $source | ConvertFrom-Json
    $row = $rowSource | ConvertFrom-Json
    switch ($mode) {
        missing { $row.gateway_id = 'absent' }
        agent { $row.agent_id = 'wrong' }
        owner { $row.owner = 'Alice' }
        profile { $row.profile_id = 'wrong' }
        size { $row.artifact_bytes = 11 }
        name { $row.artifact_name = 'wrong.safetensors' }
        file { $manifest.files = @() }
        duplicate { $manifest.jobs = @($manifest.jobs[0], $manifest.jobs[0]) }
    }
    $failed = $false
    try { Assert-TrainingBackupInventory $manifest @($row) | Out-Null } catch { $failed = $true }
    if (-not $failed) { throw "Invalid training inventory accepted: $mode" }
}
$empty = '{"jobs":null,"files":null}' | ConvertFrom-Json
if (@(Assert-TrainingBackupInventory $empty @()).Count -ne 0) { throw 'Empty backup was not handled.' }
Write-Host 'Training backup PowerShell contracts passed: valid linkage, eight mismatch cases, empty set and syntax.'

if ([string]::IsNullOrWhiteSpace($TrainingBackupTool)) { return }
$TrainingBackupTool = [IO.Path]::GetFullPath($TrainingBackupTool)
$testRoot = [IO.Path]::GetFullPath((Join-Path ([IO.Path]::GetTempPath()) ('gateway-training-backup-test-' + [guid]::NewGuid().ToString('N'))))
$utf8 = [Text.UTF8Encoding]::new($false)
New-Item -ItemType Directory -Path $testRoot | Out-Null
try {
    $artifact = Join-Path $testRoot 'source/Trained/Krea2/alice/portrait.safetensors'
    $statusPath = Join-Path $testRoot 'source/jobs/agent-001/status.json'
    New-Item -ItemType Directory -Path ([IO.Path]::GetDirectoryName($artifact)), ([IO.Path]::GetDirectoryName($statusPath)) | Out-Null
    [IO.File]::WriteAllText($artifact, 'trained LoRA', $utf8)
    $model = Join-Path $testRoot 'base-model.fixture'
    [IO.File]::WriteAllText($model, 'separate model inventory', $utf8)
    $profile = @{ id = 'raw'; family = 'krea2'; name = 'Krea'; base_model = 'fixture'; dit = $model; vae = $model; text_encoder = $model }
    $config = @{
        token = 'fixture-backup-agent-token-000000000000000'
        root_directory = (Join-Path $testRoot 'source')
        comfyui_lora_directory = (Join-Path $testRoot 'source')
        python_executable = $model
        profiles = @($profile)
    }
    $status = @{
        spec = @{ gateway_job_id = 'gateway-001'; profile_id = 'raw'; owner = 'alice' }
        status = @{ id = 'agent-001'; gateway_job_id = 'gateway-001'; profile_id = 'raw'; state = 'completed'; artifact_name = 'portrait.safetensors'; artifact_bytes = 12 }
        artifact_path = $artifact
        profile_snapshot = $profile
    }
    $configPath = Join-Path $testRoot 'config.json'
    [IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 8), $utf8)
    [IO.File]::WriteAllText($statusPath, ($status | ConvertTo-Json -Depth 8), $utf8)
    $archive = Join-Path $testRoot 'training.zip'
    $exported = Invoke-TrainingBackupTool $TrainingBackupTool @('-mode', 'export', '-config', $configPath, '-archive', $archive, '-dataset-policy', 'database')
    if ($exported.jobs[0].owner -ne 'alice') { throw 'CLI export lost owner.' }

    $backupFiles = @($archive)
    foreach ($name in @('gateway.dump', 'openwebui.tar.gz')) {
        $filename = Join-Path $testRoot $name
        [IO.File]::WriteAllText($filename, 'mock Docker payload; never sent to Docker', $utf8)
        $backupFiles += $filename
    }
    $entries = @($backupFiles | ForEach-Object {
        $item = Get-Item -LiteralPath $_
        @{ Name = $item.Name; Bytes = $item.Length; SHA256 = (Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash.ToLowerInvariant() }
    })
    $parentManifest = @{
        Version = 2; CreatedAt = [DateTime]::UtcNow.ToString('o'); Files = $entries
        Training = @{ Included = $true; Archive = 'training.zip'; DatasetPolicy = 'database' }
    }
    $manifestPath = Join-Path $testRoot 'backup-fixture.json'
    [IO.File]::WriteAllText($manifestPath, ($parentManifest | ConvertTo-Json -Depth 8), $utf8)

    # Exercise the real restore script and CLI, replacing only Docker and HTTP.
    # Unknown external commands fail instead of falling through to a real service.
    $trainingTestState = @{ Missing = $false; Calls = [Collections.Generic.List[string]]::new(); BackupDestination = (Join-Path $testRoot 'backup-set') }
    function docker {
        $global:LASTEXITCODE = 0
        $command = $args -join ' '
        $trainingTestState.Calls.Add($command)
        if ($command.StartsWith('compose ')) {
            if ($command.Contains('ps -q postgres')) { return 'fixture-postgres' }
            if ($command.Contains('ps -q app')) { return 'fixture-app' }
            if ($command.Contains(' config --quiet') -or $command.Contains(' run --rm openwebui-restore') -or
                $command.Contains(' up -d --wait postgres') -or $command.Contains(' up -d --build --wait app') -or
                $command.Contains(' down --volumes --remove-orphans --rmi local')) { return }
        }
        if ($command.StartsWith('cp ') -and $command.Contains('fixture-postgres:/tmp/gateway-restore.dump')) { return }
        if ($command.StartsWith('exec fixture-postgres pg_restore ')) { return }
        if ($command.StartsWith('exec fixture-postgres psql ')) {
            if ($command.Contains('json_agg')) {
                $row = $rowSource | ConvertFrom-Json
                if ($trainingTestState.Missing) { $row.gateway_id = 'missing-from-archive' }
                return ConvertTo-Json -InputObject @($row) -Compress
            }
            return '{"schema_version":65,"users":2,"admins":1,"permanent_users":2,"migrations_contiguous":true,"migration_checksums_valid":true}'
        }
        if ($command -eq 'exec fixture-app /app/gateway -verify-training-datasets') {
            return '{"success":true,"inventory":{"datasets":1,"snapshots":1,"assets":1,"bytes":12}}'
        }
        if ($command -eq 'exec fixture-app test -w /var/lib/gateway-spool' -or $command.StartsWith('exec fixture-app find ')) { return }
        if ($command -eq 'exec ai-gateway-postgres printenv POSTGRES_USER') { return 'fixture-user' }
        if ($command -eq 'exec ai-gateway-postgres printenv POSTGRES_DB') { return 'fixture-db' }
        if ($command.StartsWith('exec ai-gateway-postgres pg_dump ') -or $command.StartsWith('exec ai-gateway-postgres pg_restore --list ') -or
            $command.StartsWith('exec ai-gateway-postgres rm -f /tmp/ai-gateway-')) { return }
        if ($command.StartsWith('cp ai-gateway-postgres:/tmp/')) {
            [IO.File]::WriteAllText([string] $args[-1], 'mock dump', $utf8)
            return
        }
        if ($command -eq 'pause openwebui' -or $command -eq 'unpause openwebui') { return }
        if ($command.StartsWith('run --rm ') -and $command.Contains('tar -czf /backup/')) {
            $index = [Array]::IndexOf($args, '-czf')
            $outputName = [IO.Path]::GetFileName([string] $args[$index + 1])
            [IO.File]::WriteAllText((Join-Path $trainingTestState.BackupDestination $outputName), 'mock webui archive', $utf8)
            return
        }
        if ($command.StartsWith('run --rm ') -and $command.Contains('tar -tzf /backup/')) { return }
        throw "Unexpected Docker call in isolated script test: $command"
    }
    function Invoke-WebRequest {
        param([switch] $UseBasicParsing, [string] $Uri, [int] $TimeoutSec)
        $content = '{}'
        if ($Uri.EndsWith('/readyz')) { $content = '{"ready":true,"required":{"database":{"ready":true}}}' }
        if ($Uri.EndsWith('/login')) { $content = '<form>' }
        if ($Uri.EndsWith('/metrics')) { $content = 'gateway_media_inflight_capacity_bytes 1' }
        return [pscustomobject]@{ StatusCode = 200; Content = $content }
    }
    & (Join-Path $PSScriptRoot 'restore.ps1') -Manifest $manifestPath -TrainingBackupTool $TrainingBackupTool -PublicPort 18092 -AdminPort 18093 | Out-Null
    $reportFile = Get-ChildItem -LiteralPath $testRoot -Filter 'restore-drill-*.json' | Select-Object -First 1
    $report = Get-Content -LiteralPath $reportFile.FullName -Raw | ConvertFrom-Json
    if ($report.Status -ne 'ok' -or @($report.Training.VerifiedJobs).Count -ne 1 -or $report.Training.GatewayDownloadVerified -or
        $report.Training.DatasetIntegrity.assets -ne 1 -or $report.Training.AutomaticJobRecovery -ne 'disabled_for_restore_drill') {
        throw 'Restore script report overclaims or loses database linkage.'
    }
    $trainingTestState.Missing = $true
    $failed = $false
    try {
        & (Join-Path $PSScriptRoot 'restore.ps1') -Manifest $manifestPath -TrainingBackupTool $TrainingBackupTool -PublicPort 18092 -AdminPort 18093 | Out-Null
    } catch {
        $failed = $_.Exception.Message.Contains('missing-from-archive')
    }
    if (-not $failed) { throw 'Missing DB-linked artifact did not fail restore.' }
    if (@(Get-ChildItem -LiteralPath $testRoot -Filter '.training-drill-*' -Force).Count -ne 0) {
        throw 'Restore script left extracted LoRA files behind.'
    }
    $trainingTestState.Missing = $false
    & (Join-Path $PSScriptRoot 'backup.ps1') -Destination $trainingTestState.BackupDestination -TrainingConfig $configPath -TrainingBackupTool $TrainingBackupTool -DatasetPolicy database | Out-Null
    $setFile = Get-ChildItem -LiteralPath $trainingTestState.BackupDestination -Filter 'backup-*.json' | Select-Object -First 1
    $set = Get-Content -LiteralPath $setFile.FullName -Raw | ConvertFrom-Json
    if (-not $set.Training.Included -or $set.Training.DatasetPolicy -ne 'database' -or @($set.Files).Count -ne 3) {
        throw 'Backup omitted training archive/policy.'
    }
    $callsBeforeCorruption = $trainingTestState.Calls.Count
    $bytes = [IO.File]::ReadAllBytes($archive)
    $bytes[0] = $bytes[0] -bxor 1
    [IO.File]::WriteAllBytes($archive, $bytes)
    $failed = $false
    try {
        & (Join-Path $PSScriptRoot 'restore.ps1') -Manifest $manifestPath -TrainingBackupTool $TrainingBackupTool | Out-Null
    } catch {
        $failed = $_.Exception.Message.Contains('SHA-256 mismatch')
    }
    if (-not $failed -or $trainingTestState.Calls.Count -ne $callsBeforeCorruption) {
        throw 'Damaged archive was not rejected before Docker commands.'
    }
    $failureFiles = @(Get-ChildItem -LiteralPath $testRoot -Filter 'restore-drill-*.json' | ForEach-Object {
        Get-Content -LiteralPath $_.FullName -Raw | ConvertFrom-Json
    } | Where-Object { $_.Status -eq 'failed' -and $_.Error.Contains('SHA-256 mismatch') })
    if ($failureFiles.Count -eq 0) { throw 'Corruption was not recorded in a restore report.' }
    Write-Host 'Real Windows CLI, backup and restore orchestration passed; Docker/HTTP were mocked, no GPU services contacted.'
} finally {
    $resolved = [IO.Path]::GetFullPath($testRoot)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar)
    if ([IO.Path]::GetDirectoryName($resolved) -ne $tempRoot -or
        -not [IO.Path]::GetFileName($resolved).StartsWith('gateway-training-backup-test-', [StringComparison]::Ordinal)) {
        throw 'Refusing test cleanup outside its temporary directory.'
    }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
