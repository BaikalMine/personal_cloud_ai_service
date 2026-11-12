import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerChatMemoryTools } from './chat-memory-tools.mjs';
import { createChatMemoryStore } from './chat-memory-store.mjs';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

test('tools use the trusted executing session and expose only bounded results', async () => {
  const records=new Map(); let tool, prompt;
  const store={
    async add(id,text){const entry={id:'example-id',text,createdAt:'2026-01-01T00:00:00Z'}; records.set(id,[entry]);return entry;},
    async list(id){return records.get(id)||[];},
    async scan(id,visit){const all=records.get(id)||[];for(let i=0;i<all.length;i++)if(visit(all[i],i+1)===false)break;return {total:all.length,revision:'0'};},
    async delete(id,key){const items=records.get(id)||[];const found=items.some(item=>item.id===key);records.set(id,items.filter(item=>item.id!==key));return found;},
  };
  const ctx={systemPrompt:{context:value=>{if(value.name==='gateway:chat-memory')prompt=value;}},tools:{register:value=>{tool=value;}}};
  await registerChatMemoryTools(ctx,{defineTool:value=>value,store});
  assert.match(prompt.text,/shared across this profile/);
  assert.match(prompt.text,/Never silently duplicate/);
  assert.match(prompt.text,/current conversation without specifying broader scope/);
  const exec=id=>({agent:{session:{id}}});
  await tool.execute({action:'remember',text:'synthetic private fact',sessionId:'other'},exec('first'));
  assert.equal((await tool.execute({action:'recall'},exec('second'))).total,0);
  assert.equal((await tool.execute({action:'recall',query:'private'},exec('first'))).total,1);
  assert.equal((await tool.execute({action:'forget',id:'example-id'},exec('second'))).deleted,false);
  assert.equal((await tool.execute({action:'forget',id:'example-id'},exec('first'))).deleted,true);
  await assert.rejects(tool.execute({action:'recall'},{}),/session_unavailable/);
});

test('update is one scoped compare-and-swap, never forget followed by remember', async () => {
  const calls = [], contexts = [];
  let tool;
  const ctx = { systemPrompt:{context:value=>contexts.push(value)}, tools:{register:value=>{tool=value;}} };
  await registerChatMemoryTools(ctx, { defineTool:value=>value, store:{
    async update(...args) { calls.push(['update', ...args]); return {id:'synthetic-id'}; },
    async add() { throw Error('Must not add'); }, async delete() { throw Error('Must not delete'); },
  } });
  assert.ok(tool.parameters.action.enum.includes('update'));
  assert.equal(typeof tool.parameters.expected_text.description, 'string');
  const result = await tool.execute({action:'update',id:'synthetic-id',text:'New synthetic fact',expected_text:'Old synthetic fact',sessionId:'foreign'}, {agent:{session:{id:'owner'}}});
  assert.deepEqual(result, {updated:true,id:'synthetic-id'});
  assert.deepEqual(calls, [['update','owner','synthetic-id','New synthetic fact','Old synthetic fact']]);
  const guidance = contexts.find(value=>value.name==='gateway:chat-memory-update');
  assert.equal(guidance.required, true);
  assert.match(guidance.text, /Never emulate an update with forget followed by remember/);
  assert.match(guidance.text, /chat_memory_conflict/);
  assert.match(guidance.text, /acknowledgement is lost/);
});

test('an unsuccessful update propagates failure without deleting or duplicating the original', async () => {
  for (const code of ['chat_memory_conflict','chat_memory_limit','chat_memory_lock_timeout']) {
    let tool, updated = 0;
    const ctx = {systemPrompt:{context(){}},tools:{register:value=>{tool=value;}}};
    await registerChatMemoryTools(ctx,{defineTool:value=>value,store:{
      async update() { updated++; throw Error(code); },
      async add() { throw Error('Must not add'); },async delete() { throw Error('Must not delete'); },
    }});
    await assert.rejects(tool.execute({action:'update',id:'synthetic-id',text:'New fact',expected_text:'Old fact'},{agent:{session:{id:'owner'}}}),new RegExp(code));
    assert.equal(updated,1);
  }
});

test('real memory update preserves identity, rejects stale text and cannot change another chat', {skip:process.platform==='win32'}, async t => {
  const home=await mkdtemp(join(tmpdir(),'memory-tool-update-'));
  t.after(()=>rm(home,{recursive:true,force:true}));
  const store=createChatMemoryStore({dshHome:home});
  let tool;
  await registerChatMemoryTools({systemPrompt:{context(){}},tools:{register:value=>{tool=value;}}},{defineTool:value=>value,store});
  const owner={agent:{session:{id:'synthetic-owner'}}}, foreign={agent:{session:{id:'synthetic-other'}}};
  const created=await store.add('synthetic-owner','Original synthetic fact');
  const args={action:'update',id:created.id,text:'Updated synthetic fact',expected_text:created.text};
  await assert.rejects(tool.execute(args,foreign),/chat_memory_conflict/);
  assert.deepEqual(await store.list('synthetic-owner'),[created]);
  assert.deepEqual(await tool.execute(args,owner),{updated:true,id:created.id});
  const [updated]=await store.list('synthetic-owner');
  assert.equal(updated.text,args.text);assert.equal(updated.id,created.id);assert.equal(updated.createdAt,created.createdAt);
  assert.equal(typeof updated.updatedAt,'string');
  await assert.rejects(tool.execute(args,owner),/chat_memory_conflict/);
  assert.deepEqual(await store.list('synthetic-owner'),[updated]);
  await assert.rejects(tool.execute({...args,text:'Another fact',expected_text:undefined},owner),/invalid_expected_text/);
  await assert.rejects(tool.execute({...args,text:'x'.repeat(16385),expected_text:updated.text},owner),/invalid_text/);
  assert.deepEqual(await store.list('synthetic-owner'),[updated]);
  assert.deepEqual(await store.list('synthetic-other'),[]);
});

