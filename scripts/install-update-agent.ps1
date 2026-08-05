[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Executable,

    [Parameter(Mandatory = $true)]
    [string]$GatewayRoot,

    [Parameter(Mandatory = $true)]
    [string]$ComfyRoot,

    [Parameter(Mandatory = $true)]
    [string]$ComfyPython,

    [Parameter(Mandatory = $true)]
    [string]$OpenWebUIComposeFile,

    [string]$Token,
    [switch]$GenerateToken,
    [string]$GatewayComposeFile,
    [string]$OpenWebUIEnvFile,
    [string]$ListenAddress = ':8093',
    [string[]]$DockerRemoteAddresses = @(
        '192.168.65.0/24',
        '172.16.0.0/12',
        'fdc4:f303:9324::/64'
    ),
    [string]$InstallDirectory = (Join-Path $env:ProgramData 'AI-Update-Agent')
)

$ErrorActionPreference = 'Stop'
$taskName = 'AI Access Gateway Update Agent'
$watchdogTaskName = 'AI Access Gateway Update Agent Watchdog'
$firewallName = 'AI Access Gateway Update Agent - Docker only'
$port = 8093
$failureLog = Join-Path $env:TEMP 'ai-update-agent-install-error.log'

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

function Convert-ToJsonSafe([object]$Value) {
    return ($Value | ConvertTo-Json -Depth 6)
}

if ([string]::IsNullOrWhiteSpace($Token)) {
    $Token = [Environment]::GetEnvironmentVariable('AI_UPDATE_AGENT_TOKEN')
}
if ([string]::IsNullOrWhiteSpace($Token)) {
    $existingConfig = Join-Path $InstallDirectory 'config.json'
    if (Test-Path -LiteralPath $existingConfig -PathType Leaf) {
        $Token = (Get-Content -LiteralPath $existingConfig -Raw | ConvertFrom-Json).token
    }
}
if ([string]::IsNullOrWhiteSpace($Token) -and $GenerateToken) {
    $bytes = [byte[]]::new(48)
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $random.GetBytes($bytes) } finally { $random.Dispose() }
    $Token = [Convert]::ToBase64String($bytes)
}
if ([string]::IsNullOrWhiteSpace($Token) -or $Token.Length -lt 32 -or $Token.Length -gt 512) {
    throw 'Provide a 32-512 character token, AI_UPDATE_AGENT_TOKEN, or -GenerateToken.'
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script from an elevated PowerShell session.'
}

$sourceExecutable = Resolve-ExistingFile $Executable 'Update agent executable'
$gatewayRoot = Resolve-ExistingDirectory $GatewayRoot 'Gateway directory'
$comfyRoot = Resolve-ExistingDirectory $ComfyRoot 'ComfyUI directory'
$comfyPython = Resolve-ExistingFile $ComfyPython 'ComfyUI Python executable'
$gatewayComposeFile = if ([string]::IsNullOrWhiteSpace($GatewayComposeFile)) { Join-Path $gatewayRoot 'docker-compose.yml' } else { $GatewayComposeFile }
$gatewayComposeFile = Resolve-ExistingFile $gatewayComposeFile 'Gateway Docker Compose file'
$openWebUIComposeFile = Resolve-ExistingFile $OpenWebUIComposeFile 'Open WebUI Docker Compose file'
$openWebUIDirectory = Split-Path -Parent $openWebUIComposeFile
$gatewayEnvFile = Resolve-ExistingFile (Join-Path $gatewayRoot '.env') 'Gateway .env file'
if ([string]::IsNullOrWhiteSpace($OpenWebUIEnvFile)) { $OpenWebUIEnvFile = Join-Path $openWebUIDirectory '.env' }
$openWebUIEnvFile = Resolve-ExistingFile $OpenWebUIEnvFile 'Open WebUI .env file'

