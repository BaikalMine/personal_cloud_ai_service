import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chatImageStem, createChatImageNamer } from './chat-image-name.mjs';

test('untitled session UUIDs do not collapse to a shared session-prefix name',()=>{
  assert.equal(chatImageStem(undefined,'session-12345678-1111-4111-8111-111111111111'),'chat-12345678');
  assert.notEqual(chatImageStem(undefined,'session-aaaaaaaa-rest'),chatImageStem(undefined,'session-bbbbbbbb-rest'));
});

const session = (id, header = {}) => Object.defineProperties({ id, header }, {
  events: { get() { assert.fail('must not read session events'); } },
  history: { get() { assert.fail('must not read session history'); } },
});

function fixture() {
  let attach;
  const titles = new Map(), sessions = new Map(), reads = [], lookups = [], cleanups = [];
  const ctx = { inject(keys, callback) { assert.deepEqual(keys, ['sessionTitle', 'sessions']); attach = callback; } };
  const namer = createChatImageNamer(ctx);
  const scope = {
    sessionTitle: {
      get(value) { reads.push(value); return { title: titles.get(value.id) }; },
      refresh() { assert.fail('must not refresh or generate titles'); },
    },
    sessions: { get(id) { lookups.push(id); return sessions.get(id); } },
  };
  return { namer, titles, sessions, reads, lookups, scope, cleanups,
    attach(value = scope) { const cleanup = attach(value); cleanups.push(cleanup); return cleanup; },
  };
}

test('title metadata is sanitized before the strict filename helper', () => {
  for (const [title, expected] of [
    ['/story-writing', 'story-writing'], ['C:\\folder/story: writing', 'C-folder-story-writing'],
    ['One\0two\nthree\u202efour\u2066five\u2028six', 'One-two-three-four-five-six'],
    [' Cafe\u0301 \u732b.png ', 'Caf\u00e9-\u732b'], ['CON', '_CON'],
  ]) assert.equal(chatImageStem(title, '12345678-rest'), expected);
});

test('untitled, invalid, dot-only and all-punctuation titles use a session fallback', () => {
  for (const title of [undefined, null, 1, {}, '', '   ', '.', '..', '. .', '/\\:\0\u202e', '___---?!', '\u{1f3a8}', '.png']) {
    assert.equal(chatImageStem(title, '12345678-rest'), 'chat-12345678');
  }
  assert.equal(chatImageStem(undefined, undefined), 'chat-unknown');
  assert.equal(chatImageStem(undefined, '../bad:id\u202e'), 'chat-badid');
  assert.equal(chatImageStem(undefined, '\u{10400}'.repeat(12)), 'chat-' + '\u{10400}'.repeat(8));
});

test('chat names retain NFC and the 80-byte codepoint-safe bound', () => {
  for (const title of ['a'.repeat(100), '\u732b'.repeat(100), '\u{10400}'.repeat(100), 'Cafe\u0301'.repeat(30)]) {
    const stem = chatImageStem(title, 'session1');
    assert.ok(Buffer.byteLength(stem, 'utf8') <= 80);
    assert.match(stem, /^[\p{L}\p{N}_-]+$/u);
    assert.equal(stem.normalize('NFC'), stem);
    assert.equal(Buffer.from(stem).toString('utf8'), stem);
  }
});

test('soft injection permits missing services and delayed availability', () => {
  const agent = { session: session('abcdefgh-rest') };
  for (const ctx of [undefined, {}, { inject() {} }, { inject() { throw new Error('unavailable'); } }]) {
    assert.equal(createChatImageNamer(ctx)(agent), 'chat-abcdefgh');
  }
  const f = fixture();
  f.titles.set(agent.session.id, 'Actual chat');
  assert.equal(f.namer(agent), 'chat-abcdefgh');
  f.attach();
  assert.equal(f.namer(agent), 'Actual-chat');
  assert.equal(f.namer(undefined), 'chat-unknown');
});

test('each call reads the current title snapshot without caching or history', () => {
  const f = fixture(), current = session('abcdefgh-rest');
  f.attach();
  f.titles.set(current.id, 'First title');
  assert.equal(f.namer({ session: current }), 'First-title');
  f.titles.set(current.id, '/Renamed: chat');
  assert.equal(f.namer({ session: current }), 'Renamed-chat');
  assert.deepEqual(f.reads, [current, current]);
  assert.deepEqual(f.lookups, []);
});

