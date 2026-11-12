import assert from 'node:assert/strict';
import {mkdtemp,mkdir,writeFile,symlink,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {setTimeout as delay} from 'node:timers/promises';
import test from 'node:test';
import {createChatMemoryStore} from './chat-memory-store.mjs';
import {registerChatMemoryReview,collectMemoryEvidence,createGatewayMemoryReviewer,createMemoryReviewer} from './chat-memory-review.mjs';
import {createGPUHandoff} from './gpu-handoff.mjs';

test('review model uses a distinct retained gateway task and releases it even on failure',async()=>{
  for(const failed of [false,true]){
    const calls=[];
    const env={HARNESS_TASK_RETENTION:'true',HARNESS_MODEL_KEY:'synthetic-key',
      HARNESS_MODEL_URL:'http://gateway.invalid/internal/harness/v1',HARNESS_MODELS:'[{"id":"synthetic-model"}]'};
    const analyze=createGatewayMemoryReviewer({env,handoff:createGPUHandoff(),fetcher:async(url,request)=>{
      const body=JSON.parse(request.body),path=new URL(url).pathname;
      assert.equal(request.headers.Authorization,'Bearer synthetic-key');
      calls.push({path,body,session:request.headers['X-AI-Harness-Session']});
      if(path.endsWith('/task'))return Response.json({ok:true});
      if(failed)return Response.json({}, {status:503});
      return Response.json({choices:[{message:{content:'{"kind":"none"}'}}]});
    }});
    const answer=analyze({kind:'link',facts:[],evidence:[]},new AbortController().signal);
    if(failed)await assert.rejects(answer,/review_executor_unavailable/);
    else assert.deepEqual(await answer,{kind:'none'});
    assert.deepEqual(calls.map(x=>x.body.action??'model'),['begin','model','end']);
    assert.equal(calls[1].session,calls[0].body.sessionId);
    assert.equal(calls[2].body.taskId,calls[0].body.taskId);
    assert.equal(calls[1].body.model,'synthetic-model');
    assert.equal(calls[1].body.tools,undefined);
    assert.match(calls[1].body.messages[0].content,/Write reason in Russian/);
  }
});

async function fixture(t,analyze){
  const home=await mkdtemp(join(tmpdir(),'memory-review-test-'));
  const root=join(home,'workspace');
  await mkdir(root);
  t.after(()=>rm(home,{recursive:true,force:true}));
  const store=createChatMemoryStore({dshHome:home});
  function register(){
    let endpoint;
    registerChatMemoryReview({effect:fn=>fn(),get:()=>undefined,sessions:{get:id=>['owner','other'].includes(id)},
      connection:{fetch:{register:config=>{endpoint=config.fetch;}}}}, {store,root,analyze});
    return async (action,extra={},sessionId='owner')=>endpoint(new Request('http://synthetic.invalid/api/gateway-memory-review',{
      method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action,sessionId,...extra})})).then(response=>response.json());
  }
  return {home,root,store,call:register(),register};
}
const error = async (response,code) => assert.deepEqual(await response,{ok:false,error:{code:'review_'+code}});

test('whole chat scan covers all facts, paginates candidates and binds preview to the original revision',{skip:process.platform==='win32'},async t=>{
  let modelCalls=0;
  const {store,call}=await fixture(t,()=>{modelCalls++;return {kind:'advisory',reason:'Точное совпадение.'};});
  const first=[];
  for(let i=0;i<25;i++)first.push(await store.add('owner','Fixture duplicate '+i));
  for(let i=0;i<12;i++)await store.add('owner','Fixture large group');
  const fillers=[];
  for(let i=0;i<12;i++)fillers.push(await store.add('owner','Fixture individual '+i));
  const largeLast=await store.add('owner','Fixture large group');
  const second=[];
  for(let i=0;i<25;i++)second.push(await store.add('owner','Fixture duplicate '+i));
  const critical=await store.add('owner','Fixture duplicate 0',undefined,{status:'critical'});
  const foreign=await store.add('other','Fixture duplicate 0');
  const revision=await store.revision('owner');
  const {value:scan}=await call('scan',{kind:'merge'});
  assert.equal(scan.scanned,76);
  assert.equal(scan.total,76);
  assert.equal(scan.excluded,1);
  assert.equal(scan.revision,revision);
  assert.equal(scan.groupCount,27);
  assert.equal(scan.nextPage,1);
  await error(call('scanPage',{scanId:scan.id,page:1},'other'),'draft_expired');
  const {value:next}=await call('scanPage',{scanId:scan.id,page:1});
  assert.equal(next.nextPage,null);
  const groups=[...scan.groups,...next.groups];
  assert.ok(groups.every(group=>group.reason==='exact'&&group.ids.length>=2&&group.ids.length<=12));
  assert.ok(groups.some(group=>group.ids.includes(first[24].id)&&group.ids.includes(second[24].id)));
  const ids=groups.flatMap(group=>group.ids);
  assert.ok(ids.includes(largeLast.id));
  assert.ok(!ids.includes(critical.id)&&!ids.includes(foreign.id));
  assert.equal(await store.revision('owner'),revision);
  assert.equal(modelCalls,0);
  const selection={ids:[first[24].id,second[24].id],kind:'merge',expectedRevision:revision};
  assert.equal((await call('preview',selection)).value.state,'ready');
  assert.equal(modelCalls,1);
  assert.equal(await store.revision('owner'),revision);
  await store.setStatus('owner',fillers[0].id,'important','normal');
  await error(call('preview',selection),'stale');
  await error(call('scanPage',{scanId:scan.id,page:1}),'stale');
});

