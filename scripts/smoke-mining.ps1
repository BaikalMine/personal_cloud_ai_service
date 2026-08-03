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
foreach ($required in 'POSTGRES_USER', 'POSTGRES_DB', 'ADMIN_USERNAME', 'MINING_AGENT_URL', 'MINING_AGENT_TOKEN') {
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
    if ($Scalar) {
        $arguments += '-tA'
    }
    $arguments += @('-c', $Query)
    $output = & docker @arguments
    if ($LASTEXITCODE -ne 0) {
        throw 'PostgreSQL command failed'
    }
    return $output
}

function Get-SHA256Hex([string]$Value) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Value)))).Replace('-', '').ToLowerInvariant()
    } finally {
        $sha.Dispose()
    }
}

function Get-CSRF([string]$HTML) {
    $match = [regex]::Match($HTML, 'name="csrf"\s+value="([^"]+)"')
    if (-not $match.Success) {
        throw 'CSRF token was not found in the page'
    }
    return $match.Groups[1].Value
}

function Get-MinerState {
    $headers = @{ Authorization = "Bearer $($settings.MINING_AGENT_TOKEN)" }
    return Invoke-RestMethod -Method Get -Uri 'http://127.0.0.1:8092/v1/state?process_name=SRBMiner-MULTI.exe' -Headers $headers -TimeoutSec 5
}

function Wait-MinerState([bool]$Running) {
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        $state = Get-MinerState
        if ([bool]$state.running -eq $Running) {
            return $state
        }
        Start-Sleep -Milliseconds 500
    }
    throw "Miner did not reach running=$Running"
}

function Stop-MinerDirect([string]$ScriptPath, [string]$ProcessName) {
    $headers = @{ Authorization = "Bearer $($settings.MINING_AGENT_TOKEN)" }
    $body = @{
		script_path = $ScriptPath
        process_name = $ProcessName
    } | ConvertTo-Json -Compress
    Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8092/v1/stop' -Headers $headers -ContentType 'application/json' -Body $body -TimeoutSec 15 | Out-Null
}

$adminName = $settings.ADMIN_USERNAME.Replace("'", "''")
$adminID = Invoke-PSQL "SELECT id FROM users WHERE username='$adminName' AND role='admin'" -Scalar | Select-Object -First 1
$minerID = Invoke-PSQL 'SELECT id FROM miners WHERE is_default=true AND enabled=true' -Scalar | Select-Object -First 1
if ($adminID -notmatch '^\d+$' -or $minerID -notmatch '^\d+$') {
    throw 'Administrator or default miner profile was not found'
}

