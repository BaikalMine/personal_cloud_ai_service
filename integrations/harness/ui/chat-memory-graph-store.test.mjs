import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, mkdir, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash, randomUUID } from 'node:crypto';
import { DatabaseSync } from 'node:sqlite';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createChatMemoryStore } from './chat-memory-store.mjs';
import { compileMemoryQuery, scoreMemoryRecord } from './chat-memory-search.mjs';

const posix = process.platform !== 'win32' && typeof process.getuid === 'function';
const hash = value => createHash('sha256').update(value).digest('hex');
const quota = 50 * 1024 * 1024;
const exec = promisify(execFile);
const moduleUrl = new URL('./chat-memory-store.mjs', import.meta.url).href;
async function fixture(t) {
  const home = await mkdtemp(join(tmpdir(), 'chat-memory-graph-test-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  return { home, store: createChatMemoryStore({ dshHome: home }), path: join(home, 'chat-memory', 'records.sqlite') };
}
function sql(path, operation) {
  const db = new DatabaseSync(path);
  try { return operation(db); } finally { db.close(); }
}
const empty = revision => ({ nodeId: null, nodes: [], edges: [], total: 0, nextCursor: null, revision });

test('overview pages isolated nodes and cross-page directed edges on one revision', { skip: !posix }, async t => {
  const {store}=await fixture(t);
  const nodes=[];
  for(let i=0;i<61;i++)nodes.push(await store.add('owner','Synthetic node '+i,undefined,i===60?{status:'critical'}:undefined));
  const foreign=await store.add('other','Foreign fact');
  const edge=await store.link('owner',nodes[60].id,nodes[0].id,'depends_on');
  const first=await store.graphOverview('owner');
  assert.equal(first.nextPage,1);
  assert.equal(first.nodes.length,60);
  assert.deepEqual(first.edges,[{id:edge.id,fromId:nodes[60].id,toId:nodes[0].id,relation:'depends_on'}]);
  assert.deepEqual(first.totals,{nodes:61,edges:1});
  assert.ok(!first.nodes.some(node=>node.id===foreign.id));
  const last=await store.graphOverview('owner',{page:1,expectedRevision:first.revision});
  assert.deepEqual(last.nodes,[{id:nodes[60].id,preview:'Synthetic node 60',truncated:false,status:'critical'}]);
  assert.equal(last.nextPage,null);
  await store.setStatus('owner',nodes[60].id,'important','critical');
  await assert.rejects(store.graphOverview('owner',{page:1,expectedRevision:first.revision}),/chat_memory_conflict/);
});

test('graph API validates UUIDs, byte bounds, pages and revision tokens before storage', async () => {
  const store = createChatMemoryStore({ dshHome: tmpdir() });
  const a = randomUUID(), b = randomUUID();
  for (const id of [null, '', '../bad', a.toUpperCase(), '00000000-0000-1000-8000-000000000000']) {
    await assert.rejects(store.get('s', id), /invalid_id/);
    await assert.rejects(store.link('s', id, b), /invalid_id/);
    await assert.rejects(store.link('s', a, id), /invalid_id/);
    await assert.rejects(store.unlink('s', id), /invalid_id/);
    await assert.rejects(store.graph('s', { id }), /invalid_id/);
  }
  await assert.rejects(store.link('s', a, a), /invalid_link/);
  for (const relation of ['', 'RELATED', 'unknown', null, {}]) await assert.rejects(store.link('s', a, b, relation), /invalid_relation/);
  for (const note of [null, {}, 'x'.repeat(257), '\u{1f600}'.repeat(65)]) await assert.rejects(store.link('s', a, b, 'related', note), /invalid_note/);
  for (const options of [null, [], { cursor: -1 }, { cursor: 0.1 }, { limit: 0 }, { limit: 21 }, { limit: '2' }]) {
    await assert.rejects(store.graph('s', options), /invalid_page/);
  }
  for (const revision of [null, 0, '', '01', '-1', '1.0', {}]) {
    await assert.rejects(store.graph('s', { expectedRevision: revision }), /invalid_revision/);
    await assert.rejects(store.scan('s', () => {}, revision), /invalid_revision/);
    await assert.rejects(store.get('s', a, revision), /invalid_revision/);
  }
  await assert.rejects(store.scan('s', null), /invalid_visitor/);
});

test('facts and relationships are durable and cannot cross chat scope', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const a = await store.add('owner', 'owner fact'), b = await store.add('owner', 'neighbor');
  const foreign = await store.add('other', 'foreign fact');
  assert.equal(await store.get('other', a.id), null);
  await assert.rejects(store.link('owner', a.id, foreign.id), /chat_memory_conflict/);
  await assert.rejects(store.link('owner', a.id, randomUUID()), /chat_memory_conflict/);
  await assert.rejects(store.link('other', a.id, b.id), /chat_memory_conflict/);
  const edge = await store.link('owner', a.id, b.id, 'supports', 'synthetic note');
  const reopened = createChatMemoryStore({ dshHome: home });
  assert.deepEqual(await reopened.get('owner', a.id), a);
  assert.deepEqual((await reopened.graph('owner', { id: a.id })).edges, [edge]);
  assert.deepEqual(await reopened.graph('other', { id: a.id }), empty('1'));
  assert.equal(await reopened.unlink('other', edge.id), false);
  assert.equal(await reopened.delete('other', a.id), false);
  assert.deepEqual((await reopened.graph('owner')).edges, [edge]);
  assert.equal(await reopened.revision('other'), '1');
  const sameId = await store.add('other', 'separate fact sharing UUID', a.id);
  const otherEdge = await store.link('other', sameId.id, foreign.id);
  assert.deepEqual((await store.graph('other', { id: sameId.id })).edges, [otherEdge]);
  assert.deepEqual((await store.graph('owner', { id: a.id })).edges, [edge]);
});

test('symmetric deduplication, directional relations, conflicts and revisions', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  const a = await store.add('s', 'a'), b = await store.add('s', 'b');
  const edge = await store.link('s', b.id, a.id);
  assert.deepEqual([edge.fromId, edge.toId], [a.id, b.id].sort());
  assert.equal(edge.relation, 'related');
  assert.equal(edge.note, '');
  assert.ok(Number.isFinite(Date.parse(edge.createdAt)));
  assert.equal(await store.revision('s'), '3');
  assert.deepEqual(await store.link('s', a.id, b.id), edge);
  assert.equal(await store.revision('s'), '3');
  await assert.rejects(store.link('s', a.id, b.id, 'related', 'changed'), /chat_memory_conflict/);
  await assert.rejects(store.link('s', a.id, a.id), /invalid_link/);
  assert.equal(await store.revision('s'), '3');
  const expected = [edge];
  for (const relation of ['supports', 'contradicts', 'depends_on', 'updates', 'part_of']) {
    const forward = await store.link('s', a.id, b.id, relation, 'note');
    const backward = await store.link('s', b.id, a.id, relation, 'note');
    assert.notEqual(forward.id, backward.id);
    assert.equal(forward.fromId, a.id);
    assert.deepEqual(await store.link('s', a.id, b.id, relation, 'note'), forward);
    await assert.rejects(store.link('s', a.id, b.id, relation, 'different'), /chat_memory_conflict/);
    expected.push(forward, backward);
  }
  assert.equal(await store.revision('s'), '13');
  assert.deepEqual((await store.graph('s', { id: a.id })).edges, expected);
  assert.equal(await store.unlink('s', edge.id), true);
  assert.equal(await store.revision('s'), '14');
  assert.equal(await store.unlink('s', edge.id), false);
  assert.equal(await store.revision('s'), '14');
});