test('subagents use the visible root title through live parent lookup', () => {
  const f = fixture(), root = session('root1234-rest'), child = session('child123-rest', { origin: 'subagent', parentSession: root.id });
  const grandchild = session('grand123-rest', { origin: 'subagent', parentSession: child.id });
  for (const value of [root, child]) f.sessions.set(value.id, value);
  f.titles.set(root.id, 'Visible chat');
  f.titles.set(child.id, 'Private worker title');
  f.titles.set(grandchild.id, 'Other worker title');
  f.attach();
  assert.equal(f.namer({ session: grandchild }), 'Visible-chat');
  assert.deepEqual(f.reads, [root]);
  assert.deepEqual(f.lookups, [child.id, root.id]);
  const replacement = session(root.id);
  f.sessions.set(root.id, replacement);
  f.titles.set(root.id, 'Renamed root');
  assert.equal(f.namer({ session: grandchild }), 'Renamed-root');
  assert.equal(f.reads.at(-1), replacement);
  f.titles.delete(root.id);
  assert.equal(f.namer({ session: child }), 'chat-root1234');
});

test('ordinary forks keep their own title, even when a subagent descends from them', () => {
  const f = fixture(), fork = session('fork1234-rest', { parentSession: 'older-chat' });
  const child = session('child123-rest', { origin: 'subagent', parentSession: fork.id });
  f.sessions.set(fork.id, fork);
  f.titles.set(fork.id, 'Visible fork');
  f.attach();
  assert.equal(f.namer({ session: fork }), 'Visible-fork');
  assert.deepEqual(f.lookups, []);
  assert.equal(f.namer({ session: child }), 'Visible-fork');
  assert.deepEqual(f.lookups, [fork.id]);
});

test('missing parents, malformed lookup, cycles and service errors fall back without worker titles', () => {
  const f = fixture(), child = session('child123-rest', { origin: 'subagent', parentSession: 'parent12-rest' });
  f.titles.set(child.id, 'Worker must not be used');
  f.attach();
  assert.equal(f.namer({ session: child }), 'chat-child123');
  f.sessions.set('parent12-rest', session('wrong-id'));
  assert.equal(f.namer({ session: child }), 'chat-child123');
  f.sessions.set('parent12-rest', session('parent12-rest', { origin: 'subagent', parentSession: child.id }));
  assert.equal(f.namer({ session: child }), 'chat-child123');
  assert.equal(f.namer({ session: session('self1234', { origin: 'subagent', parentSession: 'self1234' }) }), 'chat-self1234');
  f.scope.sessions.get = () => { throw new Error('lookup unavailable'); };
  assert.equal(f.namer({ session: child }), 'chat-child123');
  f.scope.sessionTitle.get = () => { throw new Error('snapshot unavailable'); };
  assert.equal(f.namer({ session: session('root1234') }), 'chat-root1234');
  assert.deepEqual(f.reads, []);
});

test('parent lookup is bounded to eight hops, allowing a root at the eighth hop', () => {
  const f = fixture(), chain = Array.from({ length: 10 }, (_, i) => session('node000' + i, i < 9 ? { origin: 'subagent', parentSession: 'node000' + (i + 1) } : {}));
  for (const value of chain) f.sessions.set(value.id, value);
  f.titles.set(chain[9].id, 'Too far');
  f.attach();
  assert.equal(f.namer({ session: chain[0] }), 'chat-node0000');
  assert.equal(f.lookups.length, 8);
  assert.deepEqual(f.reads, []);
  chain[8].header = {};
  f.titles.set(chain[8].id, 'Eighth hop root');
  assert.equal(f.namer({ session: chain[0] }), 'Eighth-hop-root');
  assert.equal(f.lookups.length, 16);
});

test('cleanup clears captured services and stale cleanup does not erase a replacement', () => {
  const f = fixture(), current = session('abcdefgh-rest');
  f.titles.set(current.id, 'Title');
  const firstCleanup = f.attach(), secondCleanup = f.attach();
  firstCleanup();
  assert.equal(f.namer({ session: current }), 'Title');
  secondCleanup();
  assert.equal(f.namer({ session: current }), 'chat-abcdefgh');
  assert.equal(f.reads.length, 1);
  f.attach({ sessionTitle: f.scope.sessionTitle });
  assert.equal(f.namer({ session: current }), 'chat-abcdefgh');
});
