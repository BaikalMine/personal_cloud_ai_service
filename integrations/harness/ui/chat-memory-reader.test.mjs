import test from 'node:test';
import assert from 'node:assert/strict';
import { readChatMemory, readChatMemoryGraph } from './chat-memory-reader.mjs';

const SESSION = 'synthetic-session-a';
const OTHER = 'synthetic-session-b';
const PAGE_BYTES = 112 * 1024;
const id = number => `00000000-0000-4000-8000-${number.toString(16).padStart(12, '0')}`;
const fact = (number, text = `synthetic fact ${number}`) => ({
  id: id(number), text, createdAt: '2026-01-01T00:00:00.000Z',
});
const errorMessage = message => error => error instanceof Error && error.message === message;

// In-memory store contract only: ordered sequence numbers, revision-fenced
// snapshots, and graph previews produced by the store, not by the reader.
function fakeStore(records = [], edges = []) {
  const sessions = new Map([[SESSION, { records, edges, revision: '1' }]]);
  const calls = [];
  const hooks = {};
  function snapshot(sessionId, expectedRevision) {
    const state = sessions.get(sessionId) ?? { records: [], edges: [], revision: '0' };
    if (expectedRevision !== undefined && expectedRevision !== state.revision) {
      throw new Error('chat_memory_conflict');
    }
    return state;
  }
  return {
    calls, hooks,
    bump(sessionId = SESSION) {
      const state = sessions.get(sessionId);
      state.revision = String(BigInt(state.revision) + 1n);
    },
    async scan(sessionId, visitor, expectedRevision) {
      calls.push({ method: 'scan', sessionId, expectedRevision });
      const state = snapshot(sessionId, expectedRevision);
      for (const [index, record] of state.records.entries()) {
        hooks.beforeVisit?.(record, index);
        // Gaps ensure continuation uses sequence numbers rather than offsets.
        if (visitor(structuredClone(record), index * 3 + 2) === false) break;
      }
      hooks.afterScan?.();
      return { revision: state.revision, total: state.records.length };
    },
    async get(sessionId, recordId, expectedRevision) {
      calls.push({ method: 'get', sessionId, id: recordId, expectedRevision });
      const state = snapshot(sessionId, expectedRevision);
      hooks.afterGet?.();
      return structuredClone(state.records.find(record => record.id === recordId) ?? null);
    },
    async graph(sessionId, options) {
      calls.push({ method: 'graph', sessionId, ...options });
      const { id: nodeId, cursor = 0, limit, expectedRevision } = options;
      const state = snapshot(sessionId, expectedRevision);
      const focus = state.records.find(record => record.id === nodeId);
      if (!focus) return { nodeId: null, nodes: [], edges: [], total: 0, nextCursor: null, revision: state.revision };
      const all = state.edges.filter(edge => edge.fromId === nodeId || edge.toId === nodeId);
      const page = all.slice(cursor, cursor + limit);
      const nodeIds = new Set([nodeId, ...page.flatMap(edge => [edge.fromId, edge.toId])]);
      const nodes = state.records.filter(record => nodeIds.has(record.id)).map(record => {
        const points = Array.from(record.text);
        return { id: record.id, preview: points.slice(0, 240).join(''), truncated: points.length > 240 };
      });
      hooks.afterGraph?.();
      return {
        nodeId, nodes, edges: structuredClone(page), total: all.length, revision: state.revision,
        nextCursor: cursor + page.length < all.length ? cursor + page.length : null,
      };
    },
  };
}

test('recall exposes user-set priorities without changing lexical eligibility', async () => {
  const normal=fact(1,'Project milestone');
  const important={...fact(2,'Project dependency'),status:'important'};
  const critical={...fact(3,'Access recovery'),status:'critical'};
  const store=fakeStore([normal,important,critical]);
  assert.deepEqual((await readChatMemory(store,SESSION,{query:'Project'})).records,[normal,important]);
  assert.deepEqual((await readChatMemory(store,SESSION,{id:critical.id})).records,[critical]);
});

async function collectFacts(store, args = {}) {
  const pages = [];
  let cursor;
  for (let count = 0; count < 200; count++) {
    const page = await readChatMemory(store, SESSION, { ...args, ...(cursor ? { cursor } : {}) });
    pages.push(page);
    assert.equal(page.truncated, page.next_cursor !== null);
    if (page.next_cursor === null) return pages;
    assert.ok(page.records.length > 0, 'continuation must make progress');
    assert.notEqual(page.next_cursor, cursor, 'cursor must advance');
    cursor = page.next_cursor;
  }
  assert.fail('pagination did not terminate');
}