async function untilScanEnds(call,scanId){
  for(let i=0;i<200;i++){
    const {value}=await call('scanPage',{scanId,page:0});
    if(value.status!=='running')return value;
    await delay(5);
  }
  assert.fail('scan did not finish');
}

test('model analyzes every fact in the chat, including unsourced facts and facts beyond the first page', {skip:process.platform==='win32'},async t=>{
  const calls=[];
  const {store,call}=await fixture(t,async input=>{
    calls.push(input);
    if(input.mode==='advisory')return {kind:'advisory',reason:'Записи описывают одно событие.',text:'Предлагаемый факт.'};
    return {groups:[{ids:input.facts.slice(0,2).map(x=>x.id),reason:'Совпали детали.',text:'Сохранённый ответ.'},{ids:['foreign',input.facts[0].id]}]};
  });
  const expected=[];
  for(let i=0;i<65;i++)expected.push((await store.add('owner','Fixture distinct '+i)).id);
  const foreign=await store.add('other','Fixture private');
  const revision=await store.revision('owner');
  const {value:start}=await call('scan',{kind:'merge',semantic:true});
  assert.equal(start.status,'running');
  assert.equal(start.analyzed,0);
  await error(call('scanCancel',{scanId:start.id},'other'),'draft_expired');
  const finish=await untilScanEnds(call,start.id);
  assert.equal(finish.status,'done');
  assert.equal(finish.analyzed,65);
  assert.equal(finish.verified,0);
  assert.deepEqual(calls.flatMap(x=>x.facts.map(fact=>fact.id)).sort(),expected.sort());
  assert.ok(!calls.some(input=>input.facts.some(fact=>fact.id===foreign.id)));
  assert.ok(finish.groups.length>0&&finish.groups.every(group=>group.reason==='semantic'));
  assert.equal(await store.revision('owner'),revision);
  const groupIndex=finish.groups[0].groupIndex;
  const cachedArgs={scanId:start.id,groupIndex};
  await error(call('scanPreview',cachedArgs,'other'),'draft_expired');
  const requests=calls.length;
  const {value:cached}=await call('scanPreview',cachedArgs);
  assert.equal(cached.state,'unverified');
  assert.equal(cached.suggestedText,'Сохранённый ответ.');
  assert.equal(cached.reason,'Совпали детали.');
  assert.equal(cached.id,undefined);
  assert.equal(calls.length,requests);
  const {value:preview}=await call('preview',{kind:'merge',ids:finish.groups[0].ids,expectedRevision:revision});
  assert.equal(preview.state,'unverified');
  assert.equal(preview.reason,'Записи описывают одно событие.');
  assert.equal(preview.suggestedText,'Предлагаемый факт.');
  assert.equal(preview.id,undefined);
  assert.equal(await store.revision('owner'),revision);
});

