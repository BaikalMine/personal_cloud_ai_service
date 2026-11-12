import { randomUUID } from 'node:crypto';

const terminal = new Set(['completed', 'failed', 'cancelled']);
export function createUltraClient({ endpoint, key, fetcher = fetch, sleep = ms => new Promise(resolve => setTimeout(resolve, ms)), label = 'Krea2 Ultra' }) {
  const origin = new URL(endpoint);
  if (origin.protocol !== 'http:' || origin.hostname !== 'krea-ultra' || origin.port !== '8097' || origin.pathname !== '/' || origin.search || origin.hash || origin.username || origin.password) {
    throw new Error('Invalid private Ultra endpoint');
  }
  async function call(path, body, { signal, raw = false } = {}) {
    const timeout = AbortSignal.timeout(65000);
    const response = await fetcher(new URL(path, origin), {
      method: body === undefined ? 'GET' : 'POST',
      headers: { Authorization: `Bearer ${key}`, ...(body && !(body instanceof Uint8Array) ? { 'Content-Type': 'application/json' } : {}) },
      body: body === undefined ? undefined : body instanceof Uint8Array ? body : JSON.stringify(body),
      signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
      redirect: 'error',
    });
    if (!response.ok) {
      const error = new Error(`${label} HTTP ${response.status}: ${(await response.text()).slice(0, 1200)}`);
      error.status = response.status;
      throw error;
    }
    return raw ? new Uint8Array(await response.arrayBuffer()) : response.json();
  }
  async function collect(job, { signal, save }) {
    const started = performance.now(), files = [], id = job.id;
    if (job.state !== 'completed' || !Array.isArray(job.outputs) || !job.outputs.length) throw new Error(`${label} ${job.state}: ${job.error || 'no output'}`);
    try {
      for (const output of job.outputs) {
        const body = await call(`/v1/jobs/${id}/outputs/${output.index}`, undefined, { signal, raw: true });
        const file = await save(id, output.index, body, job.parameters?.workflow, job.parameters?.output_name || undefined);
        const size = Number.isInteger(output.width) && output.width > 0 && Number.isInteger(output.height) && output.height > 0
          ? { width: output.width, height: output.height, megapixels: Number.isFinite(output.megapixels) ? output.megapixels : output.width * output.height / (1024 * 1024),
            ...(typeof output.aspect_ratio === 'string' ? { aspect_ratio: output.aspect_ratio } : {}) } : {};
        files.push({ ...file, ...size });
      }
    } catch (error) { throw new Error(`Ultra job ${id} completed, but workspace delivery failed. Use action=retrieve with this job_id; do not generate again. ${error.message}`, { cause: error }); }
    return { id, state: job.state, elapsed_seconds: job.elapsed_seconds, files, timings: { output_delivery_seconds: (performance.now() - started) / 1000 }, ...(job.parameters ? { parameters: job.parameters } : {}) };
  }
  return {
    call,
    async retrieve(id, options) {
      return collect(await call(`/v1/jobs/${id}`, undefined, { signal: options.signal }), options);
    },
    async generate(request, images, { signal, save, notify = () => {}, id = randomUUID(), uploads: preparedUploads }) {
      const started = performance.now();
      const uploads = preparedUploads ?? [];
      if (!preparedUploads) for (const image of images) uploads.push(await call('/v1/uploads', image, { signal }));
      let submitted = false, finished = false;
      try {
        signal?.throwIfAborted();
        submitted = true;
        let job;
        try { job = await call('/v1/jobs', { id, request: { ...request, images: uploads.map(x => x.id) } }, { signal }); }
        catch (error) {
          if ([400, 404, 413, 429].includes(error.status)) finished = true;
          throw error;
        }
        let lastState;
        for (;;) {
          signal?.throwIfAborted();
          if (job.state !== lastState) { notify(job); lastState = job.state; }
          if (job.state === 'finalizing_failed') {
            finished = true;
            throw new Error(`${label} failed: ${job.error || 'generation failed'}. GPU release is still being reconciled; retry only after it is available.`);
          }
          if (terminal.has(job.state)) {
            finished = true;
            if (job.state !== 'completed') throw new Error(`${label} ${job.state}: ${job.error || 'no output'}`);
            const result = await collect({ ...job, id }, { signal, save });
            return { ...result, timings: { ...result.timings, image_job_seconds: (performance.now() - started) / 1000 - result.timings.output_delivery_seconds } };
          }
          signal?.throwIfAborted();
          await sleep(2000);
          job = await call(`/v1/jobs/${id}`, undefined, { signal });
        }
      } finally {
        if (submitted && !finished) {
          // This cleanup is independent of the cancelled agent signal. The next
          // model must not race an image still being cancelled on the GPU.
          await call(`/v1/jobs/${id}/cancel`, {}, { signal:AbortSignal.timeout(5000) }).catch(() => {});
          // The image service continues reconciliation and retains GPU ownership.
          // An explicit user cancellation must not keep the chat open for minutes.
          for (let i = 0; i < 150 && !signal?.aborted; i++) {
            const job = await call(`/v1/jobs/${id}`).catch(() => null);
            if (job && terminal.has(job.state)) break;
            await sleep(2000);
          }
        }
      }
    },
  };
}
