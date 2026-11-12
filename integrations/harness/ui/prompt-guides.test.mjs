import assert from 'node:assert/strict';
import test from 'node:test';
import { createPromptGuides } from './prompt-guides.mjs';

test('generation reuses live Prompt Assistant guides and rechecks revisions and context',async()=>{
  let revision='a'.repeat(64);const queries=[];
  const guides=createPromptGuides({call:async(path,query)=>{
    assert.equal(path,'/v1/prompt-guide');queries.push(query);
    return{source:'promptassistant.EnhancementInstruction',workflow_mode:query.mode||'text',cfg:query.cfg??2,profile:query.profile||'workflow-default',instruction:'Canonical shared instruction',revision};
  }});
  const exec={agent:{session:{}},signal:AbortSignal.timeout(1000)};
  assert((await guides.prepare({},0,exec)).instruction);
  assert.equal(await guides.prepare({},0,exec),undefined);
  revision='b'.repeat(64);assert((await guides.prepare({},0,exec)).instruction);
  const references=[{number:1,role:'base_scene'},{number:2,role:'identity'}];
  await guides.read({mode:'identity',cfg:2,profile:'krea2-edit',references},exec);
  assert.equal(await guides.prepare({mode:'identity'},2,exec),undefined);
  assert.deepEqual(queries.at(-1).references,references);
  await guides.prepare({mode:'identity'},1,exec);
  assert.deepEqual(queries.at(-1).references,[references[0]],'Absent images must not survive in the guide context');
  guides.clear(exec.agent.session);assert((await guides.prepare({},0,exec)).instruction);
});

test('missing or foreign guide sources do not silently become a local replacement',async()=>{
  const guides=createPromptGuides({call:async()=>({source:'other',instruction:'Invented local guide',revision:'a'.repeat(64)})});
  await assert.rejects(guides.prepare({},0,{agent:{session:{}}}),/Shared Prompt Assistant/);
});
