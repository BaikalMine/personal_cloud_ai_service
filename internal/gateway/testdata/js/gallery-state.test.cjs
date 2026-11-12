const { test } = require('node:test');
const assert = require('node:assert/strict');
const gallery = require('../../static/gallery-state.js');
const source = (id = '1') => ({ jobID: `gallery-test-job-${id.padStart(3, '0')}`, mediaID: id, type: 'image', sizeBytes: 100, expiresAt: 2000 });
const measurement = () => ({ version: 1, source: 'completed_file_stream_metadata', sha256: 'a'.repeat(64), media_type: 'image', width: 100, height: 200, size_bytes: 100, measured_at: '2026-09-08T00:00:00Z' });
const passport = (s, measured) => ({ data: { version: 1, job_id: s.jobID, seed: '9007199254740993', outputs: [{ media_id: s.mediaID, measurement_state: measured ? 'measured' : 'not_recorded', measurement: measured }] }, raw: '{"seed":9007199254740993}' });
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
const tick = () => new Promise(resolve => setImmediate(resolve));

test('gallery geometry fits complete media and never upscales small sources', () => {
  assert.equal(gallery.fitScale(2000, 1000, 500, 400), 0.25);
  assert.equal(gallery.fitScale(100, 50, 500, 400), 1);
  assert.equal(gallery.fitScale(0, 100, 500, 400), 1);
});
test('gallery identity, origin and persisted filters are bounded', () => {
  assert.equal(gallery.safeURL('https://evil.invalid/x', 'https://local.invalid'), '');
  assert.equal(gallery.safeURL('javascript:alert(1)', 'https://local.invalid'), '');
  assert.equal(gallery.safeURL('/generate/library/1', 'https://local.invalid'), '/generate/library/1');
  assert.equal(gallery.validMediaID('01'), false);
  assert.equal(gallery.validMediaID('9007199254740993'), true);
  assert.deepEqual(gallery.normalizedFilters({ type: 'wrong', flags: ['error', 'error', 'evil'], collection: '../x' }).flags, ['error']);
});
test('cached measurements require the same evidence validation as fetched ones', async () => {
  const s = source(); let measured = 0;
  const controller = gallery.createInspectorController({ now: () => 1000, readPassport: async () => passport(s, { ...measurement(), size_bytes: 101 }), readMeasurement: async () => { measured++; return { job_id: s.jobID, media_id: s.mediaID, measurement_state: 'measured', measurement: measurement() }; } });
  await controller.select(s); await tick();
  assert.equal(measured, 1); assert.equal(controller.snapshot().measurement.width, 100);
  assert.equal(controller.snapshot().raw, '{"seed":9007199254740993}');
});
test('switching selections aborts the old request and ignores its eventual response', async () => {
  const a = source('1'), b = source('2'), first = deferred(); let signal;
  const controller = gallery.createInspectorController({ now: () => 1000, readPassport: (s, value) => { if (s.mediaID === '1') { signal = value; return first.promise; } return passport(s, measurement()); }, readMeasurement: async () => { throw Error('unexpected'); } });
  const old = controller.select(a); await controller.select(b); first.resolve(passport(a, measurement())); await old;
  assert.equal(signal.aborted, true); assert.equal(controller.snapshot().source.mediaID, '2'); assert.equal(controller.snapshot().passport.job_id, b.jobID);
});
test('measurement from a previously selected output cannot replace current evidence', async () => {
  const a = source('1'), b = source('2'), first = deferred();
  const controller = gallery.createInspectorController({ now: () => 1000, readPassport: async s => passport(s, s.mediaID === '2' ? measurement() : null), readMeasurement: () => first.promise });
  await controller.select(a); await controller.select(b);
  first.resolve({ job_id: a.jobID, media_id: a.mediaID, measurement_state: 'measured', measurement: { ...measurement(), width: 999 } }); await tick();
  assert.equal(controller.snapshot().measurement.width, 100); assert.equal(controller.snapshot().source.mediaID, '2');
});
test('expiry before and during reads clears evidence without another network operation', async () => {
  let now = 1000, calls = 0; const read = deferred(), s = source();
  const controller = gallery.createInspectorController({ now: () => now, readPassport: () => { calls++; return read.promise; }, readMeasurement: async () => { throw Error('unexpected'); } });
  const pending = controller.select(s); now = 2100; read.resolve(passport(s, measurement())); await pending;
  assert.equal(controller.snapshot().phase, 'unavailable'); assert.equal(controller.snapshot().passport, null);
  await controller.retry(); assert.equal(calls, 1);
});
test('pausing cancels reads; returning clears stale evidence until a fresh read completes', async () => {
  const s = source(), next = deferred(); let calls = 0;
  const controller = gallery.createInspectorController({ now: () => 1000, readPassport: () => ++calls === 1 ? passport(s, measurement()) : next.promise, readMeasurement: async () => { throw Error('unexpected'); } });
  await controller.select(s); controller.setVisible(false); controller.setVisible(true);
  assert.equal(controller.snapshot().phase, 'loading'); assert.equal(controller.snapshot().measurement, null); assert.equal(controller.snapshot().raw, '');
  next.reject(Object.assign(Error('denied'), { status: 403 })); await tick(); assert.equal(controller.snapshot().phase, 'unavailable');
});
test('invalid measurement identity fails without using browser geometry as evidence', async () => {
  const s = source();
  const controller = gallery.createInspectorController({ now: () => 1000, readPassport: async () => passport(s), readMeasurement: async () => ({ job_id: s.jobID, media_id: '999', measurement_state: 'measured', measurement: measurement() }) });
  await controller.select(s); await tick(); assert.equal(controller.snapshot().measurementPhase, 'error'); assert.equal(controller.snapshot().measurement, null);
});
test('missing selected output is unavailable unless the output list is truncated', async () => {
  const s = source(); let reads = 0;
  const controller = gallery.createInspectorController({ now: () => 1000, readPassport: async () => ({ data: { version: 1, job_id: s.jobID, seed: '1', outputs: [] } }), readMeasurement: async () => { reads++; } });
  await controller.select(s); assert.equal(controller.snapshot().phase, 'unavailable'); assert.equal(reads, 0);
});
test('close cancels pending operations and leaves no selected evidence', async () => {
  const pending = deferred(), s = source();
  const controller = gallery.createInspectorController({ now: () => 1000, readPassport: () => pending.promise });
  const work = controller.select(s); controller.close(); pending.resolve(passport(s, measurement())); await work;
  assert.equal(controller.snapshot().source, null); assert.equal(controller.snapshot().raw, ''); assert.equal(controller.snapshot().phase, 'idle');
});

test('postprocess passports omit seed without weakening ordinary passport validation', async () => {
  for (const mode of ['postprocess', 'ordinary-missing-seed', 'wrong-operation', 'invented-seed']) {
    const s = { ...source(), operation: mode === 'ordinary-missing-seed' ? 'generation' : 'postprocess' };
    const result = passport(s, measurement());
    delete result.data.seed;
    result.data.operation = mode === 'wrong-operation' ? 'generation' : mode === 'ordinary-missing-seed' ? 'generation' : 'postprocess';
    result.data.postprocess = { version: 1, plan: { version: 1 } };
    if (mode === 'invented-seed') result.data.seed = '0';
    const controller = gallery.createInspectorController({ now: () => 1000, readPassport: async () => result });
    await controller.select(s);
    assert.equal(controller.snapshot().phase, mode === 'postprocess' ? 'ready' : 'error', mode);
  }
});
