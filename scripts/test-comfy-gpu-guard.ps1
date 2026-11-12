$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$image = 'ai-gateway-comfy-guard-tests:local'
$database = 'gateway-comfy-guard-test-' + [Guid]::NewGuid().ToString('N')
$started = $false

Push-Location $projectRoot
try {
    docker build -t $image -f integrations/comfyui_gpu_guard/Dockerfile.test integrations/comfyui_gpu_guard
    if ($LASTEXITCODE -ne 0) { throw 'ComfyUI guard test image build failed.' }

    docker run --rm --network none -v "${projectRoot}:/src:ro" -w /src $image `
        python3 -B -W error::ResourceWarning integrations/comfyui_gpu_guard/test_guard.py
    if ($LASTEXITCODE -ne 0) { throw 'ComfyUI guard Python tests failed.' }

    docker run -d --rm --name $database --tmpfs /var/lib/postgresql/data:rw,size=256m `
        -e POSTGRES_PASSWORD=synthetic-guard-test-only -e POSTGRES_DB=gateway_test postgres:16-alpine
    if ($LASTEXITCODE -ne 0) { throw 'ComfyUI guard test database failed to start.' }
    $started = $true
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        docker exec $database pg_isready -U postgres -d gateway_test
        if ($LASTEXITCODE -eq 0) { break }
        if ([DateTime]::UtcNow -ge $deadline) { throw 'ComfyUI guard test database was not ready.' }
        Start-Sleep -Milliseconds 200
    } while ($true)

    docker run --rm --network "container:$database" `
        -e TEST_DATABASE_URL=postgres://postgres:synthetic-guard-test-only@127.0.0.1:5432/gateway_test?sslmode=disable `
        -e COMFY_GUARD_TEST_PYTHON=python3 -v "${projectRoot}:/src:ro" -w /src $image `
        go test -p 1 -count=1 -timeout=180s ./internal/config ./internal/gateway ./internal/store `
        -run 'TestComfyGPU|TestStripComfy|TestGPUExecutor'
    if ($LASTEXITCODE -ne 0) { throw 'ComfyUI guard Go/PostgreSQL protocol tests failed.' }
} finally {
    if ($started) {
        docker rm -f -v $database | Out-Null
    }
    Pop-Location
}
