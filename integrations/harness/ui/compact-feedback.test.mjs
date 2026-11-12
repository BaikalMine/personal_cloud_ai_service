import assert from 'node:assert/strict';
import test from 'node:test';
import { registerCompactFeedback } from './compact-feedback.mjs';

function fixture() {
  const dictionaries = [], scopes = new Set(['selected-session']);
  let listener;
  const store = registerCompactFeedback({
    effect: effect => effect(),
    locale: { register: (...args) => dictionaries.push(args), bind: namespace => { assert.equal(namespace, 'gatewayCompact'); return key => key; } },
    sessions: { scope: id => scopes.has(id) ? {} : undefined },
    on: (event, callback) => { assert.equal(event, 'command/executed'); listener = callback; },
  });
  return { store, dictionaries, scopes, execute: (...args) => listener(...args) };
}

test('compact busy refusal is surfaced through that session composer, without retries or model calls', () => {
  const f = fixture();
  f.execute('selected-session', 'compact', { kind: 'error', text: 'Compaction is unavailable because this process has an active compaction, or the agent is not idle.' });
  assert.deepEqual(f.store.getSnapshot('selected-session'), { key: 'busy' });
  assert.equal(f.dictionaries[0][0], 'gatewayCompact');
});

test('compact feedback leaves other commands, successes and disposed sessions alone', () => {
  const f = fixture();
  f.execute('selected-session', 'other', { kind: 'error', text: 'ignored' });
  f.execute('selected-session', 'compact', { kind: 'success', text: 'Compacted 3 items.' });
  f.execute('disposed-session', 'compact', { kind: 'error', text: 'ignored' });
  f.execute('selected-session', 'compact', undefined);
  assert.equal(f.store.getSnapshot('selected-session'), undefined);
  assert.equal(f.store.getSnapshot('disposed-session'), undefined);
});

test('unknown compact errors never expose arbitrary backend text in the composer', () => {
  const f = fixture();
  f.execute('selected-session', 'compact', { kind: 'error', text: 'Sensitive arbitrary provider payload' });
  assert.deepEqual(f.store.getSnapshot('selected-session'), { key: 'failed' });
  f.execute('selected-session', 'compact', { kind: 'error', text: 'Compaction could not produce a useful summary. The attempt is recorded in the session log.' });
  assert.deepEqual(f.store.getSnapshot('selected-session'), { key: 'summary' });
});

test('compact notices are independently dismissible and clear only on that session success', () => {
  const f = fixture();
  f.scopes.add('other-session');
  let changes = 0;
  const unsubscribe = f.store.subscribe(() => changes++);
  f.execute('selected-session', 'compact', { kind: 'error', text: 'Compaction cancelled.' });
  f.execute('other-session', 'compact', { kind: 'error', text: 'Usage: /compact (no arguments)' });
  f.execute('selected-session', 'compact', { kind: 'success' });
  assert.equal(f.store.getSnapshot('selected-session'), undefined);
  assert.deepEqual(f.store.getSnapshot('other-session'), { key: 'usage' });
  f.store.dismiss('other-session');
  assert.equal(f.store.getSnapshot('other-session'), undefined);
  assert.equal(changes, 4);
  unsubscribe();
  f.store.dismiss('absent-session');
  assert.equal(changes, 4);
});

test('compact feedback bounds browser session history without retaining raw failures', () => {
  const f = fixture();
  for (let index = 0; index < 33; index++) {
    const id = 'session-' + index;
    f.scopes.add(id);
    f.execute(id, 'compact', { kind: 'error', text: 'untrusted provider error' });
  }
  assert.equal(f.store.getSnapshot('session-0'), undefined);
  assert.deepEqual(f.store.getSnapshot('session-32'), { key: 'failed' });
});
