param([Parameter(Mandatory)][string]$ExpectedImageId,
 [string]$Target='ai-gateway-harness:0.2.1-alpha.2-long-context-r1',
 [string]$BackupName='harness-long-context-20261010-r1')
$ErrorActionPreference='Stop'
$root='C:\ai-access-gateway'
$native='C:\ProgramData\AI-Inference-Worker'
$task='AI Access Gateway Inference Worker'
$override=Join-Path $root 'docker-compose.override.yml'
$config=Join-Path $native 'config.json'
$executable=Join-Path $native 'inference-worker.exe'
$candidate=Join-Path (Split-Path $PSScriptRoot -Parent) 'artifacts/production-release-20261010-harness-long-context-r1/inference-worker.exe'
$previous='ai-gateway-harness:0.2.1-alpha.2-gateway-ui-r2'
$restoreScript=Join-Path $PSScriptRoot 'restore-harness-model-profile.mjs'
if($BackupName -notmatch '^harness-long-context-[a-z0-9-]+$'){throw 'Invalid backup name.'}
$backup=Join-Path $root (Join-Path 'backups' $BackupName)
function Docker([string[]]$Arguments){
 $output=& docker.exe @Arguments
 if($LASTEXITCODE -ne 0){throw "Docker command failed: $($Arguments[0])"}
 return $output
}
function AssertIdle {
 $sql=@'
SELECT
 (SELECT count(*) FROM gpu_work WHERE state NOT IN ('released','cancelled')) +
 (SELECT count(*) FROM gpu_executor_permits WHERE state <> 'closed') +
 (SELECT count(*) FROM quick_generation_mining_leases);
'@
 if((Docker @('exec','ai-gateway-postgres','psql','-U','ai_gateway','-d','ai_gateway','-At','-c',$sql)).Trim() -ne '0'){throw 'GPU work active; deployment deferred.'}
}
function StopWorker {
 Stop-ScheduledTask -TaskName $task
 for($i=0;$i -lt 50;$i++){
  if(!(Get-Process -Name 'inference-worker' -ErrorAction SilentlyContinue)){return}
  Start-Sleep -Milliseconds 200
 }
 throw 'Worker did not stop; no force termination performed.'
}
function WaitWorker {
 for($i=0;$i -lt 50;$i++){
  try{$r=Invoke-RestMethod 'http://192.168.1.86:8096/healthz' -TimeoutSec 2;if($r.ready -and $r.owned -and $r.ollama_admission){return}}catch{}
  Start-Sleep -Milliseconds 500
 }
 throw 'Worker not ready.'
}
function WaitHarness {
 for($i=0;$i -lt 45;$i++){
  if((Docker @('inspect','--format','{{.State.Health.Status}}','ai-gateway-harness')).Trim() -eq 'healthy'){return}
  Start-Sleep -Seconds 2
 }
 throw 'Harness not healthy.'
}
$overlay=Get-Content -LiteralPath $override -Raw | ConvertFrom-Json
if($overlay.services.harness.image -ne $previous){throw 'Unexpected Harness image.'}
$volume=(Docker @('inspect','--format','{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}','ai-gateway-harness')).Trim()
if($volume -ne 'ai-access-gateway_harness-data'){throw 'Unexpected Harness data volume.'}
if((Docker @('image','inspect','--format','{{.Id}}',$Target)).Trim() -ne $ExpectedImageId){throw 'Verified candidate image changed.'}
if(@(Get-Process -Name inference-worker).Count -ne 1){throw 'Expected exactly one worker.'}
$actions=@((Get-ScheduledTask -TaskName $task).Actions)
if($actions.Count -ne 1 -or $actions[0].Execute.Replace('\','/') -ne $executable.Replace('\','/')){throw 'Unexpected worker task.'}
AssertIdle
if(Test-Path -LiteralPath $backup){throw 'Backup already exists.'}
$null=New-Item -ItemType Directory -Path $backup
Set-Acl -LiteralPath $backup -AclObject (Get-Acl -LiteralPath $root)
Copy-Item -LiteralPath $override -Destination (Join-Path $backup 'docker-compose.override.yml')
Copy-Item -LiteralPath $config -Destination (Join-Path $backup 'config.json')
Copy-Item -LiteralPath $executable -Destination (Join-Path $backup 'inference-worker.exe')
$null=Docker @('cp','ai-gateway-harness:/data/dsh/profiles/web/cordis.patch.yml',(Join-Path $backup 'cordis.patch.yml'))
$protected=@(Get-CimInstance Win32_Process | Where-Object {$_.Name -match 'miner|rigel|t-rex|xmrig|ollama' -or $_.Name -eq 'python.exe'} | Select-Object ProcessId,Name,CreationDate)
$gatewayId=(Docker @('inspect','--format','{{.Id}}','ai-access-gateway')).Trim()
$envHash=(Get-FileHash -LiteralPath (Join-Path $root '.env')).Hash
$changed=$false
try{
 AssertIdle
 $null=Docker @('compose','--project-directory',$root,'stop','harness')
 $changed=$true
 AssertIdle
 StopWorker
 $settings=Get-Content -LiteralPath $config -Raw | ConvertFrom-Json
 $settings | Add-Member -NotePropertyName INFERENCE_MODEL_CONTEXT_LENGTH -NotePropertyValue '131072' -Force
 $settings | Add-Member -NotePropertyName INFERENCE_MODEL_FLASH_ATTENTION -NotePropertyValue 'true' -Force
 $settings | Add-Member -NotePropertyName INFERENCE_MODEL_KV_CACHE_TYPE -NotePropertyValue 'q8_0' -Force
 [IO.File]::WriteAllText($config,($settings | ConvertTo-Json -Depth 20),[Text.UTF8Encoding]::new($false))
 Copy-Item -LiteralPath $candidate -Destination $executable -Force
 Start-ScheduledTask -TaskName $task
 WaitWorker
 $overlay.services.harness.image=$Target
 [IO.File]::WriteAllText($override,($overlay | ConvertTo-Json -Depth 100),[Text.UTF8Encoding]::new($false))
 $null=Docker @('compose','--project-directory',$root,'config','-q')
 $null=Docker @('compose','--project-directory',$root,'up','-d','--no-deps','harness')
 WaitHarness
 $check=@'
import {readFile} from 'node:fs/promises';
import {createRequire} from 'node:module';
const require=createRequire('/opt/harness/package.json');
const patch=require('yaml').parse(await readFile(process.env.DSH_HOME+'/profiles/web/cordis.patch.yml','utf8'));
const models=patch.find(x=>x.id==='llm-pi-ai').config.providers['ollama-local'].models;
if(models.some(x=>x.contextWindow!==131072||x.maxTokens!==32768))throw Error('Model budgets not migrated');
const c=patch.find(x=>x.id==='compaction-basic')?.config;
if(c?.headroomTokens!==8192||c?.retainTokens!==16384||c?.maxTokens!==8192)throw Error('Compaction policy missing');
console.log(JSON.stringify({models:models.map(x=>({id:x.id,contextWindow:x.contextWindow,maxTokens:x.maxTokens})),compaction:c}));
'@
 $null=Docker @('exec','ai-gateway-harness','node','--input-type=module','-e',$check)
 AssertIdle
 if((Docker @('inspect','--format','{{.Id}}','ai-access-gateway')).Trim() -ne $gatewayId){throw 'Gateway container changed.'}
 if((Get-FileHash -LiteralPath (Join-Path $root '.env')).Hash -ne $envHash){throw 'Environment file changed.'}
 $after=@(Get-CimInstance Win32_Process | Select-Object ProcessId,CreationDate)
 foreach($p in $protected){if(!($after | Where-Object {$_.ProcessId -eq $p.ProcessId -and $_.CreationDate -eq $p.CreationDate})){throw "Unrelated process changed: $($p.Name)"}}
}catch{
 if($changed){
  $null=Docker @('compose','--project-directory',$root,'stop','harness')
  AssertIdle
  StopWorker
  Copy-Item -LiteralPath (Join-Path $backup 'inference-worker.exe') -Destination $executable -Force
  Copy-Item -LiteralPath (Join-Path $backup 'config.json') -Destination $config -Force
  Copy-Item -LiteralPath (Join-Path $backup 'docker-compose.override.yml') -Destination $override -Force
  $null=Docker @('run','--rm','--network','none','--user','1000:1000','--entrypoint','node',
   '--mount',"type=volume,source=$volume,target=/data",'--mount',"type=bind,source=$(Join-Path $backup 'cordis.patch.yml'),target=/restore/cordis.patch.yml,readonly",
   '--mount',"type=bind,source=$restoreScript,target=/restore-profile.mjs,readonly",$previous,'/restore-profile.mjs')
  Start-ScheduledTask -TaskName $task
  $null=Docker @('compose','--project-directory',$root,'up','-d','--no-deps','harness')
 }
 throw
}
Write-Output "Harness and worker ready; rollback backup: $backup"
Write-Output 'Gateway, mining, ComfyUI and ordinary Ollama unchanged.'
