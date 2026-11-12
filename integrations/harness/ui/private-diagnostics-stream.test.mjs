import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,readFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {privateRecord,registerPrivateDiagnostics} from './private-diagnostics.mjs';

const secret='PRIVATE_STREAM_PAYLOAD_MUST_NOT_APPEAR';

async function streamDiagnostics(t) {
  const root=await mkdtemp(join(tmpdir(),'private-stream-'));
  t.after(()=>rm(root,{recursive:true,force:true}));
  const keys=['HARNESS_PRIVATE_DIAGNOSTICS','HARNESS_USER_MODE','DSH_HOME'];
  const previous=keys.map(key=>process.env[key]);
  let hook,flush;
  const warnings=[];
  try{
    process.env.HARNESS_PRIVATE_DIAGNOSTICS='true';
    process.env.HARNESS_USER_MODE='false';process.env.DSH_HOME=root;
    await registerPrivateDiagnostics({
      logger:{warn:message=>warnings.push(message)},
      effect:fn=>{flush=fn();},
      on:(event,fn)=>{if(event==='llm/stream')hook=fn;},
    });
  }finally{
    for(let i=0;i<keys.length;i++)if(previous[i]===undefined)delete process.env[keys[i]];else process.env[keys[i]]=previous[i];
  }
  t.mock.timers.enable({apis:['setInterval','Date'],now:100000});
  t.after(()=>t.mock.timers.reset());
  const denied=[];
  t.after(()=>assert.deepEqual(denied,[],'no payload properties or enumeration accessed'));
  const guard=(value,allowed)=>new Proxy(value,{
    get(target,key){
      // Async-generator transport probes then; it is not diagnostic metadata.
      if(key==='then')return undefined;
      if(allowed.includes(key))return Reflect.get(target,key);
      denied.push(String(key));throw Error('Forbidden payload access');
    },
    ownKeys(){denied.push('ownKeys');throw Error('Forbidden payload enumeration');},
  });
  return {
    stream:(source,options={})=>hook({sessionId:secret,...options},source),
    chunk:type=>guard({type,text:secret,name:secret,argumentsDelta:secret},['type']),
    finish:kind=>guard({type:'finish',reason:guard({kind,failure:secret},['kind'])},['type','reason']),
    guard,
    records:async()=>{
      await flush();assert.deepEqual(warnings,[]);
      const raw=await readFile(join(root,'private-diagnostics/events.jsonl'),'utf8');
      assert.ok(!raw.includes(secret));
      return raw.trim().split('\n').map(JSON.parse);
    },
  };
}

function counts(row) {
  return [row.reasoning_chunks,row.text_chunks,row.tool_call_chunks,row.finish_seen,row.stop];
}

test('stream schema strictly allowlists bounded counters, boolean finish and known stops',()=>{
  const session='a'.repeat(24);
  const row=privateRecord('model_end',session,{
    reasoning_chunks:1,text_chunks:0,tool_call_chunks:1e12,finish_seen:true,
    stop:'max-tokens',text:secret,reasoning:secret,name:secret,arguments:secret,result:secret,
  },123);
  assert.deepEqual(row,{
    schema:1,at_ms:123,session,event:'model_end',
    reasoning_chunks:1,text_chunks:0,tool_call_chunks:1e12,finish_seen:true,stop:'max-tokens',
  });
  for(const invalid of [-1,0.5,1e12+1,NaN,Infinity,'1',null,{},true]){
    const filtered=privateRecord('model_end',session,{
      reasoning_chunks:invalid,text_chunks:invalid,tool_call_chunks:invalid,
      finish_seen:invalid===true?'true':invalid,stop:secret,
    },123);
    for(const key of ['reasoning_chunks','text_chunks','tool_call_chunks','finish_seen','stop'])assert.equal(Object.hasOwn(filtered,key),false);
  }
  assert.equal(privateRecord('model_end',session,{finish_seen:false},123).finish_seen,false);
});

