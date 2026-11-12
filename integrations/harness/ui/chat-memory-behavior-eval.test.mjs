import test from 'node:test';
import assert from 'node:assert/strict';
import {
  evaluateMemoryTrace, MEMORY_TRACE_SCENARIOS, MEMORY_TRACE_SCOPE,
  MEMORY_TRACE_LIMITS, MEMORY_TRACE_LABELS,
} from './chat-memory-behavior-eval.mjs';

// These are observations of fictional tools, not calls to the actual tools.
const tool = (name, args, result, extra = {}) => ({ kind: 'tool', name, scope: 'chat-a', args, result, ...extra });
const memory = (args, result, extra) => tool('chat_memory', args, result, extra);
const answer = claim => ({ kind: 'answer', claims: [claim] });
const recall = (id = 'node-a', text = 'fact-v1', extra = {}) => memory(
  { action: 'recall', id }, { mode: 'exact_id', records: [{ id, text, ...extra }], next_cursor: null });
const remember = () => memory({ action: 'remember', text: 'fact-v1' }, { saved: true, id: 'node-a' });
const update = () => memory({ action: 'update', id: 'node-a', text: 'fact-v2', expected_text: 'fact-v1' },
  { updated: true, id: 'node-a' });
const related = (relation = 'depends_on') => memory({ action: 'related', id: 'node-a' }, {
  previews_only: true, nodes: [{ id: 'node-b', truncated: true }], next_cursor: null,
  edges: [{ id: 'edge-a', from_id: 'node-a', to_id: 'node-b', relation }],
});
const englishMiss = () => memory({ action: 'recall', query: 'query-en' }, {
  mode: 'browse_fallback', query_matched: false, records: [], next_cursor: null,
});
const russianHit = () => memory({ action: 'recall', query: 'query-en', queries: ['query-ru'] }, {
  mode: 'search', matched_total: 1, records: [{ id: 'node-a', text: 'fact-ru-v1' }], next_cursor: null,
});
const citedRecall = () => recall('node-a', 'fact-v1', { sources: [
  { file: 'file-a', version: 'version-1', line_from: 1, line_to: 2 },
] });
const fileRead = () => tool('file_read', { file: 'file-a' }, {
  file: 'file-a', version: 'version-2', line_from: 4, line_to: 8,
});
const fileEdit = () => tool('file_edit', {
  file: 'file-a', expected_version: 'version-2', line_from: 4, line_to: 5,
}, { edited: true, file: 'file-a', version: 'version-3' });
const resume = () => tool('context_resume', { reason: 'compaction' }, { resumed: true });
const failedWrite = () => memory({ action: 'remember', text: 'fact-v1' }, { error: 'write-failed' }, { isError: true });

function positive(id) {
  const events = {
    'remember-after-write': () => [remember()],
    'continuation-recall': () => [recall()],
    'ru-en-fallback': () => [englishMiss(), russianHit()],
    'atomic-update': () => [recall(), update()],
    'linked-node-read': () => [recall(), related(), recall('node-b', 'fact-v2')],
    'contradictory-nodes': () => [recall(), related('contradicts'), recall('node-b', 'fact-v2')],
    'source-drift': () => [citedRecall(), fileRead(), fileEdit()],
    'scope-isolation': () => [remember(), recall()],
    'failed-write': () => [failedWrite()],
    'compaction-resume': () => [recall(), resume(), recall()],
  }[id]();
  return [...events, answer(MEMORY_TRACE_SCENARIOS.find(scenario => scenario.id === id).claim)];
}

function fails(id, trace, checkId) {
  const result = evaluateMemoryTrace(id, trace);
  assert.equal(result.passed, false, `${id} unexpectedly passed`);
  if (checkId) assert.equal(result.checks.find(check => check.id === checkId)?.passed, false, checkId);
  return result;
}

test('exports ten frozen, synthetic-only scenario definitions and bounded constants', () => {
  assert.equal(MEMORY_TRACE_SCENARIOS.length, 10);
  assert.equal(new Set(MEMORY_TRACE_SCENARIOS.map(scenario => scenario.id)).size, 10);
  for (const scenario of MEMORY_TRACE_SCENARIOS) {
    assert.equal(scenario.scope, 'synthetic-protocol-only');
    assert.equal(scenario.chat, MEMORY_TRACE_LABELS.chat);
    assert.ok(Object.isFrozen(scenario));
    assert.ok(Object.isFrozen(scenario.actions));
    assert.ok(Object.isFrozen(scenario.adapters));
  }
  for (const value of [MEMORY_TRACE_SCENARIOS, MEMORY_TRACE_LIMITS, MEMORY_TRACE_LABELS]) assert.ok(Object.isFrozen(value));
});

