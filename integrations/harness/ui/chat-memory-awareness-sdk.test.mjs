import test from 'node:test';
import assert from 'node:assert/strict';
import {registerChatMemoryAwareness} from './chat-memory-awareness.mjs';

test('native prompt assembly retains live memory inventory across compaction-style suppression and refresh',{skip:process.platform==='win32'},async t=>{
  const [{Context},{SystemPrompt,renderContextSnapshot},{assembleContextFor}]=await Promise.all([
    import('@deepseek-ai/cordis'),import('@deepseek-ai/dsh-system-prompt'),import('@deepseek-ai/dsh-agent'),
  ]);
  const ctx=new Context();t.after(()=>ctx.fiber.dispose());const prompt=new SystemPrompt(ctx,{});
  let current={facts:1,links:0,revision:'1'},awareness;
  await ctx.plugin({inject:['systemPrompt'],apply:scope=>{awareness=registerChatMemoryAwareness(scope,{status:async session=>session==='owner'?current:{facts:0,links:0,revision:'0'}});}});
  const agent={session:{id:'owner'}},other={session:{id:'other'}};
  await awareness.refresh(agent);await awareness.refresh(other);
  const context=assembleContextFor(agent),assembly=await prompt.assemble(context);
  assert.match(renderContextSnapshot(assembly),/facts=1; links=0; revision=1/);
  current={facts:3,links:2,revision:'5'};await awareness.refresh(agent);
  const refreshed=renderContextSnapshot(prompt.refreshContext({...assembly,contexts:[]},context));
  assert.match(refreshed,/facts=3; links=2; revision=5/);assert.match(refreshed,/after compaction/);
  assert.match(renderContextSnapshot(await prompt.assemble(assembleContextFor(other))),/facts=0; links=0; revision=0/);
  assert.doesNotMatch(refreshed,/synthetic private fact|"records"|"text":/);
});
