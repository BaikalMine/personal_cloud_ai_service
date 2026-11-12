import assert from 'node:assert/strict';
import test from 'node:test';
import { createHarnessLease, taskEndpoint } from './task-lease.mjs';

function fixture(fetcher, options = {}) {
  const calls = [], cleared = [];
  let tick;
  const lease = createHarnessLease({ sessionId: 'session-fixture', endpoint: 'http://fixture/harness/task', key: 'fixture-key',
    fetcher: async (url, request) => {
      const body = JSON.parse(request.body);
      calls.push(body);
      assert.equal(url, 'http://fixture/harness/task');
      assert.equal(request.headers.Authorization, 'Bearer fixture-key');
      return fetcher ? fetcher(body, request) : { ok: true, json: async () => ({ ok: true }) };
    }, schedule: (fn, delay) => { assert.equal(delay, 10000); tick = fn; return 1; },
    unschedule: id => cleared.push(id),
    admissionRetries: 0,
    ...options,
  });
  return { lease, calls, cleared, tick: () => tick?.() };
}

test('private lifecycle URL keeps the gateway mount, accepts a trailing slash', () => {
  for (const suffix of ['/v1','/v1/']) assert.equal(taskEndpoint('http://gateway/internal/harness'+suffix),'http://gateway/internal/harness/harness/task');
  assert.throws(() => taskEndpoint('http://gateway/other'));
  assert.throws(() => taskEndpoint('http://gateway/v1?secret=1'));
});

test('only explicit pre-admission executor refusal is retried with the same identity', async () => {
  let attempts = 0;
  const waits = [];
  const x = fixture(async body => {
    if (body.action === 'begin' && attempts++ < 2) return Response.json({ error: { code: 'executor_unavailable' } }, { status: 503 });
    return Response.json({ ok: true });
  }, { admissionRetries: 8, wait: async ms => { waits.push(ms); } });
  await x.lease.ensure();
  await x.lease.release();
  assert.deepEqual(x.calls.map(call => call.action), ['begin', 'begin', 'begin', 'end']);
  assert.equal(new Set(x.calls.map(call => call.taskId)).size, 1);
  assert.deepEqual(waits, [1000, 2000]);
});

test('executor readiness waiting is bounded and cancellation does not replay tools', async () => {
  const x = fixture(async body => body.action === 'begin' ? Response.json({ error: { code: 'executor_unavailable' } }, { status: 503 }) : Response.json({ ok: true }), {
    admissionRetries: 2, wait: async () => {},
  });
  await assert.rejects(x.lease.ensure(), /executor_unavailable/);
  assert.deepEqual(x.calls.map(call => call.action), ['begin', 'begin', 'begin', 'end']);
});

test('steps and heartbeat retain one task identity until final release', async () => {
  const x = fixture();
  await x.lease.ensure();
  await x.lease.ensure();
  x.tick();
  await new Promise(resolve => setImmediate(resolve));
  await x.lease.release();
  await x.lease.release();
  assert.deepEqual(x.calls.map(x => x.action), ['begin','begin','touch','end']);
  assert.equal(new Set(x.calls.map(x => x.taskId)).size, 1);
  assert.deepEqual(x.cleared, [1]);
  await x.lease.ensure();
  assert.notEqual(x.calls.at(-1).taskId, x.calls[0].taskId);
  await x.lease.release();
});

test('abort releases the task independently of the cancelled request signal', async () => {
  const x = fixture();
  const controller = new AbortController();
  await x.lease.ensure(controller.signal);
  controller.abort();
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(x.calls.map(x => x.action), ['begin','end']);
  assert.deepEqual(x.cleared, [1]);
});

test('an ambiguous begin error gets cleanup, not automatic replay', async () => {
  const x = fixture(async body => {
    if (body.action === 'begin') throw new Error('lost acknowledgement');
    return { ok: true, json: async () => ({ ok: true }) };
  });
  await assert.rejects(x.lease.ensure(), /lost acknowledgement/);
  assert.deepEqual(x.calls.map(x => x.action), ['begin','end']);
});

test('cancel during begin never starts a heartbeat after late acknowledgement', async () => {
  let acknowledge;
  const x = fixture(body => body.action === 'begin' ? new Promise(resolve => { acknowledge = resolve; }) :
    Promise.resolve({ ok: true, json: async () => ({ ok: true }) }));
  const controller = new AbortController();
  const pending = x.lease.ensure(controller.signal);
  controller.abort();
  acknowledge({ ok: true, json: async () => ({ ok: true }) });
  await assert.rejects(pending);
  x.tick();
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(x.calls.map(x => x.action), ['begin','end']);
});

test('503 exposes a known executor reason but never dumps arbitrary response data or retries admission',async()=>{
  for(const code of ['executor_unavailable','harness_task_unavailable','harness_task_unconfirmed','untrusted-secret-text']){
    const x=fixture(async body=>body.action==='begin'?Response.json({error:{code,message:'private upstream details'}},{status:503}):Response.json({ok:true}));
    await assert.rejects(x.lease.ensure(),error=>{
      assert.equal(error.status,503);assert(error.message.includes('HTTP 503'));assert(!error.message.includes('private upstream details'));
      if(code==='untrusted-secret-text')assert(!error.message.includes(code));else assert.equal(error.code,code);
      return true;
    });
    assert.deepEqual(x.calls.map(call=>call.action),['begin','end']);
  }
});
