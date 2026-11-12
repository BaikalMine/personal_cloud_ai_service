param(
    [string] $Manifest,
    [ValidateRange(0, 65535)]
    [int] $PublicPort = 0,
    [ValidateRange(0, 65535)]
    [int] $AdminPort = 0,
    [switch] $KeepEnvironment,
    [string] $TrainingBackupTool
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'training-backup-common.ps1')
if ([string]::IsNullOrWhiteSpace($TrainingBackupTool)) {
    $TrainingBackupTool = Join-Path $projectRoot 'bin/training-backup.exe'
}
$composeFile = Join-Path $projectRoot 'docker-compose.restore.yml'
$defaultBackupDirectory = Join-Path $projectRoot 'backups'
$startedAt = (Get-Date).ToUniversalTime()
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$projectName = "ai-gateway-restore-$PID-$stamp".ToLowerInvariant()
$environmentCreated = $false
$reportPath = $null
$trainingRestorePath = $null
$trainingCreated = $false
$trainingReport = [ordered]@{ Status = 'not_included'; DatasetPolicy = 'database'; VerifiedJobs = @() }

function Assert-NativeSuccess([string] $message) {
    if ($LASTEXITCODE -ne 0) {
        throw $message
    }
}

function Write-RestoreFailureReport([string] $message) {
    [ordered]@{
        Status = 'failed'
        Manifest = [IO.Path]::GetFileName($manifestPath)
        Error = $message
        Training = $trainingReport
        ProjectName = $projectName
    } | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $reportPath -Encoding UTF8
}

function Get-FreeLoopbackPort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return ([Net.IPEndPoint] $listener.LocalEndpoint).Port
    } finally {
        $listener.Stop()
    }
}

function Invoke-RestoreCompose {
    param([string[]] $Arguments)
    & docker compose --file $composeFile --project-directory $projectRoot --project-name $projectName @Arguments
    Assert-NativeSuccess "Restore compose command failed: $($Arguments -join ' ')"
}

function Get-RestoreComposeOutput {
    param([string[]] $Arguments)
    $output = @(& docker compose --file $composeFile --project-directory $projectRoot --project-name $projectName @Arguments)
    Assert-NativeSuccess "Restore compose command failed: $($Arguments -join ' ')"
    return $output
}

function Resolve-BackupEntry([object] $entry, [string] $backupDirectory) {
    $rawName = [string] $entry.Name
    $name = [IO.Path]::GetFileName($rawName)
    if ([string]::IsNullOrWhiteSpace($name) -or $name -ne $rawName) {
        throw "Unsafe backup entry name: $rawName"
    }
    $candidate = [IO.Path]::GetFullPath((Join-Path $backupDirectory $name))
    $root = $backupDirectory.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if (-not $candidate.StartsWith($root, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Backup entry escapes the manifest directory: $rawName"
    }
    if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
        throw "Backup file is missing: $candidate"
    }
    $item = Get-Item -LiteralPath $candidate
    if ([int64] $entry.Bytes -ne $item.Length) {
        throw "Backup size mismatch for ${name}: got $($item.Length), want $($entry.Bytes)"
    }
    $actualHash = (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash.ToLowerInvariant()
    $expectedHash = ([string] $entry.SHA256).Trim().ToLowerInvariant()
    if ($actualHash -ne $expectedHash) {
        throw "Backup SHA-256 mismatch for $name"
    }
    return [pscustomobject]@{
        Name   = $name
        Path   = $candidate
        Bytes  = $item.Length
        SHA256 = $actualHash
    }
}

if (-not (Test-Path -LiteralPath $composeFile -PathType Leaf)) {
    throw "Restore compose file is missing: $composeFile"
}
if ([string]::IsNullOrWhiteSpace($Manifest)) {
    $latest = Get-ChildItem -LiteralPath $defaultBackupDirectory -Filter 'backup-*.json' -File |
        Sort-Object Name -Descending |
        Select-Object -First 1
    if ($null -eq $latest) {
        throw "No backup manifest found under $defaultBackupDirectory"
    }
    $Manifest = $latest.FullName
}
$manifestPath = [IO.Path]::GetFullPath($Manifest)
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw "Backup manifest is missing: $manifestPath"
}
$backupDirectory = [IO.Path]::GetDirectoryName($manifestPath)
$manifestDocument = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
$manifestFiles = @($manifestDocument.Files)
if ($manifestFiles.Count -lt 2) {
    throw 'Backup manifest must contain PostgreSQL and OpenWebUI archives.'
}

