import { performance } from 'node:perf_hooks';

const model = process.argv[2];
const long = process.argv.includes('--long');
if (!model) throw new Error('Model required');
const reference = Array.from({ length: long || process.argv.includes('--context') ? 1800 : 20 }, (_, index) =>
  `Reference ${index + 1}: This synthetic engineering note concerns reliable backups, data validation and reproducible testing.`).join('\n');
const prompt = long
  ? `${reference}\nWrite a complete engineering handbook in exactly 80 numbered paragraphs, P001 through P080. Each paragraph must have 70 to 85 words. Cover backups, observability, testing, release management, incident recovery and data validation. Finish all 80 paragraphs in this response. Do not offer to continue or ask questions.`
  : `${reference}\nReply with exactly: Runtime verification complete.`;
const start = performance.now();
const url = `${process.env.HARNESS_MODEL_URL.replace(/\/$/, '')}/chat/completions`;
const response = await fetch(url, {
  method: 'POST', headers: { authorization: `Bearer ${process.env.HARNESS_MODEL_KEY}`, 'content-type': 'application/json' },
  body: JSON.stringify({ model, messages: [{ role: 'user', content: prompt }],
    max_tokens: 32768, reasoning_effort: 'none', stream: true, stream_options: { include_usage: true } }),
  signal: AbortSignal.timeout(900000),
});
if (!response.ok) {
  console.log(JSON.stringify({ model, status: response.status, stage: 'http_error' }));
  process.exit(1);
}
console.log(JSON.stringify({ model, stage: 'stream_started', seconds: Math.round((performance.now() - start) / 1000) }));
let pending = '', content = '', finishReason, usage;
const decoder = new TextDecoder();
let firstToken;
for await (const bytes of response.body) {
  pending += decoder.decode(bytes, { stream: true });
  let end;
  while ((end = pending.indexOf('\n')) >= 0) {
    const line = pending.slice(0, end).replace(/\r$/, '');
    pending = pending.slice(end + 1);
    if (!line.startsWith('data: ') || line === 'data: [DONE]') continue;
    const chunk = JSON.parse(line.slice(6));
    if (chunk.error) throw new Error('Model stream reported an error');
    usage = chunk.usage || usage;
    for (const choice of chunk.choices || []) {
      if (choice.delta?.content) {
        firstToken ??= performance.now();
        content += choice.delta.content;
      }
      finishReason = choice.finish_reason || finishReason;
    }
  }
}
const result = { model, stage: 'complete', finishReason, usage, outputCharacters: content.length,
  elapsedSeconds: Math.round((performance.now() - start) / 1000),
  firstTokenSeconds: firstToken && Math.round((firstToken - start) / 1000),
  finalParagraph: /P080/.test(content), paragraphs: new Set(content.match(/P\d{3}/g)).size };
console.log(JSON.stringify(result));
if (finishReason !== 'stop' || (long && (!Number.isSafeInteger(usage?.completion_tokens) || usage.completion_tokens <= 4096 || !result.finalParagraph))) process.exit(1);
