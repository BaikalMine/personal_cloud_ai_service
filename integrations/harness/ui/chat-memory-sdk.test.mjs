import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,rm} from 'node:fs/promises';
import {join} from 'node:path';
import {tmpdir} from 'node:os';
import {registerChatMemoryTools} from './chat-memory-tools.mjs';
import {createChatMemoryStore} from './chat-memory-store.mjs';

test('native Harness dispatcher validates graph schemas and lossless results against real SQLite',{skip:process.platform==='win32'},async t=>{
  const [{Context},{ToolRuntime},{SystemPrompt},{SessionStore},{NativeToolExecutor}]=await Promise.all([
    import('@deepseek-ai/cordis'),import('@deepseek-ai/dsh-tools'),import('@deepseek-ai/dsh-system-prompt'),import('@deepseek-ai/dsh-session'),import('@deepseek-ai/dsh-tool-executor-native'),
  ]);
  const home=await mkdtemp(join(tmpdir(),'memory-native-sdk-'));t.after(()=>rm(home,{recursive:true,force:true}));
  const ctx=new Context();t.after(()=>ctx.fiber.dispose());
  new SystemPrompt(ctx,{});const sessions=new SessionStore(ctx),runtime=new ToolRuntime(ctx,{mode:'native'});
  runtime.setExecutor(new NativeToolExecutor(ctx));
  const store=createChatMemoryStore({dshHome:home});
  await ctx.plugin({inject:['tools','systemPrompt'],apply:scope=>registerChatMemoryTools(scope,{store})});
  const agent={ctx:new Context(),session:sessions.create('synthetic-memory-native')};agent.id=agent.session.id;
  let counter=0;
  async function dispatch(args){
    const output=await runtime.execute({calls:[{type:'tool-call',id:`memory-fixture-${++counter}`,name:'chat_memory',arguments:JSON.stringify(args)}],agent,signal:AbortSignal.timeout(5000)});
    const result=output.results[0].result;assert.equal(result.isError,false,JSON.stringify(result));
    return JSON.parse(result.content[0].text);
  }
  const sources=[{file:'drafts/chapter.md',chapter:'Chapter 2',line_from:12,line_to:14,anchor:'Synthetic observed passage'}];
  const a=await dispatch({action:'remember',text:'Synthetic archive decision',aliases:['archive event'],sources});
  const b=await dispatch({action:'remember',text:'Synthetic witness constraint'});
  const linked=await dispatch({action:'link',from_id:a.id,to_id:b.id,relation:'depends_on',note:'Synthetic evidence'});
  assert.equal(linked.linked,true);
  const read=await dispatch({action:'recall',id:a.id});assert.deepEqual(read.records[0].sources,sources);
  const related=await dispatch({action:'related',id:a.id});assert.equal(related.edges[0].to_id,b.id);
  assert.equal((await dispatch({action:'recall',query:'archive event'})).records[0].id,a.id);
  const changed=[{...sources[0],line_from:20,line_to:22}];
  assert.equal((await dispatch({action:'update',id:a.id,text:read.records[0].text,expected_text:read.records[0].text,sources:changed,expected_sources:sources})).updated,true);
  assert.deepEqual((await dispatch({action:'recall',id:a.id})).records[0].sources,changed);
  assert.equal((await dispatch({action:'unlink',edge_id:linked.edge.id})).unlinked,true);
  assert.equal((await dispatch({action:'related',id:a.id})).total,0);
  assert.equal((await dispatch({action:'recall'})).total,2);
  const other=sessions.create('synthetic-memory-foreign');
  assert.equal((await store.list(other.id)).length,0);
});
