$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$previous = Join-Path $root 'artifacts/production-release-20261010-harness-rea-r1/source'
$release = Join-Path $root 'artifacts/production-release-20261010-assistant-image-r1'
$source = Join-Path $release 'source'
if (Test-Path -LiteralPath $release) { throw 'Release snapshot already exists.' }
$null = New-Item -ItemType Directory -Path $source -Force
Get-ChildItem -LiteralPath $previous -Force | Copy-Item -Destination $source -Recurse -Force
foreach ($file in @('internal/gateway/vision_image.go', 'internal/gateway/vision_image_test.go',
    'internal/gateway/prompt_assistant.go', 'internal/gateway/prompt_assistant_test.go', 'scripts/prepare-assistant-image-release.ps1',
    'scripts/deploy-assistant-image.ps1')) {
    Copy-Item -LiteralPath (Join-Path $root $file) -Destination (Join-Path $source $file) -Force
}
Write-Output $source
