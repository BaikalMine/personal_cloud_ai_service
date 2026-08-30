[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ServiceAccount,

    [string]$GatewayRoot = 'C:\ai-access-gateway',
    [string]$ProxyRoot = 'C:\comfyui-proxy',
    [string]$MiningAgentRoot = 'C:\ai-mining-agent',
    [string]$UpdateAgentRoot = 'C:\ProgramData\AI-Update-Agent'
)

$ErrorActionPreference = 'Stop'

function Protect-ServicePath([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Path does not exist: $Path"
    }
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
        ),
        [Security.Principal.NTAccount]::new($ServiceAccount)
    )
    foreach ($principal in $principals) {
        $security.AddAccessRule(
            [Security.AccessControl.FileSystemAccessRule]::new(
                $principal,
                $fullControl,
                $inheritance,
                $propagation,
                $allow
            )
        )
    }
    Set-Acl -LiteralPath $Path -AclObject $security
}

foreach ($path in @($GatewayRoot, $ProxyRoot, $MiningAgentRoot, $UpdateAgentRoot)) {
    Protect-ServicePath $path
}

Write-Host 'Protected service ACLs applied.'