function graphFixture() {
  const records = Array.from({ length: 27 }, (_, index) =>
    fact(index + 1, `${'\u{1f680}'.repeat(260)} synthetic ${index}`));
  const edges = records.slice(1).map((record, index) => ({
    id: id(1000 + index), fromId: records[0].id, toId: record.id,
    relation: 'related', note: `synthetic edge ${index}`, createdAt: records[0].createdAt,
  }));
  return { records, edges, store: fakeStore(records, edges) };
}

test('browses every fact beyond the default 30, with stable totals and no duplicates', async () => {
  const records = Array.from({ length: 83 }, (_, index) => fact(index + 1));
  const store = fakeStore(records);
  const pages = await collectFacts(store);
  assert.deepEqual(pages.map(page => page.records.length), [30, 30, 23]);
  assert.deepEqual(pages.flatMap(page => page.records), records);
  for (const page of pages) {
    assert.equal(page.mode, 'browse');
    assert.equal(page.total, 83);
    assert.equal(page.memory_total, 83);
    assert.equal(page.matched_total, null);
    assert.equal(page.query_matched, null);
    assert.equal(page.revision, '1');
  }
  assert.deepEqual(store.calls.map(call => call.expectedRevision), [undefined, '1', '1']);
});

test('search ranks across the whole snapshot, maintaining sequence order for ties', async () => {
  const partial = Array.from({ length: 12 }, (_, index) => fact(index + 1, 'red notebook'));
  const all = Array.from({ length: 12 }, (_, index) => fact(index + 20, 'blue green red'));
  const exact = Array.from({ length: 35 }, (_, index) => fact(index + 40, 'red green blue'));
  const records = [...partial, fact(15, 'unrelated'), ...all, ...exact];
  const pages = await collectFacts(fakeStore(records), { query: 'red green blue', limit: 7 });
  assert.deepEqual(pages.flatMap(page => page.records), [...exact, ...all, ...partial]);
  for (const page of pages) {
    assert.equal(page.mode, 'search');
    assert.equal(page.total, 59);
    assert.equal(page.matched_total, 59);
    assert.equal(page.memory_total, 60);
    assert.equal(page.query_matched, true);
  }
});

test('English versus Russian zero matches falls back to all facts on every page', async () => {
  const russian = '\u043b\u044e\u0431\u0438\u0442 \u043a\u043e\u0444\u0435';
  const records = Array.from({ length: 67 }, (_, index) => fact(index + 1, `${russian} ${index}`));
  const pages = await collectFacts(fakeStore(records), { query: 'likes coffee', limit: 11 });
  assert.deepEqual(pages.flatMap(page => page.records), records);
  for (const page of pages) {
    assert.equal(page.mode, 'browse_fallback');
    assert.equal(page.matched_total, 0);
    assert.equal(page.query_matched, false);
    assert.equal(page.total, 67);
    assert.match(page.notice, /No lexical matches/);
    assert.match(page.notice, /unfiltered/);
    assert.match(page.notice, /does not establish.*absent/);
  }
  const translated = await readChatMemory(fakeStore(records), SESSION, {
    query: 'likes coffee', queries: [russian],
  });
  assert.equal(translated.mode, 'search');
  assert.equal(translated.matched_total, 67);
});

test('empty snapshots, empty queries and stopword-only searches terminate cleanly', async () => {
  for (const args of [{}, { query: '' }, { query: 'the and of' }]) {
    const page = await readChatMemory(fakeStore(), SESSION, args);
    assert.deepEqual(page.records, []);
    assert.equal(page.total, 0);
    assert.equal(page.next_cursor, null);
    assert.equal(page.truncated, false);
  }
  const store = fakeStore([fact(1)]);
  assert.equal((await readChatMemory(store, SESSION, { query: 'the and of' })).mode, 'browse_fallback');
});

test('exact-ID reads return the complete 16 KiB fact without scan or graph', async () => {
  const record = { ...fact(1, '\u{1f680}'.repeat(4096)), updatedAt: '2026-02-01T00:00:00.000Z' };
  const store = fakeStore([record]);
  const page = await readChatMemory(store, SESSION, { id: record.id });
  assert.deepEqual(page.records, [record]);
  assert.equal(Buffer.byteLength(page.records[0].text), 16384);
  assert.equal(page.mode, 'exact_id');
  assert.equal(page.total, 1);
  assert.equal(page.next_cursor, null);
  assert.equal(page.truncated, false);
  assert.deepEqual(store.calls.map(call => call.method), ['get']);
  assert.deepEqual((await readChatMemory(store, OTHER, { id: record.id })).records, []);
  assert.equal((await readChatMemory(store, SESSION, { id: id(99) })).total, 0);
});

