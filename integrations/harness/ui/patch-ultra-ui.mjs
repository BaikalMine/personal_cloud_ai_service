import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { generatedImageTest, upgradeGeneratedImageMatcher } from './generated-image-path.mjs';

const root=process.argv[2]||'/opt/harness/node_modules';
const file=join(root,'@deepseek-ai/dsh-client-ui-deliverables/lib/client.js');
let source=await readFile(file,'utf8');
if(!source.includes('gateway.deliverables.image')){
  function replace(before,after){if(source.split(before).length!==2)throw new Error('Unsupported Harness deliverables SDK shape');source=source.replace(before,after);}
  replace('function PresentedFileCard({ file, cwd, phase, host, onPreview, actions, t }) {','function PresentedFileCard({ file, cwd, phase, host, onPreview, actions, t, generatedImage }) {\n\t\t\tif (generatedImage) return generatedImage;');
  replace('file,\n\t\t\t\t\t\t\tcwd,','file,\n\t\t\t\t\t\t\tgeneratedImage: '+generatedImageTest+' ? renderSlot("gateway.deliverables.image", {file, sessionId}) : null,\n\t\t\t\t\t\t\tcwd,');
  replace('children: { "deliverables.file.actions": {','children: { "gateway.deliverables.image": {kind:"single",scope:"session"}, "deliverables.file.actions": {');
  await writeFile(file,source);
}
// Upgrade the already-patched production SDK without reinstalling its package.
if (source.includes('gateway.deliverables.image')) {
  const upgraded=upgradeGeneratedImageMatcher(source);
  if(upgraded!==source)await writeFile(file,upgraded);
}
