param(
    [switch] $Quick,
    [switch] $Deep
)

$ErrorActionPreference = 'Stop'

if ($Quick -and $Deep) {
    throw 'Quick and Deep modes cannot be used together.'
}

$projectRoot = Split-Path -Parent $PSScriptRoot
$composeFile = Join-Path $projectRoot 'docker-compose.e2e.yml'
$restoreComposeFile = Join-Path $projectRoot 'docker-compose.restore.yml'
$testDatabaseURL = 'postgres://gateway_e2e:gateway-e2e-password@postgres:5432/gateway_e2e?sslmode=disable'
$goImage = 'golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2'
$nodeImage = 'node:24-alpine@sha256:d32cdf619f63fe0471182d08996dd516c6275bb5fd31ae06e55a570bd9e1ad43'
$integrationStarted = $false

Push-Location $projectRoot
try {
    foreach ($scriptPath in @(
        (Join-Path $PSScriptRoot 'backup.ps1'),
        (Join-Path $PSScriptRoot 'restore.ps1')
    )) {
        $tokens = $null
        $syntaxErrors = $null
        [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref] $tokens, [ref] $syntaxErrors) | Out-Null
        if (@($syntaxErrors).Count -gt 0) {
            throw "PowerShell syntax check failed for $scriptPath`: $($syntaxErrors[0].Message)"
        }
    }

    $restoreEnvironment = [ordered]@{
        SESSION_SECRET           = '01234567890123456789012345678901'
        RESTORE_BACKUP_DIR       = $projectRoot
        RESTORE_OPENWEB_ARCHIVE  = 'restore-contract-placeholder.tar.gz'
        RESTORE_PUBLIC_PORT      = '18092'
        RESTORE_ADMIN_PORT       = '18093'
    }
    $previousRestoreEnvironment = @{}
    try {
        foreach ($key in $restoreEnvironment.Keys) {
            $previousRestoreEnvironment[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
            [Environment]::SetEnvironmentVariable($key, $restoreEnvironment[$key], 'Process')
        }
        docker compose -f $restoreComposeFile -p ai-gateway-restore-contract config --quiet
        if ($LASTEXITCODE -ne 0) {
            throw 'Restore Compose contract is invalid.'
        }
        $restoreVolumes = @(docker compose -f $restoreComposeFile -p ai-gateway-restore-contract config --volumes)
        if ($LASTEXITCODE -ne 0) {
            throw 'Cannot inspect Restore Compose volumes.'
        }
        $actualRestoreVolumes = @($restoreVolumes | ForEach-Object { $_.Trim() } | Where-Object { $_ } | Sort-Object)
        $expectedRestoreVolumes = @('gateway-spool', 'openwebui-restore') | Sort-Object
        if (($actualRestoreVolumes -join ',') -ne ($expectedRestoreVolumes -join ',')) {
            throw "Restore Compose exposes unexpected volumes: $($actualRestoreVolumes -join ', ')"
        }
    } finally {
        foreach ($key in $restoreEnvironment.Keys) {
            [Environment]::SetEnvironmentVariable($key, $previousRestoreEnvironment[$key], 'Process')
        }
    }

    docker run --rm -v "${projectRoot}:/src" -w /src $goImage `
        sh /src/scripts/test-unit.sh
    if ($LASTEXITCODE -ne 0) {
        throw 'Unit checks failed.'
    }

    docker run --rm -v "${projectRoot}:/src:ro" -w /src $nodeImage `
        sh -c 'for file in internal/gateway/static/*.js; do node --check "$file"; done'
    if ($LASTEXITCODE -ne 0) {
        throw 'JavaScript syntax checks failed.'
    }

    docker run --rm -v "${projectRoot}:/src:ro" -w /src $nodeImage `
        sh -c 'node --test internal/gateway/testdata/js/*.test.cjs'
    if ($LASTEXITCODE -ne 0) {
        throw 'JavaScript unit checks failed.'
    }

    if ($Quick) {
        return
    }

    & (Join-Path $PSScriptRoot 'test-ui.ps1')

    if ($Deep) {
        docker run --rm -v "${projectRoot}:/src" -w /src $goImage `
            sh -c '/usr/local/go/bin/go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...'
        if ($LASTEXITCODE -ne 0) {
            throw 'Staticcheck failed.'
        }

        docker run --rm -v "${projectRoot}:/src" -w /src $goImage `
            sh -c 'apk add --no-cache build-base >/dev/null && CGO_ENABLED=1 /usr/local/go/bin/go test -race ./...'
        if ($LASTEXITCODE -ne 0) {
            throw 'Race detector failed.'
        }
    }

    $integrationStarted = $true
    docker compose -f $composeFile up -d --wait postgres
    if ($LASTEXITCODE -ne 0) {
        throw 'The integration database did not start.'
    }

    docker run --rm --network ai-gateway-e2e_default `
        -e "TEST_DATABASE_URL=$testDatabaseURL" `
        -v "${projectRoot}:/src" -w /src $goImage `
        sh -c '/usr/local/go/bin/go test -count=1 -v ./internal/database -run TestDurableGenerationJobsMigrationBackfill'
    if ($LASTEXITCODE -ne 0) {
        throw 'Migration backfill checks failed.'
    }

    docker run --rm --network ai-gateway-e2e_default `
        -e "TEST_DATABASE_URL=$testDatabaseURL" `
        -v "${projectRoot}:/src" -w /src $goImage `
        sh -c '/usr/local/go/bin/go test -count=1 -v ./internal/store -run TestStoreIntegrationLifecycle'
    if ($LASTEXITCODE -ne 0) {
        throw 'Integration checks failed.'
    }

    docker run --rm --network ai-gateway-e2e_default `
        -e "TEST_DATABASE_URL=$testDatabaseURL" `
        -v "${projectRoot}:/src" -w /src $goImage `
        sh -c '/usr/local/go/bin/go test -count=1 -v ./internal/gateway -run TestGatewayIntegrationComfyOwnership'
    if ($LASTEXITCODE -ne 0) {
        throw 'Gateway integration checks failed.'
    }
} finally {
    if ($integrationStarted) {
        docker compose -f $composeFile down --volumes
    }
    Pop-Location
}
