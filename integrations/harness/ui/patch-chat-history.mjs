import {readFile,writeFile} from 'node:fs/promises';
import {join} from 'node:path';
const file=join(process.argv[2]||'/opt/harness/node_modules','@ai-gateway/harness-ui/index.mjs');
let source=await readFile(file,'utf8');
const marker="import { registerChatHistory } from './chat-history.mjs';";
const registration="  ctx.inject(['sessionQuery'], scope => registerChatHistory(scope));";
if (!source.includes(marker)) {
  const needle='  await registerChatMemoryTools(ctx);';
  if(source.split(needle).length!==2)throw new Error('Unsupported Harness UI entrypoint');
  source=marker+'\n'+source.replace(needle,needle+'\n'+registration);
} else if (!source.includes(registration)) throw new Error('Incomplete chat history registration');
await writeFile(file,source);
