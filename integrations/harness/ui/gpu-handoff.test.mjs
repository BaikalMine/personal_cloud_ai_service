import assert from 'node:assert/strict';
import test from 'node:test';
import { createGPUHandoff } from './gpu-handoff.mjs';
const tick = () => new Promise(resolve => setImmediate(resolve));

test('model calls are serialized and image drains every retained reference', async () => {
  const gate = createGPUHandoff(), calls = [];
  const a = { ensure: async () => calls.push('a:model'), release: async () => calls.push('a:end') };
  const b = { ensure: async () => calls.push('b:model'), release: async () => calls.push('b:end') };
  gate.add(a); gate.add(b);
  const doneA = await gate.model(a), pendingB = gate.model(b);
  await tick(); assert.deepEqual(calls, ['a:model']);
  doneA(); const doneB = await pendingB;
  const image = gate.image(undefined, async () => calls.push('image'));
  await tick(); assert.deepEqual(calls, ['a:model', 'b:model']);
  await tick(); assert.equal(calls.length, 2);
  doneB(); await image;
  assert.deepEqual(calls, ['a:model', 'b:model', 'a:end', 'b:end', 'image']);
  (await gate.model(a))();
  assert.equal(calls.at(-1), 'a:model');
});

test('queued child cancellation never starts it or steals the active slot',async()=>{
  const gate=createGPUHandoff(), calls=[];
  const lease=id=>({ensure:async()=>calls.push(id),release:async()=>{}});
  const done=await gate.model(lease('parent'));
  const abort=new AbortController();const child=gate.model(lease('cancelled-child'),abort.signal);
  abort.abort();await assert.rejects(child);
  const sibling=gate.model(lease('sibling'));await tick();assert.deepEqual(calls,['parent']);
  done();(await sibling)();assert.deepEqual(calls,['parent','sibling']);
});

test('aborting an active stream does not open a slot before its cleanup finishes',async()=>{
  const gate=createGPUHandoff(), abort=new AbortController(), calls=[];
  const lease={ensure:async()=>calls.push('start'),release:async()=>{}};
  const done=await gate.model(lease,abort.signal);abort.abort();
  const next=gate.model(lease);await tick();assert.equal(calls.length,1);
  done();(await next)();assert.equal(calls.length,2);
});

test('new model requests cannot jump ahead of the queued image', async () => {
  const gate = createGPUHandoff(), calls = [];
  const lease = { ensure: async () => calls.push('model'), release: async () => {} };
  gate.add(lease);
  let finish;
  const image = gate.image(undefined, () => new Promise(resolve => { finish = resolve; calls.push('image'); }));
  await tick();
  const model = gate.model(lease);
  await tick(); assert.deepEqual(calls, ['image']);
  finish(); await image; (await model)();
  assert.deepEqual(calls, ['image', 'model']);
});

test('failed drain prevents image execution and cancellation does not strand the gate', async () => {
  const gate = createGPUHandoff();
  const remove = gate.add({ release: async () => { throw new Error('stop unconfirmed'); } });
  await assert.rejects(gate.image(undefined, () => assert.fail('unsafe dispatch')), /unconfirmed/);
  remove();
  const abort = new AbortController(); abort.abort();
  await assert.rejects(gate.image(abort.signal, () => assert.fail('cancelled dispatch')));
  await gate.image(undefined, () => true);
});
