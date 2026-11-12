/**
 * Offline synthetic protocol evaluation ONLY. No inference, semantic scoring,
 * tool execution, storage, files, or network. A pass proves only these checks
 * against caller-supplied observations, not that a model uses memory well.
 *
 * Schema v1: a trace is an ordered array of completed, atomic observations:
 *   {kind:'tool', name, scope, args, result, isError?:boolean}
 *   {kind:'answer', claims:[scenario.claim]}
 * scope is trusted observer metadata, NOT a model-supplied tool argument.
 * chat_memory args/results mirror the relevant protocol subset. All content,
 * queries, IDs, versions and files are closed synthetic labels below. Recall
 * records contain text labels; related nodes are previews, never full evidence.
 * file_read/file_edit/context_resume/mnemon are synthetic adapter names, not
 * registered runtime tools. Source versions stand for observed file revisions;
 * line intervals stand for observed locations, with no actual file contents.
 * Answers contain declarations only, never natural language. Unknown fields,
 * accessors, non-plain values, cycles and out-of-bounds inputs fail closed.
 */

export const MEMORY_TRACE_SCOPE = 'synthetic-protocol-only';
export const MEMORY_TRACE_LIMITS = Object.freeze({
  events: 128, depth: 8, values: 4096, array: 128, keys: 16, string: 64,
});
export const MEMORY_TRACE_LABELS = Object.freeze({
  chat: 'chat-a', otherChat: 'chat-b', profile: 'profile-a',
  node: 'node-a', linkedNode: 'node-b', foreignNode: 'node-foreign',
  fact: 'fact-v1', replacement: 'fact-v2', russianFact: 'fact-ru-v1',
  englishQuery: 'query-en', russianQuery: 'query-ru', file: 'file-a',
  storedVersion: 'version-1', currentVersion: 'version-2', editedVersion: 'version-3',
});

const cases = [
  ['remember-after-write', 'remembered', ['remember'], []],
  ['continuation-recall', 'continued', ['recall'], []],
  ['ru-en-fallback', 'found', ['recall'], []],
  ['atomic-update', 'updated', ['recall', 'update'], []],
  ['linked-node-read', 'graph-used', ['recall', 'related'], []],
  ['contradictory-nodes', 'contradiction-open', ['recall', 'related'], []],
  ['source-drift', 'precise-edit', ['recall'], ['file_read', 'file_edit']],
  ['scope-isolation', 'chat-only', ['remember', 'recall'], []],
  ['failed-write', 'write-failed', ['remember'], []],
  ['compaction-resume', 'resumed', ['recall'], ['context_resume']],
];
export const MEMORY_TRACE_SCENARIOS = Object.freeze(cases.map(([id, claim, actions, adapters]) =>
  Object.freeze({ id, claim, scope: MEMORY_TRACE_SCOPE, chat: 'chat-a',
    actions: Object.freeze(actions), adapters: Object.freeze(adapters) })));

const oneOf = (...values) => value => values.includes(value);
const bool = value => typeof value === 'boolean';
const line = value => Number.isInteger(value) && value >= 1 && value <= 20;
const list = (rule, max = 4) => value => Array.isArray(value) && value.length <= max && value.every(rule);
const object = value => value !== null && !Array.isArray(value) && typeof value === 'object';
const shape = (value, required, optional = {}) => object(value)
  && Object.keys(required).every(key => Object.hasOwn(value, key))
  && Object.keys(value).every(key => {
    const rule = Object.hasOwn(required, key) ? required[key] : Object.hasOwn(optional, key) ? optional[key] : null;
    return rule !== null && rule(value[key]) === true;
  });
const id = oneOf('node-a', 'node-b', 'node-foreign');
const fact = oneOf('fact-v1', 'fact-v2', 'fact-ru-v1');
const query = oneOf('query-en', 'query-ru');
const version = oneOf('version-1', 'version-2', 'version-3');
const file = oneOf('file-a');
const cursor = oneOf(null, 'cursor-a', 'cursor-b');
const source = value => shape(value, { file, version, line_from: line, line_to: line })
  && value.line_from <= value.line_to;