test('saved proposal opens repeatedly without the model and refuses changed evidence', {skip:process.platform==='win32'},async t=>{
  let modelCalls=0;
  const {store,root,call}=await fixture(t,async input=>{
    assert.equal(input.mode,'discover');
    assert.equal(input.evidence.length,2);
    modelCalls++;
    return {groups:[{ids:input.facts.map(item=>item.id),reason:'Общий предмет.',text:'Verified combined fact'}]};
  });
  const file=join(root,'sample.md'), text='The key is brass.\nThe key is in a blue box.\n';
  await writeFile(file,text);
  for(const [i,anchor] of text.trim().split('\n').entries()){
    await store.add('owner','Note '+i,undefined,{sources:[{file:'sample.md',anchor}]});
  }
  const {value:start}=await call('scan',{kind:'merge',semantic:true});
  const result=await untilScanEnds(call,start.id);
  assert.equal(result.verified,2);
  const args={scanId:start.id,groupIndex:result.groups[0].groupIndex};
  const before=await store.list('owner');
  const {value:first}=await call('scanPreview',args);
  const {value:second}=await call('scanPreview',args);
  assert.equal(first.state,'ready');
  assert.equal(second.state,'ready');
  assert.equal(first.after.facts[0].text,second.after.facts[0].text);
  assert.equal(first.after.facts[0].text,'Verified combined fact');
  assert.equal(modelCalls,1);
  assert.deepEqual(await store.list('owner'),before);
  await writeFile(file,text+'Changed while retaining the anchors.\n');
  const {value:changed}=await call('scanPreview',args);
  assert.equal(changed.state,'source_changed');
  assert.equal(changed.suggestedText,'Verified combined fact');
  assert.equal(changed.id,undefined);
  await error(call('apply',{draftId:first.id,confirm:true}),'source_changed');
  await writeFile(file,text);
  assert.equal((await call('apply',{draftId:first.id,confirm:true})).value.state,'applied');
  assert.equal((await store.list('owner'))[0].text,'Verified combined fact');
  assert.equal((await call('undo',{operationId:first.id,confirm:true})).value.state,'undone');
  assert.deepEqual(await store.list('owner'),before);
  await error(call('scanPreview',args),'stale');
  assert.equal(modelCalls,1);
});

test('cancelling whole chat review aborts the model and never updates memory', {skip:process.platform==='win32'},async t=>{
  const {promise:started,resolve:markStarted}=Promise.withResolvers();
  const {store,call}=await fixture(t,async(_input,signal)=>{
    markStarted();
    await delay(5000,undefined,{signal});
    assert.fail('Model must be cancelled');
  });
  await store.add('owner','Fixture one');
  await store.add('owner','Fixture two');
  const revision=await store.revision('owner');
  const {value:start}=await call('scan',{kind:'merge',semantic:true});
  await started;
  await call('scanCancel',{scanId:start.id});
  const finish=await untilScanEnds(call,start.id);
  assert.equal(finish.status,'cancelled');
  assert.equal(finish.analyzed,0);
  assert.equal(await store.revision('owner'),revision);
});

test('splits a failed batch, continues past an isolated failure, and does not count failed facts as analyzed',
  {skip:process.platform==='win32'},async t=>{
    const sizes=[];
    const {store,call}=await fixture(t,async input=>{
      sizes.push(input.facts.length);
      if(input.facts.length>1||input.facts[0].text==='Synthetic unavailable')throw new Error('review_model_timeout');
      return {groups:[]};
    });
    for(const text of ['Synthetic one','Synthetic unavailable','Synthetic three'])await store.add('owner',text);
    const revision=await store.revision('owner');
    const {value:start}=await call('scan',{kind:'merge',semantic:true});
    const result=await untilScanEnds(call,start.id);
    assert.deepEqual(sizes,[3,2,1,1,1]);
    assert.equal(result.status,'partial');
    assert.equal(result.analyzed,2);
    assert.equal(result.failed,1);
    assert.equal(result.errorCode,'review_model_timeout');
    assert.equal(await store.revision('owner'),revision);
  });

test('model reader distinguishes timeouts, truncated responses and a valid fenced JSON object',async()=>{
  const cases=[
    [()=>new Response('',{status:504}),'review_model_timeout'],
    [()=>Response.json({choices:[{finish_reason:'length',message:{content:'{"groups":[]}'}}]}),'review_model_truncated'],
    [()=>Response.json({choices:[{finish_reason:'stop',message:{content:'```json\n{"groups":[]}\n```'}}]}),null],
  ];
  for(const [reply,code] of cases){
    const analyze=createMemoryReviewer('http://127.0.0.1/v1/chat/completions','fixture',{fetcher:async()=>reply()});
    const result=analyze({mode:'discover',kind:'merge',facts:[],evidence:[]},new AbortController().signal);
    if(code)await assert.rejects(result,{message:code});
    else assert.deepEqual(await result,{groups:[]});
  }
});

