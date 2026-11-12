import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { createHash } from 'node:crypto';

const RC022_ATTACHMENT_SHA256 = 'f82c6651ff8510ae4025c5c95493bf49788393108ee9aa83fac39a8592f60808';

export async function patchUploadUI(root) {
  const folder=join(root,'@deepseek-ai/dsh-client-ui-attachment');
  const manifest=JSON.parse(await readFile(join(folder,'package.json'),'utf8'));
  if(!['0.2.1-alpha.2','0.2.1-rc.5','0.2.2-rc.1'].includes(manifest.version))throw new Error('Review attachment upload patch for new Harness version');
  const path=join(folder,'lib/client.js');
  let source=await readFile(path,'utf8');
  if(source.includes('data-gateway-upload-card')) {
    if(!source.includes('uploadMetricsText(metrics,russian)') || !source.includes('const cancelLabel=state===') || !source.includes('onClick: onRemove')) throw new Error('Incomplete attachment upload patch');
    return;
  }
  if(manifest.version === '0.2.2-rc.1' && createHash('sha256').update(source).digest('hex') !== RC022_ATTACHMENT_SHA256) throw new Error('Unsupported rc.1 attachment bundle');
  const meter=(await readFile(new URL('./upload-meter.mjs',import.meta.url),'utf8')).replaceAll('export function ','function ');
  const start=source.indexOf('\t\tfunction FileCard({'),end=source.indexOf('\n\t\t//#endregion',start);
  if(start<0||end<start)throw new Error('Attachment card patch anchor missing');
  let card=source.slice(start,end);
  const replace=(from,to)=>{if(card.split(from).length!==2)throw new Error('Attachment card patch anchor changed');card=card.replace(from,to);};
  const signature='function FileCard({ name, bytes, state, progress, labels, onRemove, onRetry }) {';
  replace(signature, signature+`
      const latest=react.useRef(); latest.current={progress,bytes};
      const [metrics,setMetrics]=react.useState(null);
      const russian=/[А-Яа-я]/.test(labels.uploading);
      react.useEffect(()=>{
        if(state!=="uploading"){setMetrics(null);return;}
        const meter=createUploadMeter();
        const publish=()=>{const value=latest.current;setMetrics(meter.sample((value.progress??0)*value.bytes,value.bytes));};
        publish();const timer=setInterval(publish,500);return()=>clearInterval(timer);
      },[state,name,bytes]);
      const cancelLabel=state==="uploading"?(russian?"Отменить загрузку":"Cancel upload"):labels.remove;
  `);
  replace('const meta = state === "uploading" ?', 'const baseMeta = state === "uploading" ?');
  replace('const retryable = state === "error";', 'const meta=state==="uploading"&&metrics?`${labels.uploading} · ${metrics.percent}%\\n${uploadMetricsText(metrics,russian)}`:baseMeta;\n\t\t\tconst retryable = state === "error";');
  replace('className: `${FileCard_module_css_default.card}', '"data-gateway-upload-card":state==="uploading"?"":void 0,\n\t\t\t\tclassName: `${FileCard_module_css_default.card}');
  replace('"aria-label": labels.remove,', '"aria-label": cancelLabel,\n\t\t\t\t\t\ttitle:cancelLabel,');
  source=source.slice(0,start)+meter+'\n'+card+source.slice(end);
  await writeFile(path,source);
}
if(process.argv[1]?.endsWith('patch-upload-ui.mjs'))await patchUploadUI(process.argv[2]??'/opt/harness/node_modules');
