import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerAgentEnvironment, AGENT_ENVIRONMENT_CONTEXT } from './agent-environment.mjs';

test('environment facts are registered only for the managed image', () => {
  const original = process.env.HARNESS_PYTHON_ENV;
  const contexts = [];
  const ctx = { systemPrompt: { context: value => contexts.push(value) } };
  try {
    delete process.env.HARNESS_PYTHON_ENV;
    registerAgentEnvironment(ctx);
    assert.equal(contexts.length, 0);
    process.env.HARNESS_PYTHON_ENV = 'true';
    registerAgentEnvironment(ctx);
    assert.equal(contexts.length, 1);
    assert.equal(contexts[0].required, true);
    assert.equal(contexts[0].interpolate, false);
    assert.equal(contexts[0].text, AGENT_ENVIRONMENT_CONTEXT);
    assert.match(contexts[0].text, /python3 -m pip install/);
    assert.match(contexts[0].text, /HTTP_PROXY\/HTTPS_PROXY/);
  } finally {
    if (original === undefined) delete process.env.HARNESS_PYTHON_ENV;
    else process.env.HARNESS_PYTHON_ENV = original;
  }
});
