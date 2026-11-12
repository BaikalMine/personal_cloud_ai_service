import { appendPluginRecord } from '@deepseek-ai/dsh-session';
import { z } from 'zod';

const recordType = 'plugin:ai-gateway/ultra-progress';
const phases = ['preparing', 'awaiting_model', 'uploading', 'validating', 'waiting_gpu', 'generating', 'finalizing', 'finalizing_failed', 'delivering', 'settled', 'failed', 'cancelled'];
const rowSchema = z.object({
  call_id: z.string(), phase: z.enum(phases), started_at: z.number(), updated_at: z.number(), phase_started_at: z.number(),
  question_call_id: z.string().optional(),
}).strict();
const viewSchema = z.object({ calls: z.array(rowSchema).max(32) });

export const ultraProgressProjection = {
  key: 'gatewayUltra', stateVersion: 1,
  stateSchema: z.object({ inherited: z.number(), view: viewSchema }),
  init: (_header, inherited) => ({ inherited, view: { calls: [] } }),
  apply(state, event) {
    if (event.seq < state.inherited) return state;
    if (event.type === recordType && event.ignorable === true) {
      const row = rowSchema.safeParse(event.data);
      return row.success ? { ...state, view: { calls: [...state.view.calls.filter(item => item.call_id !== row.data.call_id), row.data].slice(-32) } } : state;
    }
    if (event.type === 'turn/end' && state.view.calls.some(row => !['settled', 'failed', 'cancelled'].includes(row.phase))) {
      return { ...state, view: { calls: state.view.calls.map(row => ['settled', 'failed', 'cancelled'].includes(row.phase) ? row : { ...row, phase: 'cancelled', updated_at: event.time, phase_started_at: event.time }) } };
    }
    return state;
  },
  wire: { viewSchema, view: state => state.view },
};

const stores = new WeakMap();
export function createUltraProgress(ctx, { now = Date.now } = {}) {
  if (stores.has(ctx)) return stores.get(ctx);
  const ready = Promise.resolve().then(() => ctx.sessionProjections.register(ultraProgressProjection)).then(() => {
  const active = new WeakMap();
  function begin(exec) {
    const started = now(), durations = {};
    let row;
    const tracker = {
      phase(phase, questionCallId) {
        if (!phases.includes(phase)) throw new Error('Invalid Ultra progress phase');
        if (row?.phase === phase && row.question_call_id === questionCallId) return;
        const time = now();
        if (row) durations[row.phase] = (durations[row.phase] || 0) + (time - row.phase_started_at) / 1000;
        row = { call_id: exec.callId, phase, started_at: started, updated_at: time, phase_started_at: time, ...(questionCallId ? { question_call_id: questionCallId } : {}) };
        appendPluginRecord(exec.agent.session, recordType, row);
      },
      seconds(phase) { return (durations[phase] || 0) + (row?.phase === phase ? (now() - row.phase_started_at) / 1000 : 0); },
      finish(phase) { tracker.phase(phase); active.delete(exec); },
    };
    active.set(exec, tracker);
    tracker.phase('preparing');
    return tracker;
  }
  const store = { begin, waiting(exec, waiting, callId) { active.get(exec)?.phase(waiting ? 'awaiting_model' : 'preparing', waiting ? callId : undefined); } };
  return store;
  });
  stores.set(ctx, ready);
  void ready.catch(() => { if (stores.get(ctx) === ready) stores.delete(ctx); });
  return ready;
}
