[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Executable,

    [Parameter(Mandatory = $true)]
    [string]$GatewayEnvPath,

    [string]$Token,
    [switch]$GenerateToken,
    [switch]$SkipPythonSetup,
    [string]$ComfyRoot = 'C:\Work\ComfyUI',
    [string]$BasePython = 'C:\Work\Python312\python.exe',
    [string]$TunerDirectory = 'C:\Work\ComfyUI\tools\musubi-tuner',
    [string]$TrainingEnvironment = 'C:\Work\ComfyUI\tools\musubi-venv',
    [string]$ListenAddress = ':8095',
    [string]$InstallDirectory = (Join-Path $env:ProgramData 'AI-LoRA-Training-Agent'),
    [string]$KreaDiT = 'C:\Work\ComfyUI\models\diffusion_models\Krea 2\base model\moodyKrea2Mix_v70BF16.safetensors',
    [string]$KreaVAE = 'C:\Work\ComfyUI\models\vae\qwen_image_vae.safetensors',
    [string]$KreaTextEncoder = 'C:\Work\ComfyUI\models\text_encoders\qwen3vl_4b_bf16.safetensors',
    [string]$FluxDiT = 'C:\Work\ComfyUI\models\diffusion_models\Flux2\pornmasterFlux2Klein_v4BaseBf16.safetensors',
    [string]$FluxVAE = 'C:\Work\ComfyUI\models\vae\flux2-vae.safetensors',
    [string]$FluxTextEncoder = 'C:\Work\ComfyUI\models\text_encoders\qwen3vl_8b_bf16.safetensors',
    [string[]]$DockerRemoteAddresses = @(
        '192.168.65.0/24',
        '172.16.0.0/12',
        'fdc4:f303:9324::/64'
    )
)

$ErrorActionPreference = 'Stop'
$taskName = 'AI Access Gateway LoRA Training Agent'
$firewallName = 'AI Access Gateway LoRA Training Agent - Docker only'
$failureLog = Join-Path $env:TEMP 'ai-lora-training-agent-install-error.log'
$port = [int]($ListenAddress -replace '^.*:', '')

Remove-Item -LiteralPath $failureLog -Force -ErrorAction SilentlyContinue
trap {
    ($_ | Out-String) | Set-Content -LiteralPath $failureLog -Encoding UTF8
    exit 1
}

function Resolve-ExistingFile([string]$Path, [string]$Description) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Description not found: $Path"
    }
    return (Resolve-Path -LiteralPath $Path).Path
}

function Resolve-ExistingDirectory([string]$Path, [string]$Description) {
    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        throw "$Description not found: $Path"
    }
    return (Resolve-Path -LiteralPath $Path).Path
}

function Set-EnvValue([string]$Path, [string]$Key, [string]$Value) {
    $lines = [Collections.Generic.List[string]](Get-Content -LiteralPath $Path)
    $found = $false
    for ($index = 0; $index -lt $lines.Count; $index++) {
        if ($lines[$index] -match ('^' + [regex]::Escape($Key) + '=')) {
            $lines[$index] = "$Key=$Value"
            $found = $true
            break
        }
    }
    if (-not $found) { $lines.Add("$Key=$Value") }
    [IO.File]::WriteAllLines($Path, $lines, [Text.UTF8Encoding]::new($false))
}

function Set-StrictServiceDirectoryAcl([string]$Path, [string[]]$AdditionalAccounts = @()) {
    & icacls.exe $Path /reset /T /C | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Failed to reset ACLs below $Path" }
    $security = [Security.AccessControl.DirectorySecurity]::new()
    $security.SetAccessRuleProtection($true, $false)
    $inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    $allow = [Security.AccessControl.AccessControlType]::Allow
    $fullControl = [Security.AccessControl.FileSystemRights]::FullControl
    $principals = @(
        [Security.Principal.SecurityIdentifier]::new([Security.Principal.WellKnownSidType]::LocalSystemSid, $null),
        [Security.Principal.SecurityIdentifier]::new([Security.Principal.WellKnownSidType]::BuiltinAdministratorsSid, $null)
    )
    foreach ($account in $AdditionalAccounts) {
        if (-not [string]::IsNullOrWhiteSpace($account)) { $principals += [Security.Principal.NTAccount]::new($account) }
    }
    foreach ($principalIdentity in $principals) {
        $security.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new(
            $principalIdentity,
            $fullControl,
            $inheritance,
            [Security.AccessControl.PropagationFlags]::None,
            $allow
        ))
    }
    [IO.Directory]::SetAccessControl($Path, $security)
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script from an elevated PowerShell session.'
}
if ($port -lt 1024 -or $port -gt 65535) { throw 'ListenAddress must contain a valid non-privileged port.' }

$sourceExecutable = Resolve-ExistingFile $Executable 'LoRA training agent executable'
$gatewayEnvPath = Resolve-ExistingFile $GatewayEnvPath 'Gateway .env file'
$comfyRoot = Resolve-ExistingDirectory $ComfyRoot 'ComfyUI directory'
$tunerDirectory = Resolve-ExistingDirectory $TunerDirectory 'Musubi Tuner directory'
$basePython = Resolve-ExistingFile $BasePython 'Base Python executable'
$comfyLoraDirectory = Join-Path $comfyRoot 'models\loras'
if (-not (Test-Path -LiteralPath $comfyLoraDirectory -PathType Container)) {
    New-Item -ItemType Directory -Path $comfyLoraDirectory -Force | Out-Null
}

