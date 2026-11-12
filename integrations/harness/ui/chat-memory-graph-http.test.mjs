import test from 'node:test';
import assert from 'node:assert/strict';
import { registerChatMemoryHttp } from './chat-memory-http.mjs';

const A = '00000000-0000-4000-8000-000000000001';
const B = '00000000-0000-4000-8000-000000000002';
const EDGE = '00000000-0000-4000-8000-000000000003';
const RELATIONS = ['related', 'supports', 'contradicts', 'depends_on', 'updates', 'part_of'];
const SOURCE = {
  file: 'notes/book.txt', chapter: 'Synthetic chapter', line_from: 4, line_to: 9,
  anchor: 'synthetic quotation', sha256: 'a'.repeat(64),
};
const RECORD = {
  id: A, text: '\u043b\u044e\u0431\u0438\u0442 \u043a\u043e\u0444\u0435', aliases: ['likes coffee'],
  sources: [SOURCE], createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-02T00:00:00Z',
  privateField: 'must not be returned', session: 'private storage key', seq: 99,
};

function fixture(t) {
  let route;
  const calls = [], forbidden = [], resolutions = [];
  const implementations = {
    get: async (_session, id, revision) => {
      if (revision !== undefined && revision !== '7') throw new Error('chat_memory_conflict');
      return id === A ? structuredClone(RECORD) : null;
    },
    graph: async (_session, options) => {
      if (options.expectedRevision !== undefined && options.expectedRevision !== '7') throw new Error('chat_memory_conflict');
      return { nodeId: options.id ?? A, nodes: [{ id: A, preview: 'synthetic', truncated: true }],
        edges: [], total: 0, nextCursor: null, revision: '7' };
    },
    link: async (_session, fromId, toId, relation = 'related', note = '') => ({
      id: EDGE, fromId, toId, relation, note, createdAt: RECORD.createdAt,
    }),
    unlink: async (_session, id) => id === EDGE,
    add: async (_session, text, requestId, options) => ({ ...RECORD, id: requestId ?? A, text, ...options }),
    update: async (_session, id, text, expectedText, options) => {
      if (expectedText !== RECORD.text) throw new Error('chat_memory_conflict');
      return { ...RECORD, id, text, ...(options?.aliases === undefined ? {} : { aliases: options.aliases }),
        ...(options?.sources === undefined ? {} : { sources: options.sources }) };
    },
    page: async () => ({ records: [structuredClone(RECORD)], nextCursor: null, revision: '7' }),
  };
  const store = new Proxy({}, { get(_object, name) {
    if (!Object.hasOwn(implementations, name)) {
      forbidden.push(`store.${String(name)}`);
      throw new Error('Unexpected store API');
    }
    return async (...args) => {
      calls.push([name, ...structuredClone(args)]);
      return implementations[name](...args);
    };
  } });
  const services = {
    effect: setup => setup(),
    connection: { fetch: { register: value => { route = value; } } },
    sessions: { get: session => {
      resolutions.push(session);
      return session === 'active' ? { id: session } : undefined;
    } },
    workingDirectory: { get: async () => '/synthetic/workspace', defaultDirectory: '/synthetic/default' },
    get: name => {
      assert.equal(name, 'sessionPersistence');
      return { stat: async session => session === 'persisted' ? { header: { cwd: '/synthetic/persisted' } } : undefined };
    },
  };
  const ctx = new Proxy(services, { get(object, name) {
    if (!Object.hasOwn(object, name)) {
      forbidden.push(`ctx.${String(name)}`);
      throw new Error('Unexpected external service');
    }
    return object[name];
  } });
  t.mock.method(globalThis, 'fetch', async () => {
    forbidden.push('network fetch');
    throw new Error('Network must not be used');
  });
  registerChatMemoryHttp(ctx, { store });
  t.after(() => assert.deepEqual(forbidden, [], 'no GPU, provider, Mnemon or network service may be used'));
  return {
    calls, resolutions, implementations, services,
    async send(input, options = {}) {
      const request = new Request('http://synthetic.invalid/api/gateway-chat-memory/manage', {
        method: 'POST', headers: { 'Content-Type': 'application/json', ...options.headers },
        body: options.raw ?? JSON.stringify(input), signal: options.signal,
      });
      const response = await route.fetch(request);
      assert.equal(response.headers.get('cache-control'), 'no-store');
      return response.json();
    },
    get route() { return route; },
  };
}

