[CmdletBinding()]
param(
    [string]$SourceDirectory = (Join-Path $env:USERPROFILE 'ComfyUI\user\default'),
    [string]$GatewayURL = 'http://127.0.0.1:8090'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$envPath = Join-Path $projectRoot '.env'

if (-not (Test-Path -LiteralPath $SourceDirectory -PathType Container)) {
    throw "ComfyUI profile was not found: $SourceDirectory"
}
if (-not (Test-Path -LiteralPath $envPath -PathType Leaf)) {
    throw "Gateway environment file was not found: $envPath"
}

$settings = @{}
Get-Content -LiteralPath $envPath | Where-Object { $_ -match '^[A-Za-z_][A-Za-z0-9_]*=' } | ForEach-Object {
    $name, $value = $_.Split('=', 2)
    $settings[$name] = $value.Trim('"')
}
foreach ($required in 'POSTGRES_USER', 'POSTGRES_DB', 'ADMIN_USERNAME') {
    if (-not $settings[$required]) {
        throw "$required is missing from .env"
    }
}

function Get-SHA256Hex([string]$Value) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Value)))).Replace('-', '').ToLowerInvariant()
    }
    finally {
        $sha.Dispose()
    }
}

function Invoke-PSQL([string]$Query, [switch]$Scalar) {
    $arguments = @('exec', 'ai-gateway-postgres', 'psql', '-U', $settings.POSTGRES_USER, '-d', $settings.POSTGRES_DB, '-v', 'ON_ERROR_STOP=1')
    if ($Scalar) {
        $arguments += @('-tA')
    }
    $arguments += @('-c', $Query)
    $output = & docker @arguments
    if ($LASTEXITCODE -ne 0) {
        throw 'PostgreSQL command failed'
    }
    return $output
}

$adminName = $settings.ADMIN_USERNAME.Replace("'", "''")
$adminID = Invoke-PSQL "SELECT id FROM users WHERE username='$adminName' AND role='admin'" -Scalar | Select-Object -First 1
if (-not $adminID -or $adminID -notmatch '^\d+$') {
    throw "Gateway administrator was not found: $($settings.ADMIN_USERNAME)"
}

$token = [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')
$tokenHash = Get-SHA256Hex $token
$sessionCreated = $false
$uploadedFiles = 0
$uploadedBytes = [int64]0

try {
    Invoke-PSQL "INSERT INTO sessions(user_id,token_hash,expires_at,user_agent,ip) VALUES($adminID,'$tokenHash',now()+interval '20 minutes','comfy-default-import','127.0.0.1')" | Out-Null
    $sessionCreated = $true

    $webSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $gatewayUri = [Uri]$GatewayURL
    $webSession.Cookies.Add((New-Object System.Net.Cookie('gateway_session', $token, '/', $gatewayUri.Host)))
    $webSession.Cookies.Add((New-Object System.Net.Cookie('gateway_service', 'comfyui', '/', $gatewayUri.Host)))

    $settingsPath = Join-Path $SourceDirectory 'comfy.settings.json'
    if (Test-Path -LiteralPath $settingsPath -PathType Leaf) {
        $settingsJSON = Get-Content -LiteralPath $settingsPath -Raw -Encoding UTF8
        $parsedSettings = $settingsJSON | ConvertFrom-Json
        if ($null -eq $parsedSettings) {
            throw 'ComfyUI settings file does not contain a JSON object'
        }
        $response = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Post `
            -Uri "$GatewayURL/api/settings" -WebSession $webSession -ContentType 'application/json' -Body $settingsJSON
        if ($response.StatusCode -ne 200) {
            throw "ComfyUI settings import returned HTTP $($response.StatusCode)"
        }
    }

    $root = (Resolve-Path -LiteralPath $SourceDirectory).Path.TrimEnd('\')
    foreach ($file in Get-ChildItem -LiteralPath $root -Recurse -File | Where-Object { $_.FullName -ne $settingsPath }) {
        $relativePath = $file.FullName.Substring($root.Length).TrimStart('\').Replace('\', '/')
        $encodedPath = [Uri]::EscapeDataString($relativePath)
        $payload = [IO.File]::ReadAllBytes($file.FullName)
        $response = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Post `
            -Uri "$GatewayURL/api/userdata/$encodedPath" -WebSession $webSession `
            -ContentType 'application/octet-stream' -Body $payload
        if ($response.StatusCode -ne 200) {
            throw "ComfyUI userdata import returned HTTP $($response.StatusCode) for $relativePath"
        }
        $uploadedFiles++
        $uploadedBytes += $payload.LongLength
    }
}
finally {
    if ($sessionCreated) {
        Invoke-PSQL "DELETE FROM sessions WHERE token_hash='$tokenHash'" | Out-Null
    }
}

[pscustomobject]@{
    Admin          = $settings.ADMIN_USERNAME
    SettingsLoaded = Test-Path -LiteralPath (Join-Path $SourceDirectory 'comfy.settings.json') -PathType Leaf
    FilesUploaded  = $uploadedFiles
    BytesUploaded  = $uploadedBytes
}