test('CAS fact edits preserve links and fact deletion cleans both directions once', { skip: !posix }, async t => {
  const { store, path } = await fixture(t);
  const a = await store.add('s', 'original'), b = await store.add('s', 'b'), c = await store.add('s', 'c');
  await store.link('s', a.id, b.id, 'supports');
  await store.link('s', c.id, a.id, 'updates');
  const retained = await store.link('s', b.id, c.id);
  const before = await store.graph('s', { id: a.id });
  await assert.rejects(store.update('s', a.id, 'bad', 'stale'), /chat_memory_conflict/);
  const changed = await store.update('s', a.id, 'changed', a.text);
  assert.equal(changed.id, a.id);
  assert.deepEqual((await store.graph('s', { id: a.id })).edges, before.edges);
  assert.equal(await store.revision('s'), '7');
  assert.equal(await store.delete('s', a.id), true);
  assert.equal(await store.revision('s'), '8');
  assert.equal(await store.delete('s', a.id), false);
  assert.equal(await store.revision('s'), '8');
  assert.deepEqual((await store.graph('s', { id: b.id })).edges, [retained]);
  assert.deepEqual(await store.graph('s', { id: a.id }), empty('8'));
  assert.equal(sql(path, db => db.prepare('SELECT COUNT(*) AS n FROM relations').get().n), 1);
});

test('graph pages incident edges by sequence, bounds previews by codepoints and fences revisions', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  assert.deepEqual(await store.graph('empty'), empty('0'));
  const a = await store.add('s', '\u{1f600}'.repeat(241));
  const edges = [], neighbors = [];
  for (let i = 0; i < 23; i++) {
    const b = await store.add('s', `neighbor ${i}`);
    neighbors.push(b);
    edges.push(await store.link('s', i % 2 ? b.id : a.id, i % 2 ? a.id : b.id, 'supports'));
  }
  const revision = await store.revision('s');
  const first = await store.graph('s', { id: a.id, expectedRevision: revision });
  assert.equal(first.total, 23);
  assert.equal(first.nextCursor, 20);
  assert.equal(first.revision, revision);
  assert.deepEqual(first.edges, edges.slice(0, 20));
  assert.deepEqual(first.nodes.map(n => n.id), [a.id, ...neighbors.slice(0, 20).map(n => n.id)]);
  assert.deepEqual(first.nodes[0], { id: a.id, preview: '\u{1f600}'.repeat(240), truncated: true });
  assert.equal(Object.hasOwn(first.nodes[0], 'text'), false);
  const last = await store.graph('s', { id: a.id, cursor: first.nextCursor, expectedRevision: revision });
  assert.deepEqual(last.edges, edges.slice(20));
  assert.equal(last.nextCursor, null);
  assert.equal(last.nodes.length, 4);
  const beyond = await store.graph('s', { id: a.id, cursor: 100 });
  assert.equal(beyond.nodes.length, 1);
  assert.equal(beyond.nextCursor, null);
  assert.equal((await store.graph('s')).nodeId, neighbors.at(-1).id);
  await store.unlink('s', edges[0].id);
  await assert.rejects(store.graph('s', { id: a.id, cursor: 20, expectedRevision: revision }), /chat_memory_conflict/);
  await assert.rejects(store.graph('s', { id: randomUUID(), expectedRevision: revision }), /chat_memory_conflict/);
  assert.equal((await store.graph('s', { id: a.id })).total, 22);
});

