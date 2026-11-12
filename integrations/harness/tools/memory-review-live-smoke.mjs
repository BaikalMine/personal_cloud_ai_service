import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readFile, rmdir, unlink, writeFile } from 'node:fs/promises';
import { request } from 'node:http';
import { networkInterfaces } from 'node:os';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const url = new URL(process.env.HARNESS_PUBLIC_URL);
const host = process.env.HARNESS_BIND_HOST || Object.values(networkInterfaces()).flat().find(x=>x.family==='IPv4'&&!x.internal)?.address;
const sessionId = 'memory-review-smoke-' + randomUUID();
const path = '/api/gateway-memory-review';
const headers = {Host:url.host,Origin:url.origin,'Content-Type':'application/json'};
const check = (ok,reason) => { if(!ok)throw new Error(reason); };
function call(port,route,auth,body,timeout=20_000) {
  return new Promise((resolve,reject)=>{
    const req=request({host,port,path:route,headers:auth,method:body?'POST':'GET'},res=>{
      const chunks=[];
      res.on('data',chunk=>chunks.push(chunk));
      res.on('error',reject);
      res.on('end',()=>{
        let data;
        try{data=JSON.parse(Buffer.concat(chunks).toString('utf8'));}catch{}
        resolve({status:res.statusCode,data});
      });
    });
    req.setTimeout(timeout,()=>req.destroy(new Error('Timeout: '+route)));
    req.on('error',reject);
    req.end(body?JSON.stringify(body):undefined);
  });
}
async function rpc(method,fields){
  const {status,data}=await call(3080,'/api/'+method,headers,
    {type:'client-request',rpcId:randomUUID(),method,payload:{args:{request:{sessionId,...fields}}}});
  check(status===200&&data?.result?.ok,method+': HTTP '+status);
}
async function post(route,action,fields,timeout){
  const {status,data}=await call(3080,route,headers,{sessionId,action,...fields},timeout);
  const code=data?.error?.code;
  check(status===200&&data?.ok,action+': HTTP '+status+(typeof code==='string'&&/^review_[a-z_]+$/.test(code)?' '+code:''));
  return data.value;
}
const memory=(action,fields)=>post('/api/gateway-chat-memory/manage',action,fields);
const review=(action,fields,timeout)=>post(path,action,fields,timeout);
const workspace=process.env.HARNESS_WORKSPACE||'/workspace';
const cachedMode=process.env.HARNESS_MEMORY_SMOKE_CACHED==='1';
const applyMode=cachedMode||process.env.HARNESS_MEMORY_SMOKE_APPLY==='1';
const fixtureDirectory=join(workspace,sessionId);
const fixtureFile=join(fixtureDirectory,'evidence.md');
let directoryCreated=false, sourceCreated=false;

async function snapshot(){
  const {records,nextCursor}=await memory('list');
  check(nextCursor===null,'Unexpected extra page of synthetic facts');
  const overview=await memory('graphOverview',{page:0});
  check(overview.nextPage===null,'Unexpected extra page of synthetic links');
  const edges=new Map();
  for(const {id} of records){
    const graph=await memory('graph',{id,expectedRevision:overview.revision});
    check(graph.nextCursor===null,'Unexpected extra page of synthetic node links');
    for(const edge of graph.edges)edges.set(edge.id,edge);
  }
  check(overview.totals.nodes===records.length&&overview.totals.edges===edges.size,'Incomplete synthetic snapshot');
  return {records,edges:[...edges.values()].sort((a,b)=>a.id.localeCompare(b.id))};
}

