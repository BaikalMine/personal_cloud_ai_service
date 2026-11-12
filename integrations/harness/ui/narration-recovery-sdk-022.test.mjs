import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { Context } from '@deepseek-ai/cordis';
import { AgentRegistry } from '@deepseek-ai/dsh-agent';
import { AgentLoop } from '@deepseek-ai/dsh-agent-loop';
import { LocalFileSystem } from '@deepseek-ai/dsh-fs-local';
import * as ObservationPolicy from '@deepseek-ai/dsh-fs-observation-policy';
import { LlmRuntime, LlmAdapter, createUserMessage } from '@deepseek-ai/dsh-llm';
import { SessionStore } from '@deepseek-ai/dsh-session';
import { SessionProjectionRegistry } from '@deepseek-ai/dsh-session-projection';
import { SystemPrompt } from '@deepseek-ai/dsh-system-prompt';
import * as ToolFS from '@deepseek-ai/dsh-tool-fs';
import { NativeToolExecutor } from '@deepseek-ai/dsh-tool-executor-native';
import { ToolRuntime } from '@deepseek-ai/dsh-tools';
import { WorkingDirectoryService } from '@deepseek-ai/dsh-working-directory';
import { registerPreciseEditTool } from './precise-edit-tool.mjs';
import { registerNarrationRecovery } from './narration-recovery.mjs';

const SOURCE = 'gateway-narrated-edit-recovery';
const narration = 'I will replace the final block in synthetic.md.\nTarget: lines 2-3.';
const human = text => createUserMessage({ source:{kind:'user'}, content:[{type:'text',text}] });
const text = value => [{type:'text-delta',index:0,text:value},{type:'finish',reason:{kind:'stop'}}];
const onlyReasoning = () => [{type:'reasoning-delta',index:0,text:'Synthetic internal planning, never an action.'},{type:'finish',reason:{kind:'stop'}}];
const call = (name,args) => [{type:'block-start',index:0,blockType:'tool-call'},
  {type:'tool-call-delta',index:0,id:`synthetic-${name}`,name,argumentsDelta:JSON.stringify(args)},
  {type:'block-end',index:0,block:{type:'tool-call',id:`synthetic-${name}`,name,arguments:JSON.stringify(args)}},
  {type:'finish',reason:{kind:'tool-calls'}}];

async function fixture(t, respond) {
  const home = await mkdtemp(join(tmpdir(),'synthetic-narration-sdk-'));
  const root = join(home,'workspace'), archive = join(home,'private','versions');
  await mkdir(root);
  const path = join(root,'synthetic.md');
  await writeFile(path,'Unchanged header\r\nalpha\r\nUnchanged footer\r\n');
  const ctx = new Context(), handles = [], events = [], requests = [], trace = [];
  t.after(async () => {
    try { for (const handle of handles.reverse()) await handle.dispose(); await ctx.fiber.dispose(); }
    finally { assert.equal(home.startsWith(join(tmpdir(),'synthetic-narration-sdk-')),true); await rm(home,{recursive:true,force:true}); }
  });
  new SystemPrompt(ctx,{});
  new SessionStore(ctx);
  await ctx.plugin(SessionProjectionRegistry);
  await ctx.plugin(AgentRegistry);
  new LocalFileSystem(ctx,{cwd:root,diffBasisMaxBytes:2<<20});
  await ctx.plugin(WorkingDirectoryService,{defaultDirectory:root});
  const tools = new ToolRuntime(ctx);
  tools.setExecutor(new NativeToolExecutor(ctx));
  await ctx.plugin(ObservationPolicy);
  await ctx.plugin(ToolFS,{readImage:false,applyPatch:false});
  await registerPreciseEditTool(ctx,{root,archive});
  const llm = new LlmRuntime(ctx);
  class Adapter extends LlmAdapter {
    providerInfo(provider) { return {id:provider,name:provider}; }
    async resolveModel(provider,id) { return {provider,id,name:id}; }
    async *stream(options) {
      requests.push(options); trace.push('model:start');
      assert(requests.length <= 10,'recovery must never create an unbounded loop');
      for (const chunk of await respond(options,requests.length,{path,ctx,events,trace})) yield chunk;
      trace.push('model:end');
    }
  }
  llm.registerAdapter(['ollama-local'],new Adapter());
  await ctx.plugin(AgentLoop,{agents:[]});
  await registerNarrationRecovery(ctx);
  const turns = new Map(), released = new Set();
  const release = sessionId => {
    const key=turns.get(sessionId);
    if(key&&!released.has(key)){released.add(key);trace.push('lease:release');}
  };
  // Production also releases on durable turn/end: the SDK can skip turn-stopping on an error.
  ctx.on('session/event',(session,event) => {
    events.push(event); trace.push(event.type);
    if(event.type==='turn/start')turns.set(session.id,`${session.id}:${event.data.turn}`);
    if(event.type==='turn/end')release(session.id);
  });
  ctx.on('agent/turn-stopping',({agent}) => { release(agent.session.id); });
  async function create(options = {}) {
    const handle = await ctx.agents.create({sessionId:`synthetic-narration-${handles.length}`,
      meta:{cwd:root,...options.meta},agentOptions:{provider:'ollama-local',model:'synthetic'},...options});
    handles.push(handle);
    return handle.agent;
  }
  const agent = await create();
  async function run(message = human('Replace alpha with beta in synthetic.md.')) {
    agent.followup(message);
    await Promise.race([agent.whenIdle(),new Promise((_,reject) => { const timer=setTimeout(() => reject(new Error('Synthetic loop did not become idle')),5000); timer.unref(); })]);
  }
  return {ctx,agent,path,root,archive,events,requests,trace,run,create};
}