test('get and scan provide full scoped records, ordering, early stop and atomic revision fences', { skip: !posix }, async t => {
  const { store, path } = await fixture(t);
  const records = [await store.add('s', 'first'), await store.add('s', 'second')];
  await store.add('other', 'not visible');
  records.push(await store.add('s', 'third'));
  const revision = await store.revision('s'), seen = [];
  const expectedSeq = sql(path, db => db.prepare('SELECT seq FROM records WHERE session = ? ORDER BY seq').all(hash('s')).map(r => r.seq));
  const result = await store.scan('s', (record, seq) => seen.push({ record, seq }), revision);
  assert.deepEqual(result, { revision, total: 3 });
  assert.deepEqual(seen.map(r => r.record), records);
  assert.deepEqual(seen.map(r => r.seq), expectedSeq);
  let visits = 0;
  assert.deepEqual(await store.scan('s', () => { visits++; return false; }), result);
  assert.equal(visits, 1);
  const failure = new Error('visitor failure');
  await assert.rejects(store.scan('s', () => { throw failure; }), error => error === failure);
  await assert.rejects(store.scan('s', () => Promise.resolve()), /invalid_visitor/);
  assert.deepEqual(await store.get('s', records[0].id, revision), records[0]);
  assert.equal(await store.get('s', randomUUID(), revision), null);
  assert.deepEqual(await store.scan('empty', () => assert.fail('empty visitor')), { revision: '0', total: 0 });
  await store.link('s', records[0].id, records[1].id);
  await assert.rejects(store.get('s', records[0].id, revision), /chat_memory_conflict/);
  await assert.rejects(store.scan('s', () => assert.fail('stale visitor invoked'), revision), /chat_memory_conflict/);
  assert.deepEqual(await store.get('s', records[0].id), records[0]);
});

test('notes enforce UTF-8 bytes while previews enforce codepoints', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  const a = await store.add('s', 'x'.repeat(240)), b = await store.add('s', 'b');
  const note = '\u{1f600}'.repeat(64);
  assert.equal(Buffer.byteLength(note), 256);
  const edge = await store.link('s', a.id, b.id, 'related', note);
  assert.equal(edge.note, note);
  await assert.rejects(store.link('s', a.id, b.id, 'supports', note + 'x'), /invalid_note/);
  assert.deepEqual((await store.graph('s', { id: a.id })).nodes[0], { id: a.id, preview: a.text, truncated: false });
  assert.equal(await store.revision('s'), '3');
});

test('SQL failures roll back relationship creation, unlink and fact cleanup with revision', { skip: !posix }, async t => {
  const { store, path } = await fixture(t);
  const a = await store.add('s', 'a'), b = await store.add('s', 'b');
  const edge = await store.link('s', a.id, b.id);
  const original = await store.graph('s', { id: a.id });
  sql(path, db => db.exec("CREATE TRIGGER reject_revision BEFORE UPDATE ON metadata BEGIN SELECT RAISE(ABORT, 'synthetic_failure'); END"));
  await assert.rejects(store.link('s', a.id, b.id, 'supports'), /synthetic_failure/);
  await assert.rejects(store.unlink('s', edge.id), /synthetic_failure/);
  await assert.rejects(store.delete('s', a.id), /synthetic_failure/);
  assert.deepEqual(await store.graph('s', { id: a.id }), original);
  assert.deepEqual(await store.get('s', a.id), a);
  sql(path, db => db.exec('DROP TRIGGER reject_revision'));
  assert.equal(await store.delete('s', a.id), true);
  assert.equal(await store.revision('s'), '4');
});

test('relationship serialized bytes share account quota with add and update; unlink frees quota', { skip: !posix }, async t => {
  const { store, path } = await fixture(t);
  const a = await store.add('s', 'a'), b = await store.add('s', 'b');
  const edge = await store.link('s', a.id, b.id, 'related', 'quota note');
  sql(path, db => {
    const stored = db.prepare('SELECT json, bytes FROM relations WHERE id = ?').get(edge.id);
    assert.equal(stored.bytes, Buffer.byteLength(JSON.stringify(edge)));
    assert.deepEqual(JSON.parse(stored.json), edge);
    const usage = db.prepare(`SELECT
      (SELECT SUM(bytes + 1) + COUNT(DISTINCT session) FROM records) +
      (SELECT SUM(bytes + 1) + COUNT(DISTINCT session) FROM relations) AS n`).get().n;
    db.prepare('UPDATE records SET bytes = bytes + ? WHERE id = ?').run(quota - usage, a.id);
  });
  assert.deepEqual(await store.link('s', b.id, a.id, 'related', 'quota note'), edge);
  await assert.rejects(store.link('s', a.id, b.id, 'supports'), /chat_memory_limit/);
  await assert.rejects(store.add('other', 'new fact'), /chat_memory_limit/);
  await assert.rejects(store.update('s', b.id, 'larger', b.text), /chat_memory_limit/);
  assert.equal(await store.revision('s'), '3');
  assert.deepEqual(await store.get('s', b.id), b);
  assert.equal(await store.unlink('s', edge.id), true);
  const replacement = await store.link('s', a.id, b.id, 'supports');
  assert.equal(replacement.relation, 'supports');
  assert.equal(await store.revision('s'), '5');
});

