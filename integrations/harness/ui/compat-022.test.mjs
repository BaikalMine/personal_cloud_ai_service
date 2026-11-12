import test from 'node:test';
import assert from 'node:assert/strict';
import { cp, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import vm from 'node:vm';
import { patchDiffPreview } from '../patch-diff-preview.mjs';
import { enableDocumentWrap } from '../tools/patch-document-wrap.mjs';
import { patchManagedSettings } from './managed-settings.mjs';
import { reconcileUi022 } from './reconcile-ui-022.mjs';
import { createHash } from 'node:crypto';

const exec = promisify(execFile);
const installed = process.env.HARNESS_022_DEEPSEEK_ROOT;
const pkg = (name, file = 'client.js') => join(installed, name, 'lib', file);

test('rc.1 native UI patches retain diff and wrap behavior', async t => {
  if (!installed) return t.skip('requires rc.1 package tree');
  const tool = await readFile(pkg('dsh-client-ui-tool'), 'utf8');
  const diff = patchDiffPreview(tool);
  assert.match(diff, /labels: diffLabels,\s*maxLines: 300,/);
  assert.equal(patchDiffPreview(diff), diff);
  const deliverables = await readFile(pkg('dsh-client-ui-deliverables'), 'utf8');
  const wrapped = enableDocumentWrap(deliverables);
  assert.match(wrapped, /split:\s*true,\s*wrap:\s*true,\s*navigated:\s*revision/);
  assert.match(wrapped, /const wrap = state\?\.wrap !== false/);
  assert.match(wrapped, /split:\s*false,\s*wrap:\s*true,\s*t,\s*retry:/);
  assert.equal(enableDocumentWrap(wrapped), wrapped);
});

test('rc.1 UI build accepts verified settings and emits valid localized client', async t => {
  if (!installed) return t.skip('requires rc.1 package tree');
  const temp = await mkdtemp(join(tmpdir(), 'harness-ui-build-'));
  t.after(() => rm(temp, {recursive:true, force:true}));
  const src = new URL('.', import.meta.url), ui = join(temp, 'ui');
  await cp(src, ui, {recursive:true});
  const settings = pkg('dsh-client-ui-settings');
  const original = await readFile(settings, 'utf8');
  const settingsCopy = join(temp, 'settings.js');
  await writeFile(settingsCopy, original);
  await patchManagedSettings(settingsCopy, 'https://fixture.example');
  assert.equal(await readFile(settingsCopy, 'utf8'), original);
  await exec(process.execPath, [join(ui, 'build.mjs')], {env:{...process.env, HARNESS_SETTINGS_CLIENT_PATH:settingsCopy, HARNESS_MANAGED_SETTINGS_ORIGINS:'https://fixture.example'}});
  const client = await readFile(join(ui, 'client.js'), 'utf8');
  new vm.Script(client);
  assert.ok(client.includes('gateway.files.row.actions'));
  assert.ok(client.includes('gateway.chat.assistant-text'));
  assert.ok(client.includes('textInteractionSource'));
  const layout = await readFile(pkg('dsh-client-ui-layout'), 'utf8');
  assert.ok(layout.includes('pI_x6G_frame') && layout.includes('pI_x6G_centerCol'));
  const attachment = await readFile(pkg('dsh-client-ui-attachment'), 'utf8');
  assert.ok(attachment.includes('gSkjMW_meta') && attachment.includes('gSkjMW_remove'));
});

test('rc.1 reconciliation is scoped, idempotent and checks native selectors', async t => {
  if (!installed) return t.skip('requires rc.1 package tree');
  const temp = await mkdtemp(join(tmpdir(), 'harness-ui-reconcile-'));
  t.after(() => rm(temp, {recursive:true, force:true}));
  const ui = join(temp, 'ui');
  await cp(new URL('.', import.meta.url), ui, {recursive:true});
  await reconcileUi022(ui, dirname(installed));
  const css = await readFile(join(ui, 'mobile.css'), 'utf8');
  assert.ok(!css.includes('.VOzbGW_content'));
  assert.ok(css.includes('.VOzbGW_options { min-height: 0; }'));
  const plugins = JSON.parse(await readFile(join(ui, 'ru-plugins.json'), 'utf8'));
  assert.match(plugins.pluginManager.restartNotice, /\{reason\}/);
  assert.match(plugins.pluginManager.reasonIncompatibleVersion, /\{requirements\}/);
  const schedules = JSON.parse(await readFile(join(ui, 'ru-schedules.json'), 'utf8'));
  assert.ok(!schedules['schedule.catalog']['frequency.cron'].includes('{expression}'));
  const before = await readFile(join(ui, 'ru-plugins.json'), 'utf8');
  await reconcileUi022(ui, dirname(installed));
  assert.equal(await readFile(join(ui, 'ru-plugins.json'), 'utf8'), before);
  assert.equal(await readFile(join(ui, 'mobile.css'), 'utf8'), css);

  let all = '';
  for (const name of await readdir(installed)) {
    try { all += await readFile(join(installed, name, 'lib/client.js'), 'utf8'); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
  }
  const selectors = [...new Set([...css.matchAll(/\.([A-Za-z0-9_]{6}_[A-Za-z0-9_]+)/g)].map(match => match[1]))];
  assert.deepEqual(selectors.filter(selector => !all.includes(selector)), []);

  plugins.pluginManager.restartNotice = 'unexpected';
  await writeFile(join(ui, 'ru-plugins.json'), JSON.stringify(plugins));
  await assert.rejects(reconcileUi022(ui, dirname(installed)), /Unexpected translation/);
  assert.equal(await readFile(join(ui, 'mobile.css'), 'utf8'), css);
});

test('build rejects stale native RU proof after source or bundle changes', async t => {
  const temp = await mkdtemp(join(tmpdir(), 'harness-ru-proof-'));
  t.after(() => rm(temp, {recursive:true, force:true}));
  const ui = join(temp, 'ui'), modules = join(temp, 'node_modules'), bundle = join(modules, '@deepseek-ai', 'fixture', 'lib', 'client.js');
  await cp(new URL('.', import.meta.url), ui, {recursive:true});
  const { mkdir } = await import('node:fs/promises');
  await mkdir(dirname(bundle), {recursive:true});
  await writeFile(bundle, 'synthetic bundle');
  const digest = value => createHash('sha256').update(value).digest('hex');
  const inputs = {};
  for (const name of (await readdir(ui)).filter(name => /^ru-.*\.json$/.test(name)).sort()) inputs[name] = digest(await readFile(join(ui,name)));
  await writeFile(join(ui, '.native-ru-022-proof.json'), JSON.stringify({version:1,inputs,files:{'@deepseek-ai/fixture/lib/client.js':digest('synthetic bundle')}}));
  const build = () => exec(process.execPath, [join(ui,'build.mjs')], {env:{...process.env,HARNESS_NATIVE_MODULES_ROOT:modules}});
  await build();
  assert.match(await readFile(join(ui,'client.js'),'utf8'), /const nativeRuMerged = true;/);
  await writeFile(bundle, 'modified bundle');
  await assert.rejects(build(), /Native RU merge bundle changed/);
  await writeFile(bundle, 'synthetic bundle');
  await writeFile(join(ui,Object.keys(inputs)[0]), '{}');
  await assert.rejects(build(), /Native RU merge input changed/);
});
