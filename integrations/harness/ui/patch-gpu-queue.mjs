import {readFile,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
const file=join(process.argv[2]||'/opt/harness/node_modules','@ai-gateway/harness-ui/index.mjs');
let source=await readFile(file,'utf8');
const marker='// Local stream admission is serialized by registerCompactionCalls.';
if(!source.includes(marker)){
  function replace(before,after){if(source.split(before).length!==2)throw new Error('Unsupported GPU lifecycle hook; no patch written');source=source.replace(before,after);}
  replace('    let modelDone;\n    const finishModel = () => { modelDone?.(); modelDone = undefined; };', '    '+marker);
  replace("    agent.ctx.on('agent/request', async ({ signal }, next) => {", "    agent.ctx.on('agent/request', async (_request, next) => {");
  replace("      finishModel();\n      if (config.provider === 'ollama-local') modelDone = await handoff.model(lease, signal);\n      else await lease.release();", "      if (config.provider !== 'ollama-local') await lease.release();");
  replace("    agent.ctx.on('agent/turn-stopping', async () => { finishModel(); await lease.release(); });\n    agent.ctx.on('agent/error', finishModel);", "    agent.ctx.on('agent/turn-stopping', async () => { await lease.release(); });");
  replace("      if (session === agent.session && (event.type === 'assistant/message' || event.type === 'assistant/attempt')) finishModel();\n",'');
  replace("        finishModel();\n        void lease.release()", "        void lease.release()");
  replace('        finishModel(); remove();','        remove();');
}else if(source.includes('modelDone'))throw new Error('Incomplete GPU queue patch');
await writeFile(file,source);
