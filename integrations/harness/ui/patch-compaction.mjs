import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
const yaml=createRequire(import.meta.url)('yaml');
const root=process.argv[2]||'/opt/harness/node_modules';
// These defaults belong to the image. Persisted explicit user preset overrides still win.
export async function patchCompactionPresets(directory) {
  for(const preset of ['standard','ptc','cordis']){
    const path=join(directory,'@deepseek-ai/dsh-web-app/presets',`${preset}.patch.yml`);
    const doc=yaml.parseDocument(await readFile(path,'utf8'),{logLevel:'silent'});
    if(doc.errors.length)throw new Error('Cannot parse Harness preset');
    let found=0;
    function walk(node){
      if(yaml.isMap(node)){
        if(node.get('id')==='compaction-basic'&&node.get('name')==='@deepseek-ai/dsh-compaction-basic'){
          const config=node.get('config')??doc.createNode({});
          if(!yaml.isMap(config))throw new Error('Unsupported Harness compaction config');
          for(const [key,value]of Object.entries({auto:true,thresholdRatio:.8,headroomTokens:8192,retainTokens:16384,maxTokens:8192}))if(!config.has(key))config.set(key,value);
          node.set('config',config);found++;
        }
        for(const item of node.items)walk(item.value);
      }else if(yaml.isSeq(node))for(const item of node.items)walk(item);
    }
    walk(doc.contents);
    if(found!==1)throw new Error(`Unsupported Harness ${preset} compaction preset`);
    await writeFile(path,String(doc));
  }
}
if(process.argv[1]&&fileURLToPath(import.meta.url)===process.argv[1])await patchCompactionPresets(root);