const record = value => shape(value, { id, text: fact }, { truncated: bool, sources: list(source) });
const edge = value => shape(value, {
  id: oneOf('edge-a'), from_id: id, to_id: id, relation: oneOf('supports', 'contradicts', 'depends_on'),
});
const claims = oneOf(...cases.map(([, claim]) => claim), 'absent', 'contradiction-resolved');
const errorResult = value => shape(value, { error: oneOf('write-failed', 'conflict', 'unavailable') });

// Copy only data descriptors before validation; never evaluate caller getters.
function boundedCopy(input) {
  let count = 0;
  const ancestors = new Set();
  function copy(value, depth) {
    if (++count > MEMORY_TRACE_LIMITS.values || depth > MEMORY_TRACE_LIMITS.depth) throw new Error();
    if (value === null || typeof value === 'boolean') return value;
    if (typeof value === 'string' && value.length <= MEMORY_TRACE_LIMITS.string) return value;
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value !== 'object' || ancestors.has(value)) throw new Error();
    const array = Array.isArray(value);
    if (Object.getPrototypeOf(value) !== (array ? Array.prototype : Object.prototype)
      && !(Object.getPrototypeOf(value) === null && !array)) throw new Error();
    const keys = Reflect.ownKeys(value);
    if (array) {
      const length = Object.getOwnPropertyDescriptor(value, 'length')?.value;
      if (!Number.isInteger(length) || length > MEMORY_TRACE_LIMITS.array || keys.length !== length + 1
        || keys.some(key => typeof key !== 'string')
        || Array.from({ length }, (_, i) => String(i)).some(key => !Object.hasOwn(value, key))) throw new Error();
    } else if (keys.length > MEMORY_TRACE_LIMITS.keys) throw new Error();
    const descriptors = Object.getOwnPropertyDescriptors(value);
    ancestors.add(value);
    const output = array ? [] : Object.create(null);
    for (const key of keys) {
      if (array && key === 'length') continue;
      const descriptor = descriptors[key];
      if (typeof key !== 'string' || key.length > MEMORY_TRACE_LIMITS.string
        || !descriptor.enumerable || !Object.hasOwn(descriptor, 'value')) throw new Error();
      output[key] = copy(descriptor.value, depth + 1);
    }
    ancestors.delete(value);
    return output;
  }
  return copy(input, 0);
}

function memoryArgs(args) {
  switch (args.action) {
    case 'remember': return shape(args, { action: oneOf('remember'), text: fact });
    case 'recall': return shape(args, { action: oneOf('recall') }, {
      id, query, queries: list(query), cursor: oneOf('cursor-a', 'cursor-b'),
    }) && !(args.id && (args.query || args.queries || args.cursor));
    case 'update': return shape(args, {
      action: oneOf('update'), id, text: fact, expected_text: fact,
    });
    case 'forget': return shape(args, { action: oneOf('forget'), id });
    case 'related': return shape(args, { action: oneOf('related'), id }, {
      cursor: oneOf('cursor-a', 'cursor-b'),
    });
    case 'link': return shape(args, {
      action: oneOf('link'), from_id: id, to_id: id, relation: oneOf('supports', 'contradicts', 'depends_on'),
    });
    case 'unlink': return shape(args, { action: oneOf('unlink'), edge_id: oneOf('edge-a') });
    default: return false;
  }
}

function memoryResult(args, result) {
  if (errorResult(result)) return true;
  switch (args.action) {
    case 'remember': return shape(result, { saved: bool, id });
    case 'update': return shape(result, { updated: bool, id });
    case 'forget': return shape(result, { deleted: bool });
    case 'unlink': return shape(result, { unlinked: bool });
    case 'link': return shape(result, { linked: bool, edge });
    case 'recall': return shape(result, {
      mode: oneOf('exact_id', 'search', 'browse', 'browse_fallback'), records: list(record), next_cursor: cursor,
    }, { matched_total: value => Number.isInteger(value) && value >= 0 && value <= 4, query_matched: bool })
      && new Set(result.records.map(row => row.id)).size === result.records.length
      && (result.mode !== 'browse_fallback' || (result.query_matched !== true
        && (result.matched_total === undefined || result.matched_total === 0)))
      && (result.mode !== 'search' || ((result.matched_total === undefined || result.matched_total >= result.records.length)
        && (result.query_matched !== false || result.records.length === 0)))
      && (args.id ? result.mode === 'exact_id' && result.next_cursor === null
        && result.records.every(row => row.id === args.id)
        : (args.query || args.queries?.length) ? ['search', 'browse_fallback'].includes(result.mode) : result.mode === 'browse');
    case 'related': return shape(result, {
      previews_only: oneOf(true), nodes: list(value => shape(value, { id, truncated: oneOf(true) })),
      edges: list(edge), next_cursor: cursor,
    });
    default: return false;
  }
}

