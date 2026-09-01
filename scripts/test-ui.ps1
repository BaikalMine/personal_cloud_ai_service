param(
    [switch] $UpdateSnapshots
)

$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot
$goImage = 'golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2'
$testImage = 'ai-access-gateway-playwright-tests:1.62.1'
$runID = "ai-gateway-ui-$PID"
$networkName = "$runID-network"
$previewName = "$runID-preview"
$artifactDir = Join-Path $projectRoot 'artifacts\playwright'
$networkCreated = $false
$previewStarted = $false

New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null

Push-Location $projectRoot
try {
    docker build --pull=false -f scripts/playwright.Dockerfile -t $testImage .
    if ($LASTEXITCODE -ne 0) {
        throw 'Cannot build the pinned Playwright test image.'
    }

    docker network create $networkName | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'Cannot create the isolated UI test network.'
    }
    $networkCreated = $true

    docker run -d --name $previewName --network $networkName --network-alias preview `
        -v "${projectRoot}:/src:ro" -v 'ai-gateway-ui-go-cache:/go/pkg/mod' -w /src $goImage `
        go run ./cmd/ui-preview | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'Cannot start the isolated UI preview.'
    }
    $previewStarted = $true

    $ready = $false
    foreach ($attempt in 1..90) {
        $previousErrorActionPreference = $ErrorActionPreference
        $ErrorActionPreference = 'SilentlyContinue'
        docker exec $previewName wget -q -O - http://127.0.0.1:18080/preview/components 2>$null | Out-Null
        $readinessExitCode = $LASTEXITCODE
        $ErrorActionPreference = $previousErrorActionPreference
        if ($readinessExitCode -eq 0) {
            $ready = $true
            break
        }
        Start-Sleep -Seconds 1
    }
    if (-not $ready) {
        docker logs $previewName
        throw 'UI preview did not become ready in time.'
    }

    $workspaceMode = if ($UpdateSnapshots) { 'rw' } else { 'ro' }
    $arguments = @('test', '--config=/workspace/playwright.config.cjs')
    if ($UpdateSnapshots) {
        $arguments += '--update-snapshots'
    }

    docker run --rm --network $networkName `
        -e 'UI_PREVIEW_URL=http://preview:18080' -e 'PLAYWRIGHT_OUTPUT_DIR=/artifacts' `
        -v "${projectRoot}:/workspace:$workspaceMode" -v "${artifactDir}:/artifacts" `
        -w /workspace $testImage @arguments
    if ($LASTEXITCODE -ne 0) {
        throw 'Playwright UI checks failed.'
    }
} finally {
    if ($previewStarted) {
        docker rm -f $previewName | Out-Null
    }
    if ($networkCreated) {
        docker network rm $networkName | Out-Null
    }
    Pop-Location
}
