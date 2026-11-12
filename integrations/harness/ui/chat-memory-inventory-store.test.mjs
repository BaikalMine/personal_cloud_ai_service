import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {createChatMemoryStore} from './chat-memory-store.mjs';
import {DatabaseSync} from 'node:sqlite';

test('inventory counts only current-chat nodes and links without disclosing text or changing revision',{skip:process.platform==='win32'},async t=>{
  const home=await mkdtemp(join(tmpdir(),'memory-inventory-'));t.after(()=>rm(home,{recursive:true,force:true}));
  const store=createChatMemoryStore({dshHome:home});
  assert.deepEqual(await store.status('owner'),{facts:0,links:0,revision:'0'});
  const a=await store.add('owner','synthetic private fact'),b=await store.add('owner','another private fact');
  await store.link('owner',a.id,b.id);await store.add('other','different private fact');
  assert.deepEqual(await store.status('owner'),{facts:2,links:1,revision:'3'});
  assert.deepEqual(await store.status('other'),{facts:1,links:0,revision:'1'});
  assert.deepEqual(await store.status('missing'),{facts:0,links:0,revision:'0'});
  const db=new DatabaseSync(join(home,'chat-memory','records.sqlite'));
  try{db.exec('BEGIN EXCLUSIVE');await assert.rejects(store.status('owner'),/chat_memory_lock_timeout/);}finally{db.exec('ROLLBACK');db.close();}
  assert.equal(await store.revision('owner'),'3');
  await store.delete('owner',a.id);assert.deepEqual(await store.status('owner'),{facts:1,links:0,revision:'4'});
});
