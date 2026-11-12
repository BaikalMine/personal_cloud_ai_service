export function chatMemorySummary(window) {
  const entries = Array.isArray(window?.entries) ? window.entries : [];
  const events = entries.filter(item => item?.type === 'event' && item.event).map(item => item.event);
  const summaries = new Map();
  for (const event of events) {
    if (event.type === 'compaction/summary' && typeof event.data?.compactionId === 'string') summaries.set(event.data.compactionId, event);
  }
  for (let index = events.length - 1; index >= 0; index--) {
    const checkpoint = events[index], source = checkpoint.data?.source;
    if (checkpoint.type !== 'user/message' || !checkpoint.surfaceOp || checkpoint.surfaceOp === 'append' || source?.kind !== 'compact-checkpoint' || typeof source.compactionId !== 'string') continue;
    const summary = summaries.get(source.compactionId);
    if (!summary || summary.seq >= checkpoint.seq) return { state:'unavailable', hasMore:!!window.hasMore };
    const text = Array.isArray(summary.data?.summary) ? summary.data.summary.filter(block => block?.type === 'text' && typeof block.text === 'string').map(block => block.text).join('') : '';
    return text.trim() ? { state:'ready', text, time:checkpoint.time, hasMore:!!window.hasMore } : { state:'unavailable', hasMore:!!window.hasMore };
  }
  return { state:window?.hasMore ? 'older' : 'empty', hasMore:!!window?.hasMore };
}