const request = (action, fields = {}) => ({ action, sessionId: 'active', ...fields });
const invalid = { ok: false, error: { code: 'invalid_request' } };

test('HTTP get dispatches a revision-fenced ID and sanitizes full fact fields', async t => {
  const f = fixture(t);
  assert.equal(f.route.path, '/api/gateway-chat-memory/manage');
  assert.deepEqual(f.route.methods, ['POST']);
  const result = await f.send(request('get', { id: A, expectedRevision: '7' }));
  assert.deepEqual(f.calls, [['get', 'active', A, '7']]);
  const { privateField, session, seq, ...view } = RECORD;
  assert.deepEqual(result, { ok: true, value: { record: view } });
  assert.deepEqual(await f.send(request('get', { id: B })), { ok: true, value: { record: null } });
});

test('HTTP graph forwards revision, numeric page options and selected/default focus', async t => {
  const f = fixture(t);
  const result = await f.send(request('graph', { id: A, cursor: 3, limit: 2, expectedRevision: '7' }));
  assert.equal(result.ok, true);
  assert.equal(result.value.revision, '7');
  assert.deepEqual(f.calls[0], ['graph', 'active', { id: A, cursor: 3, limit: 2, expectedRevision: '7' }]);
  await f.send(request('graph', { sessionId: 'persisted' }));
  assert.deepEqual(f.calls[1], ['graph', 'persisted', { id: undefined, cursor: 0, limit: 20, expectedRevision: undefined }]);
});

test('HTTP link supports all six enums and unlink routes the edge ID, not a node deletion', async t => {
  const f = fixture(t);
  for (const relation of RELATIONS) {
    const result = await f.send(request('link', { fromId: A, toId: B, relation, note: 'synthetic evidence' }));
    assert.equal(result.value.edge.relation, relation);
    assert.deepEqual(f.calls.at(-1), ['link', 'active', A, B, relation, 'synthetic evidence']);
  }
  assert.equal((await f.send(request('link', { fromId: A, toId: B }))).value.edge.relation, 'related');
  assert.deepEqual(await f.send(request('unlink', { id: EDGE })), { ok: true, value: { unlinked: true } });
  assert.deepEqual(f.calls.at(-1), ['unlink', 'active', EDGE]);
  assert.ok(f.calls.every(call => call[0] === 'link' || call[0] === 'unlink'));
});

test('HTTP malformed IDs, revisions, fields and graph bounds never reach storage', async t => {
  const f = fixture(t);
  const inputs = [
    request('get'), request('get', { id: '*' }), request('graph', { id: null }),
    request('unlink', { edge_id: EDGE }), request('link', { from_id: A, to_id: B }),
    request('link', { fromId: A, toId: A }), request('link', { fromId: A, toId: '*' }),
    request('link', { fromId: A, toId: B, relation: 'invented' }),
    request('link', { fromId: A, toId: B, note: '\u044f'.repeat(129) }),
    request('graph', { limit: 21 }), request('graph', { limit: '2' }),
    request('graph', { cursor: -1 }), request('graph', { cursor: null }),
    request('graph', { cursor: Number.MAX_SAFE_INTEGER }),
    request('get', { id: A, workspace: '/spoof' }), request('graph', { query: 'not allowed' }),
  ];
  for (const revision of [null, 7, '', '-1', '01', '1.0', '9'.repeat(33), {}]) {
    inputs.push(request('get', { id: A, expectedRevision: revision }));
    inputs.push(request('graph', { expectedRevision: revision }));
  }
  for (const input of inputs) assert.deepEqual(await f.send(input), invalid, JSON.stringify(input));
  assert.deepEqual(f.calls, []);
  assert.deepEqual(f.resolutions, []);
});

