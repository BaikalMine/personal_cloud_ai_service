[CmdletBinding()]
param(
    [string]$GatewayURL = 'http://127.0.0.1:8090',
    [string]$ComfyInputDirectory = (Join-Path $env:USERPROFILE 'ComfyUI\input')
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

function New-GatewayWebSession([string]$Token) {
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $hostName = ([Uri]$GatewayURL).Host
    $session.Cookies.Add((New-Object System.Net.Cookie('gateway_session', $Token, '/', $hostName)))
    $session.Cookies.Add((New-Object System.Net.Cookie('gateway_service', 'comfyui', '/', $hostName)))
    return $session
}

function Remove-EmptySmokeParents([IO.DirectoryInfo]$Directory, [string]$Boundary) {
    while ($Directory -and
        $Directory.FullName.StartsWith($Boundary, [StringComparison]::OrdinalIgnoreCase) -and
        $Directory.FullName -ne $Boundary -and
        -not (Get-ChildItem -LiteralPath $Directory.FullName -Force | Select-Object -First 1)) {
        $parent = $Directory.Parent
        Remove-Item -LiteralPath $Directory.FullName -Force
        $Directory = $parent
    }
}

$suffix = [guid]::NewGuid().ToString('N').Substring(0, 12)
$filename = "gateway-upload-smoke-$suffix.png"
$temporaryPNG = Join-Path ([IO.Path]::GetTempPath()) $filename
$png = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII='
[IO.File]::WriteAllBytes($temporaryPNG, [Convert]::FromBase64String($png))

$userIDs = New-Object 'System.Collections.Generic.List[Int64]'
$tokens = New-Object 'System.Collections.Generic.List[string]'
$responses = New-Object 'System.Collections.Generic.List[object]'
$gatewayInputRoot = [IO.Path]::GetFullPath((Join-Path $ComfyInputDirectory 'gateway'))

try {
    for ($index = 0; $index -lt 2; $index++) {
        $username = "__upload_smoke_${index}_$suffix"
        $id = Invoke-PSQL "INSERT INTO users(username,password_hash,role) VALUES('$username','disabled-smoke-password','user') RETURNING id" -Scalar | Select-Object -First 1
        if ($id -notmatch '^\d+$') {
            throw "Could not create smoke user $index"
        }
        $userIDs.Add([int64]$id)
        $token = [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')
        $tokens.Add($token)
        $tokenHash = Get-SHA256Hex $token
        Invoke-PSQL "INSERT INTO sessions(user_id,token_hash,expires_at,user_agent,ip) VALUES($id,'$tokenHash',now()+interval '10 minutes','upload-isolation-smoke','127.0.0.1')" | Out-Null
    }

    for ($index = 0; $index -lt 2; $index++) {
        $cookie = "gateway_session=$($tokens[$index]); gateway_service=comfyui"
        $json = & curl.exe -sS --fail-with-body `
            -H "Cookie: $cookie" `
            -F "image=@$temporaryPNG;filename=$filename;type=image/png" `
            -F 'type=input' `
            "$GatewayURL/upload/image"
        if ($LASTEXITCODE -ne 0) {
            throw "Upload $index failed"
        }
        $responses.Add(($json | ConvertFrom-Json))
    }

    if ($responses[0].subfolder -eq $responses[1].subfolder -or
        $responses[0].subfolder -notlike 'gateway/gateway-*' -or
        $responses[1].subfolder -notlike 'gateway/gateway-*') {
        throw 'Uploads did not receive independent namespaces'
    }

    $ownStatuses = @()
    for ($index = 0; $index -lt 2; $index++) {
        $cookie = "gateway_session=$($tokens[$index]); gateway_service=comfyui"
        $subfolder = [Uri]::EscapeDataString($responses[$index].subfolder)
        $ownStatuses += & curl.exe -sS -o NUL -w '%{http_code}' `
            -H "Cookie: $cookie" `
            "$GatewayURL/view?filename=$filename&subfolder=$subfolder&type=input"
    }

    $foreignSubfolder = [Uri]::EscapeDataString($responses[1].subfolder)
    $crossStatus = & curl.exe -sS -o NUL -w '%{http_code}' `
        -H "Cookie: gateway_session=$($tokens[0]); gateway_service=comfyui" `
        "$GatewayURL/view?filename=$filename&subfolder=$foreignSubfolder&type=input"

    $legacyStatuses = @{}
    foreach ($storageType in 'input', 'output') {
        $storageRoot = [IO.Path]::GetFullPath((Join-Path (Split-Path -Parent $ComfyInputDirectory) $storageType))
        $legacyFile = Get-ChildItem -LiteralPath $storageRoot -Recurse -File -ErrorAction SilentlyContinue |
            Where-Object { -not $_.FullName.StartsWith((Join-Path $storageRoot 'gateway'), [StringComparison]::OrdinalIgnoreCase) } |
            Select-Object -First 1
        if ($legacyFile) {
            $relativeDirectory = $legacyFile.DirectoryName.Substring($storageRoot.Length).TrimStart('\').Replace('\', '/')
            $legacyURL = "$GatewayURL/view?filename=$([Uri]::EscapeDataString($legacyFile.Name))&type=$storageType"
            if ($relativeDirectory) {
                $legacyURL += "&subfolder=$([Uri]::EscapeDataString($relativeDirectory))"
            }
            $legacyStatuses[$storageType] = & curl.exe -sS -o NUL -w '%{http_code}' `
                -H "Cookie: gateway_session=$($tokens[0]); gateway_service=comfyui" $legacyURL
        }
    }

    $webSession = New-GatewayWebSession $tokens[0]
    $foreignPath = "$($responses[1].subfolder)/$filename"
    $promptBody = @{
        prompt = @{
            '1' = @{ class_type = 'LoadImage'; inputs = @{ image = $foreignPath } }
        }
    } | ConvertTo-Json -Depth 8 -Compress
    $promptStatus = 0
    try {
        Invoke-WebRequest -UseBasicParsing -MaximumRedirection 0 -Method Post `
            -Uri "$GatewayURL/prompt" -WebSession $webSession `
            -ContentType 'application/json' -Body $promptBody | Out-Null
        $promptStatus = 200
    }
    catch {
        $promptStatus = [int]$_.Exception.Response.StatusCode
    }

    if ($ownStatuses[0] -ne '200' -or $ownStatuses[1] -ne '200' -or
        $crossStatus -ne '404' -or $promptStatus -ne 403 -or
        ($legacyStatuses.input -and $legacyStatuses.input -ne '404') -or
        ($legacyStatuses.output -and $legacyStatuses.output -ne '404')) {
        throw "Isolation mismatch: own=$($ownStatuses -join ',') cross=$crossStatus prompt=$promptStatus legacy-input=$($legacyStatuses.input) legacy-output=$($legacyStatuses.output)"
    }

    [pscustomobject]@{
        FirstNamespace  = $responses[0].subfolder
        SecondNamespace = $responses[1].subfolder
        OwnView         = $ownStatuses -join ','
        CrossView       = $crossStatus
        ForeignPrompt   = $promptStatus
        LegacyInput     = $legacyStatuses.input
        LegacyOutput    = $legacyStatuses.output
    }
}
finally {
    if ($userIDs.Count -gt 0) {
        $idList = $userIDs -join ','
        Invoke-PSQL "DELETE FROM users WHERE id IN ($idList) AND username LIKE '\_\_upload\_smoke\_%' ESCAPE '\'" | Out-Null
    }
    if (Test-Path -LiteralPath $gatewayInputRoot -PathType Container) {
        foreach ($file in Get-ChildItem -LiteralPath $gatewayInputRoot -Recurse -File -Filter $filename -ErrorAction SilentlyContinue) {
            $fullPath = [IO.Path]::GetFullPath($file.FullName)
            if (-not $fullPath.StartsWith($gatewayInputRoot, [StringComparison]::OrdinalIgnoreCase)) {
                throw "Unsafe cleanup path: $fullPath"
            }
            $parent = $file.Directory
            Remove-Item -LiteralPath $fullPath -Force
            Remove-EmptySmokeParents $parent $gatewayInputRoot
        }
    }
    Remove-Item -LiteralPath $temporaryPNG -Force -ErrorAction SilentlyContinue
}
