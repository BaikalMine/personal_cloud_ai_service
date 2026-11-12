// The retained native pool runs one model call at a time. Queue before opening
// its HTTP stream so child agents cannot consume the gateway's busy retry budget.
export function createGPUHandoff() {
  const leases = new Set(), readers = new Set(), listeners = new Set();
  let writers = 0, tail = Promise.resolve();
  const changed = () => { for (const fn of [...listeners]) fn(); };
  const waitFor = (ready, signal) => new Promise((resolve, reject) => {
    function finish() {
      if (!ready() && !signal?.aborted) return;
      listeners.delete(finish);
      signal?.removeEventListener('abort', finish);
      if (signal?.aborted) reject(signal.reason); else resolve();
    }
    listeners.add(finish);
    signal?.addEventListener('abort', finish, { once: true });
    finish();
  });
  return {
    add(lease) { leases.add(lease); return () => leases.delete(lease); },
    async model(lease, signal) {
      await waitFor(() => writers === 0 && readers.size === 0, signal);
      // Recheck after the promise continuation: a writer may have arrived.
      while (writers || readers.size) await waitFor(() => writers === 0 && readers.size === 0, signal);
      const reader = {};
      readers.add(reader);
      const done = () => {
        readers.delete(reader);
        changed();
      };
      // Cancellation must not open another slot until the stream's finally
      // has run. A signalled HTTP call can still be settling on the executor.
      try { signal?.throwIfAborted(); await lease.ensure(signal); return done; }
      catch (error) { done(); throw error; }
    },
    async image(signal, execute) {
      writers++;
      const previous = tail;
      let unlock;
      tail = new Promise(resolve => { unlock = resolve; });
      try {
        await previous;
        signal?.throwIfAborted();
        await waitFor(() => readers.size === 0, signal);
        for (const lease of leases) await lease.release();
        signal?.throwIfAborted();
        return await execute();
      } finally {
        writers--;
        unlock();
        changed();
      }
    },
  };
}