Write-Host "Verifying backup files from $manifestPath" -ForegroundColor Cyan
$reportPath = Join-Path $backupDirectory "restore-drill-$stamp.json"
try {
    $verifiedFiles = @($manifestFiles | ForEach-Object { Resolve-BackupEntry $_ $backupDirectory })
} catch {
    Write-RestoreFailureReport $_.Exception.Message
    throw
}
$databaseFiles = @($verifiedFiles | Where-Object { $_.Name.EndsWith('.dump', [StringComparison]::OrdinalIgnoreCase) })
$openWebFiles = @($verifiedFiles | Where-Object { $_.Name.EndsWith('.tar.gz', [StringComparison]::OrdinalIgnoreCase) })
if ($databaseFiles.Count -ne 1 -or $openWebFiles.Count -ne 1) {
    throw 'Backup manifest must contain exactly one .dump and one .tar.gz file.'
}
$databaseFile = $databaseFiles[0]
$openWebFile = $openWebFiles[0]
$trainingFiles = @($verifiedFiles | Where-Object { $_.Name.EndsWith('.zip', [StringComparison]::OrdinalIgnoreCase) })
if ($trainingFiles.Count -gt 1 -or $verifiedFiles.Count -ne (2 + $trainingFiles.Count)) {
    throw 'Unexpected backup files: expected database, OpenWebUI and optionally one training ZIP.'
}
$hasTrainingMetadata = $null -ne $manifestDocument.PSObject.Properties['Training']
if ($hasTrainingMetadata) {
    if ($manifestDocument.Training.DatasetPolicy -ne 'database' -or
        [bool] $manifestDocument.Training.Included -ne ($trainingFiles.Count -eq 1)) {
        throw 'Training backup policy does not match the archive list.'
    }
    if ($trainingFiles.Count -eq 1 -and $manifestDocument.Training.Archive -cne $trainingFiles[0].Name) {
        throw 'Training archive name does not match the backup policy.'
    }
} elseif ($trainingFiles.Count -ne 0) {
    throw 'Training archive requires explicit dataset policy in the backup manifest.'
}
if ($trainingFiles.Count -eq 1 -and -not (Test-Path -LiteralPath $TrainingBackupTool -PathType Leaf)) {
    throw "Training backup tool is missing: $TrainingBackupTool"
}

if ($PublicPort -eq 0) {
    $PublicPort = Get-FreeLoopbackPort
}
if ($AdminPort -eq 0) {
    do {
        $AdminPort = Get-FreeLoopbackPort
    } while ($AdminPort -eq $PublicPort)
}

