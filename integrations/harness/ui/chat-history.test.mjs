import test from 'node:test';
import assert from 'node:assert/strict';
import {queryChatHistory,registerChatHistory} from './chat-history.mjs';

function event(seq,text,role='assistant',extra={}) {
  const message={role,source:{kind:role==='user'?'user':'model'},content:[{type:'text',text}]};
  return {seq,time:seq+100,type:role+'/message',data:role==='user'?message:{message},...extra};
}
function fixture(events,{boundary=events.length,header={id:'current',cwd:'/private/current'}}={}) {
  let released=0,observed=0;
  const ctx={sessionProjections:{stateOf(){return {lastStepStartSeq:boundary};}},sessionQuery:{async observeSession(id,options){assert.equal(id,'current');assert.equal(options.projectionMode,'none');observed++;return {header,events,cursor:events.length-1,[Symbol.dispose](){released++;}};}}};
  const exec={agent:{session:{id:'current',header:{id:'current',cwd:'/private/current'}}},signal:new AbortController().signal};
  return {ctx,exec,run:args=>queryChatHistory(ctx,args,exec),counts:()=>({released,observed})};
}
test('original messages remain searchable when a compaction replacement exists',async()=>{
  const original=event(0,'Original chapter with BLUE DOOR.');
  const correction=event(1,'Change BLUE DOOR to green.', 'user');
  const summary=event(2,'Lossy summary','user');summary.data.source={kind:'compaction'};summary.surfaceOp={op:'replace',startSeq:0,endSeq:1};
  const f=fixture([original,correction,summary]);
  const result=await f.run({action:'search',query:'blue door'});
  assert.deepEqual(result.items.map(x=>x.seq),[1,0]);
  assert.equal(result.search_complete,true);
  assert.equal((await f.run({...result.items[1].read,char_offset:0})).text,original.data.message.content[0].text);
  assert.deepEqual(f.counts(),{released:2,observed:2});
});
test('only visible original text is returned, not reasoning, tools or injected summaries',async()=>{
  const a=event(0,'visible');a.data.message.content.push({type:'reasoning',text:'PRIVATE_REASONING'},{type:'tool_call',arguments:'PRIVATE_ARGS'});
  const system={seq:1,time:1,type:'system/message',data:{message:{content:[{type:'text',text:'PRIVATE_SYSTEM'}]}}};
  const injected=event(2,'PRIVATE_INJECTED','user');injected.data.source={kind:'compaction'};
  const f=fixture([a,system,injected]);const result=await f.run({action:'list'});
  assert.equal(result.items.length,1);assert.ok(!JSON.stringify(result).includes('PRIVATE_'));
  await assert.rejects(f.run({action:'read',seq:1}),/not_original/);
  await assert.rejects(f.run({action:'read',seq:2}),/not_original/);
});
test('long Unicode multi-block messages can be read fully without gaps',async()=>{
  const first=('\u0416\u{1f600}').repeat(10000),second='second block';const a=event(0,first);
  a.data.message.content.push({type:'text',text:second});const f=fixture([a]);
  let args={action:'read',seq:0,limit:1999},parts=['',''];let pages=0;
  do {assert.ok(++pages<100);const value=await f.run(args);parts[value.block]+=value.text;args=value.next?{...value.next,limit:1999}:null;} while(args);
  assert.deepEqual(parts,[first,second]);assert.equal(f.counts().released,pages);
});
test('search pagination crosses event and text budgets without skipping matches',async()=>{
  const events=Array.from({length:1010},(_,i)=>event(i,i%200===0?'needle '+i:'ordinary'));
  events[1009]=event(1009,'x'.repeat(2200000)+'needle tail');
  const f=fixture(events);let args={action:'search',query:'needle',limit:2};const found=[];let pages=0;
  do {assert.ok(++pages<20);const value=await f.run(args);found.push(...value.items.map(x=>x.seq));args=value.next;}while(args);
  assert.deepEqual(found,[1009,1000,800,600,400,200,0]);
});
test('literal regex characters and chunk-boundary matches retain exact source offsets',async()=>{
  const f=fixture([event(0,'x'.repeat(32766)+'[a.b]* END')]);
  const result=await f.run({action:'search',query:'[a.b]*'});
  assert.equal(result.items[0].match_char_offset,32766);
});
test('current step is excluded and pagination remains inside its captured boundary',async()=>{
  const f=fixture([event(0,'old'),event(1,'old2'),event(2,'CURRENT_STEP')],{boundary:2});
  const first=await f.run({action:'list',limit:1});assert.equal(first.items[0].seq,1);
  f.ctx.sessionProjections.stateOf=()=>({lastStepStartSeq:3});
  const second=await f.run(first.next);assert.equal(second.items[0].seq,0);
  const hidden=fixture([event(0,'old'),event(1,'current')],{boundary:1});
  await assert.rejects(hidden.run({action:'read',seq:1}),/invalid_range/);
  await assert.rejects(hidden.run({action:'list',before_seq:2}),/invalid_range/);
});
test('foreign session arguments and observations are rejected; resources are released',async()=>{
  const f=fixture([event(0,'text')]);
  await assert.rejects(f.run({action:'list',session_id:'other'}),/unknown_argument/);
  assert.equal(f.counts().observed,0);
  const bad=fixture([event(0,'text')],{header:{id:'other',cwd:'/private/current'}});
  await assert.rejects(bad.run({action:'list'}),/session_mismatch/);assert.equal(bad.counts().released,1);
  const badCwd=fixture([],{header:{id:'current',cwd:'/elsewhere'}});
  await assert.rejects(badCwd.run({action:'list'}),/session_mismatch/);
});
test('missing boundary, invalid ranges and cancellation fail explicitly',async()=>{
  const f=fixture([event(0,'text')]);
  for(const args of [{action:'read',seq:-1},{action:'search',query:''},{action:'list',limit:21},{action:'read',seq:0,char_offset:500}]) await assert.rejects(f.run(args));
  f.ctx.sessionProjections.stateOf=()=>undefined;await assert.rejects(f.run({action:'list'}),/boundary/);
  const aborted=fixture([event(0,'text')]);aborted.exec.signal=AbortSignal.abort();await assert.rejects(aborted.run({action:'list'}));assert.equal(aborted.counts().observed,0);
});
test('tool registration has no caller-supplied session selector',async()=>{
  let tool,prompt;await registerChatHistory({tools:{register(x){tool=x;}},systemPrompt:{context(x){prompt=x.text;}}},{defineTool:x=>x});
  assert.equal(tool.name,'chat_history');assert.ok(!('session_id' in tool.parameters));assert.ok(prompt.includes('historical data'));
});
test('a suffix reaching EOF is not certified as a full original message',async()=>{
  const f=fixture([event(0,'PREFIX '+ 'x'.repeat(1000)+'NEEDLE')]);
  const hit=(await f.run({action:'search',query:'NEEDLE'})).items[0];
  const tail=await f.run(hit.read);
  assert.equal(tail.reached_message_end,true);assert.equal(tail.full_message_in_this_response,false);
  const full=await f.run(hit.read_from_start);assert.equal(full.full_message_in_this_response,true);assert.ok(full.text.startsWith('PREFIX'));
});
test('many empty blocks yield a continuation rather than blocking or skipping the tail',async()=>{
  const a=event(0,'');a.data.message.content=Array.from({length:5000},()=>({type:'text',text:''}));a.data.message.content.push({type:'text',text:'TAIL'});
  const f=fixture([a]);const first=await f.run({action:'search',query:'TAIL'});
  assert.equal(first.search_complete,false);assert.equal(first.items.length,0);
  const second=await f.run(first.next);assert.equal(second.items[0].block,5000);
});
