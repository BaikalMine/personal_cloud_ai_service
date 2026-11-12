import assert from 'node:assert/strict';
import { before, test } from 'node:test';
import { mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { patchWordDiff, patchWordDiffSource } from './patch-word-diff.mjs';
import { installedWordDiffFixture } from './word-diff-fixture.mjs';
import { unpatchWordReviewSource } from './patch-word-review.mjs';

const strip = text => text.replace(/\/\* gateway-native-word-diff:0\.2\.2-rc\.1 \*\/[\s\S]*?\/\* end-gateway-native-word-diff \*\/\n/, '')
  .replace(/gatewayNativeWordDiff\.annotate\(([\w$]+)\(e\)\)/, '$1(e)')
  .replaceAll('style:gatewayNativeWordDiff.rowStyle(L),', '')
  .replace('gatewayWordDiff:gatewayNativeWordDiff,', '')
  .replace(/gatewayNativeWordDiff\.render\(L,[\w$]+\.jsx\)/g, 'L.text');
let source, version, installed, reviewSource, reviewVersion;
before(() => {
  ({ source, version, reviewSource, reviewVersion } = installedWordDiffFixture());
  reviewSource = unpatchWordReviewSource(reviewSource);
  installed = source;
  source = strip(source);
});

test('installed rc.1 patch is idempotent, parses, and only changes the DiffBlock renderer', () => {
  const after = patchWordDiffSource(source, version);
  assert.equal(patchWordDiffSource(installed, version), after);
  assert.equal(patchWordDiffSource(after, version), after);
  const syntax = spawnSync(process.execPath, ['--check', '--input-type=module'], { input: after, encoding: 'utf8' });
  assert.equal(syntax.status, 0, syntax.stderr);
  assert.equal(after.split('children:gatewayNativeWordDiff.render(L,').length, 3);
  assert.equal(after.split('style:gatewayNativeWordDiff.rowStyle(L)').length, 3);
  assert.match(after, /gatewayWordDiff:gatewayNativeWordDiff,DiffBlock:/);
  assert.equal(strip(after), source);
});

test('unknown versions, broken APIs, ambiguous renderers and partial patches fail closed', () => {
  assert.throws(() => patchWordDiffSource(source, '0.2.2'), /version/);
  assert.throws(() => patchWordDiffSource(source.replace('tokenize(t,r){return Array.from(t)}', 'tokenize(t,r){return []}')), /jsdiff API/);
  const after = patchWordDiffSource(source);
  assert.throws(() => patchWordDiffSource(after.replace('style:gatewayNativeWordDiff.rowStyle(L),', '')), /incomplete row patch/);
  assert.throws(() => patchWordDiffSource(after.replace('Math.min(12, remaining)', 'Math.min(13, remaining)')), /anchor/);
  assert.throws(() => patchWordDiffSource(source + source), /jsdiff core/);
  assert.throws(() => patchWordDiffSource(source + 'gatewayNativeWordDiff'), /partial patch/);
});

test('CLI helper accepts node_modules or frontend package, validates before writing', async t => {
  const root = await mkdtemp(join(tmpdir(), 'harness-word-diff-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const directory = join(root, '@deepseek-ai/dsh-web-frontend');
  await mkdir(join(directory, 'dist/assets'), { recursive: true });
  const manifest = join(directory, 'package.json'), file = join(directory, 'dist/assets/index-synthetic.js');
  await writeFile(manifest, JSON.stringify({ name: '@deepseek-ai/dsh-web-frontend', version }));
  await writeFile(file, source);
  const reviewDir = join(root, '@deepseek-ai/dsh-client-ui-deliverables');
  await mkdir(join(reviewDir, 'lib'), { recursive: true });
  const reviewFile = join(reviewDir, 'lib/client.js');
  await writeFile(join(reviewDir, 'package.json'), JSON.stringify({ name: '@deepseek-ai/dsh-client-ui-deliverables', version: reviewVersion }));
  await writeFile(reviewFile, reviewSource.replace('function DiffText', 'function UnsupportedText'));
  await assert.rejects(patchWordDiff(root), /Unsupported review word diff/);
  assert.equal(await readFile(file, 'utf8'), source, 'validate both targets before either is written');
  await writeFile(reviewFile, reviewSource);
  assert.equal(await patchWordDiff(root), file);
  const after = await readFile(file, 'utf8');
  const reviewAfter = await readFile(reviewFile, 'utf8');
  assert.match(reviewAfter, /gatewayReviewWordDiff/);
  await patchWordDiff(directory);
  assert.equal(await readFile(file, 'utf8'), after);
  assert.equal(await readFile(reviewFile, 'utf8'), reviewAfter);
  await writeFile(manifest, JSON.stringify({ name: '@deepseek-ai/dsh-web-frontend', version: 'bad' }));
  await assert.rejects(patchWordDiff(root), /version/);
  assert.equal(await readFile(file, 'utf8'), after);
});
