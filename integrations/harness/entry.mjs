import { spawn } from 'node:child_process';
import { timingSafeEqual } from 'node:crypto';
import { mkdir, readFile, readlink, rename, symlink, writeFile } from 'node:fs/promises';
import { createServer, request } from 'node:http';
import { networkInterfaces } from 'node:os';
import { createInterface } from 'node:readline';
import { createRequire } from 'node:module';
import { isDeepStrictEqual } from 'node:util';
import { COMPACTION_POLICY, modelPolicy, upgradeModelPolicy } from './model-policy.mjs';
import { ensurePythonEnvironment } from './python-env.mjs';

const publicUrl = new URL(process.env.HARNESS_PUBLIC_URL);
const authorities = new Set([publicUrl.host, ...(process.env.HARNESS_TRUSTED_HOSTS || '').split(',').filter(Boolean)]);
const bootstrapKey = process.env.HARNESS_BOOTSTRAP_KEY;
if (!bootstrapKey || bootstrapKey.length < 32 || !process.env.HARNESS_MODEL_KEY) throw new Error('Missing Harness credentials');
const bind = process.env.HARNESS_BIND_HOST || Object.values(networkInterfaces()).flat().find(x => x.family === 'IPv4' && !x.internal)?.address;
if (!bind) throw new Error('Missing private container interface');
if (process.env.HARNESS_PYTHON_ENV === 'true') await ensurePythonEnvironment();

const patchPath = `${process.env.DSH_HOME}/profiles/web/cordis.patch.yml`;
await mkdir(`${process.env.DSH_HOME}/profiles/web`, { recursive: true });
const models = modelPolicy(JSON.parse(process.env.HARNESS_MODELS || '[]'));
const patch = [
  { id: 'llm-pi-ai', config: { providers: { 'ollama-local': {
    displayName: 'Local Ollama', apiKeyEnv: 'HARNESS_MODEL_KEY',
    api: 'openai-completions', baseURL: process.env.HARNESS_MODEL_URL,
    compat: { supportsDeveloperRole: false, maxTokensField: 'max_tokens' },
    streamIdleTimeoutMs: 300000, retryPolicy: { mode: 'normal', maxRetries: 0 },
    models,
  } } } },
  { id: 'agent-default-model', config: { provider: 'ollama-local', model: models[0].id } },
  { id: 'workspace-controller', config: { documentsDirectory: process.env.HARNESS_WORKSPACE || '/workspace' } },
  // Session titles must not submit hidden model requests while another turn owns GPU.
  { id: 'session-title-llm', disabled: true },
  { id: 'compaction-basic', config: { ...COMPACTION_POLICY } },
];
try { await writeFile(patchPath, JSON.stringify(patch, null, 2), { flag: 'wx', mode: 0o600 }); }
catch (error) {
  if (error.code !== 'EEXIST') throw error;
  const original = await readFile(patchPath, 'utf8');
  const yaml = createRequire(import.meta.url)('yaml');
  const current = yaml.parse(original);
  const upgraded = upgradeModelPolicy(current, models);
  if (!isDeepStrictEqual(current, upgraded)) {
    const suffix = `gateway-model-policy-${Date.now()}-${process.pid}`;
    await writeFile(`${patchPath}.${suffix}.bak`, original, { mode: 0o600, flag: 'wx' });
    const temporary = `${patchPath}.${suffix}.tmp`;
    await writeFile(temporary, yaml.stringify(upgraded), { mode: 0o600, flag: 'wx' });
    await rename(temporary, patchPath);
    console.log('Harness legacy model budgets upgraded; original profile backed up');
  }
}

