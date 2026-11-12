$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$image = 'golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2'
$database = 'gateway-ollama-guard-test-' + [Guid]::NewGuid().ToString('N')
$started = $false

Push-Location $projectRoot
try {
    docker run -d --rm --name $database --tmpfs /var/lib/postgresql/data:rw,size=256m `
        -e POSTGRES_PASSWORD=synthetic-ollama-test-only -e POSTGRES_DB=gateway_test postgres:16-alpine
    if ($LASTEXITCODE -ne 0) { throw 'Ollama guard test database failed to start.' }
    $started = $true
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        docker exec $database pg_isready -U postgres -d gateway_test
        if ($LASTEXITCODE -eq 0) { break }
        if ([DateTime]::UtcNow -ge $deadline) { throw 'Ollama guard test database was not ready.' }
        Start-Sleep -Milliseconds 200
    } while ($true)

    docker run --rm --network "container:$database" `
        -e TEST_DATABASE_URL=postgres://postgres:synthetic-ollama-test-only@127.0.0.1:5432/gateway_test?sslmode=disable `
        -v "${projectRoot}:/src:ro" -w /src $image `
        go test -p 1 -count=1 -timeout=180s ./cmd/inference-worker ./internal/config ./internal/database `
        ./internal/store ./internal/inference ./internal/gateway `
        -run 'TestOllama|TestOwnedOllama|TestOwnedWorkerRecoversNativeOllama|TestGPUExecutor|TestComfyGPU'
    if ($LASTEXITCODE -ne 0) { throw 'Ollama guard protocol tests failed.' }
} finally {
    if ($started) {
        docker rm -f -v $database | Out-Null
    }
    Pop-Location
}
