import test from 'node:test';
import assert from 'node:assert/strict';
import { registerChatMemoryTools } from './chat-memory-tools.mjs';

const SESSION = 'synthetic-owner';
const A = '00000000-0000-4000-8000-000000000001';
const B = '00000000-0000-4000-8000-000000000002';
const EDGE = '00000000-0000-4000-8000-000000000003';
const RELATIONS = ['related', 'supports', 'contradicts', 'depends_on', 'updates', 'part_of'];
const SOURCE = { file: 'notes/book.txt', chapter: 'Synthetic chapter', line_from: 4, line_to: 9,
  anchor: 'synthetic quotation', sha256: 'a'.repeat(64) };
const RUSSIAN = '\u043b\u044e\u0431\u0438\u0442 \u043a\u043e\u0444\u0435';
const RECORD = { id: A, text: RUSSIAN, aliases: ['likes coffee'], sources: [SOURCE],
  createdAt: '2026-01-01T00:00:00Z', privateField: 'do not expose', session: 'private key' };
const exec = (session = SESSION) => ({ agent: { session: { id: session } } });

async function fixture(t, records = [RECORD]) {
  let tool;
  const calls = [], prompts = [], forbidden = [];
  let revision = '7';
  const implementations = {
    scan: async (session, visit, expectedRevision) => {
      if (expectedRevision !== undefined && expectedRevision !== revision) throw new Error('chat_memory_conflict');
      const rows = session === SESSION ? records : [];
      for (const [index, record] of rows.entries()) if (visit(structuredClone(record), index + 1) === false) break;
      return { revision, total: rows.length };
    },
    get: async (session, id) => session === SESSION ? structuredClone(records.find(record => record.id === id) ?? null) : null,
    graph: async (_session, options) => {
      if (options.expectedRevision !== undefined && options.expectedRevision !== revision) throw new Error('chat_memory_conflict');
      return { nodeId: options.id, nodes: [{ id: A, preview: 'synthetic', truncated: true }],
        edges: [{ id: EDGE, fromId: A, toId: B, relation: 'supports', note: 'synthetic', createdAt: RECORD.createdAt }],
        total: 2, nextCursor: options.cursor === 0 ? 1 : null, revision };
    },
    link: async (_session, fromId, toId, relation = 'related', note = '') => ({
      id: EDGE, fromId, toId, relation, note, createdAt: RECORD.createdAt,
    }),
    unlink: async (_session, edgeId) => edgeId === EDGE,
    add: async () => ({ id: A }), update: async () => ({ id: A }), delete: async () => true,
  };
  const store = new Proxy({}, { get(_object, name) {
    if (!Object.hasOwn(implementations, name)) {
      forbidden.push(`store.${String(name)}`);
      throw new Error('Unexpected store API');
    }
    return async (...args) => {
      // scan's callback is an in-process function, never a serialized payload.
      calls.push([name, ...args]);
      return implementations[name](...args);
    };
  } });
  const ctx = new Proxy({
    systemPrompt: { context: value => prompts.push(value) },
    tools: { register: value => { tool = value; } },
    on(name) { assert.equal(name,'agent/created'); },
  }, { get(object, name) {
    if (!Object.hasOwn(object, name)) {
      forbidden.push(`ctx.${String(name)}`);
      throw new Error('Unexpected GPU, provider or Mnemon API');
    }
    return object[name];
  } });
  t.mock.method(globalThis, 'fetch', async () => {
    forbidden.push('network fetch');
    throw new Error('Network must not be used');
  });
  await registerChatMemoryTools(ctx, { store, defineTool: value => value });
  t.after(() => assert.deepEqual(forbidden, []));
  return { tool, calls, prompts, implementations, bump: () => { revision = String(Number(revision) + 1); } };
}

test('tool schema exposes graph actions, typed links, translations and metadata CAS', async t => {
  const { tool, prompts } = await fixture(t);
  assert.equal(tool.name, 'chat_memory');
  for (const action of ['remember', 'recall', 'update', 'forget', 'link', 'unlink', 'related']) {
    assert.ok(tool.parameters.action.enum.includes(action));
  }
  assert.deepEqual(tool.parameters.relation.enum, RELATIONS);
  for (const name of ['queries', 'aliases', 'expected_aliases', 'sources', 'expected_sources']) {
    assert.equal(tool.parameters[name].type, 'array', name);
  }
  assert.equal(tool.parameters.sources.items.additionalProperties, false);
  assert.deepEqual(Object.keys(tool.parameters.sources.items.properties).sort(),
    ['anchor', 'chapter', 'file', 'line_from', 'line_to', 'sha256']);
  const graphPrompt = prompts.find(prompt => prompt.name === 'gateway:chat-memory-graph');
  assert.equal(graphPrompt.required, true);
  assert.match(graphPrompt.text, /UNFILTERED/);
  assert.match(graphPrompt.text, /do not prove absence/);
  assert.match(graphPrompt.text, /not multilingual semantic/);
});