// Install the image-owned addon as an external profile dependency, preserving user bundles.
const profileDirectory = `${process.env.DSH_HOME}/profiles/web`;
const manifestPath = `${profileDirectory}/package.json`;
let manifest;
try { manifest = JSON.parse(await readFile(manifestPath, 'utf8')); }
catch (error) {
  if (error.code !== 'ENOENT') throw error;
  manifest = { name: 'dsh-profile-web', private: true, dependencies: {},
    dsh: { profile: { bundles: ['@deepseek-ai/dsh-base', '@deepseek-ai/dsh-web-app'] } } };
}
const addonPath = '/opt/harness/node_modules/@ai-gateway/harness-ui';
const addonLink = `${profileDirectory}/node_modules/@ai-gateway/harness-ui`;
await mkdir(`${profileDirectory}/node_modules/@ai-gateway`, { recursive: true });
try { await symlink(addonPath, addonLink, 'dir'); }
catch (error) {
  if (error.code !== 'EEXIST' || await readlink(addonLink) !== addonPath) throw new Error('Harness UI dependency conflicts with an existing profile package');
}
const addonDependency = `file:${addonPath}`;
if (manifest.dependencies?.['@ai-gateway/harness-ui'] !== addonDependency) {
  manifest.dependencies = { ...manifest.dependencies, '@ai-gateway/harness-ui': addonDependency };
  const temporaryManifest = `${manifestPath}.gateway-ui-${process.pid}.tmp`;
  await writeFile(temporaryManifest, `${JSON.stringify(manifest, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
  await rename(temporaryManifest, manifestPath);
}

let launchToken;
if (process.env.HARNESS_REA_ENABLED === 'true') {
  const skillDirectory = `${process.env.DSH_HOME}/skills`;
  const skillLink = `${skillDirectory}/reverse-engineer-anything`;
  await mkdir(skillDirectory, { recursive: true });
  try { await symlink('/opt/rea/node_modules/rea-agents/skills/reverse-engineer-anything', skillLink, 'dir'); }
  catch (error) {
    if (error.code !== 'EEXIST') throw error;
    console.log('REA skill path already exists; preserving the installed skill');
  }
}
const childEnv = { ...process.env };
delete childEnv.HARNESS_BOOTSTRAP_KEY;
const child = spawn('/opt/harness/node_modules/.bin/dsh', [
  '--profile', 'web', '--patch', '/opt/harness/gateway-ui.patch.yml',
  ...(process.env.HARNESS_REA_ENABLED === 'true' ? ['--patch', '/opt/harness/rea.patch.yml'] : []),
  '--no-open', '--host', bind, '--port', '3080', '--public-url', publicUrl.href,
  ...[...authorities].flatMap(host => ['--trusted-host', host]),
], { env: childEnv, stdio: ['ignore', 'pipe', 'pipe'] });
for (const stream of [child.stdout, child.stderr]) {
  createInterface({ input: stream }).on('line', line => {
    const match = line.match(/https?:\/\/[^\s]+/g)?.find(value => {
      try { return new URL(value).searchParams.has('token'); } catch { return false; }
    });
    if (match) {
      launchToken = new URL(match).searchParams.get('token');
      console.log('Harness web ready (launch credential withheld)');
      return;
    }
    console.log(line.replace(/([?&]token=)[^\s&]+/g, '$1[redacted]'));
  });
}
child.on('error', () => { console.error('Harness process could not start'); process.exit(1); });
child.on('exit', code => { console.log('Harness process stopped'); process.exit(code || 1); });

const cache = new Map();
const bootstrap = createServer(async (req, res) => {
  res.setHeader('Cache-Control', 'no-store');
  const url = new URL(req.url, 'http://bootstrap.invalid');
  if (req.method === 'GET' && url.pathname === '/healthz') {
    res.writeHead(launchToken ? 200 : 503).end(); return;
  }
  const auth = Buffer.from(req.headers.authorization || '');
  const expected = Buffer.from(`Bearer ${bootstrapKey}`);
  if (auth.length !== expected.length || !timingSafeEqual(auth, expected)) { res.writeHead(404).end(); return; }
  const host = url.searchParams.get('host');
  if (req.method !== 'GET' || url.pathname !== '/session' || !authorities.has(host) || !launchToken) { res.writeHead(503).end(); return; }
  try {
    let entry = cache.get(host);
    if (!entry || Date.now() - entry.time > 3600000) {
      const cookie = await new Promise((resolve, reject) => {
        const exchange = request({ hostname: bind, port: 3080, path: `/?token=${encodeURIComponent(launchToken)}`, headers: { Host: host } }, response => {
          response.resume();
          const cookies = response.headers['set-cookie'] || [];
          if (response.statusCode !== 303 || cookies.length !== 1 || !cookies[0].startsWith('dsh-auth-')) reject(new Error('Bootstrap failed'));
          else resolve(cookies[0]);
        });
        exchange.setTimeout(5000, () => exchange.destroy(new Error('Bootstrap timed out')));
        exchange.on('error', reject); exchange.end();
      });
      entry = { cookie, time: Date.now() }; cache.set(host, entry);
    }
    res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ cookie: entry.cookie }));
  } catch { res.writeHead(503).end(); }
});
bootstrap.listen(3082, bind);
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => { bootstrap.close(); child.kill(signal); });
