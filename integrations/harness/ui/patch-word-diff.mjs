import { readFile, readdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { createWordDiff } from './word-diff.mjs';
import { patchWordReviewSource } from './patch-word-review.mjs';

const marker = '/* gateway-native-word-diff:0.2.2-rc.1 */';
const ending = '/* end-gateway-native-word-diff */';
const fail = detail => { throw new Error(`Unsupported native word diff: ${detail}`); };
function once(source, before, after) {
  if (source.split(before).length !== 2) fail(`anchor ${before}`);
  return source.replace(before, after);
}

export function nativeDiffCore(source) {
  const matches = [...source.matchAll(/class ([\w$]+)\{diff\(t,r,i=\{\}\)/g)];
  if (matches.length !== 1) fail('jsdiff core');
  const name = matches[0][1], start = matches[0].index;
  const end = source.indexOf(`class `, start + matches[0][0].length);
  const core = source.slice(start, end);
  for (const anchor of ['diffWithOptionsObj(t,r,i,n){', 'i.maxEditLength', 'i.timeout', 'tokenize(t,r){return Array.from(t)}', 'buildValues(t,r,i){']) {
    if (!core.includes(anchor)) fail(`jsdiff API ${anchor}`);
  }
  if (!source.slice(end).startsWith(`class `) || !source.slice(end, end + 100).includes(`extends ${name}{`)) fail('jsdiff subclass');
  return { name, core };
}

export function patchWordDiffSource(source, version = '0.2.2-rc.1') {
  if (version !== '0.2.2-rc.1') fail(`version ${version}`);
  const { name } = nativeDiffCore(source);
  const injection = `${marker}\nconst gatewayNativeWordDiff=(${createWordDiff.toString()})(${name});\n${ending}\n`;
  const patched = source.includes(marker);
  if (patched) source = once(source, injection, '');
  else if (source.includes('gatewayNativeWordDiff') || source.includes(ending)) fail('partial patch');
  const matches = [...source.matchAll(/function ([\w$]+)\(\{diffs:e,labels:t,maxLines:r=16,className:i\}\)\{/g)];
  if (matches.length !== 1) fail('DiffBlock signature');
  const signature = matches[0][0], start = matches[0].index;
  if (!source.includes(`DiffBlock:${matches[0][1]},`)) fail('native export');
  const nativeExport = `DiffBlock:${matches[0][1]},`;
  const wordExport = `gatewayWordDiff:gatewayNativeWordDiff,${nativeExport}`;
  if (patched) source = once(source, wordExport, nativeExport);
  const tail = source.slice(start).match(/\}const [\w$]+=16;function /);
  if (!tail) fail('DiffBlock boundary');
  const end = start + tail.index + 1;
  let body = source.slice(start, end);
  const memo = body.match(/const n=([\w$]+)\.useMemo\(\(\)=>([\w$]+)\(e\),\[e\]\),/);
  const patchedMemo = body.match(/const n=([\w$]+)\.useMemo\(\(\)=>gatewayNativeWordDiff.annotate\(([\w$]+)\(e\)\),\[e\]\),/);
  const nativeMemo = memo || patchedMemo;
  if (!nativeMemo || !!memo === patched) fail('row memo');
  const beforeMemo = `const n=${nativeMemo[1]}.useMemo(()=>${nativeMemo[2]}(e),[e]),`;
  const afterMemo = `const n=${nativeMemo[1]}.useMemo(()=>gatewayNativeWordDiff.annotate(${nativeMemo[2]}(e)),[e]),`;
  if (patched) body = once(body, afterMemo, beforeMemo);
  const row = /className:([\w$]+)\(([\w$]+)\.line,([\w$]+)\[L.kind\]\),/g;
  const renderers = [...body.matchAll(row)];
  if (renderers.length !== 2 || renderers[0][0] !== renderers[1][0]) fail('head/tail row renderers');
  const jsx = body.match(/P\.map\(\(L,F\)=>([\w$]+)\.jsx\("div",\{/);
  if (!jsx || !body.includes(`O.map((L,F)=>${jsx[1]}.jsx("div",{`)) fail('row JSX');
  const beforeRow = `${renderers[0][0]}children:L.text`;
  const afterRow = `${renderers[0][0]}style:gatewayNativeWordDiff.rowStyle(L),children:gatewayNativeWordDiff.render(L,${jsx[1]}.jsx)`;
  if (patched) {
    if (body.split(afterRow).length !== 3) fail('incomplete row patch');
    body = body.replaceAll(afterRow, beforeRow);
  }
  if (body.split(beforeRow).length !== 3) fail('native row children');
  body = once(body, beforeMemo, afterMemo).replaceAll(beforeRow, afterRow);
  return once(source.slice(0, start) + injection + body + source.slice(end), nativeExport, wordExport);
}

// Main owns final content-hash renaming and cross-chunk/HTML reference updates.
export async function patchWordDiff(root = '/opt/harness/node_modules') {
  const candidates = [root, join(root, '@deepseek-ai/dsh-web-frontend')];
  let directory, manifest;
  for (const candidate of candidates) {
    try {
      const data = JSON.parse(await readFile(join(candidate, 'package.json'), 'utf8'));
      if (data.name === '@deepseek-ai/dsh-web-frontend') { directory = candidate; manifest = data; break; }
    } catch (error) { if (error.code !== 'ENOENT') throw error; }
  }
  if (!directory) fail('frontend package not found');
  if (manifest.version !== '0.2.2-rc.1') fail(`version ${manifest.version}`);
  const assets = join(directory, 'dist/assets');
  const names = (await readdir(assets)).filter(name => /^index-.*\.js$/.test(name));
  if (names.length !== 1) fail('ambiguous index asset');
  const path = join(assets, names[0]), source = await readFile(path, 'utf8');
  const updated = patchWordDiffSource(source, manifest.version);
  const reviewDir = join(directory, '../dsh-client-ui-deliverables');
  const reviewManifest = JSON.parse(await readFile(join(reviewDir, 'package.json'), 'utf8'));
  if (reviewManifest.name !== '@deepseek-ai/dsh-client-ui-deliverables') fail('review package');
  const reviewPath = join(reviewDir, 'lib/client.js');
  const reviewSource = await readFile(reviewPath, 'utf8');
  const reviewUpdated = patchWordReviewSource(reviewSource, reviewManifest.version);
  if (updated !== source) await writeFile(path, updated);
  if (reviewUpdated !== reviewSource) await writeFile(reviewPath, reviewUpdated);
  return path;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await patchWordDiff(process.argv[2]);
}