for (const scenario of MEMORY_TRACE_SCENARIOS) {
  test(`${scenario.id}: positive protocol evidence passes without mutation`, () => {
    const trace = positive(scenario.id);
    const snapshot = structuredClone(trace);
    const result = evaluateMemoryTrace(scenario.id, trace);
    assert.equal(result.passed, true, JSON.stringify(result));
    assert.equal(result.scope, MEMORY_TRACE_SCOPE);
    assert.equal(result.schemaVersion, 1);
    assert.equal(result.scenarioId, scenario.id);
    assert.deepEqual(trace, snapshot);
    assert.deepEqual(evaluateMemoryTrace(scenario.id, trace), result);
    assert.deepEqual(Object.keys(result).sort(), ['checks', 'passed', 'scenarioId', 'schemaVersion', 'scope']);
    for (const check of result.checks) {
      assert.deepEqual(Object.keys(check).sort(), ['id', 'passed']);
      assert.equal(typeof check.passed, 'boolean');
    }
  });

  test(`${scenario.id}: missing evidence and early acknowledgements fail`, () => {
    const trace = positive(scenario.id);
    fails(scenario.id, [trace.at(-1)], 'answer.prior-protocol-evidence');
    fails(scenario.id, [trace.at(-1), ...trace], 'answer.prior-protocol-evidence');
    fails(scenario.id, trace.slice(0, -1), 'answer.present-and-final');
    fails(scenario.id, [...trace, recall()], 'answer.present-and-final');
  });

  test(`${scenario.id}: foreign scope, foreign node, Mnemon and destructive traces fail`, () => {
    const trace = positive(scenario.id);
    const crossChat = structuredClone(trace);
    crossChat[0].scope = 'chat-b';
    fails(scenario.id, crossChat, 'isolation.chat-scope');
    for (const event of [
      recall('node-foreign'),
      tool('mnemon', { action: 'remember', text: 'fact-v1' }, { ok: true }, { scope: 'profile-a' }),
      tool('mnemon', { action: 'recall', text: 'fact-v1' }, { ok: true }),
      memory({ action: 'forget', id: 'node-a' }, { deleted: true }),
      memory({ action: 'unlink', edge_id: 'edge-a' }, { unlinked: true }),
    ]) fails(scenario.id, [event, ...trace]);
  });
}

test('remember requires affirmative saved:true and an error flag overrides a success-shaped result', () => {
  for (const change of [event => { event.result.saved = false; },
    event => { event.isError = true; }, event => { event.result.id = 'node-b'; },
    event => { event.result = { error: 'unavailable' }; }]) {
    const trace = positive('remember-after-write');
    change(trace[0]);
    fails('remember-after-write', trace, 'answer.prior-protocol-evidence');
  }
});

test('continuation needs a successful full record, not an empty, truncated or failed recall', () => {
  for (const event of [recall('node-a', 'fact-v1', { truncated: true }),
    memory({ action: 'recall' }, { mode: 'browse', records: [], next_cursor: null }),
    memory({ action: 'recall', id: 'node-a' }, { error: 'unavailable' }),
    { ...recall(), isError: true }]) {
    fails('continuation-recall', [event, answer('continued')], 'answer.prior-protocol-evidence');
  }
});

test('RU/EN: automatic fallback, explicit browse and language alternatives are accepted without semantics', () => {
  const auto = englishMiss();
  auto.result.records = [{ id: 'node-a', text: 'fact-ru-v1' }];
  assert.equal(evaluateMemoryTrace('ru-en-fallback', [auto, answer('found')]).passed, true);
  const browse = memory({ action: 'recall' }, {
    mode: 'browse', records: [{ id: 'node-a', text: 'fact-ru-v1' }], next_cursor: null,
  });
  assert.equal(evaluateMemoryTrace('ru-en-fallback', [englishMiss(), browse, answer('found')]).passed, true);
  const lexicalMiss = memory({ action: 'recall', query: 'query-en' }, {
    mode: 'search', matched_total: 0, records: [], next_cursor: null,
  });
  assert.equal(evaluateMemoryTrace('ru-en-fallback', [lexicalMiss, russianHit(), answer('found')]).passed, true);
  fails('ru-en-fallback', [englishMiss(), answer('found')], 'answer.prior-protocol-evidence');
  fails('ru-en-fallback', [russianHit(), englishMiss(), answer('found')], 'answer.prior-protocol-evidence');
  fails('ru-en-fallback', [englishMiss(), answer('absent')], 'answer.allowed-claims');
  fails('ru-en-fallback', [auto, answer('absent'), answer('found')], 'answer.allowed-claims');
});