$trainingPython = Join-Path $TrainingEnvironment 'Scripts\python.exe'
if (-not $SkipPythonSetup) {
    if (-not (Test-Path -LiteralPath $trainingPython -PathType Leaf)) {
        & $basePython -m venv --system-site-packages $TrainingEnvironment
        if ($LASTEXITCODE -ne 0) { throw 'Failed to create the Musubi Python environment.' }
    }
    & $trainingPython -m pip install --disable-pip-version-check --editable "${tunerDirectory}[cu130]"
    if ($LASTEXITCODE -ne 0) { throw 'Failed to install Musubi Tuner dependencies.' }
}
$trainingPython = Resolve-ExistingFile $trainingPython 'Musubi training Python executable'
$env:USE_TF = '0'
$env:USE_FLAX = '0'
& $trainingPython -c 'import accelerate, bitsandbytes, safetensors, torch, transformers; print(torch.__version__)' | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'The Musubi Python environment failed its import check.' }

if ([string]::IsNullOrWhiteSpace($Token)) {
    $existingConfig = Join-Path $InstallDirectory 'config.json'
    if (Test-Path -LiteralPath $existingConfig -PathType Leaf) {
        $Token = (Get-Content -LiteralPath $existingConfig -Raw | ConvertFrom-Json).token
    }
}
if ([string]::IsNullOrWhiteSpace($Token)) {
    $Token = [Environment]::GetEnvironmentVariable('LORA_TRAINING_AGENT_TOKEN')
}
if ([string]::IsNullOrWhiteSpace($Token) -and $GenerateToken) {
    $bytes = [byte[]]::new(48)
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $random.GetBytes($bytes) } finally { $random.Dispose() }
    $Token = [Convert]::ToBase64String($bytes)
}
if ([string]::IsNullOrWhiteSpace($Token) -or $Token.Length -lt 32 -or $Token.Length -gt 512) {
    throw 'Provide a 32-512 character token or use -GenerateToken.'
}

$existingTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($existingTask) {
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
}

New-Item -ItemType Directory -Path $InstallDirectory -Force | Out-Null
$targetExecutable = Join-Path $InstallDirectory 'lora-training-agent.exe'
$configPath = Join-Path $InstallDirectory 'config.json'
$logPath = Join-Path $InstallDirectory 'agent.log'
$dataDirectory = Join-Path $InstallDirectory 'data'
New-Item -ItemType Directory -Path $dataDirectory -Force | Out-Null
Copy-Item -LiteralPath $sourceExecutable -Destination $targetExecutable -Force

$config = [ordered]@{
    listen_address = $ListenAddress
    token = $Token
    root_directory = $dataDirectory
    tuner_directory = $tunerDirectory
    python_executable = $trainingPython
    comfyui_lora_directory = $comfyLoraDirectory
    log_file = $logPath
    max_dataset_bytes = 536870912
    profiles = @(
        [ordered]@{
            id = 'krea2-moody-v7-bf16'
            family = 'krea2'
            name = 'Krea2 Moody v7 RAW'
            base_model = 'moodyKrea2Mix_v70BF16.safetensors'
            description = 'RAW Krea2 profile for character, style, object and product LoRA training.'
            dit = $KreaDiT
            vae = $KreaVAE
            text_encoder = $KreaTextEncoder
            strip_prefix = 'model.diffusion_model.'
            blocks_to_swap = 0
            fp8_base = $true
            fp8_text_encoder = $false
        },
        [ordered]@{
            id = 'flux2-klein-9b-pornmaster-v4-base'
            family = 'flux2-klein'
            name = 'Flux.2 Klein 9B PornMaster v4 Base'
            base_model = 'pornmasterFlux2Klein_v4BaseBf16.safetensors'
            description = 'Trainable Flux.2 Klein 9B base profile. Distilled Klein checkpoints are not used for training.'
            dit = $FluxDiT
            vae = $FluxVAE
            text_encoder = $FluxTextEncoder
            model_version = 'klein-base-9b'
            strip_prefix = 'model.diffusion_model.'
            blocks_to_swap = 0
            fp8_base = $true
            fp8_text_encoder = $true
        }
    )
}
[IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
Set-StrictServiceDirectoryAcl -Path $InstallDirectory -AdditionalAccounts @($identity.Name)

$action = New-ScheduledTaskAction -Execute $targetExecutable -Argument "-config `"$configPath`"" -WorkingDirectory $InstallDirectory
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity.Name
$taskPrincipal = New-ScheduledTaskPrincipal -UserId $identity.Name -LogonType Interactive -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $taskPrincipal -Settings $settings -Description 'Local image-only LoRA training agent for AI Access Gateway.' | Out-Null

Get-NetFirewallRule -DisplayName $firewallName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -DisplayName $firewallName -Direction Inbound -Action Allow -Protocol TCP -LocalPort $port -RemoteAddress $DockerRemoteAddresses -Profile Any | Out-Null

Start-ScheduledTask -TaskName $taskName
$healthy = $false
for ($attempt = 0; $attempt -lt 30; $attempt++) {
    Start-Sleep -Milliseconds 500
    try {
        $response = Invoke-RestMethod -Uri "http://127.0.0.1:$port/healthz" -TimeoutSec 2
        if ($response.available -eq $true) { $healthy = $true; break }
    } catch { }
}
if (-not $healthy) { throw "LoRA training agent did not become healthy. Check $logPath and task history." }

Set-EnvValue $gatewayEnvPath 'LORA_TRAINING_AGENT_URL' "http://host.docker.internal:$port"
Set-EnvValue $gatewayEnvPath 'LORA_TRAINING_AGENT_TOKEN' $Token

Write-Host "Installed and started: $taskName"
Write-Host "Health endpoint: http://127.0.0.1:$port/healthz"
Write-Host "Config: $configPath"
Write-Host "Log: $logPath"
Write-Host 'ComfyUI was not stopped or restarted.'