test('existing records-only SQLite schema upgrades without changing records or revisions', { skip: !posix }, async t => {
  const { store, home, path } = await fixture(t);
  await mkdir(join(home, 'chat-memory'), { mode: 0o700 });
  await writeFile(path, '', { mode: 0o600 });
  const original = { id: randomUUID(), text: 'existing', createdAt: '2026-01-01T00:00:00.000Z', extra: true };
  sql(path, db => {
    db.exec('CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL); CREATE TABLE records (seq INTEGER PRIMARY KEY, session TEXT NOT NULL, id TEXT NOT NULL, json TEXT NOT NULL, bytes INTEGER NOT NULL, UNIQUE(session, id))');
    db.prepare('INSERT INTO metadata VALUES (?, ?)').run('legacy_imported', '1');
    db.prepare('INSERT INTO metadata VALUES (?, ?)').run('revision:' + hash('s'), '7');
    const json = JSON.stringify(original);
    db.prepare('INSERT INTO records(session, id, json, bytes) VALUES (?, ?, ?, ?)').run(hash('s'), original.id, json, Buffer.byteLength(json));
  });
  assert.deepEqual(await store.get('s', original.id, '7'), original);
  assert.equal((await store.graph('s')).revision, '7');
  const b = await store.add('s', 'new');
  await store.link('s', original.id, b.id);
  assert.equal(await store.revision('s'), '9');
  assert.deepEqual(await store.get('s', original.id), original);
});

test('query pages use shared scores, deterministic sequence ties and no unrelated fallback', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  const texts = ['proxy stable configuration', 'configure proxy service', 'proxy configuration', 'unrelated detail', 'proxy configuration'];
  const records = [];
  for (const text of texts) records.push(await store.add('s', text));
  await store.add('other', 'proxy configuration');
  const query = 'configuration proxy', plan = compileMemoryQuery(query);
  const expected = records.map((record, seq) => ({ record, seq, score: scoreMemoryRecord(record.text, plan) }))
    .filter(row => row.score > 0).sort((a, b) => b.score - a.score || a.seq - b.seq).map(row => row.record);
  assert.ok(expected.length >= 2);
  const first = await store.page('s', 0, 2, query, true);
  assert.deepEqual(first.records, expected.slice(0, 2));
  assert.equal(first.revision, '5');
  if (first.nextCursor !== null) assert.deepEqual((await store.page('s', first.nextCursor, 50, query)).records, expected.slice(2));
  assert.deepEqual((await store.page('s', 0, 50, 'zzzznomatch')).records, []);
  assert.deepEqual((await store.page('s')).records, records);
});

test('alias arguments reject invalid options, empty strings, duplicates and UTF-8 overflow', async () => {
  const store = createChatMemoryStore({ dshHome: tmpdir() }), id = randomUUID();
  const invalid = [
    null, [], 'aliases', true,
    { aliases: null }, { aliases: 'one' }, { aliases: {} },
    { aliases: [''] }, { aliases: [' \t\n'] }, { aliases: [null] }, { aliases: [1] },
    { aliases: ['one', ' one '] }, { aliases: ['one', 'one'] },
    { aliases: Array.from({ length: 13 }, (_, i) => `alias ${i}`) },
    { aliases: ['x'.repeat(81)] }, { aliases: ['\u{1f600}'.repeat(21)] },
    { aliases: new Array(1) },
  ];
  for (const options of invalid) {
    await assert.rejects(store.add('s', 'fact', id, options), /invalid_aliases/);
    await assert.rejects(store.update('s', id, 'fact', 'fact', options), /invalid_aliases/);
  }
  await assert.rejects(store.update('s', id, 'fact', undefined, { aliases: ['hint'] }), /invalid_expected_text/);
  for (const expectedAliases of [undefined, null, 'one', {}, [''], ['  '], [null], [1], ['one', 'one'], new Array(1),
    Array.from({ length: 13 }, (_, i) => `alias ${i}`), ['x'.repeat(81)], ['\u{1f600}'.repeat(21)]]) {
    await assert.rejects(store.update('s', id, 'fact', 'fact', { aliases: ['hint'], expectedAliases }), /invalid_expected_aliases/);
  }
  await assert.rejects(store.update('s', id, 'fact', 'fact', { aliases: [] }), /invalid_expected_aliases/);
});

test('page search retains the 4096-byte query contract and permits up to 256 terms', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  const query = 'q'.repeat(4096), terms = Array.from({ length: 256 }, (_, i) => `term${i}`).join(' ');
  const record = await store.add('s', query), manyTerms = await store.add('s', terms);
  await store.add('other', query);
  assert.deepEqual(await store.page('s', 0, 50, query, true), { records: [record], nextCursor: null, revision: '2' });
  assert.deepEqual((await store.page('s', 0, 50, terms)).records, [manyTerms]);
  await assert.rejects(store.page('s', 0, 50, query + 'q'), /invalid_query/);
  await assert.rejects(store.page('s', 0, 50, '\u0436'.repeat(2049)), /invalid_query/);
  assert.deepEqual(await store.get('s', record.id), record);
  assert.equal(await store.revision('s'), '2');
});