test('atomic update requires exact ID, exact recalled CAS, affirmative update and no replacement writes', () => {
  for (const change of [trace => { trace[1].args.id = 'node-b'; },
    trace => { trace[1].args.expected_text = 'fact-v2'; },
    trace => { trace[1].result.updated = false; },
    trace => { trace[1].result.id = 'node-b'; },
    trace => { trace[1].isError = true; },
    trace => { trace[0].args = { action: 'recall' }; trace[0].result.mode = 'browse'; }]) {
    const trace = positive('atomic-update');
    change(trace);
    fails('atomic-update', trace);
  }
  fails('atomic-update', [update(), recall(), answer('updated')], 'update.exact-id-cas');
  fails('atomic-update', [recall(), remember(), update(), answer('updated')], 'tools.scenario-allowlist');
  fails('atomic-update', [recall(), memory({ action: 'forget', id: 'node-a' }, { deleted: true }),
    remember(), answer('updated')], 'mutations.no-destructive-actions');
});

test('CAS conflict requires a fresh exact-ID read, not a blind retry', () => {
  const conflict = update();
  conflict.result = { error: 'conflict' };
  fails('atomic-update', [recall(), conflict, update(), answer('updated')], 'update.exact-id-cas');
  const retry = update();
  retry.args.expected_text = 'fact-ru-v1';
  assert.equal(evaluateMemoryTrace('atomic-update', [recall(), conflict,
    recall('node-a', 'fact-ru-v1'), retry, answer('updated')]).passed, true);
});

test('every indispensable scenario observation is required, not merely any successful tool call', () => {
  const required = {
    'remember-after-write': [0], 'continuation-recall': [0], 'ru-en-fallback': [0, 1],
    'atomic-update': [0, 1], 'linked-node-read': [0, 1, 2], 'contradictory-nodes': [0, 1, 2],
    'source-drift': [0, 1, 2], 'scope-isolation': [0, 1], 'failed-write': [0], 'compaction-resume': [1, 2],
  };
  for (const [scenario, indices] of Object.entries(required)) {
    for (const index of indices) {
      const trace = positive(scenario);
      trace.splice(index, 1);
      fails(scenario, trace);
    }
  }
});

for (const [scenario, relation, claim] of [
  ['linked-node-read', 'depends_on', 'graph-used'],
  ['contradictory-nodes', 'contradicts', 'contradiction-open'],
]) {
  test(`${scenario}: linked previews, wrong ID, wrong direction and early reads are insufficient`, () => {
    fails(scenario, [recall(), related(relation), answer(claim)], 'answer.prior-protocol-evidence');
    fails(scenario, [recall(), related(relation), recall(), answer(claim)], 'answer.prior-protocol-evidence');
    fails(scenario, [recall(), recall('node-b'), related(relation), answer(claim)], 'answer.prior-protocol-evidence');
    fails(scenario, [related(relation), recall(), recall('node-b'), answer(claim)], 'answer.prior-protocol-evidence');
    const reversed = related(relation);
    reversed.result.edges[0].from_id = 'node-b';
    reversed.result.edges[0].to_id = 'node-a';
    fails(scenario, [recall(), reversed, recall('node-b'), answer(claim)], 'answer.prior-protocol-evidence');
    fails(scenario, [recall(), related(relation), recall('node-b', 'fact-v2', { truncated: true }), answer(claim)],
      'answer.prior-protocol-evidence');
  });
}

