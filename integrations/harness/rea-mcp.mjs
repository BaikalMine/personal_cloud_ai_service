import { createRequire } from 'node:module';
const require = createRequire('/opt/rea/node_modules/rea-agents/package.json');
const { Client } = await import(require.resolve('@modelcontextprotocol/client'));
const { StdioClientTransport } = await import(require.resolve('@modelcontextprotocol/client/stdio'));
const { McpServer } = await import(require.resolve('@modelcontextprotocol/server'));
const { StdioServerTransport } = await import(require.resolve('@modelcontextprotocol/server/stdio'));
const { z } = await import(require.resolve('zod'));

const backend = new Client({ name: 'gateway-rea-catalog', version: '1.0.0' });
await backend.connect(new StdioClientTransport({
  command: 'node', args: ['/opt/rea/node_modules/rea-agents/scripts/rea.mjs', 'mcp'], cwd: '/workspace',
  env: { GHIDRA_INSTALL_DIR: process.env.GHIDRA_INSTALL_DIR, JAVA_HOME: process.env.JAVA_HOME, HOME: '/workspace' },
}));
const catalog = (await backend.listTools()).tools.sort((a, b) => a.name.localeCompare(b.name));
const definitions = new Map(catalog.map(tool => [tool.name, tool]));
const result = value => ({ content: [{ type: 'text', text: JSON.stringify(value) }] });
const error = message => ({ ...result({ error: message }), isError: true });
const server = new McpServer({ name: 'gateway-rea', version: '1.0.0' }, {
  instructions: 'REA provides reverse engineering and headless Ghidra in this container. Use catalog to find tools, tool_schema for exact inputs, then invoke with a JSON object encoded in argumentsJson. All REA tools are available through invoke. Uploaded files are in the workspace. Do not guess parameters or claim analysis without a tool result. Do not execute untrusted binaries just to inspect them.',
});
server.registerTool('catalog', {
  description: 'Find available REA tools by name or description. Paged, concise catalog; no full schemas in the model context.',
  inputSchema: z.object({ query: z.string().max(200).optional(), offset: z.number().int().min(0).default(0), limit: z.number().int().min(1).max(50).default(20) }),
}, async ({ query = '', offset, limit }) => {
  const selected = catalog.filter(tool => `${tool.name} ${tool.description || ''}`.toLowerCase().includes(query.toLowerCase()));
  return result({ total: selected.length, offset,
    tools: selected.slice(offset, offset + limit).map(tool => ({ name: tool.name, description: (tool.description || '').slice(0, 280) })),
    nextOffset: offset + limit < selected.length ? offset + limit : null,
  });
});
server.registerTool('tool_schema', {
  description: 'Get the exact input schema and full description for one REA tool before invoking it.',
  inputSchema: z.object({ name: z.string().min(1).max(200) }),
}, async ({ name }) => definitions.has(name) ? result(definitions.get(name)) : error('Unknown REA tool; use catalog first.'));
server.registerTool('invoke', {
  description: 'Invoke any discovered REA tool once. argumentsJson must encode a plain JSON object conforming to tool_schema. Ghidra operations can take several minutes.',
  inputSchema: z.object({ name: z.string().min(1).max(200), argumentsJson: z.string().max(1048576) }),
}, async ({ name, argumentsJson }, extra) => {
  if (!definitions.has(name)) return error('Unknown REA tool; use catalog first.');
  let args;
  try { args = JSON.parse(argumentsJson); } catch { return error('argumentsJson is not valid JSON.'); }
  if (!args || typeof args !== 'object' || Array.isArray(args)) return error('argumentsJson must encode a plain object.');
  return backend.callTool({ name, arguments: args }, undefined, { timeout: 300000, signal: extra.signal });
});
await server.connect(new StdioServerTransport());
let closing = false;
async function close() {
  if (closing) return;
  closing = true;
  await Promise.allSettled([server.close(), backend.close()]);
}
server.server.onclose = () => { void backend.close(); };
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, async () => { await close(); process.exit(0); });
