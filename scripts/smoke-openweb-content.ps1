[CmdletBinding()]
param(
    [string]$GatewayURL = 'http://127.0.0.1:8090',
    [string]$AdminURL = 'http://127.0.0.1:8091',
    [int]$TimeoutSeconds = 180
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
    }
    finally {
        $sha.Dispose()
    }
}

$adminName = $settings.ADMIN_USERNAME.Replace("'", "''")
$adminID = Invoke-PSQL "SELECT id FROM users WHERE username='$adminName' AND role='admin'" -Scalar | Select-Object -First 1
if ($adminID -notmatch '^\d+$') {
    throw 'Gateway administrator was not found'
}

$models = (Invoke-RestMethod -Method Get -Uri 'http://127.0.0.1:11434/api/tags' -TimeoutSec 10).models
if (-not $models) {
    throw 'Ollama has no installed models'
}

$token = [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')
$tokenHash = Get-SHA256Hex $token
$chatID = 'gateway-content-smoke-' + [guid]::NewGuid().ToString('N')
$promptMarker = 'Gateway audit check ' + [guid]::NewGuid().ToString('N')
$eventID = $null
$savedChatID = $null
$savedChatClientID = 'gateway-chat-save-smoke-' + [guid]::NewGuid().ToString('N')
$savedUserMessageID = [guid]::NewGuid().ToString('N')
$savedAssistantMessageID = [guid]::NewGuid().ToString('N')
$savedPromptMarker = 'Gateway saved-chat audit check ' + [guid]::NewGuid().ToString('N')
$savedAnswer = 'Saved chat audit response ' + [guid]::NewGuid().ToString('N')
$savedEventID = $null
$streamChatID = $null
$streamPromptMarker = 'Gateway streaming audit check ' + [guid]::NewGuid().ToString('N')
$streamEventID = $null
$savedChatCreated = $false
$sessionCreated = $false

try {
    Invoke-PSQL "INSERT INTO sessions(user_id,token_hash,expires_at,user_agent,ip) VALUES($adminID,'$tokenHash',now()+interval '10 minutes','openweb-content-smoke','127.0.0.1')" | Out-Null
    $sessionCreated = $true

    $webSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $hostName = ([Uri]$GatewayURL).Host
    $webSession.Cookies.Add((New-Object System.Net.Cookie('gateway_session', $token, '/', $hostName)))
    $webSession.Cookies.Add((New-Object System.Net.Cookie('gateway_service', 'openwebui', '/', $hostName)))

    $signin = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Post `
        -Uri "$GatewayURL/api/v1/auths/signin" -WebSession $webSession `
        -ContentType 'application/json' -Body '{"email":"ignored@local.invalid","password":"ignored"}' `
        -TimeoutSec 30
    $identity = $signin.Content | ConvertFrom-Json
    if ($signin.StatusCode -ne 200 -or -not $identity.token -or $identity.role -ne 'user') {
        throw 'OpenWebUI trusted signin failed'
    }

    $authorization = @{ Authorization = "Bearer $($identity.token)" }
    $catalogResponse = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Get `
        -Uri "$GatewayURL/api/models" -WebSession $webSession -Headers $authorization -TimeoutSec 30
    $catalog = $catalogResponse.Content | ConvertFrom-Json
    $catalogEntries = if ($null -ne $catalog.data) {
        @($catalog.data)
    }
    elseif ($null -ne $catalog.models) {
        @($catalog.models)
    }
    else {
        @($catalog)
    }
    $catalogIDs = @($catalogEntries | ForEach-Object {
        if ($_.id) { $_.id } elseif ($_.model) { $_.model } else { $_.name }
    } | Where-Object { $_ })
    $model = $models | Where-Object { $catalogIDs -contains $_.name } | Sort-Object size | Select-Object -First 1
    if (-not $model.name) {
        $rawCatalog = [string]$catalogResponse.Content
        if ($rawCatalog.Length -gt 2000) { $rawCatalog = $rawCatalog.Substring(0, 2000) }
        throw "OpenWebUI catalog does not contain a direct Ollama model. IDs: $($catalogIDs -join ', '). Response: $rawCatalog"
    }

    $requestBody = @{
        chat_id  = $chatID
        model    = $model.name
        stream   = $false
        messages = @(@{ role = 'user'; content = "$promptMarker. Reply only with OK." })
        options  = @{ num_predict = 8; temperature = 0 }
    } | ConvertTo-Json -Depth 8 -Compress
    $response = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Post `
        -Uri "$GatewayURL/ollama/api/chat" -WebSession $webSession `
        -Headers $authorization `
        -ContentType 'application/json' -Body $requestBody -TimeoutSec $TimeoutSeconds
    if ($response.StatusCode -ne 200) {
        throw "OpenWebUI/Ollama request returned HTTP $($response.StatusCode)"
    }
    $ollamaResponse = $response.Content | ConvertFrom-Json
    $answer = [string]$ollamaResponse.message.content
    if (-not $answer.Trim()) {
        throw 'Ollama returned an empty response'
    }

    for ($attempt = 0; $attempt -lt 20 -and -not $eventID; $attempt++) {
        $eventID = Invoke-PSQL "SELECT id FROM content_events WHERE user_id=$adminID AND service='openwebui' AND external_id='$chatID' ORDER BY id DESC LIMIT 1" -Scalar | Select-Object -First 1
        if (-not $eventID) {
            Start-Sleep -Milliseconds 250
        }
    }
    if ($eventID -notmatch '^\d+$') {
        throw 'Gateway did not persist the OpenWebUI content event'
    }

    $query = [Uri]::EscapeDataString($promptMarker)
    $adminPage = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Get `
        -Uri "$AdminURL/admin/content?service=openwebui&q=$query" -WebSession $webSession -TimeoutSec 30
    if ($adminPage.StatusCode -ne 200 -or
        -not $adminPage.Content.Contains($promptMarker) -or
        -not $adminPage.Content.Contains($answer.Trim())) {
        throw 'Decrypted prompt/response was not visible in the admin content page'
    }

    $savedChat = @{
        id        = $savedChatClientID
        title     = 'Gateway saved chat smoke'
        models    = @($model.name)
        messages  = @()
        history   = @{
            currentId = $savedAssistantMessageID
            messages  = @{
                $savedUserMessageID = @{
                    id        = $savedUserMessageID
                    role      = 'user'
                    content   = $savedPromptMarker
                    timestamp = 1
                }
                $savedAssistantMessageID = @{
                    id        = $savedAssistantMessageID
                    role      = 'assistant'
                    parentId  = $savedUserMessageID
                    content   = $savedAnswer
                    model     = $model.name
                    timestamp = 2
                    done      = $true
                }
            }
        }
    }
    $savedChatBody = @{ chat = $savedChat } | ConvertTo-Json -Depth 12 -Compress
    $createChat = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Post `
        -Uri "$GatewayURL/api/v1/chats/new" -WebSession $webSession -Headers $authorization `
        -ContentType 'application/json' -Body $savedChatBody -TimeoutSec 30
    if ($createChat.StatusCode -ne 200) {
        throw "OpenWebUI chat create returned HTTP $($createChat.StatusCode)"
    }
    $createdChat = $createChat.Content | ConvertFrom-Json
    $savedChatID = [string]$createdChat.id
    if (-not $savedChatID) {
        throw 'OpenWebUI chat create did not return an ID'
    }
    $savedChat.id = $savedChatID
    $savedChatBody = @{ chat = $savedChat } | ConvertTo-Json -Depth 12 -Compress
    $savedChatCreated = $true
    $saveChat = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Post `
        -Uri "$GatewayURL/api/v1/chats/$savedChatID" -WebSession $webSession -Headers $authorization `
        -ContentType 'application/json' -Body $savedChatBody -TimeoutSec 30
    if ($saveChat.StatusCode -ne 200) {
        throw "OpenWebUI chat save returned HTTP $($saveChat.StatusCode)"
    }

    $streamChatID = $savedChatID
    $streamRequestBody = @{
        chat_id  = $streamChatID
        model    = $model.name
        stream   = $true
        messages = @(@{ role = 'user'; content = "$streamPromptMarker. Reply only with OK." })
        options  = @{ num_predict = 8; temperature = 0 }
    } | ConvertTo-Json -Depth 8 -Compress
    $streamResponse = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Post `
        -Uri "$GatewayURL/api/chat/completions" -WebSession $webSession `
        -Headers $authorization `
        -ContentType 'application/json' -Body $streamRequestBody -TimeoutSec $TimeoutSeconds
    if ($streamResponse.StatusCode -ne 200) {
        throw "OpenWebUI streaming request returned HTTP $($streamResponse.StatusCode)"
    }
    for ($attempt = 0; $attempt -lt 20 -and -not $streamEventID; $attempt++) {
        $streamEventID = Invoke-PSQL "SELECT id FROM content_events WHERE user_id=$adminID AND service='openwebui' AND external_id='$streamChatID' ORDER BY id DESC LIMIT 1" -Scalar | Select-Object -First 1
        if (-not $streamEventID) {
            Start-Sleep -Milliseconds 250
        }
    }
    if ($streamEventID -notmatch '^\d+$') {
        throw 'Gateway did not persist the streaming OpenWebUI content event'
    }
    $streamQuery = [Uri]::EscapeDataString($streamPromptMarker)
    $streamAdminPage = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Get `
        -Uri "$AdminURL/admin/content?service=openwebui&q=$streamQuery" -WebSession $webSession -TimeoutSec 30
    if ($streamAdminPage.StatusCode -ne 200 -or -not $streamAdminPage.Content.Contains($streamPromptMarker)) {
        throw 'Streaming prompt was not visible in the admin content page'
    }

    $savedExternalID = "$savedChatID`:$savedAssistantMessageID"
    for ($attempt = 0; $attempt -lt 20 -and -not $savedEventID; $attempt++) {
        $savedEventID = Invoke-PSQL "SELECT id FROM content_events WHERE user_id=$adminID AND service='openwebui' AND external_id='$savedExternalID' ORDER BY id DESC LIMIT 1" -Scalar | Select-Object -First 1
        if (-not $savedEventID) {
            Start-Sleep -Milliseconds 250
        }
    }
    if ($savedEventID -notmatch '^\d+$') {
        throw 'Gateway did not persist the OpenWebUI saved-chat content event'
    }

    $savedQuery = [Uri]::EscapeDataString($savedPromptMarker)
    $savedAdminPage = Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Get `
        -Uri "$AdminURL/admin/content?service=openwebui&q=$savedQuery" -WebSession $webSession -TimeoutSec 30
    if ($savedAdminPage.StatusCode -ne 200 -or
        -not $savedAdminPage.Content.Contains($savedPromptMarker) -or
        -not $savedAdminPage.Content.Contains($savedAnswer)) {
        throw 'Saved chat prompt/response was not visible in the admin content page'
    }

    [pscustomobject]@{
        Model               = $model.name
        ChatID              = $chatID
        OllamaHTTP          = $response.StatusCode
        CapturedEventID     = [int64]$eventID
        SavedChatEventID    = [int64]$savedEventID
        PromptVisible       = $true
        ResponseVisible     = $true
        ResponseLength      = $answer.Length
        SavedChatVisible    = $true
        StreamingEventID    = [int64]$streamEventID
        StreamingVisible    = $true
    }
}
finally {
    if ($savedChatCreated) {
        try {
            Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Delete `
                -Uri "$GatewayURL/api/v1/chats/$savedChatID" -WebSession $webSession -Headers $authorization -TimeoutSec 30 | Out-Null
        }
        catch {
            Write-Warning "Could not remove temporary OpenWebUI chat $savedChatID"
        }
    }
    Invoke-PSQL "DELETE FROM content_events WHERE user_id=$adminID AND external_id='$chatID'" | Out-Null
    Invoke-PSQL "DELETE FROM content_events WHERE user_id=$adminID AND external_id='$savedChatID`:$savedAssistantMessageID'" | Out-Null
    Invoke-PSQL "DELETE FROM content_events WHERE user_id=$adminID AND external_id='$streamChatID'" | Out-Null
    if ($sessionCreated) {
        Invoke-PSQL "DELETE FROM sessions WHERE token_hash='$tokenHash'" | Out-Null
    }
}
