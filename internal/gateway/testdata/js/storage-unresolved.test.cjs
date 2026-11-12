const test = require('node:test');
const assert = require('node:assert/strict');
const { unresolvedInputs } = require('../../static/storage-state.js');
const item = () => ({ id: 'input-1', user_id: '9007199254740993', job_id: null, filename: '<source>.png', subfolder: 'private', size_bytes: 42, sha256: 'a'.repeat(64), state: 'reserved', created_at: '2026-09-15T12:00:00Z', can_cleanup: false });
const page = () => ({ scope: 'unresolved_input_registry', items: [item()], next_after: 'input-1' });
test('unresolved inventory retains exact identities and strips action fields', () => {
 const data = page();data.items[0].dispatch_token = 'never expose';
 const result = unresolvedInputs(data);
 assert.equal(result.items[0].user_id, '9007199254740993');
 assert.equal(result.items[0].job_id, null);
 assert.equal(result.items[0].can_cleanup, false);
 assert.equal(result.items[0].dispatch_token, undefined);
 assert.deepEqual(unresolvedInputs({scope:data.scope,items:[],next_after:''}), {items:[],next:''});
});
test('unresolved inventory rejects imprecise IDs, actions and stale cursors', () => {
 for (const mutate of [
  data => data.items[0].user_id = 9007199254740993,
  data => data.items[0].can_cleanup = true,
  data => data.items[0].size_bytes = -1,
  data => data.items[0].sha256 = 'bad',
  data => data.items.push(item()),
  data => data.next_after = 'other',
  data => data.items = [],
  data => data.scope = 'host_metadata',
 ]) {const data=page();mutate(data);assert.throws(() => unresolvedInputs(data), /invalid_response/);}
});
