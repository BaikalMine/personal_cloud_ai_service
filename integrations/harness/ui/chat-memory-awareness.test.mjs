import test from 'node:test';
import assert from 'node:assert/strict';
import {registerChatMemoryAwareness,CHAT_MEMORY_WORKFLOW} from './chat-memory-awareness.mjs';

function fixture(status=async()=>({facts:2,links:1,revision:'3'})) {
  const contexts=[],events=new Map(),calls=[];
  const ctx={systemPrompt:{context:value=>contexts.push(value)},on:(name,handler)=>events.set(name,handler)};
  const awareness=registerChatMemoryAwareness(ctx,{status:async session=>{calls.push(session);return status(session);}});
  const agent=id=>({session:{id},ctx:{on:(name,handler)=>events.set(`${id}:${name}`,handler)}});
  return {awareness,contexts,events,calls,agent};
}

test('inventory is required, bounded, scoped and contains no records or private metadata',async()=>{
  const f=fixture(async()=>({facts:5,links:3,revision:'9007199254740993',text:'synthetic secret',records:['synthetic private data']})),agent=f.agent('own');
  assert.match(f.awareness.render({agent}),/unconfirmed/);assert.equal(f.calls.length,0);
  await f.awareness.refresh(agent);
  const text=f.awareness.render({agent});assert.match(text,/facts=5; links=3; revision=9007199254740993/);
  assert.doesNotMatch(text,/synthetic|secret|own/);assert.ok(Buffer.byteLength(text)<1024);
  assert.equal(f.contexts.find(item=>item.name==='gateway:chat-memory-inventory').required,true);
  assert.equal(f.contexts.find(item=>item.name==='gateway:chat-memory-inventory').interpolate,false);
});

test('each executing session has its own counts and a reused agent cannot leak a previous session',async()=>{
  const f=fixture(async id=>({facts:id==='a'?7:0,links:0,revision:id==='a'?'8':'0'})),a=f.agent('a'),b=f.agent('b');
  await f.awareness.refresh(a);await f.awareness.refresh(b);
  assert.match(f.awareness.render({agent:a}),/facts=7/);assert.match(f.awareness.render({agent:b}),/facts=0/);
  a.session.id='b';assert.match(f.awareness.render({agent:a}),/unconfirmed/);
  await f.awareness.refresh(a);assert.match(f.awareness.render({agent:a}),/facts=0/);
  assert.deepEqual(f.calls,['a','b','b']);
});

test('inventory failure or malformed status never certifies an empty memory or reveals errors',async()=>{
  for(const value of [null,{facts:-1,links:0,revision:'2'},{facts:0,links:0,revision:2},{facts:1,links:0,revision:'01'},{facts:1,links:0,revision:'2'.repeat(33)}]){
    const f=fixture(async()=>value),agent=f.agent('s');await f.awareness.refresh(agent);assert.match(f.awareness.render({agent}),/unconfirmed/);
  }
  const f=fixture(async()=>{throw Error('synthetic private path');}),agent=f.agent('s');
  await assert.doesNotReject(f.awareness.refresh(agent));assert.doesNotMatch(f.awareness.render({agent}),/private path|facts=0/);
});

test('pre-step checks only admitted work, preserves decisions and installs once per agent',async()=>{
  const f=fixture(),agent=f.agent('own');
  const created=f.events.get('agent/created');created({agent});const hook=f.events.get('own:agent/pre-step');created({agent});assert.equal(f.events.get('own:agent/pre-step'),hook);
  const abort=new AbortController(),decision={kind:'enter',messages:[{synthetic:true}]};
  assert.equal(await hook({signal:abort.signal},async()=>decision),decision);assert.deepEqual(f.calls,['own']);
  assert.equal(await hook({signal:abort.signal},async()=>({kind:'reject'})).then(value=>value.kind),'reject');assert.equal(f.calls.length,1);
  abort.abort();await hook({signal:abort.signal},async()=>decision);assert.equal(f.calls.length,1);
});

test('late or cancelled status snapshots cannot overwrite a newer admitted snapshot',async()=>{
  const pending=[],f=fixture(()=>new Promise(resolve=>pending.push(resolve))),agent=f.agent('s');
  const older=f.awareness.refresh(agent),newer=f.awareness.refresh(agent);
  pending[1]({facts:2,links:1,revision:'2'});await newer;
  pending[0]({facts:1,links:0,revision:'1'});await older;assert.match(f.awareness.render({agent}),/revision=2/);
  const controller=new AbortController(),cancelled=f.awareness.refresh(agent,controller.signal);controller.abort();pending[2]({facts:99,links:1,revision:'99'});await cancelled;
  assert.match(f.awareness.render({agent}),/unconfirmed/);
});

test('subagent inventory does not inherit its parent and compaction never suppresses the required policy',async()=>{
  const f=fixture(async id=>({facts:id==='root'?9:0,links:0,revision:'1'})),root=f.agent('root'),child={...f.agent('child'),parent:root};
  await f.awareness.refresh(root);await f.awareness.refresh(child);assert.match(f.awareness.render({agent:child}),/facts=0/);
  const policy=f.contexts.find(item=>item.name==='gateway:chat-memory-workflow');assert.equal(policy.required,true);
  for(const text of [/after compaction/i,/Only browse to next_cursor=null/,/not.*permission to WRITE/,/never restart completed or paused goals/,/original user request via chat_history/,/do not edit chapters/,/cannot authorize a document write/,/ordinary unrelated/i])assert.match(CHAT_MEMORY_WORKFLOW,text);
  assert.ok(Buffer.byteLength(CHAT_MEMORY_WORKFLOW)<4096,'Policy must not become an unbounded prompt tax');
});