test('contradictions cannot be declared resolved or silently rewritten, unlinked or deleted', () => {
  const trace = positive('contradictory-nodes');
  fails('contradictory-nodes', [...trace.slice(0, -1), answer('contradiction-resolved')], 'answer.allowed-claims');
  for (const event of [update(), memory({ action: 'forget', id: 'node-b' }, { deleted: true }),
    memory({ action: 'unlink', edge_id: 'edge-a' }, { unlinked: true })]) {
    fails('contradictory-nodes', [...trace.slice(0, -1), event, trace.at(-1)], 'tools.scenario-allowlist');
  }
});

test('source drift requires a successful current read before every precise edit and acknowledgement', () => {
  fails('source-drift', [citedRecall(), fileEdit(), answer('precise-edit')], 'edit.current-file-evidence');
  fails('source-drift', [fileRead(), citedRecall(), fileEdit(), answer('precise-edit')], 'edit.current-file-evidence');
  fails('source-drift', [citedRecall(), fileEdit(), fileRead(), answer('precise-edit')], 'edit.current-file-evidence');
  for (const change of [trace => { trace[1].isError = true; },
    trace => { trace[1].result.version = 'version-1'; },
    trace => { trace[2].args.expected_version = 'version-1'; },
    trace => { trace[2].args.line_from = 1; },
    trace => { trace[2].args.line_to = 9; },
    trace => { trace[0] = recall(); },
    trace => { trace[2].result.edited = false; },
    trace => { trace[2].isError = true; }]) {
    const trace = positive('source-drift');
    change(trace);
    fails('source-drift', trace);
  }
  fails('source-drift', [citedRecall(), fileRead(), fileEdit(), fileEdit(), answer('precise-edit')],
    'edit.current-file-evidence');
});

test('chat-only evidence cannot be substituted by profile or another chat memory', () => {
  const trace = positive('scope-isolation');
  trace[1].scope = 'chat-b';
  fails('scope-isolation', trace, 'isolation.chat-scope');
  fails('scope-isolation', [recall(), remember(), answer('chat-only')], 'answer.prior-protocol-evidence');
  const spoofed = remember();
  spoofed.args.scope = 'chat-a';
  fails('scope-isolation', [spoofed, recall(), answer('chat-only')], 'trace.schema');
});

test('failed writes must not become success claims, including failure-shaped results without isError', () => {
  for (const event of [failedWrite(), memory({ action: 'remember', text: 'fact-v1' }, { saved: false, id: 'node-a' }),
    memory({ action: 'remember', text: 'fact-v1' }, { error: 'write-failed' })]) {
    assert.equal(evaluateMemoryTrace('failed-write', [event, answer('write-failed')]).passed, true);
    fails('remember-after-write', [event, answer('remembered')], 'answer.prior-protocol-evidence');
    fails('failed-write', [event, answer('remembered')], 'answer.allowed-claims');
  }
  fails('failed-write', [remember(), answer('write-failed')], 'answer.prior-protocol-evidence');
  fails('failed-write', [failedWrite(), remember(), answer('write-failed')], 'answer.prior-protocol-evidence');
});

test('compaction invalidates pre-resume recall and requires rereading after the latest successful resume', () => {
  fails('compaction-resume', [recall(), resume(), answer('resumed')], 'answer.prior-protocol-evidence');
  fails('compaction-resume', [resume(), recall(), resume(), answer('resumed')], 'answer.prior-protocol-evidence');
  const failed = resume();
  failed.isError = true;
  fails('compaction-resume', [failed, recall(), answer('resumed')], 'answer.prior-protocol-evidence');
  fails('compaction-resume', [resume(), { ...recall(), isError: true }, answer('resumed')],
    'answer.prior-protocol-evidence');
});