test('SDK loop recovers narration once, executes native precise edit and retains private versions in the same turn', async t => {
  let versionResult;
  const f = await fixture(t, (options,index,{path}) => {
    if (index === 1) return [{type:'reasoning-delta',index:0,text:'Synthetic private planning.'},...text(narration).map(chunk => ({...chunk,...(chunk.index === undefined?{}:{index:1})}))];
    if (index === 2) return call('read',{file_path:path});
    if (index === 3) return call('edit_precise',{file_path:path,edits:[{old_text:'alpha',new_text:'beta'}]});
    if (index === 4) return call('edit_precise_versions',{action:'list',file_path:path});
    versionResult=options.messages.filter(message => message.role === 'tool').at(-1);
    return text('Verified the synthetic edit.');
  });
  await f.run();
  assert.equal(f.requests.length,5);
  assert.equal(await readFile(f.path,'utf8'),'Unchanged header\r\nbeta\r\nUnchanged footer\r\n');
  const reminders=f.events.filter(event => event.type==='user/message' && event.data.source?.kind===SOURCE);
  assert.equal(reminders.length,1);
  const assistants=f.events.filter(event => event.type==='assistant/message');
  assert.equal(assistants[0].data.message.content.some(block => block.type==='reasoning'),true);
  assert.equal(assistants[0].data.message.content.some(block => block.type==='text' && block.text===narration),true);
  assert.equal(f.events.filter(event => event.type==='turn/end').length,1);
  assert.equal(f.trace.filter(item => item==='lease:release').length,1);
  assert(f.trace.lastIndexOf('model:end')<f.trace.indexOf('lease:release'));
  const saved=JSON.parse(versionResult.content[0].text);
  assert.equal(saved.total,2);
  assert.equal(saved.versions.length,2);
  const toolResults=f.events.filter(event => event.type==='tool/result');
  assert.equal(toolResults.length,3);
  assert(toolResults.every(event => event.data.message?.isError===false),JSON.stringify(toolResults));
});

test('SDK loop bounds repeated narration and leaves a plans-only request alone', async t => {
  const f = await fixture(t, () => text(narration));
  await f.run();
  assert.equal(f.requests.length,2);
  await f.run(human('Explain a plan for replacing alpha in synthetic.md.'));
  assert.equal(f.requests.length,3);
  assert.equal(f.events.filter(event => event.type==='user/message' && event.data.source?.kind===SOURCE).length,1);
  assert.equal(await readFile(f.path,'utf8'),'Unchanged header\r\nalpha\r\nUnchanged footer\r\n');
});

test('SDK reasoning-only stop continues through native read and edit in the same turn', async t => {
  const f = await fixture(t, (options,index,{path}) => {
    if (index === 1) return onlyReasoning();
    if (index === 2) return call('read',{file_path:path});
    if (index === 3) return call('edit_precise',{file_path:path,edits:[{old_text:'alpha',new_text:'beta'}]});
    return text('Verified the synthetic edit.');
  });
  await f.run();
  assert.equal(f.requests.length,4);
  assert.equal(await readFile(f.path,'utf8'),'Unchanged header\r\nbeta\r\nUnchanged footer\r\n');
  assert.equal(f.events.filter(event=>event.type==='user/message'&&event.data.source?.kind===SOURCE).length,1);
  assert.equal(f.events.filter(event=>event.type==='turn/end').length,1);
  assert.equal(f.trace.filter(item=>item==='lease:release').length,1);
});

