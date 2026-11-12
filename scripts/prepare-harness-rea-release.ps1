$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
$previous=Join-Path $root 'artifacts/production-release-20261010-harness-long-context-r1/source'
$source=Join-Path $root 'artifacts/production-release-20261010-harness-rea-r1/source'
if(Test-Path -LiteralPath $source){throw 'Snapshot already exists.'}
$null=New-Item -ItemType Directory -Path $source -Force
Get-ChildItem -LiteralPath $previous -Force | Copy-Item -Destination $source -Recurse -Force
foreach($file in @('internal/gateway/harness.go','internal/gateway/harness_admission.go','internal/gateway/harness_admission_test.go',
 'internal/inference/ollama_http.go','internal/inference/ollama_completion.go','internal/inference/ollama_completion_test.go',
 'internal/inference/ollama_http_integration_test.go','integrations/harness/entry.mjs','integrations/harness/rea.patch.yml',
 'integrations/harness/rea.Dockerfile','integrations/harness/rea-mcp.mjs','integrations/harness/README.md',
 'integrations/harness/tools/patch-koffi-musl.mjs','integrations/harness/tools/rea-check.mjs',
 'integrations/harness/tools/rea-profile-check.mjs','integrations/harness/tools/gpu-stream-check.mjs',
 'scripts/deploy-harness-rea.ps1','scripts/deploy-harness-koffi.ps1','scripts/deploy-harness-rea-catalog.ps1',
 'scripts/deploy-harness-rea-pids.ps1','integrations/harness/tools/qa-model-proxy.mjs',
 'integrations/harness/tools/qa-budget-loader.mjs',
 'scripts/prepare-harness-rea-release.ps1')){
 $target=Join-Path $source $file
 $null=New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force
 Copy-Item -LiteralPath (Join-Path $root $file) -Destination $target -Force
}
Write-Output $source
