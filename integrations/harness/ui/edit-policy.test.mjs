import assert from 'node:assert/strict';
import test from 'node:test';
import { EDIT_POLICY_CONTEXT, registerEditPolicy } from './edit-policy.mjs';

function fakeContext() {
  const handlers = [];
  return {
    handlers,
    on(event, handler) {
      assert.equal(event, 'agent/created');
      handlers.push(handler);
    },
    created(agent) {
      for (const handler of handlers) handler({ agent });
    },
  };
}

function fakeAgent(provider, { parent, deferred = false } = {}) {
  const contexts = [], injections = [], pending = [];
  const scope = { systemPrompt: { context: value => contexts.push(value) } };
  return {
    options: { provider }, parent, contexts, injections,
    ctx: {
      inject(dependencies, callback) {
        injections.push(dependencies);
        if (deferred) pending.push(() => callback(scope));
        else callback(scope);
      },
    },
    ready() {
      for (const callback of pending.splice(0)) callback();
    },
  };
}

test('required policy is installed on local main agents and subagents only', () => {
  const ctx = fakeContext();
  registerEditPolicy(ctx);
  const main = fakeAgent('ollama-local');
  const child = fakeAgent('ollama-local', { parent: main });
  const foreignParent = fakeAgent('other');
  const localChild = fakeAgent('ollama-local', { parent: foreignParent });
  const foreignChild = fakeAgent('other', { parent: main });
  const agents = [main, child, foreignParent, localChild, foreignChild,
    fakeAgent('ollama'), fakeAgent('openai'), fakeAgent(undefined)];
  for (const agent of agents) ctx.created(agent);
  for (const agent of agents) {
    if (agent.options.provider !== 'ollama-local') {
      assert.deepEqual(agent.injections, []);
      assert.deepEqual(agent.contexts, []);
      continue;
    }
    assert.deepEqual(agent.injections, [['systemPrompt']]);
    assert.deepEqual(agent.contexts, [{
      name: 'gateway:edit-policy', order: 125, required: true, interpolate: false,
      text: EDIT_POLICY_CONTEXT,
    }]);
  }
});

test('registration and repeated creation events are idempotent, including deferred injection', () => {
  const ctx = fakeContext(), other = fakeContext();
  registerEditPolicy(ctx);
  registerEditPolicy(ctx);
  registerEditPolicy(other);
  assert.equal(ctx.handlers.length, 1);
  assert.equal(other.handlers.length, 1);
  const agent = fakeAgent('ollama-local', { deferred: true });
  ctx.created(agent);
  ctx.created(agent);
  other.created(agent);
  assert.equal(agent.injections.length, 1);
  assert.equal(agent.contexts.length, 0);
  agent.ready();
  ctx.created(agent);
  assert.equal(agent.contexts.length, 1);
  const nextAgent = fakeAgent('ollama-local');
  ctx.created(nextAgent);
  assert.equal(nextAgent.contexts.length, 1);
});

test('a synchronous injection failure does not prevent a later retry', () => {
  const ctx = fakeContext(), agent = fakeAgent('ollama-local');
  const inject = agent.ctx.inject;
  registerEditPolicy(ctx);
  agent.ctx.inject = () => { throw new Error('synthetic injection failure'); };
  assert.throws(() => ctx.created(agent), /synthetic injection failure/);
  agent.ctx.inject = inject;
  ctx.created(agent);
  assert.equal(agent.contexts.length, 1);
});

test('policy specifies literal minimal edits and exact preservation across task types', () => {
  for (const text of [
    'all local-agent tasks, including prose, documents, Markdown and code',
    'targeted word, sentence or punctuation fix',
    'literal edit tool with the smallest unique exact excerpt',
    'only enough unchanged context to disambiguate',
    'copy that context exactly into the replacement',
    'byte-for-byte, including Markdown, spaces, tabs, blank lines, newline style and the final newline',
    'Do not rewrite a full paragraph or file for a targeted fix unless',
    'Never use replace_all unless the user explicitly requested changing every occurrence',
  ]) assert.ok(EDIT_POLICY_CONTEXT.includes(text), text);
});

test('policy requires current complete relevant reads, fresh recovery and tool-confirmed scope checks', () => {
  for (const text of [
    'Before editing, read the current relevant passage',
    'partial or truncated, follow its continuation cursor (offset and column_offset when supplied)',
    'until the entire relevant passage and enough context',
    'does not require reading unrelated parts of the file',
    'old_string is not found or matches multiple times, re-read',
    'construct a fresh exact unique excerpt',
    'Do not replay the failed edit unchanged',
    'inspect the actual diff (or an equivalent before/after comparison)',
    'only when actual tool results confirm the edit succeeded and the scope check is complete',
    'Printed tool calls, intentions and progress messages are not confirmation',
    'not a hard guarantee of LLM behavior or a byte-preservation enforcement mechanism',
    'Legitimate full rewrites and Bash-based coding tasks remain allowed when explicitly requested',
  ]) assert.ok(EDIT_POLICY_CONTEXT.includes(text), text);
});