test('SDK failed unread edit, successful read and reasoning-only stop resume without duplicate writes', async t => {
  let versions;
  const f = await fixture(t, (options,index,{path}) => {
    if(index===1)return call('edit_precise',{file_path:path,edits:[{old_text:'alpha',new_text:'beta'}]});
    if(index===2)return call('read',{file_path:path});
    if(index===3)return onlyReasoning();
    if(index===4)return call('edit_precise',{file_path:path,edits:[{old_text:'alpha',new_text:'beta'}]});
    if(index===5)return call('edit_precise_versions',{action:'list',file_path:path});
    versions=JSON.parse(options.messages.filter(message=>message.role==='tool').at(-1).content[0].text);
    return text('Verified the resumed synthetic edit.');
  });
  await f.run();
  assert.equal(f.requests.length,6);
  const results=f.events.filter(event=>event.type==='tool/result');
  assert.equal(results.length,4);
  assert.equal(results[0].data.message.isError,true,'unread edit must actually fail');
  assert(results.slice(1).every(event=>event.data.message.isError===false));
  assert.equal(await readFile(f.path,'utf8'),'Unchanged header\r\nbeta\r\nUnchanged footer\r\n');
  assert.equal(versions.total,2,'only the original and single successful edit are versioned');
  assert.equal(f.events.filter(event=>event.type==='user/message'&&event.data.source?.kind===SOURCE).length,1);
  assert.equal(f.events.findLast(event=>event.type==='turn/end').data.reason.kind,'completed');
});

test('SDK repeated reasoning-only stops are visible failures, not completed turns or infinite retries', async t => {
  const f = await fixture(t, () => onlyReasoning());
  await f.run();
  assert.equal(f.requests.length,2);
  assert.equal(f.events.filter(event=>event.type==='user/message'&&event.data.source?.kind===SOURCE).length,1);
  const end = f.events.findLast(event=>event.type==='turn/end');
  assert.equal(end.data.reason.kind,'error');
  const attempts=f.events.filter(event=>event.type==='assistant/attempt');
  assert(attempts.length>0,'The failed attempt must be durably visible to the normal error UI');
  assert.equal(await readFile(f.path,'utf8'),'Unchanged header\r\nalpha\r\nUnchanged footer\r\n');
  assert.equal(f.trace.filter(item=>item==='lease:release').length,1);
});

test('SDK explanation-only request gets an answer without file actions after a reasoning-only stop', async t => {
  const f = await fixture(t, (_options,index) => index===1?onlyReasoning():text('Here is the requested synthetic explanation.'));
  await f.run(human('Explain a plan for replacing alpha in synthetic.md.'));
  assert.equal(f.requests.length,2);
  assert.equal(f.events.filter(event=>event.type==='tool/call').length,0);
  assert.equal(await readFile(f.path,'utf8'),'Unchanged header\r\nalpha\r\nUnchanged footer\r\n');
});

test('SDK loop gives a queued human correction priority over an already injected reminder', async t => {
  const f = await fixture(t, () => text(narration));
  let corrected=false;
  f.ctx.on('agent/assistant-stream',({agent,frame}) => {
    if (corrected || frame.type!=='end' || !agent.inbox.nextStep.some(message => message.source?.kind===SOURCE)) return;
    corrected=true;
    agent.followup(human('Stop. Do not change synthetic.md.'));
  });
  await f.run();
  assert.equal(corrected,true);
  assert.equal(f.requests.length,2);
  assert.equal(f.requests.at(-1).messages.filter(message => message.role==='user').at(-1).content[0].text,'Stop. Do not change synthetic.md.');
  assert.equal(f.events.some(event => event.type==='user/message' && event.data.source?.kind===SOURCE),false);
  assert.equal(f.agent.inbox.nextStep.length,0);
  assert.equal(f.agent.inbox.nextTurn.length,0);
  assert.equal(await readFile(f.path,'utf8'),'Unchanged header\r\nalpha\r\nUnchanged footer\r\n');
});

test('SDK loop discards recovery after rejected admission and keepInbox cancellation', async t => {
  for (const mode of ['reject','cancel']) await t.test(mode, async t => {
    const f=await fixture(t,()=>text(narration));
    if (mode==='reject') f.ctx.on('agent/pre-step',(payload,next) => payload.messages.some(message => message.source?.kind===SOURCE) ? {kind:'reject'} : next());
    else f.ctx.on('agent/assistant-stream',({agent,frame}) => {
      if(frame.type==='end' && agent.inbox.nextStep.some(message => message.source?.kind===SOURCE)) agent.cancel({kind:'user'},{keepInbox:true});
    });
    await f.run();
    assert.equal(f.requests.length,1);
    assert.equal(f.agent.inbox.nextStep.length,0);
    assert.equal(f.agent.inbox.nextTurn.length,0);
    assert.equal(f.events.some(event => event.type==='user/message' && event.data.source?.kind===SOURCE),false);
    if (mode==='cancel') assert.deepEqual(f.events.findLast(event => event.type==='turn/end')?.data.reason,{kind:'aborted',reason:{kind:'user'}});
  });
});

test('SDK loop never adds recovery to a delegated child agent', async t => {
  const f=await fixture(t,()=>text(narration));
  const child=await f.create({parentAgent:f.agent,meta:{cwd:f.root,origin:'subagent'}});
  child.followup(human('Replace alpha with beta in synthetic.md.'));
  await child.whenIdle();
  assert.equal(f.requests.length,1);
  assert.equal(f.events.some(event => event.type==='user/message' && event.data.source?.kind===SOURCE),false);
});
