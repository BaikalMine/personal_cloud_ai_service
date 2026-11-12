export function createPromptGuides({ call, defaultCFG = 2 }) {
  const reviewed = new WeakMap();
  function state(exec) {
    const session = exec.agent?.session;
    if (!session) throw new Error('Prompt guidance requires an agent Session');
    if (!reviewed.has(session)) reviewed.set(session, new Map());
    return reviewed.get(session);
  }
  const key = (mode, cfg) => JSON.stringify([mode, cfg]);
  async function load(query, exec) {
    const guide = await call('/v1/prompt-guide', query, { signal: exec.signal });
    if (guide.source !== 'promptassistant.EnhancementInstruction' || typeof guide.instruction !== 'string' || !guide.instruction.trim() || !/^[a-f0-9]{64}$/.test(guide.revision || '')) throw new Error('Shared Prompt Assistant guidance is unavailable');
    return guide;
  }
  return {
    async read(query, exec) {
      const guide = await load(query, exec);
      state(exec).set(key(guide.workflow_mode, guide.cfg), { revision: guide.revision, profile: guide.profile, references: query.references ?? [] });
      return guide;
    },
    async prepare(request, imageCount, exec) {
      const mode = request.mode ?? (imageCount ? 'references' : 'text'), cfg = request.cfg ?? defaultCFG;
      const memory = state(exec), previous = memory.get(key(mode, cfg));
      const references = previous?.references?.filter(reference => reference.number <= imageCount) ?? [];
      const guide = await load({ mode, cfg, ...(previous ? { profile: previous.profile, ...(references.length ? { references } : {}) } : {}) }, exec);
      if (previous?.revision === guide.revision) return;
      memory.set(key(mode, cfg), { revision: guide.revision, profile: guide.profile, references });
      return guide;
    },
    clear: session => reviewed.delete(session),
  };
}
