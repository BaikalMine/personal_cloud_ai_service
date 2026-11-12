import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { resolve, relative, isAbsolute, sep } from 'node:path';
import { registerPreciseEditTool, PRECISE_EDIT_CONTEXT } from './precise-edit-tool.mjs';

const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const fault = code => Object.assign(new Error(code), { code });
async function fixture(text = 'A quietly written draft.\r\nSecond unique line.\n', options = {}) {
  const root = resolve('synthetic-workspace'), path = resolve(root, 'draft.md');
  let bytes = Buffer.from(text), revision = 'v1', observed = 'v1', writes = 0, checkpoints = 0;
  const saved = new Map(), tools = new Map(), events = [], contexts = [];
  const controller = new AbortController();
  const fs = {
    sandboxMode: options.sandboxMode,
    resolve: async (path, opts = {}) => ({ targetKey: resolve(opts.cwd || root, path), displayPath: resolve(opts.cwd || root, path) }),
    contains: (parent, child) => { const r = relative(parent.targetKey, child.targetKey); return !isAbsolute(r) && r !== '..' && !r.startsWith('..' + sep); },
    processPath: target => target.targetKey,
    stat: async target => ({ type: target.targetKey === path ? 'file' : 'directory', version: revision, size: bytes.length }),
    readBytes: async () => Buffer.from(bytes),
    writeText: async (_target, text, intent, signal, policy) => {
      assert.equal(intent.kind, 'replaceIfVersion');
      assert.equal(intent.version, revision);
      signal.throwIfAborted();
      if (options.sandboxMode) {
        assert.equal(policy?.mode, options.policyMode || 'read-only');
        if (policy.mode === 'read-only') throw fault('FS_SANDBOX_DENIED');
      }
      if (options.rejectWrite) throw fault('FS_PERMISSION_DENIED');
      bytes = Buffer.from(text); revision = 'v2'; writes++;
      if (options.lostAcknowledgement) throw fault('FS_IO_ERROR');
      if (options.externalAfterWrite) { bytes = Buffer.from('Synthetic external content'); revision = 'external'; }
      return { version: 'v2', before: text, after: text };
    },
  };
  const vault = {
    supported: name => name.endsWith('.md'),
    checkpoint: async () => {
      checkpoints++;
      if (options.rejectCheckpoint === checkpoints) throw fault('document_archive_full');
      const snapshot = Buffer.from(bytes), version = sha(snapshot);
      saved.set(version, snapshot);
      if (options.abortOnCheckpoint) controller.abort();
      return { version, bytes: snapshot, path };
    },
    list: async () => [...saved].map(([version, bytes]) => ({ version, bytes: bytes.length })),
    recover: async (_path, version) => ({ path: path + '.restored.md', version, original_modified: false }),
  };
  const ctx = {
    fs, get: () => options.missingPolicy ? undefined : { resolve: () => ({ mode: options.policyMode || 'read-only', workspaceRoot: root }) },
    workingDirectory: { defaultDirectory: root, ensure: async () => options.cwd || root },
    tools: { register: tool => tools.set(tool.name, tool) },
    systemPrompt: { context: entry => contexts.push(entry) },
    waterfall: async name => {
      if (name === 'fs/edit-intent') { if (!observed) throw fault('FS_NOT_OBSERVED'); return { version: observed }; }
      if (name === 'fs/write-intent') return options.badWriteIntent ? { version: observed } : { kind: 'replaceIfVersion', version: observed };
      throw Error('Unexpected event');
    },
    emit: (_name, _target, value) => { events.push(value); observed = value.version; },
  };
  await registerPreciseEditTool(ctx, { root, vault, defineTool: value => value });
  const args = { file_path: path, edits: [{ old_text: 'quietly', new_text: 'silently' }] };
  const exec = { agent: { session: { id: 'synthetic', header: { cwd: root } } }, name: 'edit_precise', arguments: args, signal: controller.signal };
  return { ctx, tools, path, root, saved, args, exec, contexts, events,
    run: (argsOverride = {}, execution = exec) => tools.get('edit_precise').execute({ ...args, ...argsOverride }, execution),
    bytes: () => bytes, writes: () => writes, checkpoints: () => checkpoints,
    observed: value => { observed = value; },
    external: text => { bytes = Buffer.from(text); revision = 'external'; },
  };
}

