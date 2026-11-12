import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp,mkdir,readFile,writeFile,rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import vm from 'node:vm';
import { patchUploadUI } from './patch-upload-ui.mjs';

test('pinned native upload card patch is syntactically valid and idempotent',async t=>{
  const root=await mkdtemp(join(tmpdir(),'upload-ui-')),folder=join(root,'@deepseek-ai/dsh-client-ui-attachment');
  t.after(()=>rm(root,{recursive:true,force:true}));await mkdir(join(folder,'lib'),{recursive:true});
  await writeFile(join(folder,'package.json'),JSON.stringify({version:'0.2.1-alpha.2'}));
  const path=join(folder,'lib/client.js');
  await writeFile(path,`\t\tfunction FileCard({ name, bytes, state, progress, labels, onRemove, onRetry }) {
const meta = state === "uploading" ? labels.uploading : labels.failed;
const retryable = state === "error";
return {className: \x60\x24{FileCard_module_css_default.card}\x60,"aria-label": labels.remove,onClick: onRemove,meta};
}
\t\t//#endregion`);
  await patchUploadUI(root);const source=await readFile(path,'utf8');new vm.Script(source);
  assert(source.includes('data-gateway-upload-card'));assert(source.includes('Cancel upload'));assert(source.includes('uploadMetricsText(metrics,russian)'));
  await patchUploadUI(root);assert.equal(await readFile(path,'utf8'),source);
});

test('rc.1 real bundle preserves upload, retry and cancel behavior', async t => {
  const installed=process.env.HARNESS_022_ATTACHMENT_CLIENT_PATH;
  if(!installed)return t.skip('requires rc.1 attachment bundle');
  const root=await mkdtemp(join(tmpdir(),'upload-ui-022-')),folder=join(root,'@deepseek-ai/dsh-client-ui-attachment');
  t.after(()=>rm(root,{recursive:true,force:true}));await mkdir(join(folder,'lib'),{recursive:true});
  await writeFile(join(folder,'package.json'),JSON.stringify({version:'0.2.2-rc.1'}));
  const path=join(folder,'lib/client.js'), original=await readFile(installed,'utf8');
  await writeFile(path,original);await patchUploadUI(root);
  const source=await readFile(path,'utf8');new vm.Script(source);
  const start=source.indexOf('function FileCard('),end=source.indexOf('\n\t\t//#endregion',start);
  const card=source.slice(start,end);
  const jsx=(type,props)=>({type,props});
  const context={react:{useRef:()=>({}),useState:()=>[null,()=>{}],useEffect:()=>{}},
    react_jsx_runtime:{jsx,jsxs:jsx},FileCard_module_css_default:{card:'card',failed:'failed',body:'body',remove:'remove',removeFailed:'removeFailed'},
    _deepseek_ai_dsh_client_ui_primitives:{fileExtension:()=>'',fileSizeText:()=>'',FileTypeIcon:'icon',IconCloseFillRegular:'close'}};
  const FileCard=vm.runInNewContext(`${card}; FileCard`,context);
  const remove=()=>{},retry=()=>{},labels={uploading:'Uploading',failed:'Failed',remove:'Remove',retry:'Retry',label:'Attachment'};
  const upload=FileCard({name:'test.txt',bytes:10,state:'uploading',progress:.5,labels,onRemove:remove,onRetry:retry});
  assert.equal(upload.props['data-gateway-upload-card'],'');
  assert.equal(upload.props.children[2].props['aria-label'],'Cancel upload');
  assert.equal(upload.props.children[2].props.onClick,remove);
  const failed=FileCard({name:'test.txt',bytes:10,state:'error',labels,onRemove:remove,onRetry:retry});
  assert.equal(failed.props.children[1].props.onClick,retry);
  assert.equal(failed.props.children[2].props['aria-label'],'Remove');
  await patchUploadUI(root);assert.equal(await readFile(path,'utf8'),source);
  await writeFile(path,original+'\n// changed');
  await assert.rejects(patchUploadUI(root),/Unsupported rc.1 attachment bundle/);
  assert.equal(await readFile(path,'utf8'),original+'\n// changed');
});
