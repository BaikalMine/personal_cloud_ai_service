param(
    [string]$ComfyRoot = 'C:\Work\ComfyUI',
    [string]$Python = 'C:\Work\Python312\python.exe',
    [ValidateSet('default', 'none')][string]$CacheMode = 'default'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$source = (Resolve-Path -LiteralPath $ComfyRoot).Path
$pythonPath = (Resolve-Path -LiteralPath $Python).Path
$safeSource = $source.Replace('\', '/')
$commit = & git -C $source -c "safe.directory=$safeSource" rev-parse HEAD
if ($LASTEXITCODE -ne 0) { throw 'Installed ComfyUI revision could not be read.' }
& git -C $source -c "safe.directory=$safeSource" diff --quiet $commit -- '*.py' pyproject.toml requirements.txt
if ($LASTEXITCODE -ne 0) { throw 'Installed core differs from its revision; do not test a misleading archive.' }

$allowedRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot 'bin')) + [IO.Path]::DirectorySeparatorChar
$runRoot = [IO.Path]::GetFullPath((Join-Path $allowedRoot ('comfy-core-compat-' + [Guid]::NewGuid().ToString('N'))))
$archive = Join-Path $runRoot 'core.zip'
$core = Join-Path $runRoot 'core'
$state = Join-Path $runRoot 'state'
$environment = @{
    PYTHONDONTWRITEBYTECODE = '1'; CUDA_VISIBLE_DEVICES = '-1'; HIP_VISIBLE_DEVICES = '-1';
    HF_HUB_OFFLINE = '1'; HF_HUB_DISABLE_TELEMETRY = '1'; DO_NOT_TRACK = '1';
    TEMP = $state; TMP = $state; TORCH_HOME = $state; HF_HOME = $state
}
$previous = @{}
try {
    New-Item -ItemType Directory -Path $runRoot, $core, $state | Out-Null
    & git -C $source -c "safe.directory=$safeSource" archive --format=zip "--output=$archive" $commit
    if ($LASTEXITCODE -ne 0) { throw 'Core archive failed.' }
    Expand-Archive -LiteralPath $archive -DestinationPath $core
    foreach ($name in $environment.Keys) {
        $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $environment[$name], 'Process')
    }
    & $pythonPath -B (Join-Path $projectRoot 'integrations/comfyui_gpu_guard/test_installed_core.py') `
        --core $core --state $state --commit $commit --cache-mode $CacheMode
    if ($LASTEXITCODE -ne 0) { throw 'Installed ComfyUI core compatibility failed.' }
    $currentCommit = & git -C $source -c "safe.directory=$safeSource" rev-parse HEAD
    if ($LASTEXITCODE -ne 0 -or $currentCommit -ne $commit) { throw 'Installed core revision changed during verification.' }
    & git -C $source -c "safe.directory=$safeSource" diff --quiet $commit -- '*.py' pyproject.toml requirements.txt
    if ($LASTEXITCODE -ne 0) { throw 'Installed core changed during verification.' }
} finally {
    foreach ($name in $previous.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process')
    }
    $resolved = [IO.Path]::GetFullPath($runRoot)
    if (-not $resolved.StartsWith($allowedRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Refusing cleanup outside the workspace test directory.'
    }
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force }
}
