param()
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$name = 'gateway-training-restore-' + [guid]::NewGuid().ToString('N')
$created = $false
try {
    & docker run -d --name $name --label codex.test=training-restore `
        --tmpfs /var/lib/postgresql/data:rw,size=1g `
        -e POSTGRES_USER=restore_test -e POSTGRES_PASSWORD=restore-test-only-password -e POSTGRES_DB=restore_test `
        postgres:16-alpine@sha256:fd1e8d0274f13f5a03a2673a207b28e14823c2f2efc3ca4bb4197c8a9f841bdc | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Cannot create isolated restore PostgreSQL.' }
    $created = $true
    $ready = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        & docker exec $name pg_isready -U restore_test -d restore_test | Out-Null
        if ($LASTEXITCODE -eq 0) { $ready = $true; break }
        Start-Sleep -Seconds 1
    }
    if (-not $ready) { throw 'Isolated restore PostgreSQL did not become ready.' }
    & docker run --rm --network "container:$name" `
        -v "${projectRoot}:/src:ro" -v ai-gateway-go-build-cache:/go-build -e GOCACHE=/go-build `
        -e 'TEST_RESTORE_DATABASE_URL=postgres://restore_test:restore-test-only-password@127.0.0.1:5432/restore_test?sslmode=disable' `
        -w /src golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 `
        sh -ec 'apk add --no-cache postgresql16-client >/dev/null; go build -o /tmp/gateway-restore-test ./cmd/gateway; TEST_RESTORE_GATEWAY_BINARY=/tmp/gateway-restore-test go test ./internal/gateway -run TestTrainingBackupPostgresRestoreIntegration -count=1 -v'
    if ($LASTEXITCODE -ne 0) { throw 'Training backup PostgreSQL restore test failed.' }
} finally {
    if ($created) {
        & docker rm -f $name | Out-Null
        if ($LASTEXITCODE -ne 0) { Write-Warning "Could not remove isolated test container $name" }
    }
}