test('short batch edits preserve untouched bytes and retain original and final snapshots', async () => {
  const f = await fixture();
  const before = f.bytes();
  const result = await f.run({ edits: [{ old_text: 'quietly', new_text: 'silently' }, { old_text: 'Second', new_text: 'Another', start_line: 2 }] });
  assert.equal(f.bytes().toString(), 'A silently written draft.\r\nAnother unique line.\n');
  assert.equal(result.applied, true); assert.equal(result.verified, true);
  assert.equal(result.after_version_saved, true); assert.equal(f.writes(), 1);
  assert.deepEqual(f.saved.get(result.before_version), before);
  assert.deepEqual(f.saved.get(result.after_sha256), f.bytes());
  assert.equal(f.events.length, 1);
});

test('dry run, ambiguity and a failing later edit never publish or create snapshots', async () => {
  const f = await fixture();
  const before = f.bytes();
  assert.equal((await f.run({ dry_run: true })).applied, false);
  await assert.rejects(f.run({ edits: [...f.args.edits, { old_text: 'missing', new_text: 'no' }] }), /precise_edit_not_found/);
  await assert.rejects(f.run({ edits: [{ old_text: 'i', new_text: 'x' }] }), /precise_edit_ambiguous/);
  assert.deepEqual(f.bytes(), before); assert.equal(f.writes(), 0); assert.equal(f.checkpoints(), 0);
});

test('whole-line rewrite requires a preview hash and preserves adjacent bytes and both versions', async () => {
  const f = await fixture('\ufeffFirst untouched.\r\nOld line  \n\tOld line two.\r\nLast untouched.');
  const before = f.bytes();
  const edits = [{ mode:'replace_lines', start_line:2, end_line:3, new_text:'New line\nNew line two.' }];
  await assert.rejects(f.run({ edits }), /precise_edit_range_requires_preview_hash/);
  const preview = await f.run({ edits, dry_run:true });
  assert.equal(preview.applied, false);
  assert.equal(f.checkpoints(), 0);
  assert.deepEqual(f.bytes(), before);
  const result = await f.run({ edits, expected_sha256:preview.before_sha256 });
  assert.equal(result.applied, true);
  assert.equal(result.verified, true);
  assert.equal(result.after_version_saved, true);
  assert.deepEqual(result.diffs[0], {path:f.path,oldText:'Old line  \n\tOld line two.',newText:edits[0].new_text});
  assert.equal(f.bytes().toString(), '\ufeffFirst untouched.\r\nNew line\nNew line two.\r\nLast untouched.');
  assert.deepEqual(f.saved.get(result.before_version), before);
});

test('re-reading a changed file does not authorize a range preview from its old revision', async () => {
  const f = await fixture();
  const edits = [{ mode:'replace_lines', start_line:1, end_line:1, new_text:'Another synthetic line.' }];
  const preview = await f.run({ edits, dry_run:true });
  f.external('Inserted line.\n' + f.bytes().toString());
  f.observed('external');
  await assert.rejects(f.run({ edits, expected_sha256:preview.before_sha256 }), /FS_STALE_VERSION/);
  assert.equal(f.writes(), 0);
  assert.equal(f.checkpoints(), 0);
});

test('unread, stale, missing guard kind, expected hash and foreign paths fail closed', async () => {
  const f = await fixture();
  f.observed(undefined); await assert.rejects(f.run(), /FS_NOT_OBSERVED/);
  f.observed('stale'); await assert.rejects(f.run(), /FS_STALE_VERSION/);
  f.observed('v1'); await assert.rejects(f.run({ expected_sha256: '0'.repeat(64) }), /FS_STALE_VERSION/);
  await assert.rejects(f.run({ file_path: resolve(f.root, '..', 'private.md') }), /outside_workspace/);
  const bad = await fixture(undefined, { badWriteIntent: true });
  await assert.rejects(bad.run(), /FS_STALE_VERSION/); assert.equal(bad.writes(), 0);
  assert.equal(f.writes(), 0);
});

