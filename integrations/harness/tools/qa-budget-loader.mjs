import { registerHooks } from 'node:module';

registerHooks({ load(url, context, nextLoad) {
  const result = nextLoad(url, context);
  if (!url.endsWith('/@earendil-works/pi-ai/dist/api/simple-options.js')) return result;
  const marker = '    const available = model.contextWindow - estimateContextTokens(context).tokens - CONTEXT_SAFETY_TOKENS;';
  const source = String(result.source);
  if (!source.includes(marker)) throw new Error('QA budget probe no longer matches pinned pi-ai');
  return { ...result, source: source.replace(marker, `${marker}
    console.log(JSON.stringify({ stage: 'synthetic-context-estimate', contextWindow: model.contextWindow, modelMaxTokens: model.maxTokens, requestedMaxTokens: maxTokens, estimatedTokens: estimateContextTokens(context).tokens }));`) };
} });