test('HTTP rejects explicit null relation before storage rather than delegating an invalid enum', async t => {
  const f = fixture(t);
  assert.deepEqual(await f.send(request('link', { fromId: A, toId: B, relation: null })), invalid);
  assert.deepEqual(f.calls, []);
});

test('HTTP rejects explicit null graph limit rather than silently selecting the default', async t => {
  const f = fixture(t);
  assert.deepEqual(await f.send(request('graph', { id: A, limit: null })), invalid);
  assert.deepEqual(f.calls, []);
});

test('unresolved HTTP sessions cannot be supplied by auth or scope headers', async t => {
  const f = fixture(t);
  for (const input of [
    request('get', { sessionId: 'missing', id: A }), request('graph', { sessionId: 'missing', id: A }),
    request('link', { sessionId: 'missing', fromId: A, toId: B }), request('unlink', { sessionId: 'missing', id: EDGE }),
  ]) {
    assert.deepEqual(await f.send(input, { headers: {
      Authorization: 'Bearer synthetic-not-an-authentication-bypass', 'X-Session-Id': 'active',
    } }), { ok: false, error: { code: 'session_unavailable' } });
  }
  f.services.workingDirectory.get = async () => '';
  assert.equal((await f.send(request('graph', { id: A }))).error.code, 'session_unavailable');
  assert.deepEqual(f.calls, []);
});

test('HTTP alias writes preserve CAS options; text-only updates omit alias options', async t => {
  const f = fixture(t);
  await f.send(request('add', { text: RECORD.text, aliases: ['likes coffee'], requestId: B }));
  assert.deepEqual(f.calls.at(-1), ['add', 'active', RECORD.text, B, { aliases: ['likes coffee'] }]);
  await f.send(request('update', { id: A, text: 'new fact', expectedText: RECORD.text,
    aliases: ['new hint'], expectedAliases: RECORD.aliases }));
  assert.deepEqual(f.calls.at(-1), ['update', 'active', A, 'new fact', RECORD.text,
    { aliases: ['new hint'], expectedAliases: RECORD.aliases }]);
  await f.send(request('update', { id: A, text: 'text only', expectedText: RECORD.text }));
  assert.deepEqual(f.calls.at(-1), ['update', 'active', A, 'text only', RECORD.text]);
  const listed = await f.send(request('list', { query: 'likes coffee', includeRevision: true }));
  assert.deepEqual(f.calls.at(-1), ['page', 'active', 0, 50, 'likes coffee', true]);
  assert.deepEqual(listed.value.records[0].aliases, RECORD.aliases);
});

test('HTTP malformed aliases and absent/malformed alias CAS reject before storage', async t => {
  const f = fixture(t);
  for (const aliases of [null, 'hint', [1], [''], ['\u044f'.repeat(41)], Array(13).fill('hint')]) {
    assert.deepEqual(await f.send(request('add', { text: 'synthetic', aliases })), invalid);
  }
  for (const fields of [
    { aliases: [] }, { aliases: ['new'], expectedAliases: null },
    { aliases: ['new'], expectedAliases: [1] }, { expectedAliases: [] },
  ]) {
    assert.deepEqual(await f.send(request('update', { id: A, text: 'new', expectedText: RECORD.text, ...fields })), invalid);
  }
  assert.deepEqual(f.calls, []);
});

test('HTTP sources are forwarded and sanitized with independent metadata CAS', async t => {
  const f = fixture(t);
  const sources = [{ ...SOURCE, chapter: 'New synthetic chapter' }];
  const added = await f.send(request('add', { text: 'cited fact', sources }));
  assert.equal(added.ok, true);
  assert.deepEqual(added.value.record.sources, sources);
  assert.deepEqual(f.calls.at(-1), ['add', 'active', 'cited fact', undefined, { sources }]);
  const updated = await f.send(request('update', { id: A, text: 'cited update', expectedText: RECORD.text,
    sources, expectedSources: RECORD.sources, aliases: [], expectedAliases: RECORD.aliases }));
  assert.equal(updated.ok, true);
  assert.deepEqual(f.calls.at(-1), ['update', 'active', A, 'cited update', RECORD.text,
    { aliases: [], expectedAliases: RECORD.aliases, sources, expectedSources: RECORD.sources }]);
  assert.deepEqual(updated.value.record.sources, sources);
  assert.equal('expectedSources' in updated.value.record, false);
  assert.equal('privateField' in updated.value.record, false);
});

