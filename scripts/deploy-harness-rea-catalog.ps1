$ErrorActionPreference='Stop'
$root='C:\ai-access-gateway'
$file=Join-Path $root 'docker-compose.override.yml'
$backup=Join-Path $root 'backups/harness-rea-20261010-r1/docker-compose.before-catalog.yml'
$target='ai-gateway-harness:0.2.1-alpha.2-rea-r4'
$expected='sha256:f7d7b2b75345f153ecfcd93a4cab3bdc283accb8affdd4d57f909be4af6a65d4'
function Docker([string[]]$Arguments){
 $output=& docker.exe @Arguments
 if($LASTEXITCODE -ne 0){throw 'Docker operation failed.'}
 return $output
}
$overlay=Get-Content -LiteralPath $file -Raw | ConvertFrom-Json
if($overlay.services.harness.image -ne 'ai-gateway-harness:0.2.1-alpha.2-rea-r3'){throw 'Harness baseline changed.'}
if((Docker @('image','inspect','--format','{{.Id}}',$target)).Trim() -ne $expected){throw 'Candidate changed.'}
# This update recreates only Harness, not the Gateway or native worker. Queued
# generation/assistant work stays with its existing owner and is not cancelled.
$sql="SELECT count(*) FROM gpu_executor_permits WHERE state<>'closed';"
if((Docker @('exec','ai-gateway-postgres','psql','-U','ai_gateway','-d','ai_gateway','-At','-c',$sql)).Trim() -ne '0'){throw 'Native model call active; update postponed.'}
if(Test-Path -LiteralPath $backup){throw 'Backup already exists.'}
Copy-Item -LiteralPath $file -Destination $backup
try{
 $overlay.services.harness.image=$target
 $overlay.services.harness | Add-Member -NotePropertyName pids_limit -NotePropertyValue 256 -Force
 [IO.File]::WriteAllText($file,($overlay | ConvertTo-Json -Depth 100),[Text.UTF8Encoding]::new($false))
 $null=Docker @('compose','--project-directory',$root,'config','-q')
 $null=Docker @('compose','--project-directory',$root,'up','-d','--no-deps','harness')
 $healthy=$false
 for($i=0;$i -lt 45;$i++){
  if((Docker @('inspect','--format','{{.State.Health.Status}}','ai-gateway-harness')).Trim() -eq 'healthy'){$healthy=$true;break}
  Start-Sleep -Seconds 2
 }
 if(!$healthy){throw 'Harness readiness failed.'}
}catch{
 Copy-Item -LiteralPath $backup -Destination $file -Force
 $null=Docker @('compose','--project-directory',$root,'up','-d','--no-deps','harness')
 throw
}
'Harness compact REA catalog deployed; other services untouched.'