test('rejects exact-ID/query/cursor combinations and invalid limits before storage', async () => {
  const store = fakeStore();
  for (const args of [
    { id: 'not-a-uuid' }, { id: id(1), query: '' }, { id: id(1), queries: [] },
    { id: id(1), cursor: 'anything' }, { limit: 0 }, { limit: 51 }, { limit: 1.5 },
  ]) {
    await assert.rejects(readChatMemory(store, SESSION, args), /chat_memory_invalid_(read|limit)/);
  }
  assert.equal(store.calls.length, 0);
});

test('fact cursors reject cross-session, changed-query and changed-alternative replay', async () => {
  const records = Array.from({ length: 4 }, (_, index) => fact(index + 1, 'red green blue'));
  const store = fakeStore(records);
  const args = { query: 'red', queries: ['green'], limit: 1 };
  const { next_cursor: cursor } = await readChatMemory(store, SESSION, args);
  for (const [session, changes] of [
    [OTHER, {}], [SESSION, { query: 'blue' }], [SESSION, { queries: ['blue'] }],
    [SESSION, { query: '', queries: [] }],
  ]) {
    await assert.rejects(readChatMemory(store, session, { ...args, ...changes, cursor }), errorMessage('chat_memory_invalid_cursor'));
  }
  assert.equal(store.calls.length, 1);
});

test('fact cursors reject malformed, noncanonical and oversized tokens', async () => {
  const store = fakeStore([fact(1), fact(2)]);
  const { next_cursor: cursor } = await readChatMemory(store, SESSION, { limit: 1 });
  for (const invalid of ['', null, 42, '!', 'e30', `${cursor}=`, 'x'.repeat(2049)]) {
    await assert.rejects(readChatMemory(store, SESSION, { cursor: invalid }), errorMessage('chat_memory_invalid_cursor'));
  }
  assert.equal(store.calls.length, 1);
});

test('browse, search and fallback continuations enforce the original revision fence', async () => {
  for (const args of [{}, { query: 'red' }, { query: 'coffee' }]) {
    const store = fakeStore([fact(1, 'red'), fact(2, 'red'), fact(3, 'red')]);
    const first = await readChatMemory(store, SESSION, { ...args, limit: 1 });
    assert.ok(first.next_cursor);
    store.bump();
    await assert.rejects(readChatMemory(store, SESSION, { ...args, cursor: first.next_cursor }), errorMessage('chat_memory_conflict'));
    assert.equal(store.calls.at(-1).expectedRevision, '1');
  }
});

test('byte budget paginates full large facts rather than clipping their text', async () => {
  for (const character of ['x', '\u0001', '\u{1f680}']) {
    const text = `${character.repeat(Math.floor(16000 / Buffer.byteLength(character)))} END`;
    const records = Array.from({ length: 17 }, (_, index) => fact(index + 1, text));
    for (const args of [{}, { query: 'END' }, { query: 'unmatched' }]) {
      const pages = await collectFacts(fakeStore(records), { ...args, limit: 50 });
      assert.ok(pages.length > 1, 'byte budget must paginate below the record-count limit');
      assert.deepEqual(pages.flatMap(page => page.records), records);
      for (const page of pages) {
        assert.ok(Buffer.byteLength(JSON.stringify(page)) <= PAGE_BYTES);
        assert.ok(page.records.every(record => record.text === text && record.text.endsWith(' END')));
      }
    }
  }
});

test('reader enforces compiler 512-byte and 24-term limits before scanning', async () => {
  const store = fakeStore();
  for (const args of [
    { query: 'x'.repeat(513) }, { query: '\u044f'.repeat(257) },
    { queries: ['x'.repeat(513)] }, { query: Array(25).fill('red').join(' ') },
    { queries: ['red', 'blue', 'green', 'yellow', 'black'] },
  ]) {
    await assert.rejects(readChatMemory(store, SESSION, args), errorMessage('chat_memory_invalid_query'));
  }
  assert.equal(store.calls.length, 0);
});

test('graph wrapper paginates opaque cursors and preserves bounded Unicode previews', async () => {
  const { store, records, edges } = graphFixture();
  const pages = [];
  let cursor;
  for (let count = 0; count < 10; count++) {
    const page = await readChatMemoryGraph(store, SESSION, { id: records[0].id, ...(cursor ? { cursor } : {}) });
    pages.push(page);
    assert.equal(page.node_id, records[0].id);
    assert.equal(page.previews_only, true);
    assert.equal(page.total, 26);
    assert.equal(page.revision, '1');
    assert.ok(page.edges.length <= 12);
    assert.ok(page.nodes.length <= page.edges.length + 1);
    for (const node of page.nodes) {
      assert.equal([...node.preview].length, 240);
      assert.equal(node.preview, '\u{1f680}'.repeat(240));
      assert.equal(node.truncated, true);
      assert.equal('text' in node, false);
    }
    if (page.next_cursor === null) break;
    assert.equal(typeof page.next_cursor, 'string');
    assert.ok(Buffer.byteLength(page.next_cursor) <= 2048);
    assert.notEqual(page.next_cursor, cursor);
    cursor = page.next_cursor;
  }
  assert.equal(pages.at(-1).next_cursor, null);
  assert.deepEqual(pages.map(page => page.edges.length), [12, 12, 2]);
  assert.deepEqual(pages.flatMap(page => page.edges), edges.map(edge => ({
    id: edge.id, from_id: edge.fromId, to_id: edge.toId, relation: edge.relation,
    note: edge.note, createdAt: edge.createdAt,
  })));
  assert.deepEqual(store.calls.map(call => call.cursor), [0, 12, 24]);
  assert.deepEqual(store.calls.map(call => call.expectedRevision), [undefined, '1', '1']);
});

