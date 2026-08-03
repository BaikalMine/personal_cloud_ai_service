[CmdletBinding()]
param(
    [string]$GatewayURL = 'http://127.0.0.1:8090',
    [string]$AdminURL = 'http://127.0.0.1:8091'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$settings = @{}
Get-Content -LiteralPath (Join-Path $projectRoot '.env') | Where-Object {
    $_ -match '^[A-Za-z_][A-Za-z0-9_]*='
} | ForEach-Object {
    $name, $value = $_.Split('=', 2)
    $settings[$name] = $value.Trim('"')
}
foreach ($required in 'POSTGRES_USER', 'POSTGRES_DB', 'ADMIN_USERNAME') {
    if (-not $settings[$required]) {
        throw "$required is missing from .env"
    }
}

function Invoke-PSQL([string]$Query, [switch]$Scalar) {
    $arguments = @(
        'exec', 'ai-gateway-postgres', 'psql',
        '-U', $settings.POSTGRES_USER, '-d', $settings.POSTGRES_DB,
        '-v', 'ON_ERROR_STOP=1'
    )
    if ($Scalar) { $arguments += '-tA' }
    $arguments += @('-c', $Query)
    $output = & docker @arguments
    if ($LASTEXITCODE -ne 0) { throw 'PostgreSQL command failed' }
    return $output
}

function Get-CSRF([string]$HTML) {
    $match = [regex]::Match($HTML, 'name="csrf"\s+value="([^"]+)"')
    if (-not $match.Success) { throw 'CSRF token was not found' }
    return $match.Groups[1].Value
}

function Get-SHA256Hex([string]$Value) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Value)))).Replace('-', '').ToLowerInvariant()
    } finally { $sha.Dispose() }
}

$adminName = $settings.ADMIN_USERNAME.Replace("'", "''")
$adminID = Invoke-PSQL "SELECT id FROM users WHERE username='$adminName' AND role='admin'" -Scalar | Select-Object -First 1
if ($adminID -notmatch '^\d+$') { throw 'Administrator was not found' }

$suffix = [guid]::NewGuid().ToString('N').Substring(0, 12)
$username = "__dashboard_smoke_$suffix"
$userID = Invoke-PSQL "INSERT INTO users(username,password_hash,role) VALUES('$username','disabled-smoke-password','user') RETURNING id" -Scalar | Select-Object -First 1
$adminToken = [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')
$userToken = [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')
$adminTokenHash = Get-SHA256Hex $adminToken
$userTokenHash = Get-SHA256Hex $userToken
$adminSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$userSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$adminSession.Cookies.Add((New-Object System.Net.Cookie('gateway_session', $adminToken, '/', ([Uri]$AdminURL).Host)))
$userSession.Cookies.Add((New-Object System.Net.Cookie('gateway_session', $userToken, '/', ([Uri]$GatewayURL).Host)))
$inviteID = $null

try {
    Invoke-PSQL "INSERT INTO sessions(user_id,token_hash,expires_at,user_agent,ip) VALUES($adminID,'$adminTokenHash',now()+interval '10 minutes','dashboard-smoke-admin','127.0.0.1')" | Out-Null
    Invoke-PSQL "INSERT INTO sessions(user_id,token_hash,expires_at,user_agent,ip) VALUES($userID,'$userTokenHash',now()+interval '10 minutes','dashboard-smoke-user','127.0.0.1')" | Out-Null
    Invoke-PSQL "INSERT INTO proxy_requests(user_id,service,method,path,status_code,duration_ms,bytes_out,is_websocket) VALUES($userID,'comfyui','POST','/comfyui/prompt',200,120,100,false),($userID,'comfyui','GET','/comfyui/assets/app.js',200,4,20,false),($userID,'openwebui','POST','/openwebui/api/chat/completions',200,1800,4200,false)" | Out-Null

    $app = Invoke-WebRequest -UseBasicParsing -Uri "$GatewayURL/app" -WebSession $userSession -TimeoutSec 15
    if ($app.Content -notmatch 'Запуск генерации' -or $app.Content -notmatch 'Сообщение нейросети' -or $app.Content -match 'assets/app\.js') {
        throw 'User dashboard activity is not presented in a readable, filtered form'
    }

    $adminUser = Invoke-WebRequest -UseBasicParsing -Uri "$AdminURL/admin/users/$userID" -WebSession $adminSession -TimeoutSec 15
    if ($adminUser.Content -notmatch 'Понятные действия пользователя' -or $adminUser.Content -notmatch 'Запуск генерации' -or $adminUser.Content -notmatch 'Сообщение нейросети') {
        throw 'Admin user profile does not show readable activity'
    }

    $inviteTokenHash = Get-SHA256Hex ("dashboard-smoke-invite-$suffix")
    $inviteID = Invoke-PSQL "INSERT INTO invites(token_hash,created_by_user_id,max_uses,expires_at,grant_comfyui,grant_openwebui) VALUES('$inviteTokenHash',$adminID,1,now()+interval '1 hour',true,true) RETURNING id" -Scalar | Select-Object -First 1
    $invites = Invoke-WebRequest -UseBasicParsing -Uri "$AdminURL/admin/invites" -WebSession $adminSession -TimeoutSec 15
    $csrf = Get-CSRF $invites.Content
    Invoke-WebRequest -UseBasicParsing -Method Post -Uri "$AdminURL/admin/invites/$inviteID/delete" -WebSession $adminSession -Body @{ csrf = $csrf } -TimeoutSec 15 | Out-Null
    if ([int](Invoke-PSQL "SELECT count(*) FROM invites WHERE id=$inviteID" -Scalar | Select-Object -First 1) -ne 0) {
        throw 'Invite was not deleted from the database'
    }

    Write-Host "Dashboard smoke passed: readable filtered activity and invite deletion verified for temporary user $username."
} finally {
    Invoke-PSQL "DELETE FROM audit_log WHERE user_agent IN ('dashboard-smoke-admin','dashboard-smoke-user')" | Out-Null
    Invoke-PSQL "DELETE FROM sessions WHERE token_hash IN ('$adminTokenHash','$userTokenHash')" | Out-Null
    Invoke-PSQL "DELETE FROM proxy_requests WHERE user_id=$userID" | Out-Null
    if ($inviteID -match '^\d+$') { Invoke-PSQL "DELETE FROM invites WHERE id=$inviteID" | Out-Null }
    Invoke-PSQL "DELETE FROM users WHERE id=$userID" | Out-Null
}
