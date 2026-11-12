import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { patchMessageQueue } from './patch-message-queue.mjs';

test('queue UI patch is applied to the installed bundle and is idempotent', async t => {
  let source;
  try { source = await readFile('/opt/harness/node_modules/@deepseek-ai/dsh-client-ui-conversation/lib/client.js', 'utf8'); }
  catch (error) { if (error.code === 'ENOENT') return t.skip('installed bundle required'); throw error; }
  const patched = patchMessageQueue(source);
  assert.equal(patchMessageQueue(patched), patched);
  assert.match(patched, /const primarySubmitMode = "queue"/);
  assert.match(patched, /gatewayPendingCorrections/);
  assert.match(patched, /submission.placement === "steering"/);
  assert.doesNotMatch(patched, /Корректировка ожидает обработки/);
});

test('upgrading the pending correction label preserves the actual queue and other statuses',()=>{
  const pending='(inbox?.["next-step"] ?? EMPTY_QUEUE).some(item => item.id === row.id) && (0, react_jsx_runtime.jsx)("span", {className: QueueDock_module_css_default.status, role:"status", children:"Корректировка ожидает обработки"}),';
  const source='const gatewayPendingCorrections = [];\n'+pending+'\nqueueMutable && renderActions(row);\nstatus(t("queue.sending"));\nstatus(t("queue.failed"));';
  const patched=patchMessageQueue(source);
  assert.equal(patched,source.replace(pending,''));
  assert.equal(patchMessageQueue(patched),patched);
  assert.throws(()=>patchMessageQueue(source+pending),/status anchor changed/);
  assert.throws(()=>patchMessageQueue('gatewayPendingCorrections; status("Корректировка ожидает обработки")'),/status anchor changed/);
});

test('unknown bundle fails closed', () => assert.throws(() => patchMessageQueue('unknown'), /anchor changed/));