test('graph limits are bounded and missing focus produces an empty page', async () => {
  const { store, records } = graphFixture();
  for (const limit of [0, 21, -1, 1.5, '12']) {
    await assert.rejects(readChatMemoryGraph(store, SESSION, { id: records[0].id, limit }), errorMessage('chat_memory_invalid_graph'));
  }
  assert.equal(store.calls.length, 0);
  const maximum = await readChatMemoryGraph(store, SESSION, { id: records[0].id, limit: 20 });
  assert.equal(maximum.edges.length, 20);
  assert.equal(maximum.nodes.length, 21);
  const missing = await readChatMemoryGraph(store, SESSION, { id: id(900) });
  assert.equal(missing.node_id, null);
  assert.deepEqual(missing.nodes, []);
  assert.deepEqual(missing.edges, []);
  assert.equal(missing.total, 0);
  assert.equal(missing.next_cursor, null);
});

test('graph cursors reject session, focus, kind and oversized-token mismatches', async () => {
  const { store, records } = graphFixture();
  const first = await readChatMemoryGraph(store, SESSION, { id: records[0].id, limit: 1 });
  const facts = await readChatMemory(store, SESSION, { limit: 1 });
  const before = store.calls.length;
  for (const [session, args] of [
    [OTHER, { id: records[0].id, cursor: first.next_cursor }],
    [SESSION, { id: records[1].id, cursor: first.next_cursor }],
    [SESSION, { id: records[0].id, cursor: facts.next_cursor }],
    [SESSION, { id: records[0].id, cursor: 'x'.repeat(2049) }],
    [SESSION, { id: records[0].id, cursor: '!' }],
  ]) {
    await assert.rejects(readChatMemoryGraph(store, session, args), errorMessage('chat_memory_invalid_cursor'));
  }
  await assert.rejects(readChatMemory(store, SESSION, { cursor: first.next_cursor }), errorMessage('chat_memory_invalid_cursor'));
  assert.equal(store.calls.length, before);
});

test('graph continuation enforces revision conflict rather than mixing snapshots', async () => {
  const { store, records } = graphFixture();
  const first = await readChatMemoryGraph(store, SESSION, { id: records[0].id, limit: 1 });
  store.bump();
  await assert.rejects(readChatMemoryGraph(store, SESSION, { id: records[0].id, cursor: first.next_cursor }), errorMessage('chat_memory_conflict'));
  assert.equal(store.calls.at(-1).expectedRevision, '1');
});

test('already-aborted browse, exact-ID and graph requests never access storage', async () => {
  const store = fakeStore([fact(1)]);
  const controller = new AbortController();
  controller.abort();
  const aborted = error => error === controller.signal.reason;
  await assert.rejects(readChatMemory(store, SESSION, {}, controller), aborted);
  await assert.rejects(readChatMemory(store, SESSION, { id: id(1) }, controller), aborted);
  await assert.rejects(readChatMemoryGraph(store, SESSION, { id: id(1) }, controller), aborted);
  assert.equal(store.calls.length, 0);
});

test('abort during browse/search scan and after snapshot discards the response', async () => {
  for (const query of ['', 'red']) {
    for (const hook of ['beforeVisit', 'afterScan']) {
      const store = fakeStore([fact(1, 'red'), fact(2, 'red')]);
      const controller = new AbortController();
      store.hooks[hook] = () => controller.abort();
      await assert.rejects(readChatMemory(store, SESSION, { query }, controller), error => error === controller.signal.reason);
    }
  }
});

test('abort while awaiting exact-ID or graph storage discards the response', async () => {
  for (const kind of ['get', 'graph']) {
    const { store, records } = graphFixture();
    const controller = new AbortController();
    store.hooks[kind === 'get' ? 'afterGet' : 'afterGraph'] = () => controller.abort();
    const read = kind === 'get' ? readChatMemory : readChatMemoryGraph;
    await assert.rejects(read(store, SESSION, { id: records[0].id }, controller), error => error === controller.signal.reason);
  }
});