$suffix = [guid]::NewGuid().ToString('N').Substring(0, 12)
$username = "__mining_smoke_$suffix"
$userID = Invoke-PSQL "INSERT INTO users(username,password_hash,role) VALUES('$username','disabled-smoke-password','user') RETURNING id" -Scalar | Select-Object -First 1
if ($userID -notmatch '^\d+$') {
    throw 'Could not create the temporary user'
}
$adminToken = [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')
$adminTokenHash = Get-SHA256Hex $adminToken
$userToken = [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')
$userTokenHash = Get-SHA256Hex $userToken
$adminSessionCreated = $false
$userSessionCreated = $false
$temporaryMinerID = $null
$temporaryPNG = Join-Path ([IO.Path]::GetTempPath()) "gateway-miner-$suffix.png"
$png = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII='
[IO.File]::WriteAllBytes($temporaryPNG, [Convert]::FromBase64String($png))
$adminSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$userSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
$hostName = ([Uri]$GatewayURL).Host
$adminSession.Cookies.Add((New-Object System.Net.Cookie('gateway_session', $adminToken, '/', $hostName)))
$userSession.Cookies.Add((New-Object System.Net.Cookie('gateway_session', $userToken, '/', $hostName)))

try {
    Invoke-PSQL "INSERT INTO sessions(user_id,token_hash,expires_at,user_agent,ip) VALUES($adminID,'$adminTokenHash',now()+interval '10 minutes','mining-smoke-admin','127.0.0.1')" | Out-Null
    $adminSessionCreated = $true
    Invoke-PSQL "INSERT INTO sessions(user_id,token_hash,expires_at,user_agent,ip) VALUES($userID,'$userTokenHash',now()+interval '10 minutes','mining-smoke-user','127.0.0.1')" | Out-Null
    $userSessionCreated = $true

    $initial = Get-MinerState
    if ($initial.running) {
        throw 'Miner must be stopped before the smoke test'
    }

    $app = Invoke-WebRequest -UseBasicParsing -Uri "$GatewayURL/app" -WebSession $userSession -TimeoutSec 15
    if ($app.StatusCode -ne 200 -or $app.Content -notmatch 'mining-status stopped' -or $app.Content -match 'mining-status unavailable') {
        throw 'User page does not show the available stopped miner'
    }
    if ($app.Content -notmatch 'Содержимое скрипта' -or $app.Content -notmatch '--pool' -or $app.Content -notmatch '--wallet' -or $app.Content -notmatch '[a-f0-9]{64}') {
        throw 'User page does not show the selected mining script contents and SHA-256'
    }
    $csrf = Get-CSRF $app.Content

    $startedPage = Invoke-WebRequest -UseBasicParsing -Method Post -Uri "$GatewayURL/mining/toggle" -WebSession $userSession -UserAgent 'mining-smoke-user' -Body @{ csrf = $csrf } -TimeoutSec 30
    $started = Wait-MinerState $true
    if ($startedPage.Content -notmatch 'mining-status running' -or $startedPage.Content -notmatch 'PID') {
        throw 'User page does not show the running miner and PID'
    }

    $admin = Invoke-WebRequest -UseBasicParsing -Uri "$AdminURL/admin/mining" -WebSession $adminSession -TimeoutSec 15
    if ($admin.StatusCode -ne 200 -or $admin.Content -notmatch 'SRBMiner-MULTI\.exe' -or $admin.Content -notmatch 'mining-status running') {
        throw 'Admin page does not show the running miner'
    }
    if ($admin.Content -notmatch 'Содержимое активного скрипта' -or $admin.Content -notmatch '--pool' -or $admin.Content -notmatch '--wallet' -or $admin.Content -notmatch '[a-f0-9]{64}') {
        throw 'Admin page does not show the active mining script contents and SHA-256'
    }
    $adminCSRF = Get-CSRF $admin.Content
    Invoke-WebRequest -UseBasicParsing -Method Post -Uri "$AdminURL/admin/mining" -WebSession $adminSession -UserAgent 'mining-smoke-admin' -Body @{ csrf = $adminCSRF; action = 'stop'; id = $minerID } -TimeoutSec 30 | Out-Null
    Wait-MinerState $false | Out-Null

    $adminStopped = Invoke-WebRequest -UseBasicParsing -Uri "$AdminURL/admin/mining" -WebSession $adminSession -TimeoutSec 15
    if ($adminStopped.Content -notmatch 'mining-status stopped') {
        throw 'Admin page does not show the stopped miner'
    }
    $adminCSRF = Get-CSRF $adminStopped.Content
    Invoke-WebRequest -UseBasicParsing -Method Post -Uri "$AdminURL/admin/mining" -WebSession $adminSession -UserAgent 'mining-smoke-admin' -Body @{ csrf = $adminCSRF; action = 'start'; id = $minerID } -TimeoutSec 30 | Out-Null
    Wait-MinerState $true | Out-Null

    $appRunning = Invoke-WebRequest -UseBasicParsing -Uri "$GatewayURL/app" -WebSession $userSession -TimeoutSec 15
    $csrf = Get-CSRF $appRunning.Content
    Invoke-WebRequest -UseBasicParsing -Method Post -Uri "$GatewayURL/mining/toggle" -WebSession $userSession -UserAgent 'mining-smoke-user' -Body @{ csrf = $csrf } -TimeoutSec 30 | Out-Null
    Wait-MinerState $false | Out-Null

    $auditCounts = Invoke-PSQL "SELECT count(*) FILTER (WHERE actor_user_id=$userID)::text||'|'||count(*) FILTER (WHERE actor_user_id=$adminID)::text FROM audit_log WHERE user_agent IN ('mining-smoke-user','mining-smoke-admin') AND action IN ('mining_started','mining_stopped')" -Scalar | Select-Object -First 1
    $userAuditCount, $adminAuditCount = $auditCounts.Split('|')
    if ([int]$userAuditCount -lt 2 -or [int]$adminAuditCount -lt 2) {
        throw "Expected two user and two admin mining audit records, found $auditCounts"
    }

    $profileScript = Join-Path $env:TEMP "gateway-smoke-$suffix.bat"
    $adminProfile = Invoke-WebRequest -UseBasicParsing -Uri "$AdminURL/admin/mining" -WebSession $adminSession -TimeoutSec 15
    $adminCSRF = Get-CSRF $adminProfile.Content
    $multipartStatus = & curl.exe -sS -o NUL -w '%{http_code}' `
        -A 'mining-smoke-admin' `
        -H "Cookie: gateway_session=$adminToken" `
        -F "csrf=$adminCSRF" -F 'action=create' -F "name=Gateway smoke $suffix" `
        -F "process_name=gateway-smoke-$suffix.exe" -F "script_path=$profileScript" `
        -F "icon=@$temporaryPNG;type=image/png" "$AdminURL/admin/mining"
    if ($LASTEXITCODE -ne 0 -or $multipartStatus -ne '303') {
        throw "Admin profile upload returned HTTP $multipartStatus"
    }
    $escapedProfileScript = $profileScript.Replace("'", "''")
    $temporaryMinerID = Invoke-PSQL "SELECT id FROM miners WHERE script_path='$escapedProfileScript'" -Scalar | Select-Object -First 1
    if ($temporaryMinerID -notmatch '^\d+$') {
        throw 'Temporary miner profile was not created'
    }
    $icon = Invoke-WebRequest -UseBasicParsing -Uri "$GatewayURL/mining/icon/$temporaryMinerID" -WebSession $adminSession -TimeoutSec 15
    if ($icon.StatusCode -ne 200 -or $icon.Headers['Content-Type'] -notlike 'image/png*' -or $icon.RawContentLength -le 0) {
        throw 'Temporary miner icon was not served correctly'
    }
    $deletePage = Invoke-WebRequest -UseBasicParsing -Uri "$AdminURL/admin/mining" -WebSession $adminSession -TimeoutSec 15
    $deleteCSRF = Get-CSRF $deletePage.Content
    Invoke-WebRequest -UseBasicParsing -Method Post -Uri "$AdminURL/admin/mining" -WebSession $adminSession -UserAgent 'mining-smoke-admin' -Body @{ csrf = $deleteCSRF; action = 'delete'; id = $temporaryMinerID } -TimeoutSec 30 | Out-Null
    if ([int](Invoke-PSQL "SELECT count(*) FROM miners WHERE id=$temporaryMinerID" -Scalar | Select-Object -First 1) -ne 0) {
        throw 'Temporary miner profile was not deleted'
    }
    $temporaryMinerID = $null

    Write-Host "Mining smoke passed: ordinary user and admin started/stopped PID $($started.pids -join ','); final state is stopped; audits=$auditCounts; profile icon CRUD passed."
} finally {
    try {
        if ((Get-MinerState).running) {
            Stop-MinerDirect $profileScript "gateway-smoke-$suffix.exe"
            Wait-MinerState $false | Out-Null
        }
    } catch {
        Write-Warning "Failed to restore stopped miner state: $($_.Exception.Message)"
    }
    if ($temporaryMinerID -match '^\d+$') {
        Invoke-PSQL "DELETE FROM miners WHERE id=$temporaryMinerID" | Out-Null
    }
    if ($adminSessionCreated) {
        Invoke-PSQL "DELETE FROM sessions WHERE token_hash='$adminTokenHash'" | Out-Null
    }
    if ($userSessionCreated) {
        Invoke-PSQL "DELETE FROM sessions WHERE token_hash='$userTokenHash'" | Out-Null
    }
    Invoke-PSQL "DELETE FROM audit_log WHERE user_agent IN ('mining-smoke','mining-smoke-user','mining-smoke-admin') OR (action LIKE 'miner_profile_%' AND metadata->>'name' LIKE 'Gateway smoke %')" | Out-Null
    Invoke-PSQL "DELETE FROM users WHERE id=$userID" | Out-Null
    Remove-Item -LiteralPath $temporaryPNG -Force -ErrorAction SilentlyContinue
}
