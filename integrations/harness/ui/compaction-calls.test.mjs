import assert from 'node:assert/strict';
import { test } from 'node:test';
import { Context } from '@deepseek-ai/cordis';
import { LlmRuntime, LlmAdapter } from '@deepseek-ai/dsh-llm';
import { SessionStore } from '@deepseek-ai/dsh-session';
import { SessionProjectionRegistry } from '@deepseek-ai/dsh-session-projection';
import { z } from 'zod';
import { createGPUHandoff } from './gpu-handoff.mjs';
import { registerCompactionCalls } from './compaction-calls.mjs';

async function fixture(t,{open=true,none=true}={}){
  const ctx=new Context();t.after(()=>ctx.fiber.dispose());const llm=new LlmRuntime(ctx),sessions=new SessionStore(ctx),projections=new SessionProjectionRegistry(ctx);
  projections.register({key:'turnBoundary',stateVersion:1,stateSchema:z.object({lastTurn:z.number(),openTurnStartSeq:z.number().nullable()}),init:()=>({lastTurn:1,openTurnStartSeq:open?0:null}),apply:state=>state});
  const agent={session:sessions.create('compaction-fixture')},trace=[],requests=[];
  const lease={ensure:async()=>trace.push('lease:ensure'),release:async()=>trace.push('lease:release')},handoff=createGPUHandoff();handoff.add(lease);
  let complete=()=>{},running=()=>{};const wait=new Promise(resolve=>complete=resolve),started=new Promise(resolve=>running=resolve);
  class Adapter extends LlmAdapter{
    providerInfo(provider){return{id:provider,name:provider};}
    async resolveModel(provider,id){return{provider,id,name:id,reasoning:{efforts:(none?['none','high']:['high']).map(id=>({id,name:id}))}};}
    async *stream(options){requests.push({...options});trace.push('model:start');running();await wait;if(options.signal?.aborted)throw options.signal.reason;yield{type:'text-delta',text:'Checkpoint'};yield{type:'finish',reason:{kind:'stop'}};trace.push('model:end');}
  }
  llm.registerAdapter(['ollama-local'],new Adapter());
  await ctx.plugin({inject:['llm','sessionProjections'],apply:scope=>registerCompactionCalls(scope,handoff,id=>id===agent.session.id?{agent,lease}:undefined)});
  const options={provider:'ollama-local',model:'qwen-q6-fixture',sessionId:agent.session.id,purpose:'compaction',messages:[{role:'user',content:[{type:'text',text:'Synthetic checkpoint source.'}]}],maxTokens:8192};
  return{llm,options,requests,trace,complete,started,handoff};
}

test('compaction retains its exact model and blocks image handoff until the summary finishes',async t=>{
  const f=await fixture(t);const run=(async()=>{for await(const _ of f.llm.stream(f.options)){} })();await f.started;
  assert.equal(f.requests[0].model,f.options.model);assert.equal(f.requests[0].reasoningEffort,'none');assert.equal(f.requests[0].maxTokens,8192);
  const image=f.handoff.image(AbortSignal.timeout(3000),async()=>f.trace.push('image'));
  await new Promise(resolve=>setTimeout(resolve,10));assert(!f.trace.includes('image'));
  f.complete();await run;await image;
  assert(f.trace.indexOf('model:end')<f.trace.indexOf('lease:release'));assert(f.trace.indexOf('lease:release')<f.trace.indexOf('image'));
});
test('manual idle compaction releases GPU and never invents unsupported reasoning controls',async t=>{
  const f=await fixture(t,{open:false,none:false});const run=(async()=>{for await(const _ of f.llm.stream(f.options)){} })();await f.started;f.complete();await run;
  assert.equal(f.requests[0].reasoningEffort,undefined);assert.equal(f.trace.at(-1),'lease:release');
});
test('ordinary replies share the same queue but keep their reasoning settings',async t=>{
  const f=await fixture(t);delete f.options.purpose;f.options.reasoningEffort='high';
  const run=(async()=>{for await(const _ of f.llm.stream(f.options)){} })();await f.started;f.complete();await run;
  assert.equal(f.requests[0].reasoningEffort,'high');assert(f.trace.includes('lease:ensure'));assert(!f.trace.includes('lease:release'));
});
test('idle title generation releases retention after its stream completes',async t=>{
  const f=await fixture(t,{open:false});f.options.purpose='session-title';
  const run=(async()=>{for await(const _ of f.llm.stream(f.options)){} })();await f.started;f.complete();await run;
  assert(f.trace.includes('lease:ensure'));assert.equal(f.trace.at(-1),'lease:release');
});
test('failed compaction drains its model guard and idle lease',async t=>{
  const f=await fixture(t,{open:false});const controller=new AbortController();f.options.signal=controller.signal;
  const run=(async()=>{for await(const _ of f.llm.stream(f.options)){} })();await f.started;controller.abort();f.complete();await run;
  await f.handoff.image(AbortSignal.timeout(1000),async()=>f.trace.push('image'));assert.equal(f.trace.at(-1),'image');
});
