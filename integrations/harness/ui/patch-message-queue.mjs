import { readFile, writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';

export function patchMessageQueue(source) {
  const redundantStatus = '(inbox?.["next-step"] ?? EMPTY_QUEUE).some(item => item.id === row.id) && (0, react_jsx_runtime.jsx)("span", {className: QueueDock_module_css_default.status, role:"status", children:"Корректировка ожидает обработки"}),';
  if (source.includes('gatewayPendingCorrections')) {
    if (source.split(redundantStatus).length > 2) throw new Error('Message queue status anchor changed');
    source = source.replace(redundantStatus, '');
    if (source.includes('Корректировка ожидает обработки')) throw new Error('Message queue status anchor changed');
    return source;
  }
  const replace = (before, after) => {
    if (source.split(before).length !== 2) throw new Error('Message queue patch anchor changed');
    source = source.replace(before, after);
  };
  replace('const primarySubmitMode = resolveSubmitMode(busyEnter, running, "enter", steeringAvailable);',
    'const primarySubmitMode = "queue"; // Click always queues; steering is explicit in the queue dock.');
  replace('const rows = inbox?.["next-turn"] ?? EMPTY_QUEUE;',
    'const gatewayPendingCorrections = (inbox?.["next-step"] ?? EMPTY_QUEUE).filter(row => row.source?.kind === "user");\n\t\t\t\tconst rows = [...(inbox?.["next-turn"] ?? EMPTY_QUEUE), ...gatewayPendingCorrections];');
  replace('submission.placement === "queued" && !admitted.has(submission.requestId)',
    '(submission.placement === "queued" || submission.placement === "steering") && !admitted.has(submission.requestId)');
  // Keep edit/remove native; only prevent steering a correction that is already pending.
  replace('disabled: busy !== null || !running,',
    'disabled: busy !== null || !running || (inbox?.["next-step"] ?? EMPTY_QUEUE).some(item => item.id === row.id),');
  replace('"data-queue-dock": "",',
    '"data-queue-dock": "",\n\t\t\t\t"aria-label": "Очередь сообщений и корректировок",');
  return source;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const path = process.argv[2] ?? '/opt/harness/node_modules/@deepseek-ai/dsh-client-ui-conversation/lib/client.js';
  await writeFile(path, patchMessageQueue(await readFile(path, 'utf8')));
}
