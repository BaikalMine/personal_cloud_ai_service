param(
    [switch] $Deep
)

$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot
$composeFile = Join-Path $projectRoot 'docker-compose.e2e.yml'
$testDatabaseURL = 'postgres://gateway_e2e:gateway-e2e-password@postgres:5432/gateway_e2e?sslmode=disable'
$goImage = 'golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2'

Push-Location $projectRoot
try {
    docker run --rm -v "${projectRoot}:/src" -w /src $goImage `
        sh /src/scripts/test-unit.sh
    if ($LASTEXITCODE -ne 0) {
        throw 'Unit checks failed.'
    }

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

    docker compose -f $composeFile up -d --wait postgres
    if ($LASTEXITCODE -ne 0) {
        throw 'The integration database did not start.'
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
    docker compose -f $composeFile down --volumes
    Pop-Location
}
