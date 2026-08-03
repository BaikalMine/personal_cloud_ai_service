[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$OutputPath,
    [string]$InstallDirectory = (Join-Path $env:ProgramData 'AI-Mining-Agent')
)

$ErrorActionPreference = 'Stop'
$taskName = 'AI Access Gateway Mining Agent'
$firewallName = 'AI Access Gateway Mining Agent - Docker only'

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this script from an elevated PowerShell session.'
}

function Get-SanitizedLog([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return @()
    }
    return @(Get-Content -LiteralPath $Path -Tail 120 | ForEach-Object {
        $_ -replace '(?i)(--wallet(?:=|\s+))\S+', '$1<redacted>' `
            -replace '(?i)(wallet\s*[:=]\s*)\S+', '$1<redacted>' `
            -replace '[A-Za-z0-9]{45,}', '<redacted>'
    })
}

$task = Get-ScheduledTask -TaskName $taskName
$taskInfo = Get-ScheduledTaskInfo -TaskName $taskName
$rule = Get-NetFirewallRule -DisplayName $firewallName
$portFilter = $rule | Get-NetFirewallPortFilter
$addressFilter = $rule | Get-NetFirewallAddressFilter
$allProcesses = @(Get-CimInstance Win32_Process)
$includedPIDs = New-Object 'System.Collections.Generic.HashSet[UInt32]'
$queue = New-Object 'System.Collections.Generic.Queue[UInt32]'
$allProcesses | Where-Object { $_.Name -eq 'mining-agent.exe' } | ForEach-Object {
    [void]$includedPIDs.Add([uint32]$_.ProcessId)
    $queue.Enqueue([uint32]$_.ProcessId)
}
while ($queue.Count -gt 0) {
    $parentPID = $queue.Dequeue()
    $allProcesses | Where-Object { $_.ParentProcessId -eq $parentPID } | ForEach-Object {
        if ($includedPIDs.Add([uint32]$_.ProcessId)) {
            $queue.Enqueue([uint32]$_.ProcessId)
        }
    }
}
$allProcesses | Where-Object { $_.Name -eq 'SRBMiner-MULTI.exe' } | ForEach-Object {
    [void]$includedPIDs.Add([uint32]$_.ProcessId)
}
$processes = @($allProcesses | Where-Object { $includedPIDs.Contains([uint32]$_.ProcessId) } | ForEach-Object {
    $owner = Invoke-CimMethod -InputObject $_ -MethodName GetOwner
    $nativeProcess = Get-Process -Id $_.ProcessId -ErrorAction SilentlyContinue
    [ordered]@{
        name = $_.Name
        pid = $_.ProcessId
        parent_pid = $_.ParentProcessId
        session_id = $_.SessionId
        owner = "$($owner.Domain)\$($owner.User)"
        executable_path = $_.ExecutablePath
        main_window_handle = if ($nativeProcess) { $nativeProcess.MainWindowHandle.ToInt64() } else { 0 }
        main_window_title = if ($nativeProcess) { $nativeProcess.MainWindowTitle } else { '' }
    }
})
$acl = Get-Acl -LiteralPath $InstallDirectory

$result = [ordered]@{
    audit_identity = $identity.Name
    task = [ordered]@{
        name = $task.TaskName
        state = [string]$task.State
        user_id = $task.Principal.UserId
        run_level = [string]$task.Principal.RunLevel
        logon_type = [string]$task.Principal.LogonType
        last_result = $taskInfo.LastTaskResult
        trigger_users = @($task.Triggers.UserId)
    }
    firewall = [ordered]@{
        display_name = $rule.DisplayName
        enabled = [string]$rule.Enabled
        direction = [string]$rule.Direction
        action = [string]$rule.Action
        protocol = $portFilter.Protocol
        local_port = $portFilter.LocalPort
        remote_address = @($addressFilter.RemoteAddress)
    }
    acl = @($acl.Access | ForEach-Object {
        [ordered]@{
            identity = [string]$_.IdentityReference
            rights = [string]$_.FileSystemRights
            type = [string]$_.AccessControlType
            inherited = $_.IsInherited
        }
    })
    processes = $processes
    agent_log = Get-SanitizedLog (Join-Path $InstallDirectory 'agent.log')
    miner_log = Get-SanitizedLog (Join-Path $InstallDirectory 'miner.log')
}

$resolvedOutput = [IO.Path]::GetFullPath($OutputPath)
[IO.File]::WriteAllText($resolvedOutput, ($result | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
