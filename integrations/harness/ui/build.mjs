import { readFile, readdir, writeFile } from 'node:fs/promises';
import { generatedImagePattern } from './generated-image-path.mjs';
import { createHash } from 'node:crypto';
import { join } from 'node:path';
import { patchManagedSettings } from './managed-settings.mjs';
const root = new URL('./', import.meta.url);
const hash = value => createHash('sha256').update(value).digest('hex');
let nativeRuMerged = false;
let proof;
try {
  proof = JSON.parse(await readFile(new URL('.native-ru-022-proof.json', root), 'utf8'));
} catch (error) { if (error.code !== 'ENOENT') throw error; }
if (proof) {
  const names = (await readdir(root)).filter(name => /^ru-.*\.json$/.test(name)).sort();
  if (proof.version !== 1 || JSON.stringify(Object.keys(proof.inputs).sort()) !== JSON.stringify(names) || !Object.keys(proof.files).length) throw new Error('Invalid native RU merge proof');
  for (const name of names) if (hash(await readFile(new URL(name, root))) !== proof.inputs[name]) throw new Error(`Native RU merge input changed: ${name}`);
  for (const [file, expected] of Object.entries(proof.files)) {
    if (!/^@deepseek-ai\/[^/]+\/lib\/client\.js$/.test(file) || hash(await readFile(join(process.env.HARNESS_NATIVE_MODULES_ROOT || '/opt/harness/node_modules', file))) !== expected) throw new Error(`Native RU merge bundle changed: ${file}`);
  }
  nativeRuMerged = true;
}
const dictionaries = {};
for (const filename of (await readdir(root)).filter(name => /^ru-.*\.json$/.test(name)).sort()) {
  for (const [namespace, entries] of Object.entries(JSON.parse(await readFile(new URL(filename, root), 'utf8')))) {
    dictionaries[namespace] ??= {};
    for (const [key, value] of Object.entries(entries)) {
      if (typeof value !== 'string' || key in dictionaries[namespace]) throw new Error(`Invalid or duplicate translation: ${namespace}.${key}`);
      dictionaries[namespace][key] = value;
    }
  }
}
const template = await readFile(new URL('client.template.js', root), 'utf8');
const css = await readFile(new URL('mobile.css', root), 'utf8');
const files = await readFile(new URL('files-client.js', root), 'utf8');
const upload = await readFile(new URL('workspace-upload-client.mjs', root), 'utf8');
const meter = (await readFile(new URL('upload-meter.mjs', root), 'utf8')).replaceAll('export function ', 'function ');
const images = await readFile(new URL('ultra-client-ui.js', root), 'utf8');
const formatImport = "import { isGeneratedImagePath } from './generated-image-path.mjs';";
let format = await readFile(new URL('assistant-format.mjs', root), 'utf8');
if(format.split(formatImport).length!==2)throw new Error('Unsupported generated image matcher import');
format = format.replace(formatImport, `const generatedImagePathPattern = new RegExp(${JSON.stringify(generatedImagePattern)}, 'u');\nconst isGeneratedImagePath = path => typeof path === 'string' && generatedImagePathPattern.test(path);`);
if(format.split('export function reconcilePresentedText').length!==2)throw new Error('Unsupported assistant text module shape');
const assistant = await readFile(new URL('assistant-client-ui.js', root), 'utf8');
const compact = await readFile(new URL('compact-feedback.mjs', root), 'utf8');
const chatMemorySummary = await readFile(new URL('chat-memory-summary.mjs', root), 'utf8');
const reviewClient = await readFile(new URL('chat-memory-review-client.js', root), 'utf8').catch(error=>{if(error.code!=='ENOENT')throw error;return '';});
const chatMemoryClient = reviewClient+'\n'+await readFile(new URL('chat-memory-client.js', root), 'utf8');
const composerLayout = (await readFile(new URL('composer-layout.mjs', root), 'utf8')).replaceAll('export function ', 'function ');
if(template.split('/* COMPOSER_LAYOUT */').length!==2)throw new Error('Unsupported composer layout insertion boundary');
if(compact.split('export function registerCompactFeedback').length!==2)throw new Error('Unsupported compact feedback module shape');
const source = template.replace('/* NATIVE_RU_MERGED */', () => String(nativeRuMerged)).replace('/* DICTIONARIES */', () => JSON.stringify(dictionaries)).replace('/* STYLES */', () => JSON.stringify(css)).replace('/* WORKSPACE_FILES */', () => files).replace('/* ULTRA_IMAGES */', () => images).replace('/* ASSISTANT_FORMAT */',()=>format.replace('export function reconcilePresentedText','function reconcilePresentedText')).replace('/* ASSISTANT_UI */',()=>assistant).replace('/* COMPACT_FEEDBACK */',()=>compact.replace('export function registerCompactFeedback','function registerCompactFeedback')).replace('/* CHAT_MEMORY */',()=>chatMemorySummary.replace('export function chatMemorySummary','function chatMemorySummary') + '\n' + chatMemoryClient);
await writeFile(new URL('client.js', root), source.replace('/* COMPOSER_LAYOUT */',()=>composerLayout).replace('function registerWorkspaceFilesUI', meter + '\n' + upload.replace('export function uploadWorkspaceFile','function uploadWorkspaceFile') + '\nfunction registerWorkspaceFilesUI'));
console.log(`Built Harness UI: ${Object.keys(dictionaries).length} namespaces, ${Object.values(dictionaries).reduce((n, d) => n + Object.keys(d).length, 0)} translations`);
if (process.env.HARNESS_SETTINGS_CLIENT_PATH) {
  await patchManagedSettings(process.env.HARNESS_SETTINGS_CLIENT_PATH, process.env.HARNESS_MANAGED_SETTINGS_ORIGINS);
}
