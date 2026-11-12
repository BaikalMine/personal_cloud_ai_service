import {readFile, writeFile, copyFile} from 'node:fs/promises';
import {join} from 'node:path';
const root=process.argv[2]||'/opt/harness/node_modules';
const lib=join(root,'@deepseek-ai/dsh-skill-filesystem/lib');
const file=join(lib,'index.js');
const marker='import { loadStoryReferences } from "./gateway-skill-references.mjs";';
let source=await readFile(file,'utf8');
const needle='content: parsed.content\n\t\t};';
const replacement='content: await loadStoryReferences(parsed.name, locator.directory, parsed.content, options.signal)\n\t\t};';
if(!source.includes(marker)){
  if(source.split(needle).length!==2)throw new Error('Unsupported Harness skill provider: patch not applied');
  source=marker+'\n'+source.replace(needle,replacement);
} else if(!source.includes(replacement))throw new Error('Incomplete skill reference patch');
await copyFile(new URL('./skill-references.mjs',import.meta.url),join(lib,'gateway-skill-references.mjs'));
await writeFile(file,source);
