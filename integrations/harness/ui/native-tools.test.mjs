import assert from 'node:assert/strict';
import test from 'node:test';
import { configureLocalNativeTools, inject } from './index.mjs';

test('legacy native presentation applies only to local agents', async () => {
  assert.deepEqual(inject,['tools','systemPrompt','fs','workingDirectory','userQuestions','sessionProjections']);
  const modes=[];
  await configureLocalNativeTools({options:{provider:'ollama-local'},ctx:{tools:{presentAs:mode=>modes.push(mode)}}});
  await configureLocalNativeTools({options:{provider:'other'},ctx:{tools:{presentAs(){throw new Error('foreign agent modified');}}}});
  assert.deepEqual(modes,['native']);
});