test('add persists normalized aliases without changing canonical text or legacy shapes', { skip: !posix }, async t => {
  const { store, home, path } = await fixture(t);
  const plain = await store.add('s', 'plain');
  const none = await store.add('s', 'none', undefined, { aliases: [] });
  assert.deepEqual(Object.keys(plain).sort(), ['createdAt', 'id', 'text']);
  assert.deepEqual(Object.keys(none).sort(), ['createdAt', 'id', 'text']);
  const aliases = Array.from({ length: 12 }, (_, i) => `${i}`.padEnd(80, 'x'));
  aliases[0] = '\u{1f600}'.repeat(20);
  const input = aliases.map(alias => ` ${alias}\n`), id = randomUUID();
  const record = await store.add('s', 'canonical synthetic fact', id, { aliases: input });
  input[0] = 'mutated caller input';
  assert.deepEqual(record.aliases, aliases);
  assert.equal(record.text, 'canonical synthetic fact');
  assert.deepEqual(await createChatMemoryStore({ dshHome: home }).get('s', id), record);
  assert.deepEqual(await store.add('s', record.text, id, { aliases: aliases.map(alias => ` ${alias} `) }), record);
  await assert.rejects(store.add('s', record.text, id, { aliases: ['different'] }), /chat_memory_conflict/);
  await assert.rejects(store.add('s', record.text, id), /chat_memory_conflict/);
  assert.equal(await store.revision('s'), '3');
  const stored = sql(path, db => db.prepare('SELECT json, bytes FROM records WHERE session = ? AND id = ?').get(hash('s'), id));
  assert.equal(stored.bytes, Buffer.byteLength(JSON.stringify(record)));
  assert.deepEqual(JSON.parse(stored.json), record);
  assert.equal((await store.graph('s', { id })).nodes[0].preview, record.text);
  assert.equal(Object.hasOwn((await store.graph('s', { id })).nodes[0], 'aliases'), false);
});

test('alias-only updates require text CAS, preserve links and make exact no-ops idempotent', { skip: !posix }, async t => {
  const { store, home } = await fixture(t);
  const original = await store.add('s', 'canonical', undefined, { aliases: ['old hint'] });
  const neighbor = await store.add('s', 'neighbor');
  const edge = await store.link('s', original.id, neighbor.id);
  await assert.rejects(store.update('s', original.id, original.text, 'stale', { aliases: ['new hint'], expectedAliases: ['old hint'] }), /chat_memory_conflict/);
  await assert.rejects(store.update('other', original.id, original.text, original.text, { aliases: ['new hint'], expectedAliases: ['old hint'] }), /chat_memory_conflict/);
  const changed = await store.update('s', original.id, original.text, original.text, { aliases: [' new hint '], expectedAliases: ['old hint'] });
  assert.equal(changed.text, original.text);
  assert.equal(changed.createdAt, original.createdAt);
  assert.ok(Number.isFinite(Date.parse(changed.updatedAt)));
  assert.deepEqual(changed.aliases, ['new hint']);
  assert.equal(await store.revision('s'), '4');
  assert.deepEqual((await store.graph('s', { id: original.id })).edges, [edge]);
  assert.deepEqual(await store.update('s', original.id, changed.text, changed.text, { aliases: ['new hint'], expectedAliases: ['new hint'] }), changed);
  await assert.rejects(store.update('s', original.id, changed.text, changed.text, { aliases: ['new hint'], expectedAliases: ['old hint'] }), /chat_memory_conflict/);
  assert.deepEqual(await store.update('s', original.id, changed.text, changed.text), changed);
  assert.deepEqual(await store.update('s', original.id, changed.text, changed.text, {}), changed);
  assert.equal(await store.revision('s'), '4');
  const edited = await store.update('s', original.id, 'edited canonical', changed.text);
  assert.deepEqual(edited.aliases, ['new hint']);
  assert.equal(await store.revision('s'), '5');
  const cleared = await store.update('s', original.id, edited.text, edited.text, { aliases: [], expectedAliases: ['new hint'] });
  assert.equal(Object.hasOwn(cleared, 'aliases'), false);
  assert.equal(await store.revision('s'), '6');
  assert.deepEqual(await store.update('s', original.id, cleared.text, cleared.text, { aliases: [], expectedAliases: [] }), cleared);
  assert.equal(await store.revision('s'), '6');
  assert.deepEqual(await createChatMemoryStore({ dshHome: home }).get('s', original.id), cleared);
  const plain = await store.add('s', 'legacy shaped');
  assert.deepEqual(await store.update('s', plain.id, plain.text, plain.text), plain);
  assert.equal(Object.hasOwn(await store.get('s', plain.id), 'updatedAt'), false);
  assert.equal(await store.revision('s'), '7');
});

test('alias CAS compares exact ordered state and treats an omitted field as an empty list', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  const original = await store.add('s', 'canonical');
  const changed = await store.update('s', original.id, original.text, original.text, { aliases: ['first', 'second'], expectedAliases: [] });
  for (const expectedAliases of [[], ['second', 'first'], ['first ', 'second']]) {
    await assert.rejects(store.update('s', original.id, original.text, original.text, { aliases: ['first', 'second'], expectedAliases }), /chat_memory_conflict/);
  }
  assert.equal(await store.revision('s'), '2');
  assert.deepEqual(await store.get('s', original.id), changed);
  const reordered = await store.update('s', original.id, original.text, original.text, {
    aliases: ['second', 'first'], expectedAliases: ['first', 'second'],
  });
  assert.deepEqual(reordered.aliases, ['second', 'first']);
  assert.equal(await store.revision('s'), '3');
  const textOnly = await store.update('s', original.id, 'new canonical', original.text);
  assert.deepEqual(textOnly.aliases, reordered.aliases);
  assert.equal(await store.revision('s'), '4');
});

