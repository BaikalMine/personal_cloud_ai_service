import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

// Applied only to a staging copy before build.mjs; rc.5 keeps its old placeholders.
const changes = {
  'ru-plugins.json': { pluginManager: {
    restartNotice: ['Изменение вступит в силу после перезапуска', 'Изменение вступит в силу после перезапуска. {reason}'],
    installDoneRestart: ['Установлен. Будет загружен после перезапуска.', 'Установлен. Будет загружен после перезапуска. {reason}'],
    reasonIncompatibleVersion: ['{plugin} несовместим с DSH {runtime} (требует {peers}). Возможны сбои и потеря данных.', '{plugin} несовместим с DSH {runtime} (требует {requirements}). Возможны сбои и потеря данных.'],
  } },
  'ru-schedules.json': Object.fromEntries(['schedule.catalog', 'schedule.manager'].map(namespace => [namespace, {
    'frequency.cron': ['Cron {expression} ({timeZone})', 'Произвольно ({timeZone})'],
    'frequency.cronLocal': ['Cron {expression}', 'Произвольно'],
  }])),
};

export async function reconcileUi022(ui, modules) {
  const manifest = JSON.parse(await readFile(join(modules, '@deepseek-ai/dsh-client-ui-settings-general/package.json'), 'utf8'));
  if (manifest.version !== '0.2.2-rc.1') throw new Error('Unsupported settings UI version');
  const nativeCss = await readFile(join(modules, '@deepseek-ai/dsh-client-ui-settings-general/lib/client.js'), 'utf8');
  if (!nativeCss.includes('.VOzbGW_options{grid-area:VOzbGW_options;min-width:0;min-height:0;') || !nativeCss.includes('grid-template:\\"VOzbGW_title VOzbGW_header\\"')) throw new Error('Unsupported settings layout');
  const pending = [];
  for (const [name, namespaces] of Object.entries(changes)) {
    const file = join(ui, name), source = await readFile(file, 'utf8'), data = JSON.parse(source);
    let changed = false;
    for (const [namespace, entries] of Object.entries(namespaces)) for (const [key, [oldValue, newValue]] of Object.entries(entries)) {
      if (data[namespace]?.[key] === newValue) continue;
      if (data[namespace]?.[key] !== oldValue) throw new Error(`Unexpected translation ${namespace}.${key}`);
      data[namespace][key] = newValue;
      changed = true;
    }
    if (changed) pending.push([file, JSON.stringify(data, null, 2) + '\n']);
  }
  const cssFile = join(ui, 'mobile.css'), css = await readFile(cssFile, 'utf8');
  const old = 'body[data-gateway-harness] .VOzbGW_content { min-height: 0; }';
  const replacement = 'body[data-gateway-harness] .VOzbGW_options { min-height: 0; }';
  if (css.includes(old)) {
    if (css.split(old).length !== 2) throw new Error('Ambiguous settings CSS');
    pending.push([cssFile, css.replace(old, replacement)]);
  } else if (!css.includes(replacement)) throw new Error('Missing settings CSS anchor');
  for (const [file, content] of pending) await writeFile(file, content);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (!process.argv[2] || !process.argv[3]) throw new Error('Supply staged UI directory and node_modules root');
  await reconcileUi022(process.argv[2], process.argv[3]);
}
