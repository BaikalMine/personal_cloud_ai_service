const test = require('node:test');
const assert = require('node:assert/strict');
const model = require('../../static/storage-state.js');
const hash = 'a'.repeat(64), id = 'b'.repeat(32);
const pointer = (kind = 'comfy') => ({ v: 1, kind, id, phase: 'prepare', selection: kind === 'comfy' ? [{ key: 'cleanup:9007199254740993', version: hash }] : ['training_cache:' + 'c'.repeat(32)] });
const memory = () => { const values = new Map(); return { getItem: key => values.has(key) ? values.get(key) : null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) }; };

test('cleanup eligibility keeps reference and lifecycle protection', () => {
  const item = { key: 'cleanup:1', version: hash, pending: true, can_inspect: true, eligible_at: '2026-09-01T00:00:00Z', protection: ['executor_not_fenced'] };
  const now = Date.parse('2026-09-01T01:00:00Z');
  assert.equal(model.canPrepare(item, now), true);
  for (const patch of [{ reference_protected: true }, { pending: false }, { can_inspect: false }, { key: 'output:1' }, { version: 'x' }, { eligible_at: '2026-09-02T00:00:00Z' }, { protection: ['generation_active'] }, { protection: undefined }]) assert.equal(model.canPrepare({ ...item, ...patch }, now), false, JSON.stringify(patch));
});
test('recovery pointer preserves string IDs and rejects invalid/duplicate selections', () => {
  assert.deepEqual(model.readPointer(JSON.stringify(pointer())), pointer());
  assert.deepEqual(model.readPointer(JSON.stringify(pointer('training'))), pointer('training'));
  for (const patch of [{ v: 2 }, { id: '../escape' }, { phase: 'done' }, { kind: 'arbitrary' }, { selection: [] }, { selection: [{ key: 'output:1', version: hash }] }, { selection: [pointer().selection[0], pointer().selection[0]] }]) assert.equal(model.readPointer(JSON.stringify({ ...pointer(), ...patch })), null);
  assert.equal(model.readPointer('{'), null);
  assert.equal(model.readPointer(' '.repeat(17000)), null);
  assert.equal(model.operationID('training-request-'.padEnd(48, 'a'), 'training'), true);
  assert.equal(model.operationID('training-request-'.padEnd(48, 'a'), 'comfy'), false);
});
test('recovery write is verified before execution and cannot downgrade confirmation', () => {
  const storage = memory(), state = model.storagePointer(storage, 'owner-1');
  assert.equal(state.read(), null);
  state.write(pointer());
  assert.throws(() => state.write({ ...pointer(), id: 'd'.repeat(32) }), /recovery_conflict/);
  state.write({ ...pointer(), phase: 'confirm' }, id);
  assert.throws(() => state.write(pointer(), id), /recovery_conflict/);
  assert.throws(() => state.write({ ...pointer(), phase: 'confirm', selection: [{ key: 'cleanup:2', version: hash }] }, id), /recovery_conflict/);
  state.clear('d'.repeat(32)); assert.equal(state.read().phase, 'confirm');
  state.clear(id); assert.equal(state.read(), null);
});
test('blocked or corrupt browser storage does not fabricate a persisted pointer', () => {
  const silent = { getItem: () => null, setItem() {}, removeItem() {} };
  assert.throws(() => model.storagePointer(silent, 'x').write(pointer()), /recovery_unavailable/);
  const corrupt = memory(); corrupt.setItem('x', '{}');
  assert.throws(() => model.storagePointer(corrupt, 'x').read(), /recovery_invalid/);
  const denied = { getItem() { throw new Error('denied'); } };
  assert.throws(() => model.storagePointer(denied, 'x').read(), /denied/);
});
test('operation completion is not inferred from transport status or a missing record', () => {
  for (const state of ['dispatching', 'unconfirmed']) { assert.equal(model.active({ state }), true); assert.equal(model.terminal({ state }), false); }
  for (const state of ['preview', 'missing', '404', 'failed', undefined]) assert.equal(model.terminal({ state }), false);
  for (const state of ['completed', 'partial', 'closed_unconfirmed', 'cancelled', 'expired', 'rejected']) assert.equal(model.terminal({ state }), true);
  assert.equal(model.history({ Items: [], FreedBytes: 123, Total: 9 }).freed, 123);
  assert.equal(model.history({ items: [], freed_bytes: 0, total: 0 }).freed, 0);
});
