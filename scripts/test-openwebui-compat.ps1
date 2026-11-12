param(
    [string]$OpenWebUIImage = 'ghcr.io/open-webui/open-webui@sha256:6bb1fbe8ab0a3e0456067f493044ffb66a30a65a34be47f6a5862176a370dd16',
    [string]$ExpectedVersion = '0.11.1',
    [string]$GoImage = 'golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2',
    [ValidateRange(1, 10)][int]$Count = 1
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$id = [Guid]::NewGuid().ToString('N')
$database = "gateway-openwebui-compat-db-$id"
$app = "gateway-openwebui-compat-app-$id"
$runner = "gateway-openwebui-compat-run-$id"
$output = Join-Path $projectRoot "bin/openwebui-compat-$id"
$startedDB = $false
$startedApp = $false

Push-Location $projectRoot
try {
    New-Item -ItemType Directory -Path $output -Force | Out-Null
    docker run --rm -e CGO_ENABLED=0 -v "${projectRoot}:/src:ro" -v "${output}:/out" -w /src $GoImage `
        go test -c -o /out/inference.test ./internal/inference
    if ($LASTEXITCODE -ne 0) { throw 'Compatibility test compilation failed.' }

    docker run -d --rm --name $database --network none --tmpfs /var/lib/postgresql/data:rw,size=256m `
        -e POSTGRES_PASSWORD=synthetic-openwebui-compat-only -e POSTGRES_DB=gateway_test postgres:16-alpine
    if ($LASTEXITCODE -ne 0) { throw 'Compatibility database failed to start.' }
    $startedDB = $true
    docker run -d --rm --name $app --network "container:$database" --init --memory=2g --cpus=2 --pids-limit=256 `
        --tmpfs /app/backend/data:rw,size=512m -e WEBUI_NAME=AI-Gateway-Compatibility-Fixture `
        -e WEBUI_SECRET_KEY=synthetic-openwebui-compat-secret-not-production `
        -e OFFLINE_MODE=true -e HF_HUB_OFFLINE=1 -e ENABLE_OLLAMA_API=false -e ENABLE_OPENAI_API=false `
        -e ENABLE_SIGNUP=true -e RAG_EMBEDDING_ENGINE=ollama -e RAG_EMBEDDING_MODEL=fixture `
        -e USER_AGENT=AI-Gateway-Compatibility-Test -e SCARF_NO_ANALYTICS=true -e DO_NOT_TRACK=true `
        -e ANONYMIZED_TELEMETRY=false -e OMP_NUM_THREADS=1 -e TOKENIZERS_PARALLELISM=false $OpenWebUIImage
    if ($LASTEXITCODE -ne 0) { throw 'Compatibility OpenWebUI failed to start.' }
    $startedApp = $true

    docker run --rm --name $runner --network "container:$database" `
        -e TEST_DATABASE_URL=postgres://postgres:synthetic-openwebui-compat-only@127.0.0.1:5432/gateway_test?sslmode=disable `
        -e OPENWEBUI_COMPAT_TEST_URL=http://127.0.0.1:8080 -e "OPENWEBUI_COMPAT_TEST_VERSION=$ExpectedVersion" `
        -v "${output}:/out:ro" -w /tmp $GoImage /out/inference.test `
        '-test.run=TestOllamaCatalog|TestOllamaOpenWebUICompatibility' '-test.timeout=300s' "-test.count=$Count" '-test.v'
    if ($LASTEXITCODE -ne 0) { throw 'OpenWebUI compatibility tests failed.' }
} finally {
    # Only invocation-owned names and files are removed, never user containers/data.
    docker rm -f $runner 2>$null | Out-Null
    if ($startedApp) { docker rm -f -v $app | Out-Null }
    if ($startedDB) { docker rm -f -v $database | Out-Null }
    $binary = Join-Path $output 'inference.test'
    if (Test-Path -LiteralPath $binary) { Remove-Item -LiteralPath $binary -Force }
    if (Test-Path -LiteralPath $output) { Remove-Item -LiteralPath $output }
    Pop-Location
}
