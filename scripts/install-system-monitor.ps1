[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Executable,

    [string]$Token,
    [switch]$GenerateToken,
    [string]$GatewayEnvPath,
    [string]$InstallDirectory = (Join-Path $env:ProgramData 'AI-System-Monitor'),
    [string]$ListenAddress = ':8094',
    [string]$ComfyCommandSignature = 'main.py --enable-manager',
    [string[]]$DockerRemoteAddresses = @(
        '192.168.65.0/24',
        '172.16.0.0/12',
        'fdc4:f303:9324::/64'
    )
)

$ErrorActionPreference = 'Stop'
$taskName = 'AI Access Gateway System Monitor'
$firewallName = 'AI Access Gateway System Monitor - Docker only'
$port = 8094

function Set-StrictServiceDirectoryAcl([string]$Path) {
    & icacls.exe $Path /reset /T /C | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to reset ACLs below $Path"
    }

    $security = [Security.AccessControl.DirectorySecurity]::new()
    $security.SetAccessRuleProtection($true, $false)
    $inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor
        [Security.AccessControl.InheritanceFlags]::ObjectInherit
    $propagation = [Security.AccessControl.PropagationFlags]::None
    $allow = [Security.AccessControl.AccessControlType]::Allow
    $fullControl = [Security.AccessControl.FileSystemRights]::FullControl
    $principals = @(
        [Security.Principal.SecurityIdentifier]::new(
            [Security.Principal.WellKnownSidType]::LocalSystemSid,
            $null
        ),
        [Security.Principal.SecurityIdentifier]::new(
            [Security.Principal.WellKnownSidType]::BuiltinAdministratorsSid,
            $null
        )
    )
    foreach ($principalIdentity in $principals) {
        $security.AddAccessRule(
            [Security.AccessControl.FileSystemAccessRule]::new(
                $principalIdentity,
                $fullControl,
                $inheritance,
                $propagation,
                $allow
            )
        )
    }
    [IO.Directory]::SetAccessControl($Path, $security)
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script from an elevated PowerShell session.'
}

$sourceExecutable = (Resolve-Path -LiteralPath $Executable).Path
if (-not (Test-Path -LiteralPath $sourceExecutable -PathType Leaf)) {
    throw "System monitor executable not found: $sourceExecutable"
}

$existingConfigPath = Join-Path $InstallDirectory 'system-monitor.json'
if ([string]::IsNullOrWhiteSpace($Token) -and (Test-Path -LiteralPath $existingConfigPath -PathType Leaf)) {
    $existingConfig = [IO.File]::ReadAllText($existingConfigPath, [Text.UTF8Encoding]::new($false)) | ConvertFrom-Json
    $Token = $existingConfig.token
    if ($ListenAddress -eq ':8094' -and -not [string]::IsNullOrWhiteSpace($existingConfig.listen_address)) {
        $ListenAddress = $existingConfig.listen_address
    }
    if ($ComfyCommandSignature -eq 'main.py --enable-manager' -and -not [string]::IsNullOrWhiteSpace($existingConfig.comfy_command_signature)) {
        $ComfyCommandSignature = $existingConfig.comfy_command_signature
    }
}
if ([string]::IsNullOrWhiteSpace($Token) -and $GenerateToken) {
    $tokenBytes = [byte[]]::new(48)
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $random.GetBytes($tokenBytes)
    } finally {
        $random.Dispose()
    }
    $Token = [Convert]::ToBase64String($tokenBytes)
}
if ([string]::IsNullOrWhiteSpace($Token) -or $Token.Length -lt 32 -or $Token.Length -gt 512) {
    throw 'Provide a 32-512 character token, an existing configuration, or -GenerateToken.'
}

$existingTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($existingTask) {
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
}

New-Item -ItemType Directory -Path $InstallDirectory -Force | Out-Null
$targetExecutable = Join-Path $InstallDirectory 'system-monitor.exe'
$configPath = Join-Path $InstallDirectory 'system-monitor.json'
$logPath = Join-Path $InstallDirectory 'system-monitor.log'

Get-CimInstance Win32_Process -Filter "Name = 'system-monitor.exe'" -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and $_.ExecutablePath.Equals($targetExecutable, [StringComparison]::OrdinalIgnoreCase) } |
    ForEach-Object {
        try { & taskkill.exe /PID $_.ProcessId /T /F 2>$null | Out-Null } catch {}
    }
Start-Sleep -Milliseconds 400

Copy-Item -LiteralPath $sourceExecutable -Destination $targetExecutable -Force
$config = [ordered]@{
    listen_address = $ListenAddress
    token = $Token
    log_file = $logPath
    comfy_command_signature = $ComfyCommandSignature
}
[IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json), [Text.UTF8Encoding]::new($false))

Set-StrictServiceDirectoryAcl -Path $InstallDirectory

$action = New-ScheduledTaskAction -Execute $targetExecutable -Argument "-config `"$configPath`"" -WorkingDirectory $InstallDirectory
$trigger = New-ScheduledTaskTrigger -AtStartup
$taskPrincipal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $taskPrincipal -Settings $settings -Description 'Read-only Windows resource monitor for AI Access Gateway.' | Out-Null

Get-NetFirewallRule -DisplayName $firewallName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
New-NetFirewallRule -DisplayName $firewallName -Direction Inbound -Action Allow -Protocol TCP -LocalPort $port -RemoteAddress $DockerRemoteAddresses -Profile Any | Out-Null

Start-ScheduledTask -TaskName $taskName
$healthy = $false
for ($attempt = 0; $attempt -lt 20; $attempt++) {
    Start-Sleep -Milliseconds 500
    try {
        $response = Invoke-RestMethod -Uri "http://127.0.0.1:$port/healthz" -TimeoutSec 2
        if ($response.status -eq 'ok') {
            $healthy = $true
            break
        }
    } catch {
        # The task can take a moment to start after registration.
    }
}
if (-not $healthy) {
    throw "System monitor did not become healthy. Check $logPath and the scheduled task history."
}

if (-not [string]::IsNullOrWhiteSpace($GatewayEnvPath)) {
    $resolvedEnvPath = (Resolve-Path -LiteralPath $GatewayEnvPath).Path
    $lines = [Collections.Generic.List[string]](Get-Content -LiteralPath $resolvedEnvPath)
    $updates = [ordered]@{
        SYSTEM_MONITOR_AGENT_URL = "http://host.docker.internal:$port"
        SYSTEM_MONITOR_AGENT_TOKEN = $Token
    }
    foreach ($key in $updates.Keys) {
        $found = $false
        for ($index = 0; $index -lt $lines.Count; $index++) {
            if ($lines[$index] -match ('^' + [regex]::Escape($key) + '=')) {
                $lines[$index] = "$key=$($updates[$key])"
                $found = $true
                break
            }
        }
        if (-not $found) {
            $lines.Add("$key=$($updates[$key])")
        }
    }
    [IO.File]::WriteAllLines($resolvedEnvPath, $lines, [Text.UTF8Encoding]::new($false))
}

Write-Host "Installed and started: $taskName"
Write-Host "Health endpoint: http://127.0.0.1:$port/healthz"
Write-Host "Allowed remote networks: $($DockerRemoteAddresses -join ', ')"
