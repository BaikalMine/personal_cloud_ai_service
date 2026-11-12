import assert from 'node:assert/strict';
import test from 'node:test';
import { COMPACTION_POLICY, modelPolicy, upgradeModelPolicy } from './model-policy.mjs';

const models = modelPolicy([{ id: 'model', name: 'Model' }]);
test('long-context default reserves room for answer, compaction and recent history', () => {
  assert.equal(models[0].contextWindow, 131072);
  assert.equal(models[0].maxTokens, 32768);
  assert.equal(Math.min(131072 * COMPACTION_POLICY.thresholdRatio,
    131072 - 32768 - COMPACTION_POLICY.headroomTokens), 90112);
});
test('explicit large-model budgets are supported', () => {
  assert.equal(modelPolicy([{ id: 'large', name: 'Large', contextWindow: 262144, maxTokens: 65536 }])[0].maxTokens, 65536);
});
test('invalid budgets and duplicate identities fail before startup', () => {
  for (const values of [[], [{ id: 'x', name: 'X', contextWindow: '131072' }],
    [{ id: 'x', name: 'X', maxTokens: 131072 }], [{ id: 'x', name: 'X', maxTokens: -1 }],
    [{ id: 'x', name: 'X' }, { id: 'x', name: 'Y' }]]) assert.throws(() => modelPolicy(values));
});
test('migration is scoped, detached and idempotent', () => {
  const patch = [{ id: 'llm-pi-ai', config: { unrelated: true, providers: {
    other: { unchanged: true }, 'ollama-local': { compat: { custom: true }, models: [
      { id: 'model', contextWindow: 16384, maxTokens: 4096, reasoning: true },
      { id: 'custom', contextWindow: 16384, maxTokens: 4096 },
    ] },
  } } }, { id: 'user-plugin', config: { value: 123 } }];
  const next = upgradeModelPolicy(patch, models);
  assert.equal(patch[0].config.providers['ollama-local'].models[0].maxTokens, 4096);
  assert.equal(next[0].config.providers['ollama-local'].models[0].maxTokens, 32768);
  assert.equal(next[0].config.providers['ollama-local'].models[0].reasoning, true);
  assert.deepEqual(next[0].config.providers.other, patch[0].config.providers.other);
  assert.deepEqual(next[0].config.providers['ollama-local'].models[1], patch[0].config.providers['ollama-local'].models[1]);
  assert.deepEqual(next[1], patch[1]);
  assert.deepEqual(upgradeModelPolicy(next, models), next);
});
test('explicit user budgets and compaction settings are never overwritten', () => {
  const patch = [{ id: 'llm-pi-ai', config: { providers: { 'ollama-local': { models: [
    { id: 'model', contextWindow: 65536, maxTokens: 8192 },
  ] } } } }, { id: 'compaction-basic', config: { auto: false } }];
  assert.deepEqual(upgradeModelPolicy(patch, models), patch);
  assert.throws(() => upgradeModelPolicy([...patch, patch[0]], models));
});