test('concurrent alias-only writers with the same expected aliases have exactly one winner', { skip: !posix, timeout: 30000 }, async t => {
  const { store, home } = await fixture(t);
  const original = await store.add('s', 'unchanged canonical', undefined, { aliases: ['base'] });
  const script = `import { createChatMemoryStore } from ${JSON.stringify(moduleUrl)};
    const store = createChatMemoryStore({ dshHome: process.argv[1] });
    try {
      const record = await store.update('s', process.argv[2], 'unchanged canonical', 'unchanged canonical', {
        aliases: [process.argv[3]], expectedAliases: ['base']
      });
      process.stdout.write(JSON.stringify({ record }));
    } catch (error) {
      if (error.message !== 'chat_memory_conflict') throw error;
      process.stdout.write(JSON.stringify({ error: error.message }));
    }`;
  const results = await Promise.all(['writer one', 'writer two'].map(alias =>
    exec(process.execPath, ['--input-type=module', '-e', script, home, original.id, alias], { timeout: 15000 })
      .then(result => JSON.parse(result.stdout))));
  assert.equal(results.filter(result => result.error === 'chat_memory_conflict').length, 1);
  const winners = results.filter(result => result.record);
  assert.equal(winners.length, 1);
  const winner = winners[0].record;
  assert.deepEqual(await store.get('s', original.id), winner);
  assert.equal(winner.text, original.text);
  assert.equal(winner.createdAt, original.createdAt);
  assert.ok(Number.isFinite(Date.parse(winner.updatedAt)));
  assert.equal(await store.revision('s'), '2');
  await assert.rejects(store.update('s', original.id, original.text, original.text, {
    aliases: winner.aliases, expectedAliases: ['base'],
  }), /chat_memory_conflict/);
  assert.deepEqual(await store.update('s', original.id, original.text, original.text, {
    aliases: winner.aliases, expectedAliases: winner.aliases,
  }), winner);
  assert.equal(await store.revision('s'), '2');
});

test('alias JSON bytes count toward quota and failed alias writes preserve facts and revisions', { skip: !posix }, async t => {
  const { store, path } = await fixture(t);
  const original = await store.add('s', 'a', undefined, { aliases: ['old'] });
  const filler = await store.add('filler', 'synthetic filler');
  sql(path, db => {
    const usage = db.prepare('SELECT SUM(bytes + 1) + COUNT(DISTINCT session) AS n FROM records').get().n;
    db.prepare('UPDATE records SET bytes = bytes + ? WHERE id = ?').run(quota - usage, filler.id);
  });
  assert.deepEqual(await store.add('s', original.text, original.id, { aliases: ['old'] }), original);
  assert.deepEqual(await store.update('s', original.id, original.text, original.text, { aliases: ['old'], expectedAliases: ['old'] }), original);
  await assert.rejects(store.update('s', original.id, original.text, original.text, { aliases: ['x'.repeat(80)], expectedAliases: ['old'] }), /chat_memory_limit/);
  await assert.rejects(store.add('new', 'a', undefined, { aliases: ['hint'] }), /chat_memory_limit/);
  assert.equal(await store.revision('s'), '1');
  assert.equal(await store.revision('new'), '0');
  assert.deepEqual(await store.get('s', original.id), original);
  await store.delete('filler', filler.id);
  sql(path, db => db.exec("CREATE TRIGGER reject_alias_revision BEFORE UPDATE ON metadata BEGIN SELECT RAISE(ABORT, 'synthetic_alias_failure'); END"));
  await assert.rejects(store.update('s', original.id, original.text, original.text, { aliases: ['new'], expectedAliases: ['old'] }), /synthetic_alias_failure/);
  assert.deepEqual(await store.get('s', original.id), original);
  assert.equal(await store.revision('s'), '1');
  sql(path, db => db.exec('DROP TRIGGER reject_alias_revision'));
  const updated = await store.update('s', original.id, original.text, original.text, { aliases: ['new'], expectedAliases: ['old'] });
  assert.deepEqual(updated.aliases, ['new']);
  assert.equal(await store.revision('s'), '2');
});

test('search scores aliases independently with max, not concatenation or accumulated boosts', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  const a = await store.add('s', 'unrelated', undefined, { aliases: ['proxy configuration'] });
  const b = await store.add('s', 'proxy configuration');
  const c = await store.add('s', 'different', undefined, { aliases: ['proxy', 'configuration'] });
  const d = await store.add('s', 'x'.repeat(16384), undefined, { aliases: ['proxy configuration', 'configuration proxy'] });
  await store.add('other', 'private', undefined, { aliases: ['proxy configuration'] });
  const query = 'proxy configuration';
  const result = await store.page('s', 0, 2, query, true);
  assert.deepEqual(result.records, [a, b]);
  assert.equal(result.nextCursor, 2);
  assert.equal(result.revision, '4');
  assert.deepEqual((await store.page('s', 2, 2, query)).records, [d, c]);
  assert.deepEqual((await store.page('s', 0, 50, 'zzzznomatch')).records, []);
  const scan = [];
  await store.scan('s', record => scan.push(record));
  assert.deepEqual(scan, [a, b, c, d]);
  assert.equal((await store.get('s', a.id)).text, 'unrelated');
});