test('HTTP invalid source descriptors and missing source CAS reject before storage', async t => {
  const f = fixture(t);
  const sources = [null, 'source', Array(5).fill(SOURCE)];
  const oversized = Array.from({ length: 4 }, (_, index) => ({
    ...SOURCE, file: `${index}${'x'.repeat(500)}`, chapter: 'c'.repeat(128), anchor: 'a'.repeat(512),
  }));
  assert.ok(Buffer.byteLength(JSON.stringify(oversized)) > 4096);
  sources.push(oversized);
  for (const change of [
    { file: '../secret' }, { file: '/absolute' }, { file: 'C:/absolute' }, { file: 'notes\\book.txt' },
    { chapter: 'x'.repeat(129) }, { anchor: '\u044f'.repeat(257) }, { sha256: 'not-a-hash' },
    { line_from: 0 }, { line_to: 1000000001 }, { line_from: 10, line_to: 9 },
    { line_from: '4' }, { line_to: 1.5 }, { unknown: 'not allowed' },
  ]) sources.push([{ ...SOURCE, ...change }]);
  for (const value of sources) {
    assert.deepEqual(await f.send(request('add', { text: 'synthetic', sources: value })), invalid, JSON.stringify(value));
  }
  for (const fields of [
    { sources: [] }, { sources: [], expectedSources: null }, { expectedSources: [] },
    { sources: [], expectedSources: [{ ...SOURCE, line_from: 0 }] },
  ]) {
    assert.deepEqual(await f.send(request('update', { id: A, text: 'new', expectedText: RECORD.text, ...fields })), invalid);
  }
  assert.deepEqual(f.calls, []);
});

test('HTTP source metadata can be cleared without changing aliases', async t => {
  const f = fixture(t);
  const result = await f.send(request('update', { id: A, text: RECORD.text, expectedText: RECORD.text,
    sources: [], expectedSources: RECORD.sources }));
  assert.equal(result.ok, true);
  assert.deepEqual(f.calls, [['update', 'active', A, RECORD.text, RECORD.text,
    { sources: [], expectedSources: RECORD.sources }]]);
  assert.deepEqual(result.value.record.aliases, RECORD.aliases);
  assert.equal('sources' in result.value.record, false);
});

test('HTTP conflicts and unexpected failures are sanitized without extra dispatch', async t => {
  const f = fixture(t);
  assert.deepEqual(await f.send(request('get', { id: A, expectedRevision: '8' })), { ok: false, error: { code: 'conflict' } });
  assert.deepEqual(await f.send(request('graph', { id: A, expectedRevision: '8' })), { ok: false, error: { code: 'conflict' } });
  f.implementations.link = async () => { throw new Error('private provider path or token'); };
  assert.deepEqual(await f.send(request('link', { fromId: A, toId: B })), { ok: false, error: { code: 'unavailable' } });
  assert.deepEqual(f.calls.map(call => call[0]), ['get', 'graph', 'link']);
});

test('HTTP abort and malformed envelopes never dispatch memory operations', async t => {
  const f = fixture(t);
  const controller = new AbortController();
  controller.abort();
  assert.equal((await f.send(request('graph', { id: A }), { signal: controller.signal })).ok, false);
  assert.deepEqual(await f.send({}, { raw: '{' }), invalid);
  assert.deepEqual(await f.send(request('graph'), { headers: { 'Content-Type': 'text/plain' } }), invalid);
  assert.deepEqual(f.calls, []);
});
