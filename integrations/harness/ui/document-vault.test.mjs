import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, rm, unlink, symlink, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createDocumentVault, destructiveChange } from './document-vault.mjs';

async function fixture(t, options = {}) {
  const temp = await mkdtemp(join(tmpdir(), 'document-vault-')), root = join(temp, 'workspace'), archive = join(temp, 'private');
  await mkdir(root);
  t.after(() => rm(temp, { recursive: true, force: true }));
  return { root, archive, vault: createDocumentVault({ root, archive, ...options }) };
}
test('versions deduplicate and recovery never overwrites the current file', async t => {
  const { root, vault } = await fixture(t), file = join(root, 'draft.md');
  await writeFile(file, 'First complete synthetic version.\r\n');
  const first = await vault.checkpoint(file);
  await vault.checkpoint(file);
  assert.equal((await vault.list(file)).length, 1);
  await writeFile(file, 'Second complete synthetic version.\n');
  await vault.checkpoint(file);
  assert.equal((await vault.list(file)).length, 2);
  const restored = await vault.recover(file, first.version);
  assert.notEqual(restored.path, file);
  assert.equal(await readFile(restored.path, 'utf8'), 'First complete synthetic version.\r\n');
  assert.equal(await readFile(file, 'utf8'), 'Second complete synthetic version.\n');
});
test('mass truncation is blocked after a durable snapshot', async t => {
  const { root, vault } = await fixture(t), file = join(root, 'draft.md'), text = 'Synthetic prose.\n'.repeat(1000);
  await writeFile(file, text);
  await assert.rejects(vault.beforeWrite(file, 'placeholder'), /protected_document_truncation/);
  assert.equal((await vault.list(file)).length, 1);
  assert.equal((await stat(file)).size, Buffer.byteLength(text));
});
test('chapter removal and placeholders are detected even if total output grows', () => {
  const before = '# Chapter 0\n' + 'A'.repeat(4000) + '\n# Chapter 1\n' + 'B'.repeat(10000);
  const after = '# Chapter 0\nplaceholder\n# Chapter 1\n' + 'B'.repeat(20000);
  assert.equal(destructiveChange(before, after), true);
  assert.equal(destructiveChange(before, '# Chapter 1\n' + 'B'.repeat(30000)), true);
  assert.equal(destructiveChange(before, before.replace('AAAA', 'ZZZZ')), false);
});
test('targeted replacement cannot silently remove most of the manuscript', async t => {
  const { root, vault } = await fixture(t), file = join(root, 'draft.md'), section = 'Synthetic paragraph.\n'.repeat(700);
  await writeFile(file, section + 'Remaining tail.\n');
  await assert.rejects(vault.beforeEdit(file, { old_string: section, new_string: 'placeholder\n' }), /protected_document_truncation/);
  assert.equal((await vault.list(file)).length, 1);
});
test('shell checkpoints allow recovering an externally deleted document', async t => {
  const { root, vault } = await fixture(t), file = join(root, 'draft.md');
  await writeFile(file, 'Synthetic recovery reference.\n');
  await vault.checkpointTree();
  const [version] = await vault.list(file);
  await unlink(file);
  const restored = await vault.recover(file, version.version);
  assert.equal((await readFile(restored.path)).length, Buffer.byteLength('Synthetic recovery reference.\n'));
});
test('archive quota fails closed and preserves previously saved versions', async t => {
  const { root, vault } = await fixture(t, { maxVersions: 1 }), file = join(root, 'draft.md');
  await writeFile(file, 'First synthetic version.');
  const first = await vault.checkpoint(file);
  await writeFile(file, 'Second synthetic version.');
  await assert.rejects(vault.checkpoint(file), /document_archive_full/);
  assert.equal((await vault.list(file))[0].version, first.version);
});
test('outside paths are rejected and backups cannot be put inside workspace', async t => {
  const { root, vault } = await fixture(t);
  await assert.rejects(vault.checkpoint('../outside.md'), /document_outside_workspace/);
  assert.throws(() => createDocumentVault({ root, archive: join(root, 'backups') }), /document_archive_inside_workspace/);
});
test('symlink document targets are never followed', { skip: process.platform === 'win32' }, async t => {
  const { root, vault } = await fixture(t), outside = join(root, '..', 'outside.md');
  await writeFile(outside, 'Outside synthetic reference.');
  await symlink(outside, join(root, 'linked.md'));
  await assert.rejects(vault.checkpoint(join(root, 'linked.md')), /document_symlink/);
});