test('remember and update store workspace paths as relative without accessing the source file', {skip:process.platform==='win32'}, async t => {
  const home=await mkdtemp(join(tmpdir(),'memory-tool-sources-'));
  t.after(()=>rm(home,{recursive:true,force:true}));
  const store=createChatMemoryStore({dshHome:home});
  let tool;
  await registerChatMemoryTools({systemPrompt:{context(){}},tools:{register:value=>{tool=value;}}},
    {defineTool:value=>value,store,workspaceRoot:'/workspace'});
  const exec={agent:{session:{id:'synthetic'}}};
  const source={file:'\u041f\u0430\u043f\u043a\u0430 \u043f\u0440\u043e\u0435\u043a\u0442\u0430/notes/draft.md',line_from:12,line_to:20};
  const absolute={...source,file:'/workspace/'+source.file};
  const fact={action:'remember',text:'Synthetic fact',sources:[absolute]};
  const {id}=await tool.execute(fact,exec);
  const recall=()=>tool.execute({action:'recall',id},exec);
  assert.deepEqual((await recall()).records[0].sources,[source]);
  const update={action:'update',id,text:'Updated synthetic fact',expected_text:fact.text,expected_sources:[source],
    sources:[{...absolute,line_from:13}]};
  await assert.rejects(tool.execute({...update,expected_sources:undefined},exec),/invalid_expected_sources/);
  await assert.rejects(tool.execute({...update,sources:[{file:'/workspace-other/draft.md'}]},exec),/invalid_sources/);
  assert.deepEqual((await recall()).records[0].sources,[source]);
  assert.equal((await tool.execute(update,exec)).updated,true);
  assert.deepEqual((await recall()).records[0].sources,[{...source,line_from:13}]);
});

test('model sets priority as metadata without changing text or overwriting a newer priority', {skip:process.platform==='win32'}, async t => {
  const home=await mkdtemp(join(tmpdir(),'memory-tool-priority-'));
  t.after(()=>rm(home,{recursive:true,force:true}));
  const store=createChatMemoryStore({dshHome:home});
  let tool;
  await registerChatMemoryTools({systemPrompt:{context(){}},tools:{register:value=>{tool=value;}}},
    {defineTool:value=>value,store});
  const exec={agent:{session:{id:'synthetic'}}};
  const text='Original synthetic fact';
  const saved=await tool.execute({action:'remember',text,status:'important'},exec);
  assert.equal(saved.status,'important');
  const {id}=saved;
  const change={action:'set_status',id,status:'critical',expected_status:'important'};
  await assert.rejects(tool.execute({...change,expected_status:undefined},exec),/invalid_expected_status/);
  assert.equal((await tool.execute(change,exec)).status,'critical');
  await assert.rejects(tool.execute({...change,status:'normal'},exec),/chat_memory_conflict/);
  assert.equal((await tool.execute({...change,status:'normal',expected_status:'critical'},exec)).status,'normal');
  assert.deepEqual((await store.list('synthetic')).map(record=>[record.id,record.text,record.status]),[[id,text,undefined]]);
});

test('forget returns a scoped receipt for exactly the deleted fact and its incident links', {skip:process.platform==='win32'}, async t => {
  const home=await mkdtemp(join(tmpdir(),'memory-tool-forget-'));
  t.after(()=>rm(home,{recursive:true,force:true}));
  const store=createChatMemoryStore({dshHome:home});
  let tool;
  await registerChatMemoryTools({systemPrompt:{context(){}},tools:{register:value=>{tool=value;}}},
    {defineTool:value=>value,store});
  const call=(sessionId,id)=>tool.execute({action:'forget',id},{agent:{session:{id:sessionId}}});
  const fact=await store.add('a','Known fact '+ 'x'.repeat(300),undefined,{status:'important'});
  const other=await store.add('a','Linked entry');
  await store.link('a',fact.id,other.id,'related');
  assert.deepEqual(await call('b',fact.id),{deleted:false,id:fact.id});
  const receipt=await call('a',fact.id);
  assert.deepEqual(receipt,{deleted:true,id:fact.id,removed_links:1,fact:{id:fact.id,status:'important',preview:fact.text.slice(0,240),truncated:true}});
  assert.deepEqual(await call('a',fact.id),{deleted:false,id:fact.id});
  assert.equal((await store.list('a'))[0].id,other.id);
});

test('recall bounds results and rejects invalid queries', async () => {
  let tool;
  const entries = Array.from({length: 40}, (_, i) => ({id: String(i), text: 'fact', createdAt: '2026-01-01T00:00:00Z'}));
  const ctx = {systemPrompt:{context(){}}, tools:{register(value){tool=value;}}};
  await registerChatMemoryTools(ctx, {defineTool:value=>value, store:{async scan(_id,visit){for(let i=0;i<entries.length;i++)if(visit(entries[i],i+1)===false)break;return {total:entries.length,revision:'0'};}}});
  const exec = {agent:{session:{id:'synthetic'}}};
  const result = await tool.execute({action:'recall'}, exec);
  assert.equal(result.records.length, 30);
  assert.equal(result.records[0].id, '0');
  assert.equal(result.total, 40);
  assert.equal(result.truncated, true);
  const next=await tool.execute({action:'recall',cursor:result.next_cursor},exec);
  assert.equal(next.records[0].id,'30');assert.equal(next.records.length,10);assert.equal(next.next_cursor,null);
  await assert.rejects(tool.execute({action:'recall',query:'x'.repeat(513)}, exec), /invalid_query/);
});