test('preview is read-only; confirmation merges, reserves space and rollback restores exact graph', {skip:process.platform==='win32'},async t=>{
  const {store,call,register}=await fixture(t);
  const a=await store.add('owner','Repeated fact',undefined,{aliases:['first']});
  const b=await store.add('owner','Repeated fact',undefined,{status:'important',aliases:['second']});
  const c=await store.add('owner','Unchanged neighbor');
  await store.link('owner',b.id,c.id,'depends_on');
  const before=await store.list('owner'), graphBefore=await store.graphOverview('owner'), capacity=await store.usage('owner');
  const {value}=await call('preview',{ids:[a.id,b.id],kind:'merge'});
  assert.equal(value.state,'ready');
  assert.deepEqual(await store.list('owner'),before);
  assert.equal(value.after.facts[0].id,a.id);
  assert.deepEqual(value.after.facts[0].aliases,['first','second']);
  assert.equal(value.after.facts[0].status,'important');
  assert.equal(value.after.edges[0].fromId,a.id);
  await error(call('apply',{draftId:value.id,confirm:false}),'request');
  await error(call('apply',{draftId:value.id,confirm:true},'other'),'draft_expired');
  assert.equal((await call('apply',{draftId:value.id,confirm:true})).value.state,'applied');
  const applied=await store.usage('owner');
  assert.ok(applied.reservedBytes>0);
  assert.equal(applied.remainingBytes,capacity.remainingBytes);
  const revision=await store.revision('owner');
  assert.equal((await call('apply',{draftId:value.id,confirm:true})).value.state,'applied');
  assert.equal(await store.revision('owner'),revision);
  await store.update('owner',c.id,'Later unrelated edit',c.text);
  const fresh=register();
  assert.equal((await fresh('history')).value[0].id,value.id);
  assert.equal((await fresh('undo',{operationId:value.id,confirm:true})).value.state,'undone');
  const after=await store.list('owner');
  assert.deepEqual(after.slice(0,2),before.slice(0,2));
  assert.equal(after[2].text,'Later unrelated edit');
  assert.deepEqual((await store.graphOverview('owner')).edges,graphBefore.edges);
  assert.equal((await store.usage('owner')).reservedBytes,0);
});

test('verifies exact sources, detects source and memory changes, and fails closed on conflicting rollback', {skip:process.platform==='win32'},async t=>{
  const {store,root,home,call}=await fixture(t,async input=>{
    assert.equal(input.evidence[0].lineFrom,3);
    return {kind:'link',fromId:input.facts[0].id,toId:input.facts[1].id,relation:'supports',reason:'Synthetic evidence'};
  });
  const file=join(root,'chapter.md');
  await writeFile(file,'Header\nExtra\nFirst quoted line\nSecond quoted line\n');
  const a=await store.add('owner','First note',undefined,{sources:[{file:'chapter.md',line_from:1,anchor:'First quoted line'}]});
  const b=await store.add('owner','Second note',undefined,{sources:[{file:'chapter.md',anchor:'Second quoted line'}]});
  const c=await store.add('owner','Another entry');
  const selection={ids:[a.id,b.id],kind:'link'};
  const first=(await call('preview',selection)).value;
  assert.equal(first.state,'ready');
  assert.ok(first.evidence.every(entry=>entry.sha256));
  await writeFile(file,'Header changed\nExtra\nFirst quoted line\nSecond quoted line\n');
  await error(call('apply',{draftId:first.id,confirm:true}),'source_changed');
  const second=(await call('preview',selection)).value;
  await store.setStatus('owner',a.id,'important','normal');
  await error(call('apply',{draftId:second.id,confirm:true}),'stale');
  const third=(await call('preview',selection)).value;
  assert.equal((await call('apply',{draftId:third.id,confirm:true})).value.state,'applied');
  await store.link('owner',a.id,c.id,'related');
  await error(call('undo',{operationId:third.id,confirm:true}),'undo_conflict');
  assert.equal((await store.graphOverview('owner')).totals.edges,2);
  await writeFile(join(home,'outside.md'),'First quoted line');
  await symlink(join(home,'outside.md'),join(root,'escape.md'));
  const [outside]=await collectMemoryEvidence(root,[{id:'sample',sources:[{file:'escape.md',anchor:'First quoted line'}]}]);
  assert.equal(outside.excerpt,undefined);
});
