$ErrorActionPreference='Stop'
$root='C:\ai-access-gateway'
$file=Join-Path $root 'docker-compose.override.yml'
$backup=Join-Path $root 'backups/harness-rea-20261010-r1/docker-compose.before-musl.yml'
$target='ai-gateway-harness:0.2.1-alpha.2-rea-r3'
$expected='sha256:afe802f339ae22249b95d54dfec275c127a41ac0940a24de85b23e646336babd'
function Docker([string[]]$Arguments){
 $output=& docker.exe @Arguments
 if($LASTEXITCODE -ne 0){throw 'Docker operation failed.'}
 return $output
}
$overlay=Get-Content -LiteralPath $file -Raw | ConvertFrom-Json
if($overlay.services.harness.image -ne 'ai-gateway-harness:0.2.1-alpha.2-rea-r2'){throw 'Harness baseline changed.'}
if((Docker @('image','inspect','--format','{{.Id}}',$target)).Trim() -ne $expected){throw 'Candidate changed.'}
$sql="SELECT (SELECT count(*) FROM gpu_work WHERE state NOT IN ('released','cancelled'))+(SELECT count(*) FROM gpu_executor_permits WHERE state<>'closed')+(SELECT count(*) FROM quick_generation_mining_leases);"
if((Docker @('exec','ai-gateway-postgres','psql','-U','ai_gateway','-d','ai_gateway','-At','-c',$sql)).Trim() -ne '0'){throw 'GPU work active; update postponed.'}
if(Test-Path -LiteralPath $backup){throw 'Backup already exists.'}
Copy-Item -LiteralPath $file -Destination $backup
try{
 $overlay.services.harness.image=$target
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
'Harness musl loader correction deployed; other services untouched.'