test('strict schema rejects non-plain data, real text, extra fields and malformed protocol observations', () => {
  const valid = positive('remember-after-write');
  const malformed = [undefined, null, {}, [], [null], [1], [new Date()], [new Map()],
    [Object.create({ kind: 'answer' })], new Array(1),
    [{ kind: 'answer', claims: ['remembered'], text: 'actual text' }],
    [{ kind: 'answer', claims: ['remembered', 'remembered'] }],
    [{ kind: 'answer', claims: [] }],
    [{ ...remember(), isError: 'false' }],
    [{ ...remember(), args: { action: 'remember', text: 'actual natural language' } }],
    [{ ...remember(), result: { saved: 'true', id: 'node-a' } }],
    [{ ...remember(), result: { saved: true, id: 'node-a', extra: true } }],
    [{ ...remember(), args: null }], [{ ...remember(), result: [] }],
    [{ ...remember(), args: { action: 'unknown' } }],
    [{ ...recall(), result: { mode: 'search', records: [], next_cursor: null } }],
    [{ ...recall(), args: { action: 'recall', id: 'node-a', query: 'query-en' } }],
    [{ ...recall(), result: { mode: 'exact_id', records: [{ id: 'node-b', text: 'fact-v1' }], next_cursor: null } }],
    [{ ...recall(), result: { mode: 'exact_id', records: [{ id: 'node-a', text: 'fact-v1' },
      { id: 'node-a', text: 'fact-v2' }], next_cursor: null } }],
    [{ ...recall(), result: { ...recall().result, next_cursor: 'cursor-a' } }],
    [{ ...englishMiss(), result: { ...englishMiss().result, query_matched: true } }],
    [{ ...englishMiss(), result: { ...englishMiss().result, matched_total: 1 } }],
    [{ ...russianHit(), result: { ...russianHit().result, matched_total: 0 } }],
    [{ ...russianHit(), result: { ...russianHit().result, query_matched: false } }],
    [{ ...fileRead(), result: { file: 'file-a', version: 'version-2', line_from: 8, line_to: 4 } }],
  ];
  for (const trace of malformed) fails('remember-after-write', trace, 'trace.schema');
  assert.equal(evaluateMemoryTrace('remember-after-write', valid).passed, true);
  const unknown = evaluateMemoryTrace('unknown', valid);
  assert.equal(unknown.passed, false);
  assert.equal(unknown.scenarioId, null);
  assert.deepEqual(unknown.checks, [{ id: 'scenario.known', passed: false }]);
});

test('data validation does not invoke getters or accept symbols, sparse arrays or custom array properties', () => {
  let calls = 0;
  const getter = { get kind() { calls += 1; throw new Error('must not run'); } };
  const nestedGetter = remember();
  Object.defineProperty(nestedGetter.result, 'saved', { enumerable: true, get() { calls += 1; return true; } });
  const symbol = remember();
  symbol[Symbol('unexpected')] = true;
  const extra = positive('remember-after-write');
  extra.extra = true;
  const hidden = remember();
  Object.defineProperty(hidden, 'hidden', { value: true });
  for (const trace of [[getter], [nestedGetter], [symbol], extra, [hidden], [() => true], [1n], [Infinity]]) {
    fails('remember-after-write', trace, 'trace.schema');
  }
  assert.equal(calls, 0);
});

test('trace size, string, nesting, object width, total work and cycles are bounded', () => {
  fails('remember-after-write', Array.from({ length: MEMORY_TRACE_LIMITS.events + 1 }, () => remember()), 'trace.schema');
  fails('remember-after-write', [{ kind: 'answer', claims: ['x'.repeat(MEMORY_TRACE_LIMITS.string + 1)] }], 'trace.schema');
  const cycle = {};
  cycle.self = cycle;
  fails('remember-after-write', [cycle], 'trace.schema');
  let deep = {};
  for (let index = 0; index <= MEMORY_TRACE_LIMITS.depth; index += 1) deep = { child: deep };
  fails('remember-after-write', [deep], 'trace.schema');
  const wide = Object.fromEntries(Array.from({ length: MEMORY_TRACE_LIMITS.keys + 1 }, (_, i) => [`k${i}`, true]));
  fails('remember-after-write', [wide], 'trace.schema');
  const broad = Array.from({ length: 128 }, () => Array.from({ length: 128 }, () => true));
  fails('remember-after-write', broad, 'trace.schema');
});

test('evaluation accepts deeply frozen and null-prototype plain records without network calls', t => {
  t.mock.method(globalThis, 'fetch', () => { throw new Error('network forbidden'); });
  const freeze = value => {
    if (value && typeof value === 'object') {
      for (const child of Object.values(value)) freeze(child);
      Object.freeze(value);
    }
    return value;
  };
  for (const scenario of MEMORY_TRACE_SCENARIOS) {
    assert.equal(evaluateMemoryTrace(scenario.id, freeze(positive(scenario.id))).passed, true);
  }
  const event = Object.assign(Object.create(null), remember());
  assert.equal(evaluateMemoryTrace('remember-after-write', [event, answer('remembered')]).passed, true);
});