test('sources and expectedSources enforce strict descriptors, paths, line ranges and byte limits', async () => {
  const store = createChatMemoryStore({ dshHome: tmpdir() }), id = randomUUID();
  const invalidFiles = ['', ' ', '/absolute.md', 'C:/drive.md', 'https://example.test/story.md',
    'a\\b.md', 'a//b.md', 'a/./b.md', 'a/../b.md', './a.md', '../a.md', 'a.md/',
    'a\nb.md', 'a\u0000b.md', 'a\u007fb.md', 'x'.repeat(513), '\u{1f600}'.repeat(129)];
  const invalid = [null, {}, 'file.md', [null], [[]], [{}], new Array(1),
    Array.from({ length: 5 }, () => ({ file: 'a.md' })),
    ...invalidFiles.map(file => [{ file }]),
    [{ file: 'a.md', unknown: 'no' }], [{ file: 'a.md', text: 'no' }],
    ...['', ' ', null, 1, 'x'.repeat(129), '\u{1f600}'.repeat(33), 'chapter\n2'].map(chapter => [{ file: 'a.md', chapter }]),
    ...['', ' ', null, 1, 'x'.repeat(513), '\u{1f600}'.repeat(129)].map(anchor => [{ file: 'a.md', anchor }]),
    ...[0, -1, 1.1, 1_000_000_001, Number.MAX_SAFE_INTEGER + 1, '1', null].map(line_from => [{ file: 'a.md', line_from }]),
    [{ file: 'a.md', line_to: 2 }], [{ file: 'a.md', line_from: 3, line_to: 2 }],
    ...[0, 1.1, 1_000_000_001, '2', null].map(line_to => [{ file: 'a.md', line_from: 1, line_to }]),
    ...['', 'a'.repeat(63), 'a'.repeat(65), 'A'.repeat(64), 'g'.repeat(64), null].map(sha256 => [{ file: 'a.md', sha256 }]),
    Array.from({ length: 4 }, (_, i) => ({ file: `${i}`.padEnd(512, 'f'), chapter: 'c'.repeat(128), anchor: 'a'.repeat(512), sha256: 'a'.repeat(64) })),
  ];
  for (const sources of invalid) {
    await assert.rejects(store.add('s', 'fact', id, { sources }), /invalid_sources/);
    await assert.rejects(store.update('s', id, 'fact', 'fact', { sources, expectedSources: [] }), /invalid_sources/);
    await assert.rejects(store.update('s', id, 'fact', 'fact', { sources: [], expectedSources: sources }), /invalid_expected_sources/);
  }
  await assert.rejects(store.update('s', id, 'fact', 'fact', { sources: [] }), /invalid_expected_sources/);
  await assert.rejects(store.update('s', id, 'fact', 'fact', { sources: [], expectedSources: undefined }), /invalid_expected_sources/);
});

test('source file and chapter reject C1 control characters through the shared validator', async () => {
  const store = createChatMemoryStore({ dshHome: tmpdir() }), id = randomUUID();
  for (const sources of [[{ file: 'a\u0085b.md' }], [{ file: 'a.md', chapter: 'chapter\u0085' }]]) {
    await assert.rejects(store.add('s', 'fact', id, { sources }), /invalid_sources/);
    await assert.rejects(store.update('s', id, 'fact', 'fact', { sources: [], expectedSources: sources }), /invalid_expected_sources/);
  }
});

test('sources persist as cloned canonical metadata, preserve legacy shapes and participate in add replay', { skip: !posix }, async t => {
  const { store, home, path } = await fixture(t);
  const plain = await store.add('s', 'plain');
  const none = await store.add('s', 'none', undefined, { sources: [] });
  assert.equal(Object.hasOwn(plain, 'sources'), false);
  assert.equal(Object.hasOwn(none, 'sources'), false);
  const descriptor = { sha256: 'a'.repeat(64), anchor: 'quoted evidence', line_to: 12, line_from: 10, chapter: 'Chapter 2', file: 'missing/story.md' };
  const sources = [descriptor, { file: 'missing/general.md' }], id = randomUUID();
  const expected = [{ file: 'missing/story.md', chapter: 'Chapter 2', line_from: 10, line_to: 12, anchor: 'quoted evidence', sha256: 'a'.repeat(64) }, { file: 'missing/general.md' }];
  const record = await store.add('s', 'canonical story fact', id, { sources });
  descriptor.chapter = 'caller mutation';
  sources.push({ file: 'not stored.md' });
  assert.deepEqual(record.sources, expected);
  assert.deepEqual(Object.keys(record.sources[0]), ['file', 'chapter', 'line_from', 'line_to', 'anchor', 'sha256']);
  assert.deepEqual(await createChatMemoryStore({ dshHome: home }).get('s', id), record);
  assert.deepEqual(await store.add('s', record.text, id, { sources: expected.map(source => Object.fromEntries(Object.entries(source).reverse())) }), record);
  await assert.rejects(store.add('s', record.text, id), /chat_memory_conflict/);
  await assert.rejects(store.add('s', record.text, id, { sources: expected.toReversed() }), /chat_memory_conflict/);
  await assert.rejects(store.add('s', record.text, id, { sources: [{ file: 'different.md' }] }), /chat_memory_conflict/);
  assert.equal(await store.revision('s'), '3');
  assert.equal(await store.get('other', id), null);
  const stored = sql(path, db => db.prepare('SELECT json, bytes FROM records WHERE id = ?').get(id));
  assert.equal(stored.bytes, Buffer.byteLength(JSON.stringify(record)));
  assert.deepEqual(JSON.parse(stored.json), record);
  assert.deepEqual((await store.graph('s', { id })).nodes, [{ id, preview: record.text, truncated: false }]);
});

test('source array accepts exactly 4096 serialized UTF-8 bytes but rejects one more', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  const sources = Array.from({ length: 4 }, (_, i) => ({ file: `${i}`.padEnd(512, 'f'), anchor: 'a'.repeat(400) }));
  let remaining = 4096 - Buffer.byteLength(JSON.stringify(sources));
  for (const source of sources) {
    const extra = Math.min(remaining, 112);
    source.anchor += 'a'.repeat(extra);
    remaining -= extra;
  }
  assert.equal(remaining, 0);
  assert.equal(Buffer.byteLength(JSON.stringify(sources)), 4096);
  const record = await store.add('s', 'boundary', undefined, { sources });
  assert.deepEqual(record.sources, sources);
  const tooLarge = sources.map(source => ({ ...source }));
  tooLarge.at(-1).anchor += 'a';
  assert.ok(tooLarge.at(-1).anchor.length <= 512);
  await assert.rejects(store.add('s', 'overflow', undefined, { sources: tooLarge }), /invalid_sources/);
  await assert.rejects(store.update('s', record.id, record.text, record.text, { sources, expectedSources: tooLarge }), /invalid_expected_sources/);
  assert.equal(await store.revision('s'), '1');
});

