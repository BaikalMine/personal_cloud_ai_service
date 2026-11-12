import assert from 'node:assert/strict';
import test from 'node:test';
import { Context } from '@deepseek-ai/cordis';
import { AgentRegistry, assembleContextFor } from '@deepseek-ai/dsh-agent';
import { GoalService } from '@deepseek-ai/dsh-goal';
import { SessionStore } from '@deepseek-ai/dsh-session';
import { SessionProjectionRegistry } from '@deepseek-ai/dsh-session-projection';
import { SystemPrompt, renderContextSnapshot } from '@deepseek-ai/dsh-system-prompt';
import { registerGoalContext } from './goal-context.mjs';

test('snapshot fails closed without exposing goal data or errors', () => {
  let provider;
  const secret = 'synthetic objective must not appear';
  const ctx = { systemPrompt:{ context(value) { provider = value; } }, goals:{ get(agent) {
    if (agent === 'failure') throw new Error(secret);
    if (agent === 'missing') return undefined;
    return { phase:agent, revision:7, activation:'disarmed', objective:secret };
  } } };
  registerGoalContext(ctx);
  assert.equal(provider.required, true);
  assert.equal(provider.interpolate, false);
  for (const [agent, expected] of [['active','phase=active'],['paused','phase=paused'],['blocked','phase=blocked'],['complete','phase=complete'],['missing','state: none'],['failure','state: unknown'],['unexpected','state: unknown']]) {
    const text = provider.text({ agent });
    assert.ok(text.includes(expected));
    assert.ok(!text.includes(secret));
    assert.match(text, /new direct user request normally/);
  }
  assert.match(provider.text(), /state: unknown/);
});

test('real goal projection drives scoped prompt assembly and refresh', async t => {
  const ctx = new Context();
  t.after(() => ctx.fiber.dispose());
  const prompt = new SystemPrompt(ctx, {});
  const sessions = new SessionStore(ctx);
  await ctx.plugin(SessionProjectionRegistry);
  await ctx.plugin(AgentRegistry);
  await ctx.plugin(GoalService);
  const agents = ctx.agents;
  const goals = ctx.goals;
  const parent = { id:'goal-parent', session:sessions.create('goal-parent') };
  const child = { id:'goal-child', session:sessions.create('goal-child') };
  const leaveParent = agents.enter(parent, undefined), leaveChild = agents.enter(child, parent);
  t.after(() => { leaveChild(); leaveParent(); });
  await ctx.plugin({ inject:['systemPrompt','goals'], apply:registerGoalContext });

  const parentGoal = goals.create(parent, {objective:'synthetic private parent'});
  const childGoal = goals.create(child, {objective:'synthetic private child'});
  const parentContext = assembleContextFor(parent), childContext = assembleContextFor(child);
  const initial = await prompt.assemble(parentContext);
  const initialText = renderContextSnapshot(initial);
  assert.match(initialText, /phase=active; revision=1; activation=armed/);
  assert.doesNotMatch(initialText, /synthetic private/);

  goals.complete(parent, {id:parentGoal.id,revision:parentGoal.revision});
  const refreshed = renderContextSnapshot(prompt.refreshContext(initial, parentContext));
  assert.match(refreshed, /phase=complete; revision=2; activation=disarmed/);
  assert.match(refreshed, /supersedes older goal-round instructions and summaries/);
  assert.doesNotMatch(refreshed, /synthetic private/);
  const childText = renderContextSnapshot(await prompt.assemble(childContext));
  assert.match(childText, /phase=active; revision=1; activation=armed/);

  goals.pause(child, {id:childGoal.id,revision:childGoal.revision});
  const paused = await prompt.assemble(childContext);
  assert.match(renderContextSnapshot(paused), /phase=paused; revision=2; activation=disarmed/);
  goals.resume(child, {id:childGoal.id,revision:2});
  assert.match(renderContextSnapshot(prompt.refreshContext(paused, childContext)), /phase=active; revision=3; activation=armed/);
  goals.disarm(child);
  assert.match(renderContextSnapshot(prompt.refreshContext(paused, childContext)), /phase=active; revision=3; activation=disarmed/);
});