test('snapshot failure blocks publication; a later snapshot failure reports the committed edit honestly', async () => {
  const beforeFailure = await fixture(undefined, { rejectCheckpoint: 1 });
  await assert.rejects(beforeFailure.run(), /document_archive_full/); assert.equal(beforeFailure.writes(), 0);
  const afterFailure = await fixture(undefined, { rejectCheckpoint: 2 });
  const result = await afterFailure.run();
  assert.equal(result.applied, true); assert.equal(result.verified, true); assert.equal(result.after_version_saved, false);
  assert.match(result.warning, /original_version_saved/); assert.ok(afterFailure.saved.has(result.before_version));
});

test('cancellation before publication preserves the file, and permission denial never emits a new observation', async () => {
  const aborted = await fixture(undefined, { abortOnCheckpoint: true });
  await assert.rejects(aborted.run(), /abort/i); assert.equal(aborted.writes(), 0);
  const denied = await fixture(undefined, { rejectWrite: true });
  await assert.rejects(denied.run(), /FS_PERMISSION_DENIED/); assert.equal(denied.writes(), 0); assert.equal(denied.events.length, 0);
});

test('a lost publication acknowledgement is reconciled once, never replayed', async () => {
  const f = await fixture(undefined, { lostAcknowledgement: true });
  const result = await f.run();
  assert.equal(result.applied, true); assert.equal(result.verified, true); assert.equal(f.writes(), 1);
  assert.match(result.warning, /confirmation_lost/); assert.equal(result.after_version_saved, true);
});

test('a post-write external change is not overwritten or certified as the planned result', async () => {
  const f = await fixture(undefined, { externalAfterWrite: true });
  const result = await f.run();
  assert.equal(result.applied, true); assert.equal(result.verified, false); assert.equal(f.writes(), 1);
  assert.match(result.warning, /changed_after_publication/); assert.equal(f.events.length, 0);
  assert.equal(f.bytes().toString(), 'Synthetic external content');
});

test('model output and stored UI metadata contain only bounded edits, not the untouched manuscript', async () => {
  const prefix = 'Synthetic unchanged paragraph. '.repeat(20000);
  const f = await fixture(prefix + 'quietly\n');
  const result = await f.run();
  const tool = f.tools.get('edit_precise');
  const content = JSON.stringify(tool.output.render(f.args, result));
  const metadata = JSON.stringify(tool.output.presentationMeta(f.args, result));
  assert.ok(content.length < 4096); assert.ok(metadata.length < 1024);
  assert.ok(JSON.stringify(result).length < 8192);
  assert.ok(!content.includes('Synthetic unchanged paragraph'));
  assert.ok(!metadata.includes('Synthetic unchanged paragraph'));
  assert.ok(JSON.stringify(f.args).length * 100 < Buffer.byteLength(prefix));
});

test('session sandbox policy reaches the backend, and read-only recovery is also denied', async () => {
  const f = await fixture(undefined, { sandboxMode: 'workspace-write', policyMode: 'read-only' });
  await assert.rejects(f.run(), /FS_SANDBOX_DENIED/); assert.equal(f.writes(), 0);
  await assert.rejects(f.tools.get('edit_precise_versions').execute({ action: 'recover', file_path: f.path, version: '0'.repeat(64) }, f.exec), /FS_SANDBOX_DENIED/);
  await assert.rejects(fixture(undefined, { sandboxMode: 'workspace-write', missingPolicy: true }), /sandbox_policy_unavailable/);
});

test('invalid and escalation fields are rejected and guidance favors short original-snapshot batches', async () => {
  const f = await fixture();
  for (const args of [{ sandbox_permissions: 'danger-full-access' }, { expected_sha256: 'bad' }, { dry_run: 'yes' }, { file_path: '' }]) {
    await assert.rejects(f.run(args), /precise_edit_invalid/);
  }
  assert.equal(f.writes(), 0);
  assert.match(PRECISE_EDIT_CONTEXT, /ORIGINAL file snapshot/);
  assert.match(PRECISE_EDIT_CONTEXT, /BEFORE writing/);
  assert.match(PRECISE_EDIT_CONTEXT, /do not repeat an unchanged paragraph/);
});
