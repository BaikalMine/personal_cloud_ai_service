import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,readFile,readdir,rm,symlink,writeFile,stat} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {privateRecord,sessionEventMetadata,createPrivateDiagnostics,registerPrivateDiagnostics} from './private-diagnostics.mjs';
const secret='PRIVATE_CONTENT_MUST_NEVER_APPEAR';
const forbidden={get content(){throw Error('content accessed')},get message(){throw Error('message accessed')},get arguments(){throw Error('arguments accessed')},get objective(){throw Error('objective accessed')}};

test('event projection never reads message, arguments, reasoning, or goal text',()=>{
 const data={...Object.getOwnPropertyDescriptors(forbidden)};
 const payload=Object.defineProperties({turn:1,step:2,usage:{inputTokens:120,outputTokens:20},stream:[{type:'reasoning-chunks',get texts(){throw Error('reasoning accessed')}},{type:'chunk',chunk:{type:'finish',reason:{kind:'stop',get failure(){throw Error('error accessed')}}}}]},data);
 const projected=sessionEventMetadata({type:'assistant/message',data:payload});
 assert.equal(projected.values.input_tokens,120);assert.equal(projected.values.stop,'stop');
 const goal=Object.defineProperties({phase:'active',maxGoalRounds:256},Object.getOwnPropertyDescriptors(forbidden));
 assert.equal(sessionEventMetadata({type:'goal/change',data:{goal,roundsStarted:3}}).values.goal_state,'active');
 const error=Object.defineProperties({},Object.getOwnPropertyDescriptors(forbidden));
 assert.equal(sessionEventMetadata({type:'tool/result',data:{error}}).values.failure,true);
 const raw=privateRecord('tool_result','a'.repeat(24),{...projected.values,filename:secret,text:secret,error:secret,stop:secret,command:secret},123);
 assert.ok(!JSON.stringify(raw).includes(secret));assert.equal(raw.input_tokens,120);assert.equal(raw.stop,undefined);
 assert.equal(privateRecord(secret,'a'.repeat(24),{},123),null);
 assert.equal(privateRecord('turn_end',secret,{},123),null);
 assert.ok(privateRecord('turn_end','a'.repeat(24),{}));
});

test('journal uses keyed labels, private files, bounded rotation and no content',async t=>{
 const root=await mkdtemp(join(tmpdir(),'private-diag-'));t.after(()=>rm(root,{recursive:true,force:true}));
 const a=await createPrivateDiagnostics(join(root,'a'),{maxBytes:512});
 const label=a.label(secret);assert.match(label,/^[a-f0-9]{24}$/);assert.notEqual(label,secret);
 for(let n=0;n<40;n++)a.emit('tool_result',secret,{turn:n,failure:n%2===0,content:secret});
 await a.flush();
 const b=await createPrivateDiagnostics(join(root,'b'));
 assert.notEqual(b.label(secret),label);
 const restarted=await createPrivateDiagnostics(join(root,'a'));assert.equal(restarted.label(secret),label);
 const files=await readdir(join(root,'a'));assert.ok(files.length<=4);
 assert.ok(files.includes('events.jsonl'));assert.ok(files.includes('events.jsonl.2'));
 for(const name of files.filter(x=>x.startsWith('events.'))){
  const text=await readFile(join(root,'a',name),'utf8');assert.ok(!text.includes(secret));
  assert.ok((await stat(join(root,'a',name))).size<=512);assert.equal((await stat(join(root,'a',name))).mode&0o777,0o600);
 }
 const victim=join(root,'untouched');await writeFile(victim,secret);
 await symlink(victim,join(root,'b/events.jsonl'));b.emit('turn_start','x',{turn:1});await b.flush();
 assert.equal(await readFile(victim,'utf8'),secret);
});

test('ordinary user mode never registers diagnostics',async()=>{
 const previous={flag:process.env.HARNESS_PRIVATE_DIAGNOSTICS,user:process.env.HARNESS_USER_MODE};
 try{
  process.env.HARNESS_PRIVATE_DIAGNOSTICS='true';process.env.HARNESS_USER_MODE='true';
  await registerPrivateDiagnostics(new Proxy({},{get(){throw Error('diagnostics registered for other users')}}));
 }finally{
  for(const [key,value] of [['HARNESS_PRIVATE_DIAGNOSTICS',previous.flag],['HARNESS_USER_MODE',previous.user]])if(value===undefined)delete process.env[key];else process.env[key]=value;
 }
});

test('silent-stream diagnostics contain only timing and stop after stream cleanup',async t=>{
 const root=await mkdtemp(join(tmpdir(),'private-wait-'));t.after(()=>rm(root,{recursive:true,force:true}));
 const keys=['HARNESS_PRIVATE_DIAGNOSTICS','HARNESS_USER_MODE','DSH_HOME'];
 const previous=keys.map(key=>process.env[key]);
 let streamHook,flush,finish;
 const gate=new Promise(resolve=>{finish=resolve;});
 try{
  process.env.HARNESS_PRIVATE_DIAGNOSTICS='true';process.env.HARNESS_USER_MODE='false';process.env.DSH_HOME=root;
  await registerPrivateDiagnostics({logger:{warn:()=>{}},effect:fn=>{flush=fn();},on:(event,fn)=>{if(event==='llm/stream')streamHook=fn;}});
  t.mock.timers.enable({apis:['setInterval','Date'],now:100000});
  const stream=streamHook({sessionId:secret},async function*(){yield {type:'text-delta',text:secret};await gate;});
  await stream.next();
  const pending=stream.next();
  t.mock.timers.tick(60000);
  finish();await pending;await flush();
  const path=join(root,'private-diagnostics/events.jsonl');
  const before=await readFile(path,'utf8');
  const records=before.trim().split('\n').map(JSON.parse);
  const waiting=records.filter(row=>row.event==='model_wait');
  assert.equal(waiting.length,1);assert.equal(waiting[0].chunks,1);assert.equal(waiting[0].idle_ms,60000);
  assert.ok(!before.includes(secret));
  t.mock.timers.tick(120000);await flush();
  assert.equal(await readFile(path,'utf8'),before);
 }finally{
  finish();t.mock.timers.reset();
  for(let i=0;i<keys.length;i++)if(previous[i]===undefined)delete process.env[keys[i]];else process.env[keys[i]]=previous[i];
 }
});