$gatewayRemote = (& git -C $gatewayRoot remote get-url origin).Trim()
$gatewayBranch = (& git -C $gatewayRoot branch --show-current).Trim()
$comfyRemote = (& git -C $comfyRoot remote get-url origin).Trim()
$comfyBranch = (& git -C $comfyRoot branch --show-current).Trim()
if ([string]::IsNullOrWhiteSpace($gatewayRemote) -or [string]::IsNullOrWhiteSpace($gatewayBranch)) { throw 'Gateway Git origin or branch is not configured.' }
if ([string]::IsNullOrWhiteSpace($comfyRemote) -or [string]::IsNullOrWhiteSpace($comfyBranch)) { throw 'ComfyUI Git origin or branch is not configured.' }

$currentOpenWebUIImage = (& docker inspect --format '{{.Config.Image}}' openwebui).Trim()
if ([string]::IsNullOrWhiteSpace($currentOpenWebUIImage)) { throw 'Open WebUI container "openwebui" was not found.' }
if ($currentOpenWebUIImage -notmatch '^ghcr\.io/open-webui/open-webui(?:[:@].*)?$') {
    throw 'Open WebUI container image is not the official ghcr.io/open-webui/open-webui image.'
}

# Make the current Open WebUI image explicit before the agent manages future version pins.
$composeBackup = Join-Path $InstallDirectory ('openwebui-compose-before-update-agent-{0}.yml' -f (Get-Date -Format 'yyyyMMdd-HHmmss'))
New-Item -ItemType Directory -Path $InstallDirectory -Force | Out-Null
Copy-Item -LiteralPath $openWebUIComposeFile -Destination $composeBackup -Force
$composeText = Get-Content -LiteralPath $openWebUIComposeFile -Raw
if ($composeText -notmatch '(?m)^\s*image:\s*\$\{OPENWEBUI_IMAGE\}') {
    $pattern = '(?m)^(\s*image:\s*)ghcr\.io/open-webui/open-webui(?:\:[^\s@]+)?(?:@sha256:[a-fA-F0-9]+)?\s*$'
    $matches = [regex]::Matches($composeText, $pattern)
    if ($matches.Count -ne 1) {
        throw 'Could not safely locate the Open WebUI image line in Docker Compose. No files were changed except the backup.'
    }
    $composeText = [regex]::Replace($composeText, $pattern, '$1${OPENWEBUI_IMAGE}', 1)
    [IO.File]::WriteAllText($openWebUIComposeFile, $composeText, [Text.UTF8Encoding]::new($false))
}
Set-EnvValue $openWebUIEnvFile 'OPENWEBUI_IMAGE' $currentOpenWebUIImage

$existingTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($existingTask) {
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
}

$targetExecutable = Join-Path $InstallDirectory 'update-agent.exe'
$configPath = Join-Path $InstallDirectory 'config.json'
$logPath = Join-Path $InstallDirectory 'agent.log'
$launcherPath = Join-Path $InstallDirectory 'start-comfyui.cmd'
$stopperPath = Join-Path $InstallDirectory 'stop-comfyui.ps1'
$watchdogPath = Join-Path $InstallDirectory 'ensure-update-agent.ps1'

Copy-Item -LiteralPath $sourceExecutable -Destination $targetExecutable -Force
$launcher = "@echo off`r`ntitle ComfyUI`r`ncd /d `"$comfyRoot`"`r`n`"$comfyPython`" main.py --listen 0.0.0.0`r`necho.`r`necho ComfyUI stopped. Press any key to close this window.`r`npause > nul`r`n"
[IO.File]::WriteAllText($launcherPath, $launcher, [Text.UTF8Encoding]::new($false))
$stopper = "`$processes = Get-CimInstance Win32_Process -Filter `"Name = 'python.exe'`" | Where-Object { `$_.CommandLine -match '(?i)(^|\s)main\.py(\s|$)' -and `$_.CommandLine -match '(?i)--listen\s+0\.0\.0\.0' }`r`nforeach (`$process in `$processes) { Stop-Process -Id `$process.ProcessId -Force -ErrorAction Stop }`r`n"
[IO.File]::WriteAllText($stopperPath, $stopper, [Text.UTF8Encoding]::new($false))
$watchdog = "`$ErrorActionPreference = 'Stop'`r`n`$listener = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue`r`nif (-not `$listener) { Start-ScheduledTask -TaskName '$taskName' }`r`n"
[IO.File]::WriteAllText($watchdogPath, $watchdog, [Text.UTF8Encoding]::new($false))

