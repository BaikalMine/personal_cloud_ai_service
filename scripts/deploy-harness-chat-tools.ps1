[CmdletBinding()]
param(
  [switch]$Execute,
  [switch]$MemoryNodes,
  [switch]$NodeBindingFix,
  [switch]$MemoryReview,
  [switch]$MemoryReviewAll,
  [switch]$MemoryReviewSemantic,
  [switch]$MemoryReviewResilience,
  [switch]$MemoryReviewCached,
  [Parameter(Mandatory)][ValidatePattern('^sha256:[a-f0-9]{64}$')][string]$AdminImage,
  [Parameter(Mandatory)][ValidatePattern('^sha256:[a-f0-9]{64}$')][string]$UsersImage
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (!$Execute) { throw 'Explicit -Execute is required.' }
$root = 'C:/ai-access-gateway'
$repo = Split-Path $PSScriptRoot -Parent
$config = Join-Path $root 'docker-compose.override.yml'
$services = @('harness','harness-users')
$old = @{
  harness = 'sha256:d6032392aa77f3a6e834523fa1d8448b37a4ab591f281b63ff383fcae2569222'
  'harness-users' = 'sha256:a9be65ff1a001093a35891a99af5019afec453e5a39e816c6b4026bb02fe9d7c'
}
if ($MemoryNodes) {
  $old = @{
    harness = 'sha256:d25a833bf806fa1656c2befd01a498eecd8d229240af674ed0b28621ed29d9f1'
    'harness-users' = 'sha256:72b567d67794cc2f078ce894f42484be7887ad1900422e8ba8d6ac2fe8aa98bf'
  }
}
if ($NodeBindingFix -or $MemoryReview) {
  $old = @{
    harness = 'sha256:23e30fb5826f6e3ffda47b08e924070028489b7299e0126aac7d1d2011ec25b9'
    'harness-users' = 'sha256:8f442dbf0e36cb316b4640afde50f696dff745085646b65ce7726f44e99b070d'
  }
}
if ($MemoryReviewAll) {
  $old = @{
    harness = 'sha256:91a5d9ed595a11d0f69d17896ae71d1bfe269cc9f303d9c76b46ed333fc4eb06'
    'harness-users' = 'sha256:31366734835480041c320e55b47ba4ca296ea86fb0e3cc78b27d612689ab6013'
  }
}
if ($MemoryReviewSemantic) {
  $old = @{
    harness = 'sha256:95d9f624cda9859b78bdf131c5f28fdd42612c4ddaddaa1e6ab90968d1883352'
    'harness-users' = 'sha256:753c4f207c53e2720367c4027ac29e1349c0359a0148c522f2791789c1997176'
  }
}
if ($MemoryReviewResilience) {
  $old = @{
    harness = 'sha256:e86494a89c970e342c793fb45085b7c5da8bb4f15f4d7f9dac8561893045b27a'
    'harness-users' = 'sha256:0b46a28e55e6abba6ebae611b7f5ef6632c75c7e066da46d7c373e18f0b49609'
  }
}
if ($MemoryReviewCached) {
  $old = @{
    harness = 'sha256:caa3a927b5825fed885965cf990e94e8f18e69cd8a22cccc41a2e2821c3dd13a'
    'harness-users' = 'sha256:87f00b4999490cf48a63b71227b2c739a74357042b25594b838ce27965d5ad04'
  }
}
$reviewResilience = $MemoryReviewResilience -or $MemoryReviewCached
$reviewSemantic = $MemoryReviewSemantic -or $reviewResilience
$reviewAll = $MemoryReviewAll -or $reviewSemantic
$new = @{ harness=$AdminImage; 'harness-users'=$UsersImage }
$effectiveUser = @{ harness='1000:1000'; 'harness-users'='0:0' }
$oldMounts = @{}

function Docker([string[]]$Arguments) {
  $out = & docker.exe @Arguments
  if ($LASTEXITCODE -ne 0) { throw "Docker $($Arguments[0]) failed ($LASTEXITCODE)." }
  return ($out -join "`n")
}
function State($service) {
  return ((Docker @('inspect','--format','{{.Image}} {{.State.Health.Status}} {{.Config.User}}',"ai-gateway-$service")).Trim() -split ' ')
}
function Mounts($service) {
  $mounts = Docker @('inspect','--format','{{json .Mounts}}',"ai-gateway-$service") | ConvertFrom-Json
  return (ConvertTo-Json -InputObject @($mounts | Sort-Object Destination) -Depth 10 -Compress)
}
function AssertIdle {
  $out = & docker.exe exec ai-gateway-harness node --input-type=module -e (Get-Content -Raw (Join-Path $repo 'integrations/harness/tools/idle-check.mjs'))
  if ($LASTEXITCODE -eq 2) { throw 'Harness is busy: admin chat. Deployment deferred.' }
  if ($LASTEXITCODE -ne 0 -or ($out | ConvertFrom-Json).running -ne 0) { throw 'Could not verify admin chat is idle.' }
  $probe = 'fetch("http://127.0.0.1:8098/control/idle",{headers:{authorization:"Bearer "+process.env.HARNESS_TENANT_KEY},signal:AbortSignal.timeout(15000)}).then(async r=>{if(!r.ok)throw Error();const x=await r.json();if(!Number.isSafeInteger(x.running)||x.running<0)throw Error();console.log(x.running)}).catch(()=>process.exit(1))'
  if ((Docker @('exec','ai-gateway-harness-users','node','-e',$probe)).Trim() -ne '0') { throw 'Harness is busy: user chat. Deployment deferred.' }
  $sql = "SELECT (SELECT count(*) FROM gpu_work WHERE state NOT IN ('released','cancelled'))+(SELECT count(*) FROM gpu_executor_permits WHERE state<>'closed')+(SELECT count(*) FROM quick_generation_mining_leases)+(SELECT count(*) FROM generation_jobs WHERE state NOT IN ('completed','failed','cancelled','expired'))+(SELECT count(*) FROM prompt_assistant_jobs WHERE state IN ('queued','running'))+(SELECT count(*) FROM gpu_inference_requests WHERE state IN ('queued','running'));"
  if ((Docker @('exec','ai-gateway-postgres','psql','-U','ai_gateway','-d','ai_gateway','-At','-c',$sql)).Trim() -ne '0') { throw 'GPU work is active. Deployment deferred.' }
}
function AssertRelease($expected) {
  $end = [DateTime]::UtcNow.AddMinutes(2)
  do {
    $ready = $true
    foreach ($service in $services) {
      $image,$health,$user = State $service
      if ($image -ne $expected[$service] -or $user -ne $effectiveUser[$service] -or (Mounts $service) -cne $oldMounts[$service]) { throw "Unexpected image, user or mounts: $service" }
      if ($health -ne 'healthy') { $ready = $false }
    }
    if ($ready) { return }
    Start-Sleep -Seconds 2
  } while ([DateTime]::UtcNow -lt $end)
  throw 'Harness health check did not pass.'
}

$hash = (Get-FileHash -LiteralPath $config).Hash
$cfg = Get-Content -Raw -LiteralPath $config | ConvertFrom-Json -AsHashtable
foreach ($service in $services) {
  $image,$health,$user = State $service
  if ($image -ne $old[$service] -or $cfg.services[$service].image -ne $old[$service] -or $health -ne 'healthy' -or $user -ne $effectiveUser[$service]) { throw "Unexpected baseline: $service" }
  if ((Docker @('image','inspect','--format','{{.Id}}',$new[$service])).Trim() -ne $new[$service]) { throw "Release image missing: $service" }
  $oldMounts[$service] = Mounts $service
}
AssertIdle
$protectedNames = @('ai-access-gateway','ai-gateway-postgres','ai-gateway-harness-ultra','ai-gateway-harness-retention')
$protectedBefore = Docker (@('inspect','--format','{{.Id}} {{.State.StartedAt}}') + $protectedNames)
$backup = Join-Path $root ('backups/harness-chat-tools-' + [DateTime]::UtcNow.ToString('yyyyMMdd-HHmmss-fff'))
$null = New-Item -ItemType Directory -Path $backup
$acl = [Security.AccessControl.DirectorySecurity]::new()
$acl.SetAccessRuleProtection($true, $false)
foreach ($sid in @([Security.Principal.WindowsIdentity]::GetCurrent().User.Value,'S-1-5-18','S-1-5-32-544')) {
  $rule = [Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($sid),'FullControl','ContainerInherit,ObjectInherit','None','Allow')
  $acl.AddAccessRule($rule)
}
Set-Acl -LiteralPath $backup -AclObject $acl
$previous = Join-Path $backup 'docker-compose.override.yml'
Copy-Item -LiteralPath $config -Destination $previous
$next = Join-Path $backup 'next.json'
foreach ($service in $services) { $cfg.services[$service].image = $new[$service] }
[IO.File]::WriteAllText($next, ($cfg | ConvertTo-Json -Depth 100), [Text.UTF8Encoding]::new($false))
$changed = $false
$recreated = $false
try {
  AssertIdle
  if ((Get-FileHash -LiteralPath $config).Hash -ne $hash) { throw 'Compose changed concurrently.' }
  [IO.File]::Replace($next,$config,[NullString]::Value)
  $changed = $true
  $null = Docker @('compose','--project-directory',$root,'config','-q')
  AssertIdle
  $recreated = $true
  $null = Docker @('compose','--project-directory',$root,'up','-d','--no-deps','--pull','never','harness','harness-users')
  AssertRelease $new
  $probe = 'const fs=require("node:fs"),p="/opt/harness/node_modules/@ai-gateway/harness-ui/";for(const [f,x] of [["client.js","function MemoryUsage"],["chat-memory-http.mjs","store.usage("],["chat-memory-tools.mjs","normalizeMemorySources"],["precise-edit-tool.mjs","precise_edit_range_requires_preview_hash"],["index.mjs","registerArchivedChatDelete"],["archive-delete.mjs","gateway-archive/delete"],["workspace-files.mjs","copyTree"]])if(!fs.readFileSync(p+f,"utf8").includes(x))process.exit(1);if(!fs.readFileSync("/opt/harness/node_modules/@deepseek-ai/dsh-client-ui-workspace/lib/client.js","utf8").includes("permanent-delete"))process.exit(1);'
  foreach ($service in $services) {
    $null = Docker @('exec',"ai-gateway-$service",'node','-e',$probe)
    if ($MemoryNodes -or $NodeBindingFix -or $MemoryReview -or $reviewAll) {
      $nodes = 'const f=require("node:fs"),p="/opt/harness/node_modules/@ai-gateway/harness-ui/";if(!f.readFileSync(p+"client.js","utf8").includes("gateway.memory-nodes")||!f.readFileSync(p+"chat-memory-tools.mjs","utf8").includes("set_status")||!f.readFileSync(p+"chat-memory-http.mjs","utf8").includes("graphOverview"))process.exit(1)'
      $null = Docker @('exec',"ai-gateway-$service",'node','-e',$nodes)
    }
    if ($MemoryReview -or $reviewAll) {
      $review = 'const f=require("node:fs"),p="/opt/harness/node_modules/@ai-gateway/harness-ui/";if(!f.readFileSync(p+"client.js","utf8").includes("gateway.memory-review")||!f.readFileSync(p+"index.mjs","utf8").includes("createGatewayMemoryReviewer")||!f.readFileSync("/opt/harness/node_modules/@deepseek-ai/dsh-compaction-basic/lib/index.js","utf8").includes("chapters as read-only"))process.exit(1)'
      $null = Docker @('exec',"ai-gateway-$service",'node','-e',$review)
    }
    if ($reviewAll) {
      $scan = 'const f=require("node:fs"),p="/opt/harness/node_modules/@ai-gateway/harness-ui/";for(const [file,marker] of [["client.js","gateway-review-candidates"],["client.js","gateway-node-dot"],["chat-memory-review.mjs","scanPage"],["chat-memory-review-scan.mjs","store.scan("]])if(!f.readFileSync(p+file,"utf8").includes(marker))process.exit(1);'
      $null = Docker @('exec',"ai-gateway-$service",'node','-e',$scan)
    }
    if ($reviewSemantic) {
      $semantic = 'const f=require("node:fs"),p="/opt/harness/node_modules/@ai-gateway/harness-ui/";for(const [file,marker] of [["client.js","semantic:true"],["client.js","advisoryTitle"],["chat-memory-review.mjs","scanCancel"],["chat-memory-review.mjs","Write reason in Russian"],["chat-memory-review-scan.mjs","memoryReviewBatches"]])if(!f.readFileSync(p+file,"utf8").includes(marker))process.exit(1);'
      $null = Docker @('exec',"ai-gateway-$service",'node','-e',$semantic)
    }
    if ($reviewResilience) {
      $resilience = 'const f=require("node:fs"),p="/opt/harness/node_modules/@ai-gateway/harness-ui/";for(const [file,marker] of [["client.js","skippedFacts"],["chat-memory-review.mjs","analyzeBatch"],["chat-memory-review.mjs","review_model_timeout"]])if(!f.readFileSync(p+file,"utf8").includes(marker))process.exit(1);'
      $null = Docker @('exec',"ai-gateway-$service",'node','-e',$resilience)
    }
    if ($MemoryReviewCached) {
      $cached = 'const f=require("node:fs"),p="/opt/harness/node_modules/@ai-gateway/harness-ui/";for(const [file,marker] of [["client.js","openCandidate"],["client.js","scanPreview"],["chat-memory-review.mjs","evidenceFingerprint"],["chat-memory-review.mjs","async function scanPreview"]])if(!f.readFileSync(p+file,"utf8").includes(marker))process.exit(1);'
      $null = Docker @('exec',"ai-gateway-$service",'node','-e',$cached)
    }
  }
  if ((Docker (@('inspect','--format','{{.Id}} {{.State.StartedAt}}') + $protectedNames)) -cne $protectedBefore) { throw 'Unexpected change in an unrelated service.' }
  Write-Output "Harness admin and users updated and healthy. Backup: $backup"
} catch {
  if ($changed) {
    try {
      $restore = Join-Path $backup 'restore.json'
      Copy-Item -LiteralPath $previous -Destination $restore
      [IO.File]::Replace($restore,$config,[NullString]::Value)
      if ($recreated) {
        $null = Docker @('compose','--project-directory',$root,'up','-d','--no-deps','--pull','never','harness','harness-users')
        AssertRelease $old
      }
    } catch { Write-Warning "Rollback requires attention. Backup: $previous" }
  }
  throw
}