async function verifyApplyUndo(){
  const anchors=[
    'The observatory stores its spare brass key in a blue box.',
    'The observatory keeps that blue box in the north cabinet.',
  ];
  const source=anchors.join('\n')+'\n';
  const sha256=createHash('sha256').update(source).digest('hex');
  await writeFile(fixtureFile,source,{flag:'wx',mode:0o600});
  sourceCreated=true;
  const ids=[];
  const other=cachedMode?['An unrelated greenhouse uses drip irrigation.','A museum exhibit shows a fossil.']:
    ['The observatory keeps a key inventory.','The north cabinet has a paper label.'];
  for(const [i,text] of [...anchors,...other].entries()){
    const fields=i>=2?{text}:{text,aliases:[i?'QA north cabinet':'QA spare key'],
      sources:[{file:sessionId+'/evidence.md',anchor:text,sha256,line_from:i+1,line_to:i+1}]};
    ids.push((await memory('add',fields)).record.id);
  }
  const [first,second,inventory,label]=ids;
  await memory('setStatus',{id:second,status:'important',expectedStatus:'normal'});
  const edgeInputs=[
    [first,second,'related','Internal link restored on undo'],
    [second,inventory,'supports','Outgoing link reattached during merge'],
    [label,second,'depends_on','Incoming link reattached during merge'],
    [inventory,label,'related','Unrelated link must not change'],
  ];
  const linked=[];
  for(const [fromId,toId,relation,note] of edgeInputs)linked.push((await memory('link',{fromId,toId,relation,note})).edge);
  const before=await snapshot();
  const {revision}=await memory('revision');
  let preview;
  if(cachedMode){
    const analysisStart=new Date().toISOString();
    const scan=await review('scan',{kind:'merge',semantic:true});
    scanId=scan.id;
    let result=scan;
    for(let i=0;i<120&&result.status==='running';i++){
      await delay(2000);
      result=await review('scanPage',{scanId,page:0});
    }
    check(result.status==='done'&&result.analyzed===4&&result.verified===2,
      'Full analysis did not complete on the synthetic facts: '+result.status+' '+result.analyzed+'/4');
    const group=result.groups.find(candidate=>candidate.hasProposal&&candidate.reason==='semantic'&&
      candidate.ids.length===2&&candidate.ids.includes(first)&&candidate.ids.includes(second));
    check(group&&typeof group.note==='string'&&/\p{Script=Cyrillic}/u.test(group.note),'Model did not return a saved proposal with Russian reasoning');
    const previewStart=new Date().toISOString();
    const input={scanId,groupIndex:group.groupIndex};
    preview=await review('scanPreview',input);
    const reopened=await review('scanPreview',input);
    const previewEnd=new Date().toISOString();
    check(preview.state==='ready'&&reopened.state==='ready','Saved proposal was not ready on both opens');
    check(preview.reason===group.note&&reopened.reason===preview.reason&&
      reopened.after.facts[0].text===preview.after.facts[0].text,'Saved model output changed on reopening');
    const merged=preview.after.facts[0].text.toLowerCase();
    check(['brass','blue box','north cabinet'].every(detail=>merged.includes(detail)),'Model output lost synthetic source details');
    console.log('Cached proposal opened twice with identical text and reasoning.');
    console.log('QA_PERMIT_WINDOWS '+JSON.stringify({analysisStart,previewStart,previewEnd}));
  }else preview=await review('preview',{ids:[first,second],kind:'merge',expectedRevision:revision},330_000);
  check(preview.state==='ready'&&!!preview.id,'Verified model proposal did not produce a draft: '+preview.state);
  check(preview.evidence.length===2&&preview.evidence.every((entry,i)=>entry.sha256===sha256&&entry.excerpt===anchors[i]&&entry.lineFrom===i+1),
    'Both sources must be confirmed against the synthetic file');
  check(preview.before.facts.length===2&&preview.after.facts.length===1&&preview.after.facts[0].text.trim(),
    'Unexpected preview of a verified merge');
  assert.deepEqual(await memory('revision'),{revision},'Preview must be read-only');
  assert.deepEqual(await snapshot(),before,'Preview changed facts or edges');
  console.log('Real-model preview: 2 confirmed source excerpts; no memory changes.');
  if(cachedMode){
    assert.equal(await readFile(fixtureFile,'utf8'),source,'Review modified the source file');
    return;
  }
  const refused=await call(3080,path,headers,{sessionId,action:'apply',draftId:preview.id,confirm:false});
  check(refused.data?.ok===false&&refused.data.error?.code==='review_request','Apply without confirmation was not rejected');
  assert.deepEqual(await memory('revision'),{revision},'Unconfirmed apply changed memory');
  const applied=await review('apply',{draftId:preview.id,confirm:true});
  check(applied.state==='applied','Apply did not commit the proposal');
  try{
    const after=await snapshot();
    const merged=after.records.find(record=>record.id===first);
    check(after.records.length===3&&after.edges.length===3&&!!merged&&!after.records.some(record=>record.id===second),
      'Unexpected fact or edge count after confirmed merge');
    check(merged.status==='important'&&merged.sources?.length===2&&merged.aliases?.length===2,'Merged metadata was lost');
    assert.deepEqual(after.edges,[
      {...linked[1],fromId:first},{...linked[2],toId:first},linked[3],
    ].sort((x,y)=>x.id.localeCompare(y.id)),'Confirmed merge changed or dropped an external link');
    console.log('Apply: 4 to 3 facts, 4 to 3 links; external links and metadata preserved.');
  }finally{
    const undo=await review('undo',{operationId:preview.id,confirm:true});
    check(undo.state==='undone','Undo did not restore the proposal');
  }
  assert.deepEqual(await snapshot(),before,'Undo did not restore exact original facts and links');
  const history=await review('history');
  check(history.some(op=>op.id===preview.id&&op.state==='undone'),'Undo not recorded in history');
  assert.equal(await readFile(fixtureFile,'utf8'),source,'Review modified the source file');
  console.log('Undo: all 4 facts and all 4 links match the original snapshot; source unchanged.');
}
let created=false, scanId, failed=false;
try{
  const {status,data}=await call(3082,'/session?host='+encodeURIComponent(url.host),
    {Authorization:'Bearer '+process.env.HARNESS_BOOTSTRAP_KEY});
  check(status===200&&data?.cookie,'Could not authenticate test chat: HTTP '+status);
  headers.Cookie=data.cookie.split(';')[0];
  if(applyMode){await mkdir(fixtureDirectory,{mode:0o700});directoryCreated=true;}
  await rpc('session/create',{cwd:applyMode?fixtureDirectory:workspace});
  created=true;
  await rpc('session/rename',{title:'QA: Memory review (synthetic)'});
  if(applyMode)await verifyApplyUndo();
  else{
  const facts=[
    'The fictional observatory stores its spare brass key in a blue box.',
    'The blue box holds the fictional observatory\'s spare brass key.',
    ...Array.from({length:8},(_,i)=>'Synthetic station '+(i+1)+' has a numbered test beacon.'),
  ];
  if(process.env.HARNESS_MEMORY_SMOKE_LARGE==='1'){
    for(let i=0;i<6;i++)facts.splice(2+i,1,'Учебная запись №'+(i+1)+'. '+('В вымышленной лаборатории проверили показания датчиков. Результат записан в журнал наблюдений. ').repeat(28));
    for(let i=facts.length;i<64;i++)facts.push('Синтетический тестовый факт №'+(i+1)+': резервный датчик находится на отдельной испытательной площадке.');
  }
  const ids=[];
  for(const text of facts)ids.push((await memory('add',{text})).record.id);
  const before=await memory('revision');
  try{
    const result=await review('preview',{kind:'merge',ids:ids.slice(0,2)},330_000);
    check(result.state==='unverified'&&typeof result.reason==='string'&&/\p{Script=Cyrillic}/u.test(result.reason)&&!result.id,
      'Manual preview did not return a Russian advisory: '+result.state);
    console.log('Manual: Russian advisory, no write draft.');
  }catch(error){console.log('Manual: '+error.message);failed=true;}
  try{
    const start=await review('scan',{kind:'merge',semantic:true});
    scanId=start.id;
    let result=start;
    let lastAnalyzed=0;
    for(let i=0;i<180&&result.status==='running';i++){
      await delay(3000);
      result=await review('scanPage',{scanId,page:0});
      if(facts.length>10&&result.analyzed!==lastAnalyzed){lastAnalyzed=result.analyzed;console.log('Model progress: '+lastAnalyzed+'/'+facts.length+'.');}
    }
    check(result.status==='done'&&result.analyzed===facts.length,
      'Whole chat: '+result.status+', '+result.analyzed+'/'+facts.length+(result.errorCode?' '+result.errorCode:''));
    console.log('Whole chat: '+result.analyzed+'/'+facts.length+', proposals='+result.groupCount+'.');
  }catch(error){console.log(error.message);failed=true;}
  assert.deepEqual(await memory('revision'),before,'Synthetic memory changed without confirmation');
  }
}catch(error){console.log(error.message);failed=true;}
finally{
  if(created){
    if(scanId)await review('scanCancel',{scanId}).catch(()=>{});
    try{
      await rpc('workspace/archiveSession',{});
      const {status,data}=await call(3080,'/api/gateway-archive/delete',headers,{sessionId,confirm:true});
      check(status===200&&data?.ok,'Test chat cleanup failed: HTTP '+status);
      console.log('Synthetic chat removed; existing chats untouched.');
    }catch(error){console.log(error.message+' ('+sessionId+')');failed=true;}
  }
  try{
    if(sourceCreated)await unlink(fixtureFile);
    if(directoryCreated)await rmdir(fixtureDirectory);
  }catch(error){console.log('Synthetic source cleanup failed: '+error.code);failed=true;}
}
if(failed)process.exitCode=1;
