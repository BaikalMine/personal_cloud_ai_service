param(
    [string] $Destination,
    [ValidateRange(1, 100)]
    [int] $Keep = 3,
    [switch] $SkipRestoreDrill
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($Destination)) {
    $Destination = Join-Path $projectRoot 'backups'
}
$Destination = [IO.Path]::GetFullPath($Destination)
New-Item -ItemType Directory -Path $Destination -Force | Out-Null

$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$databaseName = "ai-gateway-$stamp.dump"
$openWebName = "openwebui-data-$stamp.tar.gz"
$databasePath = Join-Path $Destination $databaseName
$openWebPath = Join-Path $Destination $openWebName
$databaseTempPath = "/tmp/$databaseName"
$openWebPaused = $false

function Assert-NativeSuccess([string] $message) {
    if ($LASTEXITCODE -ne 0) {
        throw $message
    }
}

try {
    $postgresUser = (docker exec ai-gateway-postgres printenv POSTGRES_USER).Trim()
    Assert-NativeSuccess 'Cannot read the PostgreSQL user from the container.'
    $postgresDatabase = (docker exec ai-gateway-postgres printenv POSTGRES_DB).Trim()
    Assert-NativeSuccess 'Cannot read the PostgreSQL database from the container.'

    docker exec ai-gateway-postgres pg_dump -U $postgresUser -d $postgresDatabase `
        --format=custom --file=$databaseTempPath
    Assert-NativeSuccess 'PostgreSQL backup failed.'
    docker exec ai-gateway-postgres pg_restore --list $databaseTempPath | Out-Null
    Assert-NativeSuccess 'PostgreSQL backup validation failed.'
    docker cp "ai-gateway-postgres:${databaseTempPath}" $databasePath
    Assert-NativeSuccess 'Cannot copy the PostgreSQL backup to Windows.'

    docker pause openwebui | Out-Null
    Assert-NativeSuccess 'Cannot pause OpenWebUI for a consistent volume backup.'
    $openWebPaused = $true
    docker run --rm --volumes-from openwebui -v "${Destination}:/backup" `
        alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 `
        tar -czf "/backup/$openWebName" -C /app/backend/data .
    Assert-NativeSuccess 'OpenWebUI volume backup failed.'
    docker unpause openwebui | Out-Null
    Assert-NativeSuccess 'Cannot unpause OpenWebUI after backup.'
    $openWebPaused = $false

    docker run --rm -v "${Destination}:/backup:ro" `
        alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 `
        tar -tzf "/backup/$openWebName" | Out-Null
    Assert-NativeSuccess 'OpenWebUI archive validation failed.'

    $files = @($databasePath, $openWebPath) | ForEach-Object {
        $item = Get-Item -LiteralPath $_
        $hash = Get-FileHash -LiteralPath $_ -Algorithm SHA256
        [pscustomobject] [ordered]@{
            Name   = $item.Name
            Bytes  = $item.Length
            SHA256 = $hash.Hash.ToLowerInvariant()
        }
    }
    $manifest = [ordered]@{
        CreatedAt = (Get-Date).ToUniversalTime().ToString('o')
        Files     = $files
    }
    $manifestPath = Join-Path $Destination "backup-$stamp.json"
    $manifest | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $manifestPath -Encoding UTF8

    if (-not $SkipRestoreDrill) {
        & (Join-Path $PSScriptRoot 'restore.ps1') -Manifest $manifestPath
        $expiredManifests = @(Get-ChildItem -LiteralPath $Destination -Filter 'backup-*.json' -File |
            Sort-Object Name -Descending | Select-Object -Skip $Keep)
        foreach ($expiredManifest in $expiredManifests) {
            $expired = Get-Content -LiteralPath $expiredManifest.FullName -Raw -Encoding UTF8 | ConvertFrom-Json
            foreach ($file in @($expired.Files)) {
                $candidate = [IO.Path]::GetFullPath((Join-Path $Destination ([IO.Path]::GetFileName($file.Name))))
                if ([IO.Path]::GetDirectoryName($candidate) -ne $Destination) {
                    throw "Refusing to remove a backup outside $Destination"
                }
                if (Test-Path -LiteralPath $candidate -PathType Leaf) {
                    Remove-Item -LiteralPath $candidate -Force
                }
            }
            Remove-Item -LiteralPath $expiredManifest.FullName -Force
        }
    } else {
        Write-Warning 'Restore drill and backup retention were skipped; no previously verified set was removed.'
    }
    Write-Host "Backup completed: $manifestPath" -ForegroundColor Green
    $files | Format-Table -AutoSize
} finally {
    if ($openWebPaused) {
        docker unpause openwebui | Out-Null
    }
    docker exec ai-gateway-postgres rm -f $databaseTempPath 2>$null
}
