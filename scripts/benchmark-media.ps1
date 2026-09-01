$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot
$goImage = 'golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2'

Push-Location $projectRoot
try {
    docker run --rm `
        -v "${projectRoot}:/src" `
        -w /src `
        $goImage `
        sh -c '/usr/local/go/bin/go test -run "^$" -bench "^Benchmark(SpoolGenerationOutputArchive|EncryptMediaChunks)$" -benchmem -benchtime=1x ./internal/gateway'
    if ($LASTEXITCODE -ne 0) {
        throw 'Media benchmark failed.'
    }
} finally {
    Pop-Location
}
