import assert from 'node:assert/strict';
import test from 'node:test';
import {Context} from '@deepseek-ai/cordis';
import {LlmRuntime,LlmAdapter} from '@deepseek-ai/dsh-llm';
import {SessionStore} from '@deepseek-ai/dsh-session';
import {createGPUHandoff} from './gpu-handoff.mjs';
import {registerCompactionCalls} from './compaction-calls.mjs';
const tick=()=>new Promise(resolve=>setImmediate(resolve));

test('parent, children and compaction queue before transport and never overlap',async t=>{
  const ctx=new Context();t.after(()=>ctx.fiber.dispose());
  const llm=new LlmRuntime(ctx),sessions=new SessionStore(ctx),gate=createGPUHandoff();
  const tasks=new Map(),trace=[],pending=new Map();let active=0,peak=0;
  for(const id of ['parent','child-a','child-b']){
    const agent={session:sessions.create('synthetic-'+id)};
    const lease={ensure:async()=>trace.push('begin:'+agent.session.id),release:async()=>trace.push('end:'+agent.session.id)};
    tasks.set(agent.session.id,{agent,lease});gate.add(lease);
  }
  class Adapter extends LlmAdapter {
    providerInfo(provider){return {id:provider,name:provider};}
    async resolveModel(provider,id){return {provider,id,name:id};}
    async *stream(options){
      const id=options.sessionId+(options.purpose==='compaction'?':compact':'');
      active++;peak=Math.max(peak,active);trace.push('start:'+id);
      try {
        await new Promise(resolve=>pending.set(id,resolve));
        options.signal?.throwIfAborted();
        yield {type:'text-delta',text:'Synthetic result'};yield {type:'finish',reason:{kind:'stop'}};
      } finally {active--;trace.push('finish:'+id);}
    }
  }
  llm.registerAdapter(['ollama-local'],new Adapter());
  await ctx.plugin({inject:['llm'],apply:scope=>registerCompactionCalls({on:scope.on.bind(scope),llm:scope.llm,sessionProjections:{stateOf:()=>({openTurnStartSeq:0})}},gate,id=>tasks.get(id))});
  const run=(sessionId,purpose,signal)=>(async()=>{for await(const _ of llm.stream({provider:'ollama-local',model:'synthetic',sessionId,purpose,signal,messages:[]})){} })();
  const parent=run('synthetic-parent');
  while(!pending.has('synthetic-parent'))await tick();
  const childA=run('synthetic-child-a'), childB=run('synthetic-child-b'),compact=run('synthetic-parent','compaction');
  await tick();assert.equal(pending.size,1);assert.equal(active,1);
  pending.get('synthetic-parent')();await parent;
  for(const [id,promise]of [['synthetic-child-a',childA],['synthetic-child-b',childB],['synthetic-parent:compact',compact]]){
    for(let i=0;i<100&&!pending.has(id);i++)await tick();
    assert.ok(pending.has(id),`queued stream never started: ${id}`);
    pending.get(id)();await promise;
  }
  assert.equal(peak,1);assert.equal(active,0);
  assert.deepEqual(trace.filter(x=>x.startsWith('start:')),['start:synthetic-parent','start:synthetic-child-a','start:synthetic-child-b','start:synthetic-parent:compact']);
  await gate.image(undefined,async()=>trace.push('image'));
  assert.equal(trace.filter(x=>x.startsWith('end:')).length,3);assert.equal(trace.at(-1),'image');
});

test('failed model admission releases its slot for a later helper',async()=>{
  const gate=createGPUHandoff();
  await assert.rejects(gate.model({ensure:async()=>{throw new Error('not admitted');}}),/not admitted/);
  let entered=false;const done=await gate.model({ensure:async()=>{entered=true;}});assert.equal(entered,true);done();
});
