import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { registerDocumentSafety } from './document-safety.mjs';

test('native edit and shell hooks create versions, and recover creates a separate file', async t => {
  const temp = await mkdtemp(join(tmpdir(), 'document-hook-')), root = join(temp, 'workspace'), home = join(temp, 'private');
  await mkdir(root);
  const saved = { ...process.env }, effects = [], hooks = new Map(), tools = new Map();
  t.after(async () => {
    for (const dispose of effects) await dispose();
    for (const key of ['HARNESS_DOCUMENT_PROTECTION', 'HARNESS_USER_MODE', 'HARNESS_WORKSPACE', 'DSH_HOME']) {
      if (saved[key] === undefined) delete process.env[key]; else process.env[key] = saved[key];
    }
    await rm(temp, { recursive: true, force: true });
  });
  Object.assign(process.env, { HARNESS_DOCUMENT_PROTECTION: 'true', HARNESS_USER_MODE: 'false', HARNESS_WORKSPACE: root, DSH_HOME: home });
  const file = join(root, 'draft.md'), original = 'Synthetic durable draft.\n'.repeat(700);
  await writeFile(file, original);
  const ctx = {
    systemPrompt: { context() {} }, fs: { processPath: target => target.path }, logger: { warn() {} },
    on: (name, callback) => hooks.set(name, callback),
    tools: { register: definition => tools.set(definition.name, definition) },
    effect: factory => effects.push(factory()),
  };
  let sharedVault;
  const registration = await registerDocumentSafety(ctx, { onVault: vault => { sharedVault = vault; } });
  assert.equal(registration, undefined, 'Cordis apply must not return the vault as an invalid effect');
  assert.equal(typeof sharedVault?.checkpoint, 'function');
  const dryFile = join(root, 'synthetic-dry.md');
  await writeFile(dryFile, 'Synthetic unchanged dry-run document.\n');
  const editIntent = { version: 'synthetic-version' };
  assert.equal(await hooks.get('fs/edit-intent')({ path: dryFile }, { name: 'edit_precise', arguments: { dry_run: true } }, async () => editIntent), editIntent);
  assert.equal((await sharedVault.list(dryFile)).length, 0, 'precise dry-run must not create archive entries');
  await assert.rejects(hooks.get('fs/write-intent')({ path: file }, { arguments: { content: 'placeholder' } }, async () => undefined), /protected_document_truncation/);
  const versionTool = tools.get('document_versions');
  const before = await versionTool.execute({ action: 'list', path: file });
  assert.equal(before.versions.length, 1);
  const gate = { kind: 'allow' };
  assert.equal(await hooks.get('tools/pre-execute')({ name: 'bash' }, async () => gate), gate);
  await writeFile(file, 'Changed by synthetic shell.\n');
  await hooks.get('tools/post-execute')({ name: 'bash' }, {}, async () => ({ kind: 'accept' }));
  assert.equal((await versionTool.execute({ action: 'list', path: file })).versions.length, 2);
  const restored = await versionTool.execute({ action: 'recover', path: file, version: before.versions[0].version });
  assert.notEqual(restored.path, file);
  assert.equal(restored.bytes, Buffer.byteLength(original));
});