function validEvent(event) {
  if (event.kind === 'answer') return shape(event, { kind: oneOf('answer'), claims: list(claims, 8) })
    && event.claims.length > 0 && new Set(event.claims).size === event.claims.length;
  if (!shape(event, {
    kind: oneOf('tool'), name: oneOf('chat_memory', 'mnemon', 'file_read', 'file_edit', 'context_resume'),
    scope: oneOf('chat-a', 'chat-b', 'profile-a'), args: object, result: object,
  }, { isError: bool })) return false;
  const { name, args, result } = event;
  if (name === 'chat_memory') return memoryArgs(args) && memoryResult(args, result);
  if (name === 'mnemon') return shape(args, { action: oneOf('remember', 'recall'), text: fact })
    && (shape(result, { ok: bool }) || errorResult(result));
  if (name === 'context_resume') return shape(args, { reason: oneOf('compaction') })
    && (shape(result, { resumed: bool }) || errorResult(result));
  if (name === 'file_read') return shape(args, { file }) && (errorResult(result) || shape(result, {
    file, version, line_from: line, line_to: line,
  }) && result.line_from <= result.line_to);
  return shape(args, { file, expected_version: version, line_from: line, line_to: line })
    && args.line_from <= args.line_to && (errorResult(result) || shape(result, { edited: bool, file, version }));
}

const successful = event => event.kind === 'tool' && event.isError !== true && !event.result.error;
const memory = (event, action) => event.kind === 'tool' && event.name === 'chat_memory' && event.args.action === action;
const recalled = (event, node = 'node-a') => memory(event, 'recall') && successful(event)
  && event.result.records.some(row => row.id === node && row.truncated !== true);
const exact = (event, node = 'node-a') => recalled(event, node) && event.args.id === node;
const saved = event => memory(event, 'remember') && successful(event)
  && event.result.saved === true && event.result.id === 'node-a' && event.args.text === 'fact-v1';
const updated = event => memory(event, 'update') && successful(event)
  && event.result.updated === true && event.result.id === 'node-a';
const hasForeignLabel = value => value === 'node-foreign' || (value !== null && typeof value === 'object'
  && Object.values(value).some(hasForeignLabel));

function updateEvidence(before, event) {
  const previous = before.findLast(item => exact(item) || memory(item, 'update') && item.args.id === 'node-a');
  return event.args.id === 'node-a' && event.args.text === 'fact-v2' && previous && exact(previous)
    && previous.result.records.find(row => row.id === 'node-a').text === event.args.expected_text;
}

function editEvidence(before, event) {
  const sourceIndex = before.findLastIndex(item => exact(item)
    && item.result.records.some(row => row.sources?.some(ref => ref.file === 'file-a' && ref.version === 'version-1')));
  const latest = before.findLastIndex(item => item.kind === 'tool' && ['file_read', 'file_edit'].includes(item.name));
  if (sourceIndex < 0 || latest <= sourceIndex) return false;
  const read = before[latest];
  return read.name === 'file_read' && successful(read) && read.result.file === event.args.file
    && read.result.version === 'version-2' && event.args.expected_version === read.result.version
    && read.result.line_from <= event.args.line_from && read.result.line_to >= event.args.line_to;
}

function graphEvidence(before, relation) {
  return before.some((event, index) => memory(event, 'related') && successful(event)
    && event.args.id === 'node-a' && before.slice(0, index).some(item => exact(item))
    && event.result.edges.some(link => link.from_id === 'node-a' && link.to_id === 'node-b'
      && link.relation === relation)
    && before.slice(index + 1).some(item => exact(item, 'node-b')));
}

