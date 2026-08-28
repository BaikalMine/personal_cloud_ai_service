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
    $grants = @(
        '*S-1-5-18:(OI)(CI)F',
        '*S-1-5-32-544:(OI)(CI)F',
        "$ServiceAccount:(OI)(CI)F"
    )
    & icacls.exe $Path /inheritance:r /grant:r $grants /T /C | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to apply protected ACL to $Path"
    }
}

foreach ($path in @($GatewayRoot, $ProxyRoot, $MiningAgentRoot, $UpdateAgentRoot)) {
    Protect-ServicePath $path
}

Write-Host 'Protected service ACLs applied.'
