import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp,mkdir,writeFile,readFile,rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createRequire } from 'node:module';
import { patchCompactionPresets } from './patch-compaction.mjs';
const yaml=createRequire(import.meta.url)('yaml');
test('all isolated preset compaction engines get defaults without replacing tools, JS tags or explicit tuning',async t=>{
  const root=await mkdtemp(join(tmpdir(),'harness-compact-'));t.after(()=>rm(root,{recursive:true,force:true}));
  const directory=join(root,'@deepseek-ai/dsh-web-app/presets');await mkdir(directory,{recursive:true});
  for(const preset of ['standard','ptc','cordis'])await writeFile(join(directory,preset+'.patch.yml'),`- insert:\n    - id: preset-${preset}\n      config:\n        plugins:\n          - id: keep-me\n            disabled: !!js process.platform === 'win32'\n          - id: compaction\n            config:\n              - id: compaction-basic\n                name: '@deepseek-ai/dsh-compaction-basic'\n${preset==='ptc'?'                config:\n                  maxTokens: 4096\n':''}`);
  await patchCompactionPresets(root);
  for(const preset of ['standard','ptc','cordis']){
    const file=join(directory,preset+'.patch.yml'),before=await readFile(file,'utf8');
    assert(before.includes('!!js'));const data=yaml.parseDocument(before,{logLevel:'silent'}).toJS();
    const plugins=data[0].insert[0].config.plugins;assert.equal(plugins[0].id,'keep-me');
    const settings=plugins[1].config[0].config;assert.equal(settings.headroomTokens,8192);assert.equal(settings.retainTokens,16384);
    assert.equal(settings.maxTokens,preset==='ptc'?4096:8192);assert.equal(settings.thresholdRatio,.8);
    await patchCompactionPresets(root);assert.equal(await readFile(file,'utf8'),before);
  }
});
