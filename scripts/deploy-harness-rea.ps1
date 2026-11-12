param([Parameter(Mandatory)][string]$GatewayImageId,[Parameter(Mandatory)][string]$HarnessImageId)
$ErrorActionPreference='Stop'
$root='C:\ai-access-gateway'
$native='C:\ProgramData\AI-Inference-Worker'
$task='AI Access Gateway Inference Worker'
$override=Join-Path $root 'docker-compose.override.yml'
$backup=Join-Path $root 'backups/harness-rea-20261010-r1'
$worker=Join-Path $native 'inference-worker.exe'
$candidate=Join-Path (Split-Path $PSScriptRoot -Parent) 'artifacts/production-release-20261010-harness-rea-r1/inference-worker.exe'
$gateway='ai-gateway-ready:20261010-harness-rea-r1'
$harness='ai-gateway-harness:0.2.1-alpha.2-rea-r4'
function Docker([string[]]$Arguments){
 $output=& docker.exe @Arguments
 if($LASTEXITCODE -ne 0){throw "Docker operation failed: $($Arguments[0])"}
 return $output
}
function AssertIdle {
 $sql="SELECT (SELECT count(*) FROM gpu_work WHERE state NOT IN ('released','cancelled'))+(SELECT count(*) FROM gpu_executor_permits WHERE state<>'closed')+(SELECT count(*) FROM quick_generation_mining_leases);"
 if((Docker @('exec','ai-gateway-postgres','psql','-U','ai_gateway','-d','ai_gateway','-At','-c',$sql)).Trim() -ne '0'){throw 'GPU tasks active; no deployment performed.'}
}
function StopWorker {
 Stop-ScheduledTask -TaskName $task
 for($i=0;$i -lt 50;$i++){
  if(!(Get-Process -Name inference-worker -ErrorAction SilentlyContinue)){return}
  Start-Sleep -Milliseconds 200
 }
 throw 'Worker did not stop; no force termination performed.'
}
function WaitWorker {
 for($i=0;$i -lt 50;$i++){
  try{$r=Invoke-RestMethod 'http://192.168.1.86:8096/healthz' -TimeoutSec 2;if($r.ready -and $r.owned -and $r.ollama_admission){return}}catch{}
  Start-Sleep -Milliseconds 500
 }
 throw 'Worker readiness failed.'
}
function WaitContainer([string]$Name){
 for($i=0;$i -lt 45;$i++){
  if((Docker @('inspect','--format','{{.State.Health.Status}}',$Name)).Trim() -eq 'healthy'){return}
  Start-Sleep -Seconds 2
 }
 throw "Container readiness failed: $Name"
}
$overlay=Get-Content -LiteralPath $override -Raw | ConvertFrom-Json
if($overlay.services.app.image -ne 'ai-gateway-ready:20261010-service-repair-r1' -or
 $overlay.services.harness.image -ne 'ai-gateway-harness:0.2.1-alpha.2-long-context-r1'){throw 'Production baseline changed.'}
if((Docker @('image','inspect','--format','{{.Id}}',$gateway)).Trim() -ne $GatewayImageId -or
 (Docker @('image','inspect','--format','{{.Id}}',$harness)).Trim() -ne $HarnessImageId){throw 'Candidate image changed.'}
$actions=@((Get-ScheduledTask -TaskName $task).Actions)
if($actions.Count -ne 1 -or $actions[0].Execute.Replace('\','/') -ne $worker.Replace('\','/')){throw 'Unexpected native task.'}
if(@(Get-Process -Name inference-worker).Count -ne 1){throw 'Expected exactly one native worker.'}
AssertIdle
if(Test-Path -LiteralPath $backup){throw 'Backup already exists.'}
$null=New-Item -ItemType Directory -Path $backup
Set-Acl -LiteralPath $backup -AclObject (Get-Acl -LiteralPath $root)
Copy-Item -LiteralPath $override -Destination (Join-Path $backup 'docker-compose.override.yml')
Copy-Item -LiteralPath $worker -Destination (Join-Path $backup 'inference-worker.exe')
$null=Docker @('cp','ai-gateway-harness:/data/dsh/profiles/web/cordis.patch.yml',(Join-Path $backup 'cordis.patch.yml'))
$null=Docker @('cp','ai-gateway-harness:/data/dsh/profiles/web/package.json',(Join-Path $backup 'profile-package.json'))
$protected=@(Get-CimInstance Win32_Process | Where-Object {$_.Name -match 'miner|rigel|t-rex|xmrig|ollama' -or $_.Name -eq 'python.exe'} | Select-Object ProcessId,CreationDate)
$envHash=(Get-FileHash -LiteralPath (Join-Path $root '.env')).Hash
$configHash=(Get-FileHash -LiteralPath (Join-Path $native 'config.json')).Hash
try{
 $null=Docker @('compose','--project-directory',$root,'stop','harness')
 AssertIdle
 StopWorker
 Copy-Item -LiteralPath $candidate -Destination $worker -Force
 Start-ScheduledTask -TaskName $task
 WaitWorker
 $overlay.services.app.image=$gateway
 $overlay.services.harness.image=$harness
 $overlay.services.harness | Add-Member -NotePropertyName mem_limit -NotePropertyValue '4g' -Force
 $overlay.services.harness | Add-Member -NotePropertyName pids_limit -NotePropertyValue 256 -Force
 $overlay.services.harness.environment | Add-Member -NotePropertyName HARNESS_REA_ENABLED -NotePropertyValue 'true' -Force
 [IO.File]::WriteAllText($override,($overlay | ConvertTo-Json -Depth 100),[Text.UTF8Encoding]::new($false))
 $null=Docker @('compose','--project-directory',$root,'config','-q')
 $null=Docker @('compose','--project-directory',$root,'up','-d','--no-deps','app','harness')
 WaitContainer 'ai-access-gateway'
 WaitContainer 'ai-gateway-harness'
}catch{
 $failure=$_
 $null=Docker @('compose','--project-directory',$root,'stop','harness')
 AssertIdle
 StopWorker
 Copy-Item -LiteralPath (Join-Path $backup 'inference-worker.exe') -Destination $worker -Force
 Copy-Item -LiteralPath (Join-Path $backup 'docker-compose.override.yml') -Destination $override -Force
 Start-ScheduledTask -TaskName $task
 $null=Docker @('compose','--project-directory',$root,'up','-d','--no-deps','app','harness')
 throw $failure
}
$now=@(Get-CimInstance Win32_Process | Select-Object ProcessId,CreationDate)
foreach($p in $protected){if(!($now | Where-Object {$_.ProcessId -eq $p.ProcessId -and $_.CreationDate -eq $p.CreationDate})){throw 'Unrelated process changed during deployment.'}}
if((Get-FileHash -LiteralPath (Join-Path $root '.env')).Hash -ne $envHash -or
 (Get-FileHash -LiteralPath (Join-Path $native 'config.json')).Hash -ne $configHash){throw 'Unrelated configuration changed.'}
'Harness, Gateway and owned inference worker updated; mining selection and unrelated processes preserved.'
