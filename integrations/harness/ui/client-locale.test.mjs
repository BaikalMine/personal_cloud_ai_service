import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';

test('native RU uses upstream dictionaries while gateway and managed plugins retain translations', async () => {
  const source = await readFile(new URL('./client.template.js', import.meta.url), 'utf8');
  const start = source.indexOf('      const nativeRussian ='), end = source.indexOf('      let saved;', start);
  assert.ok(start >= 0 && end > start);
  const register = vm.runInNewContext(`(ctx, dictionaries, nativeRuMerged) => { ${source.slice(start, end)} }`);
  const dictionaries = { approval:{x:'a'}, gatewayFiles:{x:'f'}, mnemon:{x:'m'}, 'dsh-context':{x:'c'}, 'skills-manager':{x:'s'} };
  for (const native of [true, false]) {
    const added = [], registrations = [];
    const ctx = { effect:fn=>fn(), locale:{ getSnapshot:()=>({locales:native ? [{id:'ru'}] : [{id:'en'}]}),
      addLanguage:language=>added.push(language.id), register:namespace=>registrations.push(namespace) } };
    if (native) assert.throws(() => register(ctx, dictionaries, false), /Native RU overrides were not verified/);
    register(ctx, dictionaries, true);
    assert.deepEqual(added, native ? [] : ['ru']);
    assert.deepEqual(registrations, native ? ['gatewayFiles','mnemon','dsh-context','skills-manager'] : Object.keys(dictionaries));
  }
});
