[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Executable,

    [string]$Token,
    [switch]$GenerateToken,
    [string]$GatewayEnvPath,
    [switch]$InteractiveConsole,

    [string]$InstallDirectory = (Join-Path $env:ProgramData 'AI-Mining-Agent'),
    [string]$MiningRoot = (Join-Path $env:USERPROFILE 'AI-Mining'),
    [string]$ListenAddress = ':8092',
    [string[]]$DockerRemoteAddresses = @(
        '192.168.65.0/24',
        '172.16.0.0/12',
        'fdc4:f303:9324::/64'
    )
)

$ErrorActionPreference = 'Stop'
$taskName = 'AI Access Gateway Mining Agent'
$firewallName = 'AI Access Gateway Mining Agent - Docker only'
$port = 8092
$failureLog = Join-Path $env:TEMP 'ai-mining-agent-install-error.log'

Remove-Item -LiteralPath $failureLog -Force -ErrorAction SilentlyContinue
trap {
    ($_ | Out-String) | Set-Content -LiteralPath $failureLog -Encoding UTF8
    exit 1
}

if ([string]::IsNullOrWhiteSpace($Token)) {
    $Token = [Environment]::GetEnvironmentVariable('AI_MINING_AGENT_TOKEN')
}
if ([string]::IsNullOrWhiteSpace($Token)) {
    $existingConfigPath = Join-Path $InstallDirectory 'config.json'
    if (Test-Path -LiteralPath $existingConfigPath -PathType Leaf) {
        # Windows PowerShell 5 reads BOM-less UTF-8 as the legacy code page.
        # Read explicitly so an existing Cyrillic mining root survives an update.
        $existingConfig = [IO.File]::ReadAllText($existingConfigPath, [Text.UTF8Encoding]::new($false)) | ConvertFrom-Json
        $Token = $existingConfig.token
        $defaultMiningRoot = Join-Path $env:USERPROFILE 'AI-Mining'
        if ($MiningRoot -eq $defaultMiningRoot -and -not [string]::IsNullOrWhiteSpace($existingConfig.mining_root)) {
            $MiningRoot = $existingConfig.mining_root
        }
        if ($ListenAddress -eq ':8092' -and -not [string]::IsNullOrWhiteSpace($existingConfig.listen_address)) {
            $ListenAddress = $existingConfig.listen_address
        }
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
    throw 'Provide a 32-512 character token, AI_MINING_AGENT_TOKEN, or -GenerateToken.'
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script from an elevated PowerShell session.'
}

$sourceExecutable = (Resolve-Path -LiteralPath $Executable).Path
$resolvedMiningRoot = (Resolve-Path -LiteralPath $MiningRoot).Path
if (-not (Test-Path -LiteralPath $sourceExecutable -PathType Leaf)) {
    throw "Mining agent executable not found: $sourceExecutable"
}
if (-not (Test-Path -LiteralPath $resolvedMiningRoot -PathType Container)) {
    throw "Mining root does not exist: $resolvedMiningRoot"
}

$existingTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($existingTask) {
    # Stopping an interactive task causes Task Scheduler to tear down every
    # child process, including the miner console. Disable new starts first;
    # the agent executable itself is terminated below without its tree.
    Disable-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue | Out-Null
}

New-Item -ItemType Directory -Path $InstallDirectory -Force | Out-Null
$targetExecutable = Join-Path $InstallDirectory 'mining-agent.exe'
$configPath = Join-Path $InstallDirectory 'config.json'
$logPath = Join-Path $InstallDirectory 'agent.log'
$minerLogPath = Join-Path $InstallDirectory 'miner.log'
$logViewerPath = Join-Path $InstallDirectory 'view-miner-log.cmd'

$agentProcesses = @(Get-CimInstance Win32_Process -Filter "Name = 'mining-agent.exe'" -ErrorAction SilentlyContinue |
    Where-Object { $_.ExecutablePath -and $_.ExecutablePath.Equals($targetExecutable, [StringComparison]::OrdinalIgnoreCase) })
foreach ($agentProcess in $agentProcesses) {
    try {
        # Do not terminate the complete process tree here: an active miner and
        # its visible console are descendants of a previous agent process.
        & taskkill.exe /PID $agentProcess.ProcessId /F 2>$null | Out-Null
    } catch {
        # The process may exit between the CIM query and taskkill.
    }
    try {
        Wait-Process -Id $agentProcess.ProcessId -Timeout 10 -ErrorAction SilentlyContinue
    } catch {
        # The process may already be gone.
    }
}
Start-Sleep -Milliseconds 400

if ($existingTask) {
    Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
}

Copy-Item -LiteralPath $sourceExecutable -Destination $targetExecutable -Force
if (Test-Path -LiteralPath $minerLogPath -PathType Leaf) {
    $existingLog = Get-Item -LiteralPath $minerLogPath
    if ($existingLog.Length -gt 0) {
        $archivePath = Join-Path $InstallDirectory ("miner-{0}.log" -f (Get-Date -Format 'yyyyMMdd-HHmmss'))
        Move-Item -LiteralPath $minerLogPath -Destination $archivePath -Force
    }
}
$config = [ordered]@{
    listen_address = $ListenAddress
    mining_root = $resolvedMiningRoot
    token = $Token
    log_file = $logPath
    miner_log_file = $minerLogPath
}
$configJSON = $config | ConvertTo-Json
[IO.File]::WriteAllText($configPath, $configJSON, [Text.UTF8Encoding]::new($false))
$logViewer = "@echo off`r`ntitle Miner log`r`npowershell.exe -NoLogo -NoProfile -NoExit -Command `"`$p='$minerLogPath'; while (-not (Test-Path -LiteralPath `$p)) { Write-Host 'Waiting for miner.log...'; Start-Sleep -Seconds 1 }; Get-Content -LiteralPath `$p -Tail 200 -Wait`"`r`n"
[IO.File]::WriteAllText($logViewerPath, $logViewer, [Text.ASCIIEncoding]::new())

