function Invoke-TrainingBackupTool {
    param([string] $Tool, [string[]] $ToolArguments)
    if (-not (Test-Path -LiteralPath $Tool -PathType Leaf)) {
        throw "Training backup tool is missing: $Tool. Build cmd/training-backup for Windows first."
    }
    $output = @(& $Tool @ToolArguments)
    $exitCode = $LASTEXITCODE
    try {
        $result = ($output -join "`n") | ConvertFrom-Json
    } catch {
        throw "Training backup tool returned an invalid report (exit $exitCode)."
    }
    if ($exitCode -ne 0 -or -not $result.success) {
        throw "Training backup failed: $($result.error)"
    }
    return $result.manifest
}

function Assert-TrainingBackupInventory {
    param([object] $TrainingManifest, [object[]] $DatabaseJobs)
    $jobs = [Collections.Generic.Dictionary[string, object]]::new([StringComparer]::Ordinal)
    $files = [Collections.Generic.Dictionary[string, object]]::new([StringComparer]::Ordinal)
    foreach ($job in @($TrainingManifest.jobs)) {
        if ($null -eq $job) { continue }
        $jobs.Add([string] $job.gateway_id, $job)
    }
    foreach ($file in @($TrainingManifest.files)) {
        if ($null -eq $file) { continue }
        $files.Add([string] $file.path, $file)
    }
    $verified = @()
    foreach ($row in @($DatabaseJobs)) {
        if ($null -eq $row) { continue }
        $id = [string] $row.gateway_id
        if (-not $jobs.ContainsKey($id)) {
            throw "Completed database LoRA has no archived execution/artifact: $id"
        }
        $job = $jobs[$id]
        if ([string] $row.agent_id -cne [string] $job.agent_id -or
            [string] $row.owner -cne [string] $job.owner -or
            [string] $row.profile_id -cne [string] $job.profile_id) {
            throw "Database owner/run/profile differs from archived LoRA: $id"
        }
        if (-not $files.ContainsKey([string] $job.artifact)) {
            throw "Archived LoRA file is missing from the manifest: $id"
        }
        $file = $files[[string] $job.artifact]
        # Agent artifact_name can include the Trained subfolder, not only a basename.
        $artifactName = ([string] $row.artifact_name).Replace('\', '/')
        $basename = ($artifactName -split '/')[-1]
        if ([int64] $row.artifact_bytes -ne [int64] $file.bytes -or
            $basename -cne (([string] $job.artifact -split '/')[-1])) {
            throw "Database artifact name/size differs from archived LoRA: $id"
        }
        $verified += [pscustomobject]@{
            GatewayID = $id
            AgentID = $job.agent_id
            UserID = $row.user_id
            Owner = $job.owner
            Artifact = $job.artifact
            SHA256 = $file.sha256
        }
    }
    return $verified
}
