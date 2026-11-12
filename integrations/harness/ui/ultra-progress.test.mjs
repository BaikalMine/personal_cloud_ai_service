import assert from 'node:assert/strict';
import test from 'node:test';
import { Context } from '@deepseek-ai/cordis';
import { SessionStore } from '@deepseek-ai/dsh-session';
import { SessionProjectionRegistry } from '@deepseek-ai/dsh-session-projection';
import { createUltraProgress, ultraProgressProjection } from './ultra-progress.mjs';

test('Ultra stages are durable UI-only records and measure human waiting separately', async t => {
  const ctx = new Context(); t.after(() => ctx.fiber.dispose());
  const sessions = new SessionStore(ctx), projections = new SessionProjectionRegistry(ctx);
  let time = 1000;
  const progress = await createUltraProgress(ctx, { now: () => time });
  const session = sessions.create('ultra-progress-fixture'), exec = { callId: 'image-call', agent: { session } };
  const call = progress.begin(exec);
  time += 250; progress.waiting(exec, true, exec.callId);
  time += 14 * 60000;
  const waiting = projections.snapshot(session).values.gatewayUltra.calls[0];
  assert.equal(waiting.phase, 'awaiting_model'); assert.equal(waiting.question_call_id, exec.callId);
  const restored = session.log.reduce(ultraProgressProjection.apply, ultraProgressProjection.init({}, 0));
  assert.deepEqual(restored.view.calls, [waiting]);
  progress.waiting(exec, false);
  assert.equal(call.seconds('awaiting_model'), 840);
  call.phase('validating'); time += 250; call.phase('waiting_gpu'); time += 400;
  call.phase('generating'); time += 77000; call.phase('delivering'); time += 80;
  call.finish('settled');
  assert.equal(call.seconds('generating'), 77); assert.equal(call.seconds('awaiting_model'), 840);
  assert(session.log.every(event => event.ignorable === true && event.type.startsWith('plugin:')));
  assert.equal(projections.snapshot(session).values.gatewayUltra.calls[0].phase, 'settled');
});

test('registration is shared, awaited, and a failed attempt can be retried', async () => {
  let resolve, reject, calls = 0;
  const ctx = { sessionProjections:{ register() { calls++; return new Promise((ok, fail) => { resolve=ok; reject=fail; }); } } };
  const first = createUltraProgress(ctx), same = createUltraProgress(ctx);
  assert.equal(first, same);
  await Promise.resolve();
  assert.equal(calls, 1);
  let settled = false; first.then(() => { settled=true; }, () => { settled=true; });
  await Promise.resolve(); assert.equal(settled, false);
  reject(new Error('synthetic registration failure'));
  await assert.rejects(first, /synthetic registration failure/);
  const retry = createUltraProgress(ctx);
  await Promise.resolve(); assert.equal(calls, 2);
  resolve({});
  assert.equal(typeof (await retry).begin, 'function');
});

test('Ultra progress ignores inherited/invalid records, bounds history and closes unfinished turns', () => {
  let state = ultraProgressProjection.init({}, 10);
  const event = n => ({ seq: n, time: n, type: 'plugin:ai-gateway/ultra-progress', ignorable: true,
    data: { call_id: `call-${n}`, phase: 'generating', started_at: n, updated_at: n, phase_started_at: n } });
  assert.equal(ultraProgressProjection.apply(state, event(0)), state);
  assert.equal(ultraProgressProjection.apply(state, { ...event(10), data: { secret: 'must not reach UI' } }), state);
  const pendingFailure = { ...event(10), data: { ...event(10).data, phase: 'finalizing_failed' } };
  assert.equal(ultraProgressProjection.apply(state, pendingFailure).view.calls[0].phase, 'finalizing_failed');
  for (let i = 10; i < 60; i++) state = ultraProgressProjection.apply(state, event(i));
  assert.equal(state.view.calls.length, 32);
  state = ultraProgressProjection.apply(state, { type: 'turn/end', seq: 60, time: 60 });
  assert(state.view.calls.every(row => row.phase === 'cancelled'));
});