test('all new tool actions ignore spoofed scopes and use only the executing session', async t => {
  const { tool, calls } = await fixture(t);
  const spoof = { sessionId: 'foreign', session_id: 'foreign', scope: 'account', workspace: '/foreign',
    agent: { session: { id: 'foreign' } } };
  await tool.execute({ action: 'link', from_id: A, to_id: B, relation: 'supports', ...spoof }, exec());
  await tool.execute({ action: 'unlink', edge_id: EDGE, ...spoof }, exec());
  await tool.execute({ action: 'related', id: A, limit: 1, ...spoof }, exec());
  await tool.execute({ action: 'recall', id: A, ...spoof }, exec());
  await tool.execute({ action: 'remember', text: 'synthetic', aliases: ['hint'], ...spoof }, exec());
  await tool.execute({ action: 'update', id: A, text: 'new', expected_text: RUSSIAN,
    aliases: [], expected_aliases: RECORD.aliases, ...spoof }, exec());
  assert.ok(calls.every(call => call[1] === SESSION));
  assert.deepEqual(calls.map(call => call[0]), ['link', 'unlink', 'graph', 'get', 'add', 'update']);
});

test('missing trusted tool session rejects every action before storage', async t => {
  const { tool, calls } = await fixture(t);
  for (const context of [undefined, {}, { agent: {} }, { agent: { session: {} } }, exec(''), exec(42)]) {
    for (const action of ['link', 'unlink', 'related', 'recall', 'remember', 'update', 'forget']) {
      await assert.rejects(tool.execute({ action, id: A, edge_id: EDGE, from_id: A, to_id: B,
        text: 'synthetic', sessionId: SESSION }, context), /chat_memory_session_unavailable/);
    }
  }
  assert.deepEqual(calls, []);
});

test('tool link enum values and edge-ID unlink dispatch without deleting nodes', async t => {
  const { tool, calls } = await fixture(t);
  for (const relation of RELATIONS) {
    const result = await tool.execute({ action: 'link', from_id: A, to_id: B, relation, note: 'synthetic' }, exec());
    assert.equal(result.linked, true);
    assert.equal(result.edge.relation, relation);
    assert.deepEqual(calls.at(-1), ['link', SESSION, A, B, relation, 'synthetic']);
  }
  assert.deepEqual(await tool.execute({ action: 'unlink', edge_id: EDGE, id: A }, exec()), { unlinked: true });
  assert.deepEqual(calls.at(-1), ['unlink', SESSION, EDGE]);
});

test('tool translated queries and saved aliases retrieve full Russian facts without invented translation', async t => {
  const noAliases = { ...RECORD, aliases: [] };
  const f = await fixture(t, [noAliases]);
  const fallback = await f.tool.execute({ action: 'recall', query: 'likes coffee' }, exec());
  assert.equal(fallback.mode, 'browse_fallback');
  assert.equal(fallback.query_matched, false);
  const translated = await f.tool.execute({ action: 'recall', query: 'likes coffee', queries: [RUSSIAN] }, exec());
  assert.equal(translated.mode, 'search');
  assert.equal(translated.matched_total, 1);
  assert.equal(translated.records[0].text, RUSSIAN);
  f.implementations.scan = async (_session, visit) => {
    visit(structuredClone(RECORD), 1);
    return { total: 1, revision: '7' };
  };
  const aliasMatch = await f.tool.execute({ action: 'recall', query: 'likes coffee' }, exec());
  assert.equal(aliasMatch.mode, 'search');
  assert.deepEqual(aliasMatch.records[0].aliases, RECORD.aliases);
});

test('tool exact recall sanitizes record fields while preserving full text, aliases and sources', async t => {
  const record = { ...RECORD, text: '\u{1f680}'.repeat(4096) };
  const { tool, calls } = await fixture(t, [record]);
  const result = await tool.execute({ action: 'recall', id: A }, exec());
  const { privateField, session, ...view } = record;
  assert.deepEqual(result.records, [view]);
  assert.equal(result.mode, 'exact_id');
  assert.deepEqual(calls, [['get', SESSION, A]]);
  assert.deepEqual(result.records[0].sources, [SOURCE]);
});

test('tool related wraps cursors, maps edges and forwards the snapshot revision', async t => {
  const { tool, calls, bump } = await fixture(t);
  const first = await tool.execute({ action: 'related', id: A, limit: 1 }, exec());
  assert.equal(first.previews_only, true);
  assert.equal(first.revision, '7');
  assert.equal(first.edges[0].from_id, A);
  assert.equal(first.edges[0].to_id, B);
  assert.equal('fromId' in first.edges[0], false);
  assert.equal(typeof first.next_cursor, 'string');
  await tool.execute({ action: 'related', id: A, cursor: first.next_cursor, limit: 1 }, exec());
  assert.deepEqual(calls.at(-1), ['graph', SESSION, { id: A, cursor: 1, limit: 1, expectedRevision: '7' }]);
  bump();
  await assert.rejects(tool.execute({ action: 'related', id: A, cursor: first.next_cursor }, exec()), /chat_memory_conflict/);
});

