import { request } from 'node:http';
import { networkInterfaces } from 'node:os';
import { randomUUID } from 'node:crypto';

const address = Object.values(networkInterfaces()).flat().find(value => value.family === 'IPv4' && !value.internal).address;
const authority = new URL(process.env.HARNESS_PUBLIC_URL).host;
async function call(port, path, headers, body) {
  return new Promise((resolve, reject) => {
    const req = request({ hostname: address, port, path, method: body ? 'POST' : 'GET', headers }, response => {
      let text = '';
      response.setEncoding('utf8');
      response.on('data', chunk => { text += chunk; });
      response.on('end', () => {
        if (response.statusCode !== 200) reject(new Error(`QA API status ${response.statusCode}`));
        else { try { resolve(JSON.parse(text)); } catch (error) { reject(error); } }
      });
    });
    req.setTimeout(30000, () => req.destroy(new Error('QA API timeout')));
    req.on('error', reject);
    req.end(body);
  });
}
const bootstrap = await call(3082, `/session?host=${encodeURIComponent(authority)}`, {
  Authorization: `Bearer ${process.env.HARNESS_BOOTSTRAP_KEY}`,
});
async function rpc(method, payload) {
  const result = await call(3080, `/api/${method}`, {
    Host: authority,
    Origin: new URL(process.env.HARNESS_PUBLIC_URL).origin,
    Cookie: bootstrap.cookie.split(';')[0],
    'Content-Type': 'application/json',
  }, JSON.stringify({ type: 'client-request', rpcId: randomUUID(), method, payload: { args: payload } }));
  if (!result.result.ok) throw new Error(`QA RPC rejected: ${method}: ${JSON.stringify(result.result.error)}`);
  return result.result.value;
}
const inventory = await rpc('pluginInventory/list', {});
if (process.env.QA_INVENTORY_ONLY === 'true') {
  const entry = inventory.entries.find(value => value.entryId === 'include:gateway-rea');
  if (!entry?.enabled || entry.fiberPhase !== 'active') throw new Error('REA production plugin is not active');
  console.log(JSON.stringify({ reaPluginRegistered: true, reaFiberPhase: entry.fiberPhase }));
  process.exit(0);
}
const session = process.env.QA_SESSION ? { sessionId: process.env.QA_SESSION } : await rpc('session/create', { request: { cwd: '/workspace' } });
const skills = await rpc('skills/list', { request: { sessionId: session.sessionId } });
const entry = inventory.entries.find(value => value.entryId === 'include:gateway-rea');
const selected = entry?.enabled === true && entry?.fiberPhase === 'active';
const skill = JSON.stringify(skills).includes('reverse-engineer-anything');
if (!selected || !skill) throw new Error(`REA registration missing: plugin=${selected}, skill=${skill}`);
console.log(JSON.stringify({ reaPluginRegistered: selected, reaSkillAvailable: skill, reaFiberPhase: entry?.fiberPhase }));
if (process.env.QA_AGENT === 'true') {
  if (!process.env.QA_SESSION && process.env.QA_MODEL) await rpc('session/selectModel', { request: {
    sessionId: session.sessionId, provider: 'ollama-local', model: process.env.QA_MODEL,
  } });
  if (!process.env.QA_SESSION) await rpc('session/prompt', { request: {
    sessionId: session.sessionId, requestId: randomUUID(), mode: 'queue',
    content: [{ type: 'text', text: 'Integration smoke test: use mcp__rea__tool_schema with name binary_session, then use mcp__rea__invoke with name binary_session and argumentsJson {}. Report whether a binary is open. Do not use shell tools or inspect files. The REA backend tool is accessible through invoke, not as a standalone tool.' }],
  } });
  console.log(JSON.stringify({ stage: 'agent-admitted', sessionId: session.sessionId }));
  const deadline = Date.now() + 8 * 60000;
  let finished = false;
  while (Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 3000));
    const sessions = await rpc('session/list', { _request: {} });
    const current = sessions.items.find(value => value.sessionId === session.sessionId);
    if (current && !current.running && !current.blank) { finished = true; break; }
  }
  if (!finished) {
    await rpc('session/cancel', { request: { sessionId: session.sessionId } });
    throw new Error('Synthetic Harness agent timed out and was cancelled');
  }
  const projections = await rpc('session/projections', { request: { sessionId: session.sessionId } });
  const page = await rpc('session/page', { request: {
    address: { kind: 'session', sessionId: session.sessionId }, throughSeq: projections.asOfSeq, maxMessages: 20,
  } });
  const text = JSON.stringify(page);
  const events = (page.records || page.items || []).map(value => value.event);
  const toolCalls = events.filter(value => value?.type === 'assistant/message').flatMap(value => value.data.message.content).filter(value => value.type === 'tool-call');
  const results = events.filter(value => value?.type === 'tool/result');
  const steps = events.filter(value => value?.type === 'step/start').length;
  const failures = results.filter(value => value.data.message.isError === true || value.data.error);
  const final = events.filter(value => value?.type === 'assistant/message').at(-1)?.data.message;
  const answer = final?.content.some(value => value.type === 'text' && value.text.trim());
  console.log(JSON.stringify({ stage: 'agent-finished', gpuBusyObserved: text.includes('gpu_busy'), steps, toolCalls: toolCalls.map(value => value.name), toolResults: results.length, toolErrors: failures.length, answerPresent: answer, stopReason: final?.source?.replayState?.response?.stopReason }));
  if (text.includes('gpu_busy') || steps < 2 || !toolCalls.some(value => value.name === 'mcp__rea__invoke') || !results.length || failures.length || !answer) throw new Error('Synthetic agent did not complete the REA tool step');
}
