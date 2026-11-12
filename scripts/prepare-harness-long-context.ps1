$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$previous = Join-Path $root 'artifacts/production-release-20261010-service-repair-r1/source'
$release = Join-Path $root 'artifacts/production-release-20261010-harness-long-context-r1'
$source = Join-Path $release 'source'
if (Test-Path -LiteralPath $source) { throw 'Snapshot already exists.' }
$null = New-Item -ItemType Directory -Path $source -Force
Get-ChildItem -LiteralPath $previous -Force | Copy-Item -Destination $source -Recurse -Force
foreach ($file in @('internal/modelruntime/runtime.go','internal/modelruntime/runtime_windows.go',
 'internal/modelruntime/settings.go','internal/modelruntime/settings_test.go',
 'cmd/inference-worker/main.go','cmd/inference-worker/config.go','cmd/inference-worker/config_test.go',
 'integrations/harness/entry.mjs','integrations/harness/model-policy.mjs','integrations/harness/model-policy.test.mjs',
 'integrations/harness/Dockerfile','integrations/harness/long-context.Dockerfile','integrations/harness/README.md',
 'integrations/harness/tools/runtime-budget-check.mjs',
 'scripts/prepare-harness-long-context.ps1','scripts/deploy-harness-long-context.ps1','scripts/restore-harness-model-profile.mjs')) {
 $target=Join-Path $source $file
 $null=New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force
 Copy-Item -LiteralPath (Join-Path $root $file) -Destination $target -Force
}
Write-Output $source
