export const DEFAULT_CONTEXT_WINDOW = 131072;
export const DEFAULT_MAX_TOKENS = 32768;
export const COMPACTION_POLICY = Object.freeze({
  auto: true, thresholdRatio: 0.8, headroomTokens: 8192,
  retainTokens: 16384, maxTokens: 8192,
});

export function modelPolicy(models) {
  if (!Array.isArray(models) || !models.length) throw new Error('Missing Harness models');
  const ids = new Set();
  return models.map(model => {
    if (!model || typeof model.id !== 'string' || !model.id.trim() ||
        typeof model.name !== 'string' || !model.name.trim() || ids.has(model.id)) {
      throw new Error('Invalid or duplicate Harness model');
    }
    ids.add(model.id);
    const contextWindow = model.contextWindow ?? DEFAULT_CONTEXT_WINDOW;
    const maxTokens = model.maxTokens ?? DEFAULT_MAX_TOKENS;
    if (!Number.isSafeInteger(contextWindow) || contextWindow < 65536 || contextWindow > 262144 ||
        !Number.isSafeInteger(maxTokens) || maxTokens < 1 ||
        maxTokens + COMPACTION_POLICY.headroomTokens + COMPACTION_POLICY.retainTokens >= contextWindow) {
      throw new Error(`Invalid context/output budget for Harness model ${model.id}`);
    }
    return { id: model.id, name: model.name, contextWindow, maxTokens, input: ['text', 'image'] };
  });
}

// Change only integration-owned legacy defaults. Explicit user tuning stays intact.
export function upgradeModelPolicy(patch, models) {
  if (!Array.isArray(patch)) throw new Error('Invalid Harness profile patch');
  const next = structuredClone(patch);
  const rows = next.filter(row => row.id === 'llm-pi-ai');
  if (rows.length !== 1) throw new Error('Ambiguous Harness local provider patch');
  const provider = rows[0].config?.providers?.['ollama-local'];
  if (!Array.isArray(provider?.models)) throw new Error('Missing Harness local model settings');
  const policies = new Map(models.map(model => [model.id, model]));
  for (const model of provider.models) {
    const policy = policies.get(model.id);
    if (!policy) continue;
    if (model.contextWindow === 16384) model.contextWindow = policy.contextWindow;
    if (model.maxTokens === 4096) model.maxTokens = policy.maxTokens;
  }
  const compact = next.filter(row => row.id === 'compaction-basic');
  if (compact.length > 1) throw new Error('Ambiguous Harness compaction patch');
  if (compact.length === 0) next.push({ id: 'compaction-basic', config: { ...COMPACTION_POLICY } });
  return next;
}