test('source-only CAS preserves identity, aliases and links, rejects stale states, and clears explicitly', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  const initial = [{ file: 'story.md', chapter: 'First', line_from: 1, line_to: 9 }];
  const next = [{ file: 'story.md', chapter: 'Second', line_from: 10, line_to: 19 }, { file: 'notes.md' }];
  const original = await store.add('s', 'fact', undefined, { aliases: ['hint'], sources: initial });
  const neighbor = await store.add('s', 'neighbor'), edge = await store.link('s', original.id, neighbor.id);
  await assert.rejects(store.update('s', original.id, original.text, 'stale', { sources: next, expectedSources: initial }), /chat_memory_conflict/);
  await assert.rejects(store.update('s', original.id, original.text, original.text, { sources: next, expectedSources: [] }), /chat_memory_conflict/);
  const changed = await store.update('s', original.id, original.text, original.text, { sources: next, expectedSources: initial });
  assert.deepEqual(changed.sources, next);
  assert.deepEqual(changed.aliases, ['hint']);
  assert.equal(changed.createdAt, original.createdAt);
  assert.ok(Number.isFinite(Date.parse(changed.updatedAt)));
  assert.equal(await store.revision('s'), '4');
  assert.deepEqual((await store.graph('s', { id: original.id })).edges, [edge]);
  await assert.rejects(store.update('s', original.id, original.text, original.text, { sources: next, expectedSources: initial }), /chat_memory_conflict/);
  await assert.rejects(store.update('s', original.id, original.text, original.text, { sources: next, expectedSources: next.toReversed() }), /chat_memory_conflict/);
  assert.deepEqual(await store.update('s', original.id, original.text, original.text, { sources: next, expectedSources: next }), changed);
  assert.deepEqual(await store.update('s', original.id, original.text, original.text), changed);
  assert.equal(await store.revision('s'), '4');
  const edited = await store.update('s', original.id, 'edited', original.text);
  assert.deepEqual(edited.sources, next);
  assert.equal(await store.revision('s'), '5');
  await assert.rejects(store.update('s', original.id, edited.text, edited.text, {
    aliases: ['new hint'], expectedAliases: ['hint'], sources: [], expectedSources: initial,
  }), /chat_memory_conflict/);
  assert.deepEqual(await store.get('s', original.id), edited);
  const cleared = await store.update('s', original.id, edited.text, edited.text, { sources: [], expectedSources: next });
  assert.equal(Object.hasOwn(cleared, 'sources'), false);
  assert.deepEqual(cleared.aliases, ['hint']);
  assert.equal(await store.revision('s'), '6');
  assert.deepEqual(await store.update('s', original.id, edited.text, edited.text, { sources: [], expectedSources: [] }), cleared);
  const restored = await store.update('s', original.id, edited.text, edited.text, { sources: initial, expectedSources: [] });
  assert.deepEqual(restored.sources, initial);
  assert.equal(await store.revision('s'), '7');
});

test('source JSON bytes share account quota and source write failures roll back metadata and revision', { skip: !posix }, async t => {
  const { store, path } = await fixture(t);
  const initial = [{ file: 'a.md' }], next = [{ file: 'a.md', chapter: 'new evidence' }];
  const original = await store.add('s', 'fact', undefined, { sources: initial });
  const filler = await store.add('filler', 'synthetic filler');
  sql(path, db => {
    const usage = db.prepare('SELECT SUM(bytes + 1) + COUNT(DISTINCT session) AS n FROM records').get().n;
    db.prepare('UPDATE records SET bytes = bytes + ? WHERE id = ?').run(quota - usage, filler.id);
  });
  assert.deepEqual(await store.add('s', original.text, original.id, { sources: initial }), original);
  assert.deepEqual(await store.update('s', original.id, original.text, original.text, { sources: initial, expectedSources: initial }), original);
  await assert.rejects(store.add('new', 'fact', undefined, { sources: initial }), /chat_memory_limit/);
  await assert.rejects(store.update('s', original.id, original.text, original.text, { sources: next, expectedSources: initial }), /chat_memory_limit/);
  assert.deepEqual(await store.get('s', original.id), original);
  assert.equal(await store.revision('s'), '1');
  assert.equal(await store.revision('new'), '0');
  await store.delete('filler', filler.id);
  sql(path, db => db.exec("CREATE TRIGGER reject_source_revision BEFORE UPDATE ON metadata BEGIN SELECT RAISE(ABORT, 'synthetic_source_failure'); END"));
  await assert.rejects(store.update('s', original.id, original.text, original.text, { sources: next, expectedSources: initial }), /synthetic_source_failure/);
  assert.deepEqual(await store.get('s', original.id), original);
  assert.equal(await store.revision('s'), '1');
  sql(path, db => db.exec('DROP TRIGGER reject_source_revision'));
  const updated = await store.update('s', original.id, original.text, original.text, { sources: next, expectedSources: initial });
  assert.deepEqual(updated.sources, next);
  assert.equal(await store.revision('s'), '2');
});
