import test from 'node:test';
import assert from 'node:assert/strict';
import { chatMemorySummary } from './chat-memory-summary.mjs';

const wrap = event => ({ type:'event', event });
const summary = (id, seq, text) => wrap({ type:'compaction/summary', seq, data:{ compactionId:id, summary:[{ type:'text', text }] } });
const checkpoint = (id, seq) => wrap({ type:'user/message', seq, time:1234, surfaceOp:{ startSeq:1,endSeq:2 }, data:{ source:{ kind:'compact-checkpoint', compactionId:id } } });
test('only a completed compaction for this event window is returned', () => {
  const events = [summary('first', 3, 'Earlier'), checkpoint('first', 4), summary('second', 5, 'Current'), checkpoint('second', 6), summary('failed', 7, 'Never landed')];
  assert.deepEqual(chatMemorySummary({ entries:events, hasMore:false }), { state:'ready', text:'Current', time:1234, hasMore:false });
});
test('a summary alone, an unrelated session, or an append is not a checkpoint', () => {
  assert.equal(chatMemorySummary({ entries:[summary('uncommitted', 1, 'Private synthetic')], hasMore:false }).state, 'empty');
  const append = checkpoint('a', 2); append.event.surfaceOp = 'append';
  assert.equal(chatMemorySummary({ entries:[summary('a', 1, 'Other'), append], hasMore:false }).state, 'empty');
  assert.equal(chatMemorySummary({ entries:[summary('other', 1, 'Other'), checkpoint('a', 2)], hasMore:false }).state, 'unavailable');
});
test('newer missing summary is not replaced with older stale content', () => {
  assert.deepEqual(chatMemorySummary({ entries:[summary('a', 1, 'Earlier'), checkpoint('a', 2), checkpoint('b', 4)], hasMore:true }), { state:'unavailable', hasMore:true });
});
test('older history and unavailable summaries are distinguished', () => {
  assert.deepEqual(chatMemorySummary({ entries:[], hasMore:true }), { state:'older', hasMore:true });
  assert.deepEqual(chatMemorySummary({ entries:[], hasMore:false }), { state:'empty', hasMore:false });
  assert.deepEqual(chatMemorySummary({ entries:[summary('a', 4, '  '), checkpoint('a', 5)], hasMore:false }), { state:'unavailable', hasMore:false });
  assert.deepEqual(chatMemorySummary({ entries:[checkpoint('a', 1), summary('a', 2, 'Late')], hasMore:false }), { state:'unavailable', hasMore:false });
});
