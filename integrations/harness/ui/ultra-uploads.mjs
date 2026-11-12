import { createHash } from 'node:crypto';

// Reuse only identical bytes within one active task; filenames are not cache keys.
export function createUltraUploads({ call, now = Date.now }) {
  const sessions = new WeakMap(), lifetime = 10 * 60 * 1000;
  return {
    async upload(bytes, exec) {
      const session = exec.agent?.session;
      if (!session) throw new Error('Image upload requires an agent Session');
      let cache = sessions.get(session);
      if (!cache) sessions.set(session, cache = new Map());
      const key = createHash('sha256').update(bytes).digest('hex'), cached = cache.get(key);
      if (cached && now() - cached.saved < lifetime) return cached.upload;
      const upload = await call('/v1/uploads', bytes, { signal: exec.signal });
      if (typeof upload.id !== 'string' || !upload.id) throw new Error('Invalid Ultra upload response');
      if (cache.size >= 16) cache.delete(cache.keys().next().value);
      cache.set(key, { upload, saved: now() });
      return upload;
    },
    clear: session => sessions.delete(session),
  };
}
