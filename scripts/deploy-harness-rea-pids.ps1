$ErrorActionPreference='Stop'
$root='C:\ai-access-gateway'
$file=Join-Path $root 'docker-compose.override.yml'
$backup=Join-Path $root 'backups/harness-rea-20261010-r1/docker-compose.before-pids.yml'
function Docker([string[]]$Arguments){
 $output=& docker.exe @Arguments
 if($LASTEXITCODE -ne 0){throw 'Docker operation failed.'}
 return $output
}
$overlay=Get-Content -LiteralPath $file -Raw | ConvertFrom-Json
if($overlay.services.harness.image -ne 'ai-gateway-harness:0.2.1-alpha.2-rea-r4' -or
 (Docker @('inspect','--format','{{.HostConfig.PidsLimit}}','ai-gateway-harness')).Trim() -ne '128'){throw 'Harness baseline changed.'}
if(Test-Path -LiteralPath $backup){throw 'Backup already exists.'}
Copy-Item -LiteralPath $file -Destination $backup
try{
 $overlay.services.harness | Add-Member -NotePropertyName pids_limit -NotePropertyValue 256 -Force
 [IO.File]::WriteAllText($file,($overlay | ConvertTo-Json -Depth 100),[Text.UTF8Encoding]::new($false))
 $null=Docker @('compose','--project-directory',$root,'config','-q')
 $null=Docker @('update','--pids-limit','256','ai-gateway-harness')
 if((Docker @('inspect','--format','{{.HostConfig.PidsLimit}}','ai-gateway-harness')).Trim() -ne '256'){throw 'PID limit not applied.'}
}catch{
 Copy-Item -LiteralPath $backup -Destination $file -Force
 throw
}
'Harness PID/thread cap increased in place to 256; no service restarted.'
