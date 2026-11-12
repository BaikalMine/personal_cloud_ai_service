const model = JSON.parse(process.env.HARNESS_MODELS)[0].id;
const url = `${process.env.HARNESS_MODEL_URL.replace(/\/$/, '')}/chat/completions`;
for (let attempt = 1; attempt <= 2; attempt++) {
  const started = Date.now();
  const abort = new AbortController();
  const response = await fetch(url, {
    method: 'POST', signal: abort.signal,
    headers: { Authorization: `Bearer ${process.env.HARNESS_MODEL_KEY}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ model, stream: true, max_tokens: 16, messages: [{ role: 'user', content: 'Reply with exactly OK.' }] }),
  });
  if (response.status !== 200) throw new Error(`Synthetic model request status ${response.status}`);
  const reader = response.body.getReader();
  let data = '';
  let terminal = false;
  const decode = new TextDecoder();
  while (!terminal) {
    const next = await reader.read();
    if (next.done) break;
    data += decode.decode(next.value, { stream: true });
    let boundary;
    while ((boundary = data.indexOf('\n\n')) >= 0) {
      const frame = data.slice(0, boundary);
      data = data.slice(boundary + 2);
      const payload = frame.split('\n').filter(line => line.startsWith('data:')).map(line => line.slice(5).trim()).join('\n');
      if (!payload || payload === '[DONE]') continue;
      const value = JSON.parse(payload);
      if (value.choices?.some(choice => choice.finish_reason)) { terminal = true; break; }
    }
  }
  if (!terminal) throw new Error('Synthetic model stream ended without a terminal event');
  // Match the adapter: cancel immediately on finish and start the next call.
  await reader.cancel();
  abort.abort();
  console.log(JSON.stringify({ attempt, status: response.status, terminal: true, elapsedMs: Date.now() - started }));
}
