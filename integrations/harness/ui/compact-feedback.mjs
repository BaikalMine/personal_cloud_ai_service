const compactFailureKeys = new Map([
  ['Compaction is unavailable because this process has an active compaction, or the agent is not idle.', 'busy'],
  ['Compaction cancelled.', 'cancelled'],
  ['The history selected for compaction changed before it could be replaced. The attempt is recorded in the session log.', 'changed'],
  ['Compaction could not produce a useful summary. The attempt is recorded in the session log.', 'summary'],
  ['Compaction did not finish cleanly; some session history may have changed. Inspect the current session state before retrying.', 'commit'],
  ['Compaction finished, but the session could not be saved.', 'persistence'],
  ['Usage: /compact (no arguments)', 'usage'],
]);

export function registerCompactFeedback(ctx) {
  ctx.effect(() => ctx.locale.register('gatewayCompact', { en: {
    busy: 'Compaction did not start because the agent was busy or already compacting. Retry after the response finishes.',
    cancelled: 'Context compaction was cancelled.',
    changed: 'Compaction did not replace the history because the conversation changed. Retry when the agent is idle.',
    summary: 'The model could not produce a smaller context summary. The attempt is recorded in the chat.',
    commit: 'Compaction did not finish cleanly. Check the chat before retrying.',
    persistence: 'The context summary could not be saved. Check the chat before retrying.',
    usage: 'Send /compact without arguments or attachments.',
    failed: 'Context compaction failed. Details are recorded in the chat.',
    dismiss: 'Dismiss compact notice',
  } }), 'gateway: compact feedback copy');
  const notices = new Map(), listeners = new Set();
  const publish = () => { for (const listener of listeners) listener(); };
  const dismiss = sessionId => { if (notices.delete(sessionId)) publish(); };
  ctx.on('command/executed', (sessionId, name, result) => {
    if (name !== 'compact' || !result || !ctx.sessions.scope(sessionId)) return;
    if (result.kind === 'success') { dismiss(sessionId); return; }
    if (result.kind !== 'error') return;
    notices.delete(sessionId);
    notices.set(sessionId, { key: compactFailureKeys.get(result.text) || 'failed' });
    while (notices.size > 32) notices.delete(notices.keys().next().value);
    publish();
  });
  ctx.effect(() => () => { notices.clear(); listeners.clear(); }, 'gateway: compact feedback lifetime');
  return {
    getSnapshot: sessionId => notices.get(sessionId),
    subscribe: listener => { listeners.add(listener); return () => listeners.delete(listener); },
    dismiss,
  };
}
