[CmdletBinding()]
param(
    [string]$GatewayURL = 'http://127.0.0.1:8090',
    [string]$ComfyRoot = (Join-Path $env:USERPROFILE 'ComfyUI'),
    [int]$TimeoutSeconds = 45
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
foreach ($required in 'POSTGRES_USER', 'POSTGRES_DB') {
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

function Remove-SmokeFile([string]$Root, [string]$Candidate) {
    if (-not (Test-Path -LiteralPath $Candidate -PathType Leaf)) {
        return
    }
    $rootPath = [IO.Path]::GetFullPath($Root)
    $candidatePath = [IO.Path]::GetFullPath($Candidate)
    if (-not $candidatePath.StartsWith($rootPath, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Unsafe cleanup path: $candidatePath"
    }
    Remove-Item -LiteralPath $candidatePath -Force
}

$suffix = [guid]::NewGuid().ToString('N').Substring(0, 12)
$username = "__comfy_content_smoke_$suffix"
$filename = "gateway-content-input-$suffix.png"
$filenamePrefix = "gateway-content-smoke-$suffix"
$temporaryPNG = Join-Path ([IO.Path]::GetTempPath()) $filename
$inputRoot = Join-Path $ComfyRoot 'input'
$outputRoot = Join-Path $ComfyRoot 'output'
Add-Type -AssemblyName System.Drawing
$bitmap = New-Object Drawing.Bitmap 16, 16
try {
    $bitmap.SetPixel(0, 0, [Drawing.Color]::FromArgb(36, 170, 224))
    $bitmap.Save($temporaryPNG, [Drawing.Imaging.ImageFormat]::Png)
}
finally {
    $bitmap.Dispose()
}

$userID = $null
$upload = $null
$outputItem = $null

try {
    $userID = Invoke-PSQL "INSERT INTO users(username,password_hash,role) VALUES('$username','disabled-smoke-password','user') RETURNING id" -Scalar | Select-Object -First 1
    if ($userID -notmatch '^\d+$') {
        throw 'Could not create temporary ComfyUI user'
    }
    $token = [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')
    $tokenHash = Get-SHA256Hex $token
    Invoke-PSQL "INSERT INTO sessions(user_id,token_hash,expires_at,user_agent,ip) VALUES($userID,'$tokenHash',now()+interval '10 minutes','comfy-content-smoke','127.0.0.1')" | Out-Null
    $cookie = "gateway_session=$token; gateway_service=comfyui"

    $uploadJSON = & curl.exe -sS --fail-with-body `
        -H "Cookie: $cookie" `
        -F "image=@$temporaryPNG;filename=$filename;type=image/png" `
        -F 'type=input' `
        "$GatewayURL/upload/image"
    if ($LASTEXITCODE -ne 0) {
        throw 'ComfyUI input upload failed'
    }
    $upload = $uploadJSON | ConvertFrom-Json

    # This workflow uses only built-in image nodes: no model download or GPU generation.
    $workflow = @{
        prompt = @{
            '1' = @{ class_type = 'LoadImage'; inputs = @{ image = "$($upload.subfolder)/$filename" } }
            '2' = @{ class_type = 'SaveImage'; inputs = @{ images = @(@('1', 0)); filename_prefix = $filenamePrefix } }
        }
    } | ConvertTo-Json -Depth 10 -Compress
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $hostName = ([Uri]$GatewayURL).Host
    $session.Cookies.Add((New-Object System.Net.Cookie('gateway_session', $token, '/', $hostName)))
    $session.Cookies.Add((New-Object System.Net.Cookie('gateway_service', 'comfyui', '/', $hostName)))
    $promptResponse = Invoke-WebRequest -UseBasicParsing -Method Post `
        -Uri "$GatewayURL/prompt" -WebSession $session `
        -ContentType 'application/json' -Body $workflow -TimeoutSec 30
    $promptID = ($promptResponse.Content | ConvertFrom-Json).prompt_id
    if (-not $promptID) {
        throw 'ComfyUI did not return prompt_id'
    }

    $historyEntry = $null
    $attempts = [Math]::Ceiling($TimeoutSeconds * 2)
    for ($attempt = 0; $attempt -lt $attempts -and -not $historyEntry; $attempt++) {
        Start-Sleep -Milliseconds 500
        $historyResponse = Invoke-WebRequest -UseBasicParsing -Method Get `
            -Uri "$GatewayURL/history/$promptID" -WebSession $session -TimeoutSec 30
        $history = $historyResponse.Content | ConvertFrom-Json
        $historyEntry = $history.PSObject.Properties[$promptID].Value
    }
    if (-not $historyEntry) {
        throw 'ComfyUI workflow did not complete in time'
    }
    $outputItem = @($historyEntry.outputs.'2'.images)[0]
    if (-not $outputItem.filename) {
        throw 'ComfyUI workflow did not produce an image output'
    }

    $viewURL = "$GatewayURL/view?filename=$([Uri]::EscapeDataString($outputItem.filename))&subfolder=$([Uri]::EscapeDataString($outputItem.subfolder))&type=$([Uri]::EscapeDataString($outputItem.type))"
    $viewStatus = & curl.exe -sS -o NUL -w '%{http_code}' -H "Cookie: $cookie" $viewURL
    if ($viewStatus -ne '200') {
        throw "ComfyUI output view returned HTTP $viewStatus"
    }

    $capture = $null
    for ($attempt = 0; $attempt -lt 20 -and -not $capture; $attempt++) {
        $capture = Invoke-PSQL "SELECT e.id || '|' || (SELECT count(*) FROM content_media m WHERE m.event_id=e.id) FROM content_events e WHERE e.user_id=$userID AND e.service='comfyui' AND e.external_id='$promptID' ORDER BY e.id DESC LIMIT 1" -Scalar | Select-Object -First 1
        if (-not $capture -or $capture -notmatch '^\d+\|[1-9]\d*$') {
            $capture = $null
            Start-Sleep -Milliseconds 250
        }
    }
    if (-not $capture) {
        throw 'Gateway did not persist the ComfyUI workflow and media capture'
    }

    [pscustomobject]@{
        PromptID        = $promptID
        Output           = $outputItem.filename
        ViewHTTP         = $viewStatus
        Captured         = $capture
        WorkflowSummary  = '[workflow] LoadImage, SaveImage'
    }
}
finally {
    if ($userID -and $userID -match '^\d+$') {
        Invoke-PSQL "DELETE FROM users WHERE id=$userID AND username='$username'" | Out-Null
    }
    if ($upload -and $upload.subfolder) {
        $inputCandidate = Join-Path $inputRoot (($upload.subfolder -replace '/', '\\') + '\\' + $filename)
        Remove-SmokeFile $inputRoot $inputCandidate
    }
    if ($outputItem -and $outputItem.filename -and $outputItem.filename -like "$filenamePrefix*") {
        $outputCandidate = Join-Path $outputRoot (($outputItem.subfolder -replace '/', '\\') + '\\' + $outputItem.filename)
        Remove-SmokeFile $outputRoot $outputCandidate
    }
    Remove-Item -LiteralPath $temporaryPNG -Force -ErrorAction SilentlyContinue
}
