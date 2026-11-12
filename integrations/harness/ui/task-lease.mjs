import { randomUUID } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';

export function taskEndpoint(modelUrl) {
  const url = new URL(modelUrl);
  if (!/\/v1\/?$/.test(url.pathname) || url.search || url.hash) throw new Error('Invalid Harness model endpoint');
  url.pathname = url.pathname.replace(/\/v1\/?$/, '/harness/task');
  return url.href;
}

export function createHarnessLease({ sessionId, endpoint, key, fetcher = fetch,
  schedule = setInterval, unschedule = clearInterval, warn = () => {},
  admissionRetries = 8, wait = (ms, signal) => delay(ms, undefined, { signal }) }) {
  let current;
  async function send(action, state, signal) {
    const timeout = AbortSignal.timeout(action === 'begin' ? 270000 : 35000);
    const requestSignal = signal ? AbortSignal.any([signal, timeout]) : timeout;
    for (let attempt = 0; ; attempt++) {
    const response = await fetcher(endpoint, {
      method: 'POST', headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ action, taskId: state.id, sessionId }),
      signal: requestSignal,
    });
    if (!response.ok) {
      const known = {executor_unavailable:'Исполнитель GPU временно недоступен.',harness_task_unavailable:'Удержание GPU для этого задания потеряно.',harness_task_unconfirmed:'Исполнитель не подтвердил допуск GPU.',gpu_busy:'GPU занят другой задачей.',admission_unconfirmed:'Допуск GPU не подтверждён.'};
      const body = await response.json().catch(() => null), code = body?.error?.code ?? body?.code;
      // This exact refusal occurs before admission; the same task identity is safe to retry.
      if (action === 'begin' && response.status === 503 && code === 'executor_unavailable' && attempt < admissionRetries) {
        warn('Harness is waiting for its GPU executor to become ready');
        await wait(Math.min(1000 * 2 ** attempt, 5000), requestSignal);
        requestSignal.throwIfAborted();
        continue;
      }
      const detail = Object.hasOwn(known, code ?? '') ? ` (${code}). ${known[code]}` : '';
      throw Object.assign(new Error(`Harness GPU task ${action} failed: HTTP ${response.status}${detail}`), { status:response.status, ...(detail ? { code } : {}) });
    }
    const reply = await response.json();
    if (reply.ok !== true) throw new Error('Harness GPU task acknowledgement missing');
    return;
    }
  }
  async function release() {
    const state = current;
    if (!state) return;
    if (state.closing) return state.closing;
    if (state.timer) unschedule(state.timer);
    state.signal?.removeEventListener('abort', state.abort);
    state.closing = send('end', state).finally(() => { if (current === state) current = undefined; });
    return state.closing;
  }
  async function ensure(signal) {
    signal?.throwIfAborted();
    if (current?.closing) await current.closing;
    if (current) { await send('begin', current, signal); return; }
    const state = { id: randomUUID(), signal, timer: undefined, touching: false };
    current = state;
    state.abort = () => { void release().catch(() => warn('Harness GPU task release will be reconciled by its watchdog')); };
    signal?.addEventListener('abort', state.abort, { once: true });
    try {
      await send('begin', state, signal);
      signal?.throwIfAborted();
      if (state.closing) { await state.closing; throw new Error('Harness task cancelled'); }
      state.timer = schedule(() => {
        if (state.touching || state.closing) return;
        state.touching = true;
        void send('touch', state).catch(() => warn('Harness GPU task heartbeat not confirmed'))
          .finally(() => { state.touching = false; });
      }, 10000);
      state.timer?.unref?.();
    } catch (error) {
      await release().catch(() => warn('Harness GPU task cleanup not confirmed'));
      throw error;
    }
  }
  return { ensure, release };
}