$config = [ordered]@{
    listen_address = $ListenAddress
    token = $Token
    log_file = $logPath
    gateway = [ordered]@{
        working_directory = $gatewayRoot
        remote_url = $gatewayRemote
        branch = $gatewayBranch
        compose_file = $gatewayComposeFile
        compose_service = 'app'
        health_url = 'http://127.0.0.1:8090/healthz'
    }
    comfyui = [ordered]@{
        working_directory = $comfyRoot
        remote_url = $comfyRemote
        branch = $comfyBranch
        python_executable = $comfyPython
        launch_arguments = @('main.py', '--listen', '0.0.0.0')
        launch_command = @($env:ComSpec, '/c', "start `"ComfyUI`" `"$launcherPath`"")
        stop_command = @('powershell.exe', '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $stopperPath)
        health_url = 'http://127.0.0.1:8188/'
    }
    openwebui = [ordered]@{
        compose_directory = $openWebUIDirectory
        compose_file = $openWebUIComposeFile
        compose_service = 'openwebui'
        container_name = 'openwebui'
        env_file = $openWebUIEnvFile
        image_variable = 'OPENWEBUI_IMAGE'
        image_repository = 'ghcr.io/open-webui/open-webui'
        release_api = 'https://api.github.com/repos/open-webui/open-webui/releases/latest'
        health_url = 'docker://openwebui'
    }
}
[IO.File]::WriteAllText($configPath, (Convert-ToJsonSafe $config), [Text.UTF8Encoding]::new($false))

$aclGrants = @('*S-1-5-18:(OI)(CI)F', '*S-1-5-32-544:(OI)(CI)F', "$($identity.Name):(OI)(CI)F")
& icacls.exe $InstallDirectory /inheritance:r /grant:r $aclGrants | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Failed to apply the update agent directory ACL.' }

$action = New-ScheduledTaskAction -Execute $targetExecutable -Argument "-config `"$configPath`"" -WorkingDirectory $InstallDirectory
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity.Name
$taskPrincipal = New-ScheduledTaskPrincipal -UserId $identity.Name -LogonType Interactive -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $taskPrincipal -Settings $settings -Description 'Windows host agent for AI Access Gateway updates and visible ComfyUI restarts.' | Out-Null

$watchdogCommand = "powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$watchdogPath`""
& schtasks.exe /Create /TN $watchdogTaskName /TR $watchdogCommand /SC MINUTE /MO 2 /RU SYSTEM /RL HIGHEST /F | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Failed to create the update agent watchdog task.' }

Get-NetFirewallRule -DisplayName $firewallName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -DisplayName $firewallName -Direction Inbound -Action Allow -Protocol TCP -LocalPort $port -RemoteAddress $DockerRemoteAddresses -Profile Any | Out-Null

Start-ScheduledTask -TaskName $taskName
$healthy = $false
for ($attempt = 0; $attempt -lt 20; $attempt++) {
    Start-Sleep -Milliseconds 500
    try {
        $response = Invoke-RestMethod -Uri "http://127.0.0.1:$port/healthz" -TimeoutSec 2
        if ($response.available -eq $true) { $healthy = $true; break }
    } catch { }
}
if (-not $healthy) { throw "Update agent did not become healthy. Check $configPath, $logPath and task history." }

Set-EnvValue $gatewayEnvFile 'UPDATE_AGENT_URL' "http://host.docker.internal:$port"
Set-EnvValue $gatewayEnvFile 'UPDATE_AGENT_TOKEN' $Token
Push-Location $gatewayRoot
try { & docker compose -f $gatewayComposeFile up -d --build app } finally { Pop-Location }

Write-Host "Installed and started: $taskName"
Write-Host "Health endpoint: http://127.0.0.1:$port/healthz"
Write-Host "Allowed remote networks: $($DockerRemoteAddresses -join ', ')"
Write-Host "Gateway update agent URL: http://host.docker.internal:$port"
Write-Host "ComfyUI restarts will open a visible console window."