$managedEnvironment = [ordered]@{
    RESTORE_BACKUP_DIR      = $backupDirectory
    RESTORE_OPENWEB_ARCHIVE = $openWebFile.Name
    RESTORE_PUBLIC_PORT     = [string] $PublicPort
    RESTORE_ADMIN_PORT      = [string] $AdminPort
}
$previousEnvironment = @{}
foreach ($key in $managedEnvironment.Keys) {
    $previousEnvironment[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
    [Environment]::SetEnvironmentVariable($key, $managedEnvironment[$key], 'Process')
}

$preMigrationInventorySQL = @'
SELECT json_build_object(
    'schema_version', COALESCE((SELECT max(version) FROM schema_migrations),0),
    'migration_count', (SELECT count(*) FROM schema_migrations),
    'users', (SELECT count(*) FROM users),
    'permanent_users', (SELECT count(*) FROM users WHERE account_expires_at IS NULL),
    'temporary_users', (SELECT count(*) FROM users WHERE account_expires_at IS NOT NULL),
    'admins', (SELECT count(*) FROM users WHERE role='admin'),
    'content_events', (SELECT count(*) FROM content_events),
    'content_media', (SELECT count(*) FROM content_media),
    'comfy_settings', (SELECT count(*) FROM comfy_settings),
    'comfy_userdata', (SELECT count(*) FROM comfy_userdata),
    'access_policies', (SELECT count(*) FROM quick_generation_access_policies),
    'miners', (SELECT count(*) FROM miners),
    'generation_jobs', (SELECT count(*) FROM generation_jobs),
    'database_size_bytes', pg_database_size(current_database())
)::text;
'@
$postMigrationInventorySQL = @'
SELECT json_build_object(
    'schema_version', COALESCE((SELECT max(version) FROM schema_migrations),0),
    'migration_count', (SELECT count(*) FROM schema_migrations),
    'migrations_contiguous', COALESCE((SELECT min(version)=1 AND max(version)=count(*) FROM schema_migrations),false),
    'migration_checksums_valid', COALESCE((SELECT bool_and(length(checksum)=64) FROM schema_migrations),false),
    'users', (SELECT count(*) FROM users),
    'permanent_users', (SELECT count(*) FROM users WHERE account_expires_at IS NULL),
    'temporary_users', (SELECT count(*) FROM users WHERE account_expires_at IS NOT NULL),
    'admins', (SELECT count(*) FROM users WHERE role='admin'),
    'content_events', (SELECT count(*) FROM content_events),
    'content_media', (SELECT count(*) FROM content_media),
    'content_media_chunks', (SELECT count(*) FROM content_media_chunks),
    'comfy_settings', (SELECT count(*) FROM comfy_settings),
    'comfy_userdata', (SELECT count(*) FROM comfy_userdata),
    'access_policies', (SELECT count(*) FROM quick_generation_access_policies),
    'miners', (SELECT count(*) FROM miners),
    'generation_jobs', (SELECT count(*) FROM generation_jobs),
    'database_size_bytes', pg_database_size(current_database())
)::text;
'@

try {
    Invoke-RestoreCompose -Arguments @('config', '--quiet')
    $environmentCreated = $true

    Write-Host 'Restoring the OpenWebUI archive into an isolated volume.' -ForegroundColor Cyan
    Invoke-RestoreCompose -Arguments @('run', '--rm', 'openwebui-restore')

    Write-Host 'Starting an isolated PostgreSQL instance.' -ForegroundColor Cyan
    Invoke-RestoreCompose -Arguments @('up', '-d', '--wait', 'postgres')
    $postgresContainer = ((Get-RestoreComposeOutput -Arguments @('ps', '-q', 'postgres')) -join '').Trim()
    if ([string]::IsNullOrWhiteSpace($postgresContainer)) {
        throw 'Cannot resolve the restore PostgreSQL container.'
    }

    docker cp $databaseFile.Path "${postgresContainer}:/tmp/gateway-restore.dump" | Out-Null
    Assert-NativeSuccess 'Cannot copy the PostgreSQL dump into the restore container.'
    docker exec $postgresContainer pg_restore -U gateway_restore -d gateway_restore `
        --no-owner --no-privileges --exit-on-error /tmp/gateway-restore.dump
    Assert-NativeSuccess 'PostgreSQL restore failed.'

    $beforeRaw = @(& docker exec $postgresContainer psql -U gateway_restore -d gateway_restore -Atc $preMigrationInventorySQL)
    Assert-NativeSuccess 'Cannot read the pre-migration restore inventory.'
    $before = (($beforeRaw -join "`n").Trim() | ConvertFrom-Json)
    if ([int64] $before.users -lt 1 -or [int64] $before.admins -lt 1) {
        throw 'The restored database does not contain the expected user and administrator records.'
    }

    # Query the restored dump before workers/retention start, not the live source DB.
    $databaseTrainingJobs = @()
    if ([int64] $before.schema_version -ge 51) {
        $trainingSQL = @'
SELECT COALESCE(json_agg(row_to_json(j)), '[]'::json)::text FROM (
 SELECT public_id AS gateway_id, agent_job_id AS agent_id, user_id,
        username_snapshot AS owner, profile_id, artifact_name, artifact_bytes
 FROM lora_training_jobs WHERE state='completed' ORDER BY public_id
) j;
'@
        $trainingRows = @(& docker exec $postgresContainer psql -U gateway_restore -d gateway_restore -Atc $trainingSQL)
        Assert-NativeSuccess 'Cannot inspect training jobs from the restored database.'
        $databaseTrainingJobs = @(($trainingRows -join "`n") | ConvertFrom-Json)
    }
    if ($trainingFiles.Count -eq 1) {
        $trainingRestorePath = Join-Path $backupDirectory ('.training-drill-' + [guid]::NewGuid().ToString('N'))
        $trainingManifest = Invoke-TrainingBackupTool -Tool $TrainingBackupTool -ToolArguments @(
            '-mode', 'restore', '-archive', $trainingFiles[0].Path, '-destination', $trainingRestorePath
        )
        $trainingCreated = $true
        $verifiedJobs = @(Assert-TrainingBackupInventory -TrainingManifest $trainingManifest -DatabaseJobs $databaseTrainingJobs)
        $trainingReport = [ordered]@{
            Status = 'files_and_database_verified'
            DatasetPolicy = $trainingManifest.dataset_policy
            RestoredDirectory = $trainingRestorePath
            VerifiedJobs = $verifiedJobs
            ArchivedJobs = @($trainingManifest.jobs | Where-Object { $null -ne $_ }).Count
            Warnings = $(if ($trainingManifest.PSObject.Properties['warnings']) { @($trainingManifest.warnings) } else { @() })
            GatewayDownloadVerified = $false
        }
    } elseif ($databaseTrainingJobs.Count -gt 0) {
        throw 'Restored database contains completed LoRA jobs, but this backup has no training archive.'
    }

    Write-Host 'Building and starting Gateway against the restored database.' -ForegroundColor Cyan
    Invoke-RestoreCompose -Arguments @('up', '-d', '--build', '--wait', 'app')
    $appContainer = ((Get-RestoreComposeOutput -Arguments @('ps', '-q', 'app')) -join '').Trim()
    if ([string]::IsNullOrWhiteSpace($appContainer)) {
        throw 'Cannot resolve the restore Gateway container.'
    }

    $afterRaw = @(& docker exec $postgresContainer psql -U gateway_restore -d gateway_restore -Atc $postMigrationInventorySQL)
    Assert-NativeSuccess 'Cannot read the post-migration restore inventory.'
    $after = (($afterRaw -join "`n").Trim() | ConvertFrom-Json)
    if (-not [bool] $after.migrations_contiguous -or -not [bool] $after.migration_checksums_valid) {
        throw 'Restored schema migration history is incomplete or invalid.'
    }
    if ([int64] $after.schema_version -lt [int64] $before.schema_version) {
        throw 'Gateway moved the restored schema backwards.'
    }
    if ([int64] $after.permanent_users -ne [int64] $before.permanent_users) {
        throw 'Permanent account count changed while starting the restored Gateway.'
    }

    $datasetVerificationRaw = @(& docker exec $appContainer /app/gateway -verify-training-datasets)
    $datasetVerificationExit = $LASTEXITCODE
    $datasetVerification = ($datasetVerificationRaw -join "`n") | ConvertFrom-Json
    if ($datasetVerificationExit -ne 0 -or -not $datasetVerification.success) {
        throw "Saved dataset restore verification failed: $($datasetVerification.error)"
    }
    $trainingReport.DatasetIntegrity = $datasetVerification.inventory
    $trainingReport.AutomaticJobRecovery = 'disabled_for_restore_drill'

    $healthResponse = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$PublicPort/healthz" -TimeoutSec 15
    $readyResponse = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$PublicPort/readyz" -TimeoutSec 15
    $loginResponse = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$PublicPort/login" -TimeoutSec 15
    $metricsResponse = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$AdminPort/metrics" -TimeoutSec 15
    $ready = $readyResponse.Content | ConvertFrom-Json
    if ($healthResponse.StatusCode -ne 200 -or $readyResponse.StatusCode -ne 200 -or
        -not [bool] $ready.ready -or -not [bool] $ready.required.database.ready) {
        throw 'Gateway smoke-test failed against the restored database.'
    }
    if ($loginResponse.StatusCode -ne 200 -or -not $loginResponse.Content.Contains('<form')) {
        throw 'Gateway login page smoke-test failed.'
    }
    if ($metricsResponse.StatusCode -ne 200 -or -not $metricsResponse.Content.Contains('gateway_media_inflight_capacity_bytes')) {
        throw 'Gateway metrics smoke-test failed.'
    }
    docker exec $appContainer test -w /var/lib/gateway-spool
    Assert-NativeSuccess 'Gateway restore spool is not writable.'
    $spoolFiles = @(& docker exec $appContainer find /var/lib/gateway-spool `
        -mindepth 1 -maxdepth 1 -type f -print -quit)
    Assert-NativeSuccess 'Cannot inspect the Gateway restore spool.'
    if (-not [string]::IsNullOrWhiteSpace(($spoolFiles -join "`n"))) {
        throw 'Gateway restore spool was not cleaned.'
    }

    $readyAt = (Get-Date).ToUniversalTime()
    $createdAtValue = $manifestDocument.CreatedAt
    if ($createdAtValue -is [DateTimeOffset]) {
        $backupCreatedAt = $createdAtValue.UtcDateTime
    } elseif ($createdAtValue -is [DateTime]) {
        $backupCreatedAt = $createdAtValue.ToUniversalTime()
    } else {
        $backupCreatedAt = [DateTimeOffset]::Parse(
            [string] $createdAtValue,
            [Globalization.CultureInfo]::InvariantCulture,
            [Globalization.DateTimeStyles]::RoundtripKind
        ).UtcDateTime
    }
    $report = [ordered]@{
        Status                   = 'ok'
        Manifest                 = [IO.Path]::GetFileName($manifestPath)
        BackupCreatedAt          = $backupCreatedAt.ToString('o')
        DrillStartedAt           = $startedAt.ToString('o')
        GatewayReadyAt           = $readyAt.ToString('o')
        RPOSecondsAtDrill         = [math]::Round(($startedAt - $backupCreatedAt).TotalSeconds, 3)
        RestoreToReadySeconds     = [math]::Round(($readyAt - $startedAt).TotalSeconds, 3)
        ProjectName              = $projectName
        PublicPort               = $PublicPort
        AdminPort                = $AdminPort
        EnvironmentKept          = [bool] $KeepEnvironment
        VerifiedFiles            = $verifiedFiles
        OpenWebUIArchiveRestored = $true
        DatabaseBeforeMigration  = $before
        DatabaseAfterMigration   = $after
        Training                 = $trainingReport
        Gateway                  = [ordered]@{
            HealthStatus   = $healthResponse.StatusCode
            ReadyStatus    = $readyResponse.StatusCode
            Ready          = [bool] $ready.ready
            DatabaseReady  = [bool] $ready.required.database.ready
            LoginStatus    = $loginResponse.StatusCode
            MetricsStatus  = $metricsResponse.StatusCode
        }
    }
    $reportPath = Join-Path $backupDirectory "restore-drill-$stamp.json"
    $report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $reportPath -Encoding UTF8
    Write-Host "Restore drill passed in $($report.RestoreToReadySeconds) seconds: $reportPath" -ForegroundColor Green
} catch {
    Write-RestoreFailureReport $_.Exception.Message
    throw
} finally {
    if ($environmentCreated -and -not $KeepEnvironment) {
        & docker compose --file $composeFile --project-directory $projectRoot --project-name $projectName `
            down --volumes --remove-orphans --rmi local
        if ($LASTEXITCODE -ne 0) {
            Write-Warning "Could not completely remove restore project $projectName"
        }
    } elseif ($environmentCreated) {
        Write-Host "Restore environment retained: docker compose -f `"$composeFile`" -p $projectName ps" -ForegroundColor Yellow
    }
    if ($trainingCreated -and -not $KeepEnvironment) {
        $resolvedTrainingPath = [IO.Path]::GetFullPath($trainingRestorePath)
        if ([IO.Path]::GetDirectoryName($resolvedTrainingPath) -ne $backupDirectory -or
            -not [IO.Path]::GetFileName($resolvedTrainingPath).StartsWith('.training-drill-', [StringComparison]::Ordinal)) {
            throw 'Refusing to remove training restore files outside the isolated drill directory.'
        }
        Remove-Item -LiteralPath $resolvedTrainingPath -Recurse -Force
    }
    foreach ($key in $managedEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($key, $previousEnvironment[$key], 'Process')
    }
}

if (-not [string]::IsNullOrWhiteSpace($reportPath)) {
    Write-Output $reportPath
}
