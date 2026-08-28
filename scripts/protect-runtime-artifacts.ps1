[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ServiceAccount
)

$ErrorActionPreference = 'Stop'
$paths = @(
    'C:\ai-access-gateway\.env',
    'C:\ai-access-gateway\docker-compose.yml',
    'C:\comfyui-proxy\.env',
    'C:\comfyui-proxy\.htpasswd',
    'C:\comfyui-proxy\openwebui.htpasswd',
    'C:\ai-mining-agent\config.json',
    'C:\ai-mining-agent\mining-agent.exe',
    'C:\ProgramData\AI-Update-Agent\config.json',
    'C:\ProgramData\AI-Update-Agent\update-agent.exe'
)

foreach ($path in $paths) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Required runtime artifact does not exist: $path"
    }
    $acl = Get-Acl -LiteralPath $path
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($rule in @($acl.Access)) {
        [void]$acl.RemoveAccessRuleAll($rule)
    }
    foreach ($identity in @('NT AUTHORITY\SYSTEM', 'BUILTIN\Administrators', $ServiceAccount)) {
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new(
            $identity,
            [Security.AccessControl.FileSystemRights]::FullControl,
            [Security.AccessControl.AccessControlType]::Allow
        ))
    }
    Set-Acl -LiteralPath $path -AclObject $acl
}

Write-Host 'Protected runtime artifacts applied.'