test('tool malformed reader fields and cursors reject before store dispatch', async t => {
  const { tool, calls } = await fixture(t);
  for (const args of [
    { action: 'related', id: '*' }, { action: 'related', id: A, limit: 21 },
    { action: 'related', id: A, cursor: 1 }, { action: 'related', id: A, cursor: '!' },
    { action: 'recall', id: A, query: 'mixed modes' }, { action: 'recall', queries: [1] },
    { action: 'recall', query: 'x'.repeat(513) }, { action: 'recall', limit: 51 },
  ]) await assert.rejects(tool.execute(args, exec()), /chat_memory_invalid_/);
  assert.deepEqual(calls, []);
});

test('tool alias updates forward exact CAS and leave omitted metadata untouched', async t => {
  const { tool, calls } = await fixture(t);
  await tool.execute({ action: 'remember', text: RUSSIAN, aliases: ['likes coffee'] }, exec());
  assert.deepEqual(calls.at(-1), ['add', SESSION, RUSSIAN, undefined, { aliases: ['likes coffee'] }]);
  await tool.execute({ action: 'update', id: A, text: 'new', expected_text: RUSSIAN,
    aliases: [], expected_aliases: RECORD.aliases }, exec());
  assert.deepEqual(calls.at(-1), ['update', SESSION, A, 'new', RUSSIAN,
    { aliases: [], expectedAliases: RECORD.aliases }]);
  await tool.execute({ action: 'update', id: A, text: 'text only', expected_text: RUSSIAN }, exec());
  assert.deepEqual(calls.at(-1), ['update', SESSION, A, 'text only', RUSSIAN]);
});

test('tool citation writes forward source CAS separately and together with alias CAS', async t => {
  const { tool, calls } = await fixture(t);
  const sources = [{ ...SOURCE, chapter: 'Next chapter' }];
  await tool.execute({ action: 'remember', text: 'cited synthetic', sources }, exec());
  assert.deepEqual(calls.at(-1), ['add', SESSION, 'cited synthetic', undefined, { sources }]);
  await tool.execute({ action: 'update', id: A, text: 'cited update', expected_text: RUSSIAN,
    sources, expected_sources: RECORD.sources }, exec());
  assert.deepEqual(calls.at(-1), ['update', SESSION, A, 'cited update', RUSSIAN,
    { sources, expectedSources: RECORD.sources }]);
  await tool.execute({ action: 'update', id: A, text: 'both', expected_text: RUSSIAN,
    aliases: [], expected_aliases: RECORD.aliases, sources: [], expected_sources: RECORD.sources }, exec());
  assert.deepEqual(calls.at(-1), ['update', SESSION, A, 'both', RUSSIAN,
    { aliases: [], expectedAliases: RECORD.aliases, sources: [], expectedSources: RECORD.sources }]);
});

test('tool propagates store validation and metadata conflicts without compensating writes', async t => {
  const { tool, calls, implementations } = await fixture(t);
  for (const [action, method, code, fields] of [
    ['link', 'link', 'invalid_relation', { from_id: A, to_id: B, relation: 'invalid' }],
    ['unlink', 'unlink', 'invalid_id', { edge_id: '*' }],
    ['update', 'update', 'chat_memory_conflict', { id: A, text: 'new', expected_text: RUSSIAN,
      aliases: [], expected_aliases: ['stale'], sources: [], expected_sources: [] }],
  ]) {
    implementations[method] = async () => { throw new Error(code); };
    const before = calls.length;
    await assert.rejects(tool.execute({ action, ...fields }, exec()), error => error.message === code);
    assert.equal(calls.length, before + 1);
    assert.equal(calls.at(-1)[0], method);
  }
  const before = calls.length;
  await assert.rejects(tool.execute({action:'remember',text:'synthetic',sources:[{file:'../bad'}]},exec()), /invalid_sources: use a workspace-relative/);
  assert.equal(calls.length,before);
});

test('tool output renderer emits result data and aborted reads never touch storage', async t => {
  const { tool, calls } = await fixture(t);
  const value = { linked: true, edge: { id: EDGE } };
  assert.deepEqual(tool.output.render({}, value), [{ type: 'text', text: JSON.stringify(value) }]);
  const controller = new AbortController();
  controller.abort();
  for (const args of [{ action: 'recall' }, { action: 'related', id: A }]) {
    await assert.rejects(tool.execute(args, { ...exec(), signal: controller.signal }), error => error === controller.signal.reason);
  }
  assert.deepEqual(calls, []);
});