test('stream counters distinguish reasoning-only, tool-call and text deltas per request',async t=>{
  const d=await streamDiagnostics(t);
  const cases=[
    {types:['reasoning-delta','reasoning-delta'],stop:'stop',expected:[2,0,0,true,'stop']},
    {types:['reasoning-delta','tool-call-delta','tool-call-delta'],stop:'tool-calls',expected:[1,0,2,true,'tool-calls']},
    {types:['text-delta','text-delta','block-end','usage'],stop:'max-tokens',expected:[0,2,0,true,'max-tokens']},
    {types:[],stop:'stop',expected:[0,0,0,true,'stop']},
  ];
  for(const scenario of cases){
    const chunks=[...scenario.types.map(d.chunk),d.finish(scenario.stop)];
    let cleaned=false,index=0;
    const stream=d.stream(async function*(){try{yield* chunks;}finally{cleaned=true;}});
    for await(const chunk of stream)assert.equal(chunk,chunks[index++]);
    assert.equal(index,chunks.length);assert.equal(cleaned,true);
  }
  const rows=await d.records();
  const ends=rows.filter(row=>row.event==='model_end');
  assert.equal(ends.length,cases.length);
  for(let i=0;i<cases.length;i++){
    assert.deepEqual(counts(ends[i]),cases[i].expected);
    assert.equal(ends[i].request,i+1);assert.equal(ends[i].chunks,cases[i].types.length+1);
  }
  t.mock.timers.tick(120000);
  assert.deepEqual(await d.records(),rows,'finished streams leave no wait timer');
});

test('stream handles absent, unknown and throwing discriminants without breaking delivery',async t=>{
  const d=await streamDiagnostics(t);
  const throws=()=>{throw Error('Unreadable discriminant');};
  const cases=[
    {chunks:[d.chunk('reasoning-delta')],expected:[1,0,0,false,undefined]},
    {chunks:[],expected:[0,0,0,false,undefined]},
    {chunks:[null,undefined,d.chunk(secret),d.guard({get type(){return throws();}},['type'])],expected:[0,0,0,false,undefined]},
    {chunks:[d.finish(secret)],expected:[0,0,0,true,undefined]},
    {chunks:[d.guard({type:'finish',get reason(){return throws();}},['type','reason'])],expected:[0,0,0,true,undefined]},
    {chunks:[d.guard({type:'finish',reason:d.guard({get kind(){return throws();}},['kind'])},['type','reason'])],expected:[0,0,0,true,undefined]},
    {chunks:[d.finish('stop'),d.finish(secret)],expected:[0,0,0,true,undefined]},
    {chunks:[d.finish('stop'),d.finish('aborted')],expected:[0,0,0,true,'aborted']},
  ];
  for(const scenario of cases){
    let index=0;
    for await(const chunk of d.stream(async function*(){yield* scenario.chunks;}))assert.equal(chunk,scenario.chunks[index++]);
    assert.equal(index,scenario.chunks.length);
  }
  const ends=(await d.records()).filter(row=>row.event==='model_end');
  assert.equal(ends.length,cases.length);
  for(let i=0;i<cases.length;i++)assert.deepEqual(counts(ends[i]),cases[i].expected);
});

test('stream errors and aborts preserve thrown identity, partial counts and cleanup',async t=>{
  const d=await streamDiagnostics(t);
  for(const aborted of [false,true]){
    const controller=new AbortController();
    const failure=d.guard({message:secret,stack:secret},[]);
    const chunk=d.chunk('tool-call-delta');
    let cleaned=false;
    const stream=d.stream(async function*(){
      try{yield chunk;if(aborted)controller.abort(failure);throw failure;}
      finally{cleaned=true;}
    },{signal:controller.signal});
    assert.equal((await stream.next()).value,chunk);
    await assert.rejects(stream.next(),error=>error===failure);
    assert.equal(cleaned,true);
  }
  const rows=await d.records();
  assert.equal(rows.filter(row=>row.event==='model_end').length,0);
  const errors=rows.filter(row=>row.event==='model_error');
  assert.equal(errors.length,2);
  for(let i=0;i<errors.length;i++){
    assert.deepEqual(counts(errors[i]),[0,0,1,false,undefined]);
    assert.equal(errors[i].failure,true);assert.equal(errors[i].aborted,i===1);
  }
  t.mock.timers.tick(120000);
  assert.deepEqual(await d.records(),rows,'failed streams leave no wait timer');
});

test('consumer early close preserves upstream cleanup without inventing completion',async t=>{
  const d=await streamDiagnostics(t);
  let cleaned=false;
  const chunk=d.chunk('reasoning-delta');
  const stream=d.stream(async function*(){
    try{yield chunk;yield d.finish('stop');}finally{cleaned=true;}
  });
  assert.equal((await stream.next()).value,chunk);
  assert.deepEqual(await stream.return(),{value:undefined,done:true});
  assert.equal(cleaned,true);
  const rows=await d.records();
  assert.equal(rows.filter(row=>row.event==='model_end'||row.event==='model_error').length,0);
  t.mock.timers.tick(120000);
  assert.deepEqual(await d.records(),rows,'closed streams leave no wait timer');
});