$aclGrants = @('*S-1-5-18:(OI)(CI)F', '*S-1-5-32-544:(OI)(CI)F')
if ($InteractiveConsole) {
    $aclGrants += "$($identity.Name):(OI)(CI)F"
}
& icacls.exe $InstallDirectory /inheritance:r /grant:r $aclGrants | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw 'Failed to apply the mining agent directory ACL.'
}

$action = New-ScheduledTaskAction -Execute $targetExecutable -Argument "-config `"$configPath`"" -WorkingDirectory $InstallDirectory
if ($InteractiveConsole) {
    $interactiveUser = $identity.Name
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $interactiveUser
    $taskPrincipal = New-ScheduledTaskPrincipal -UserId $interactiveUser -LogonType Interactive -RunLevel Highest
    $taskDescription = 'Interactive Windows host agent for visible AI Access Gateway mining consoles.'
} else {
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $taskPrincipal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $taskDescription = 'Restricted Windows host agent for AI Access Gateway mining controls.'
}
$settings = New-ScheduledTaskSettingsSet -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $taskPrincipal -Settings $settings -Description $taskDescription | Out-Null

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
        # The task can take a moment to enter the running state after registration.
    }
}
if (-not $healthy) {
    throw "Mining agent did not become healthy. Check $logPath and the scheduled task history."
}

if (-not [string]::IsNullOrWhiteSpace($GatewayEnvPath)) {
    $resolvedEnvPath = (Resolve-Path -LiteralPath $GatewayEnvPath).Path
    $lines = [Collections.Generic.List[string]](Get-Content -LiteralPath $resolvedEnvPath)
    $updates = [ordered]@{
        MINING_AGENT_URL = "http://host.docker.internal:$port"
        MINING_AGENT_TOKEN = $Token
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
Write-Host "Miner log: $minerLogPath"
Write-Host "Live log viewer: $logViewerPath"
Write-Host "Interactive console: $InteractiveConsole"