function answerEvidence(scenarioId, before) {
  switch (scenarioId) {
    case 'remember-after-write': return before.some(saved);
    case 'continuation-recall': return before.some(item => recalled(item));
    case 'ru-en-fallback': return before.some((event, index) => memory(event, 'recall') && successful(event)
      && event.args.query === 'query-en' && (event.result.mode === 'browse_fallback' || event.result.matched_total === 0)
      && before.slice(index).some(item => recalled(item) && item.result.records.some(row => row.id === 'node-a'
        && row.text === 'fact-ru-v1') && (item.result.mode === 'browse_fallback' || item.result.mode === 'browse'
          || item.args.query === 'query-ru' || item.args.queries?.includes('query-ru'))));
    case 'atomic-update': return before.some(updated);
    case 'linked-node-read': return graphEvidence(before, 'depends_on');
    case 'contradictory-nodes': return graphEvidence(before, 'contradicts');
    case 'source-drift': return before.some(item => item.name === 'file_edit' && successful(item)
      && item.result.edited === true && item.result.file === 'file-a' && item.result.version === 'version-3');
    case 'scope-isolation': return before.some((event, index) => saved(event)
      && before.slice(index + 1).some(item => exact(item)
        && item.result.records.find(row => row.id === 'node-a').text === 'fact-v1'));
    case 'failed-write': return before.some(item => memory(item, 'remember')
      && (!successful(item) || item.result.saved === false)) && !before.some(saved);
    case 'compaction-resume': {
      const index = before.findLastIndex(item => item.name === 'context_resume');
      return index >= 0 && successful(before[index]) && before[index].result.resumed === true
        && before.slice(index + 1).some(item => exact(item));
    }
    default: return false;
  }
}

/** Returns machine checks only. Invalid scenarios/traces return failed checks, not exceptions. */
export function evaluateMemoryTrace(scenarioId, trace) {
  const scenario = MEMORY_TRACE_SCENARIOS.find(item => item.id === scenarioId);
  const checks = [];
  const check = (id, passed) => checks.push({ id, passed: Boolean(passed) });
  const finish = () => ({ scope: MEMORY_TRACE_SCOPE, schemaVersion: 1, scenarioId: scenario?.id ?? null,
    passed: checks.every(item => item.passed), checks });
  check('scenario.known', scenario !== undefined);
  if (!scenario) return finish();
  let events;
  try {
    events = boundedCopy(trace);
    if (!Array.isArray(events) || events.length === 0 || events.length > MEMORY_TRACE_LIMITS.events
      || !events.every(validEvent)) throw new Error();
  } catch {
    check('trace.schema', false);
    return finish();
  }
  check('trace.schema', true);
  const tools = events.filter(event => event.kind === 'tool');
  const answers = events.filter(event => event.kind === 'answer');
  check('answer.present-and-final', answers.length > 0 && events.at(-1).kind === 'answer');
  check('answer.allowed-claims', answers.every(event => event.claims.every(claim => claim === scenario.claim)));
  check('isolation.chat-scope', tools.every(event => event.scope === scenario.chat && event.name !== 'mnemon'));
  check('isolation.node-ids', !hasForeignLabel(events));
  check('tools.scenario-allowlist', tools.every(event => event.name === 'chat_memory'
    ? scenario.actions.includes(event.args.action) : scenario.adapters.includes(event.name)));
  check('mutations.no-destructive-actions', tools.every(event => event.name !== 'chat_memory'
    || !['forget', 'unlink'].includes(event.args.action)));
  check('update.exact-id-cas', events.every((event, index) => !memory(event, 'update')
    || updateEvidence(events.slice(0, index), event)));
  check('edit.current-file-evidence', events.every((event, index) => event.name !== 'file_edit'
    || editEvidence(events.slice(0, index), event)));
  check('answer.prior-protocol-evidence', answers.length > 0 && events.every((event, index) => event.kind !== 'answer'
    || answerEvidence(scenario.id, events.slice(0, index))));
  return finish();
}
