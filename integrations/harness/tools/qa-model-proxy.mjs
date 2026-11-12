import { createServer, request } from 'node:http';
import { networkInterfaces } from 'node:os';
import { spawn } from 'node:child_process';

const address = Object.values(networkInterfaces()).flat().find(value => value.family === 'IPv4' && !value.internal).address;
const upstream = process.env.HARNESS_MODEL_URL.replace(/\/$/, '');
const proxy = createServer(async (req, res) => {
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 8 << 20) { res.writeHead(413).end(); return; }
    chunks.push(chunk);
  }
  const body = Buffer.concat(chunks);
  if (req.method === 'POST') {
    const value = JSON.parse(body.toString('utf8'));
    console.log(JSON.stringify({ stage: 'synthetic-request-budget', model: value.model, max_tokens: value.max_tokens,
      max_completion_tokens: value.max_completion_tokens, tools: value.tools?.length, messages: value.messages?.length }));
  }
  const forward = request(`${upstream}${req.url.replace(/^\/v1/, '')}`, { method: req.method, headers: {
    Authorization: req.headers.authorization, 'Content-Type': 'application/json', 'Content-Length': body.length,
  } }, response => {
    res.writeHead(response.statusCode, response.headers);
    response.pipe(res);
  });
  forward.on('error', () => res.destroy());
  res.on('close', () => forward.destroy());
  forward.end(body);
});
await new Promise(resolve => proxy.listen(3090, address, resolve));
const child = spawn('node', ['/opt/harness/entry.mjs'], { env: {
  ...process.env, HARNESS_MODEL_URL: `http://${address}:3090/v1`,
}, stdio: 'inherit' });
child.on('exit', code => { proxy.close(); process.exit(code || 0); });
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => child.kill(signal));
