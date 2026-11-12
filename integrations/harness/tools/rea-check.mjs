import { createRequire } from 'node:module';
const require = createRequire('/opt/rea/node_modules/rea-agents/package.json');
const { Client } = await import(require.resolve('@modelcontextprotocol/client'));
const { StdioClientTransport } = await import(require.resolve('@modelcontextprotocol/client/stdio'));
const client = new Client({ name: 'gateway-rea-verification', version: '1.0.0' });
try {
  await client.connect(new StdioClientTransport({ command: 'node', args: ['/opt/harness/rea-mcp.mjs'],
    env: { GHIDRA_INSTALL_DIR: process.env.GHIDRA_INSTALL_DIR, JAVA_HOME: process.env.JAVA_HOME, HOME: '/workspace' } }));
  const catalog = await client.listTools();
  if (catalog.tools.length !== 3) throw new Error('REA compact bridge must expose exactly three discovery tools');
  const listed = await client.callTool({ name: 'catalog', arguments: { limit: 1 } });
  const available = JSON.parse(listed.content.find(value => value.type === 'text').text);
  if (available.total !== 139 || available.tools.length !== 1 || available.nextOffset !== 1) throw new Error('REA catalog missing tools or pagination');
  const schema = await client.callTool({ name: 'tool_schema', arguments: { name: 'open_binary' } });
  if (!JSON.parse(schema.content.find(value => value.type === 'text').text).inputSchema) throw new Error('REA schema discovery failed');
  for (const argumentsJson of ['not-json', '[]', 'null']) {
    const invalid = await client.callTool({ name: 'invoke', arguments: { name: 'binary_session', argumentsJson } });
    if (!invalid.isError) throw new Error('REA invocation accepted malformed input');
  }
  const missing = await client.callTool({ name: 'invoke', arguments: { name: 'missing-fixture-tool', argumentsJson: '{}' } });
  if (!missing.isError) throw new Error('REA invocation accepted an unknown tool');
  console.log(JSON.stringify({ stage: 'catalog', staticTools: catalog.tools.length, availableTools: available.total, invalidInputsRejected: true }));
  let procedure;
  for (const [name, args] of [
    ['open_binary', { path: '/usr/lib/jvm/java-21-openjdk/bin/java', provider_id: 'ghidra' }],
    ['list_procedures', {}],
    ['procedure_pseudo_code', {}],
    ['close_binary', {}],
  ]) {
    const result = await client.callTool({ name: 'invoke', arguments: { name, argumentsJson: JSON.stringify(name === 'procedure_pseudo_code' ? { procedure } : args) } }, undefined, { timeout: 330000 });
    const text = result.content?.filter(x => x.type === 'text').map(x => x.text).join('\n') || '';
    if (result.isError) throw new Error(`REA ${name} failed: ${text.slice(0, 5000)}`);
    const data = JSON.parse(text);
    if (name === 'list_procedures') {
      procedure = data.raw_result.find(x => !x.procedure?.external && !x.procedure?.thunk && /^0x/.test(x.address))?.address;
      if (!procedure) throw new Error('No real native function discovered');
      console.log(JSON.stringify({ stage: name, functions: data.raw_result.length, procedure }));
    } else if (name === 'procedure_pseudo_code') {
      if (typeof data.raw_result !== 'string' || !data.raw_result.length) throw new Error('Native decompiler produced no pseudocode');
      console.log(JSON.stringify({ stage: name, provider: data.provider, characters: data.raw_result.length, pseudocode: data.raw_result.slice(0, 1000) }));
    } else console.log(JSON.stringify({ stage: name, isError: false, output: text.slice(0, 1200) }));
  }
} finally { await client.close(); }
