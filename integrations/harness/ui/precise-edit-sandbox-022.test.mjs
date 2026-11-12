import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, readdir, realpath, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import test from 'node:test';
import { Context } from '@deepseek-ai/cordis';
import { AgentRegistry } from '@deepseek-ai/dsh-agent';
import * as ObservationPolicy from '@deepseek-ai/dsh-fs-observation-policy';
import { SandboxedFileSystem } from '@deepseek-ai/dsh-fs-sandbox';
import { writableRoots } from '@deepseek-ai/dsh-sandbox';
import { SandboxPolicyService, setSandboxMode } from '@deepseek-ai/dsh-sandbox-policy';
import { SessionStore } from '@deepseek-ai/dsh-session';
import { SessionProjectionRegistry } from '@deepseek-ai/dsh-session-projection';
import { SystemPrompt } from '@deepseek-ai/dsh-system-prompt';
import * as ToolFS from '@deepseek-ai/dsh-tool-fs';
import { NativeToolExecutor } from '@deepseek-ai/dsh-tool-executor-native';
import { ToolRuntime } from '@deepseek-ai/dsh-tools';
import { WorkingDirectoryService } from '@deepseek-ai/dsh-working-directory';
import { createDocumentVault } from './document-vault.mjs';
import { registerPreciseEditTool } from './precise-edit-tool.mjs';

const original = 'Synthetic sandbox document\nalpha\n';
const revised = 'Synthetic sandbox document\nbeta\n';

async function fixture(t, { sessionRoot = 'workspace', register = true } = {}) {
  // Native workspace-write permits /tmp, so root-denial fixtures must live elsewhere.
  const base = process.env.PRECISE_EDIT_SANDBOX_TEST_ROOT ?? process.cwd();
  const home = await realpath(await mkdtemp(join(base, '.synthetic-precise-sandbox-')));
  let ctx;
  const leave = [];
  t.after(async () => {
    try {
      for (const dispose of leave.reverse()) dispose();
      await ctx?.fiber.dispose();
    } finally {
      await rm(home, { recursive: true, force: true });
    }
  });
  const root = join(home, 'workspace'), other = join(home, 'other');
  const archive = join(home, 'private', 'document-versions');
  await mkdir(root);
  await mkdir(other);
  const path = join(root, 'synthetic.md');
  await writeFile(path, original);
  const vault = createDocumentVault({ root, archive });
  const saved = await vault.checkpoint(path);

  ctx = new Context();
  new SystemPrompt(ctx, {});
  const sessions = new SessionStore(ctx);
  await ctx.plugin(SessionProjectionRegistry);
  await ctx.plugin(AgentRegistry);
  await ctx.plugin(SandboxPolicyService, { mode: 'workspace-write', workspaceRoot: root });
  const fs = new SandboxedFileSystem(ctx, { cwd: root, diffBasisMaxBytes: 2 << 20 });
  await ctx.plugin(WorkingDirectoryService, { defaultDirectory: root });
  const tools = new ToolRuntime(ctx);
  tools.setExecutor(new NativeToolExecutor(ctx));
  const agent = {
    ctx: new Context(),
    session: sessions.create('synthetic-sandbox', { meta: { cwd: sessionRoot === 'other' ? other : root } }),
  };
  agent.id = agent.session.id;
  leave.push(ctx.agents.enter(agent, undefined));
  await ctx.plugin(ObservationPolicy);
  await ctx.plugin(ToolFS, { readImage: false, applyPatch: false });
  if (register) await registerPreciseEditTool(ctx, { root, archive, vault });

  let sequence = 0;
  async function execute(name, args) {
    const batch = await tools.execute({
      agent,
      signal: AbortSignal.timeout(5000),
      calls: [{ type: 'tool-call', id: `synthetic-sandbox-${++sequence}`, name, arguments: JSON.stringify(args) }],
    });
    assert.equal(batch.results.length, 1);
    return batch.results[0].result;
  }
  async function read() {
    const result = await execute('read', { file_path: path });
    assert.equal(result.isError, false, JSON.stringify(result));
  }
  const editArgs = { file_path: path, edits: [{ old_text: 'alpha', new_text: 'beta' }] };
  const recoverArgs = { action: 'recover', file_path: path, version: saved.version };
  return { ctx, fs, root, other, archive, path, vault, saved, agent, tools, execute, read, editArgs, recoverArgs };
}

function assertDenied(result, pattern = /FS_SANDBOX_DENIED|file access denied/) {
  assert.equal(result.isError, true, JSON.stringify(result));
  assert.match(JSON.stringify(result), pattern);
}

test('session read-only override blocks precise writes and recovery under a writable deployment', async t => {
  const f = await fixture(t);
  const policy = f.ctx.get('sandboxPolicy');
  assert.equal(policy.resolve().mode, 'workspace-write');
  assert.equal(f.fs.sandboxMode, 'workspace-write');
  setSandboxMode(f.agent.session, 'read-only');
  assert.equal(policy.resolve({ session: f.agent.session }).mode, 'read-only');
  await f.read();
  const files = await readdir(f.root);
  assertDenied(await f.execute('edit_precise', f.editArgs));
  assertDenied(await f.execute('edit_precise_versions', f.recoverArgs));
  assert.equal(await readFile(f.path, 'utf8'), original);
  assert.deepEqual(await readdir(f.root), files);

  setSandboxMode(f.agent.session, 'workspace-write');
  const allowed = await f.execute('edit_precise', f.editArgs);
  assert.equal(allowed.isError, false, JSON.stringify(allowed));
  assert.equal(allowed.value.applied, true);
  assert.equal(allowed.value.verified, true);
  assert.equal(await readFile(f.path, 'utf8'), revised);
});

test('workspace-write allows precise edits and separate-file recovery in the session root', async t => {
  const f = await fixture(t);
  setSandboxMode(f.agent.session, 'workspace-write');
  const policy = f.ctx.get('sandboxPolicy').resolve({ session: f.agent.session });
  assert.equal(policy.workspaceRoot, f.root);
  await f.read();
  const result = await f.execute('edit_precise', f.editArgs);
  assert.equal(result.isError, false, JSON.stringify(result));
  assert.equal(result.value.applied, true);
  assert.equal(result.value.verified, true);
  assert.equal(result.value.after_version_saved, true);
  assert.equal(await readFile(f.path, 'utf8'), revised);
  const recovered = await f.execute('edit_precise_versions', f.recoverArgs);
  assert.equal(recovered.isError, false, JSON.stringify(recovered));
  assert.equal(recovered.value.original_modified, false);
  assert.notEqual(recovered.value.path, f.path);
  const recoveredTarget = await f.fs.resolve(recovered.value.path);
  assert.equal(f.fs.contains(await f.fs.resolve(f.root), recoveredTarget), true);
  assert.equal(await readFile(recovered.value.path, 'utf8'), original);
  assert.equal(await readFile(f.path, 'utf8'), revised);
});

test('changing execution cwd does not widen the original session sandbox root', async t => {
  const f = await fixture(t, { sessionRoot: 'other' });
  setSandboxMode(f.agent.session, 'workspace-write');
  await f.ctx.workingDirectory.set(f.agent, f.root, AbortSignal.timeout(5000));
  assert.equal(await f.ctx.workingDirectory.ensure(f.agent), f.root);
  const policy = f.ctx.get('sandboxPolicy').resolve({ session: f.agent.session });
  assert.equal(policy.workspaceRoot, f.other);
  const target = await f.fs.resolve(f.path);
  for (const root of writableRoots(policy)) {
    assert.equal(f.fs.contains(await f.fs.resolve(root), target), false,
      'Set PRECISE_EDIT_SANDBOX_TEST_ROOT outside native temporary writable roots');
  }
  await f.read();
  const files = await readdir(f.root);
  const outsideRoot = /FS_SANDBOX_DENIED|file access denied|precise_edit_outside_workspace/;
  assertDenied(await f.execute('edit_precise', f.editArgs), outsideRoot);
  assertDenied(await f.execute('edit_precise_versions', f.recoverArgs), outsideRoot);
  const info = await f.fs.stat(target);
  await assert.rejects(f.fs.writeText(target, revised,
    { kind: 'replaceIfVersion', version: info.version }, AbortSignal.timeout(5000), policy),
  error => error.code === 'FS_SANDBOX_DENIED');
  assert.equal(await readFile(f.path, 'utf8'), original);
  assert.deepEqual(await readdir(f.root), files);
});

test('a confining real filesystem without an available policy fails registration closed', async t => {
  const ctx = new Context();
  t.after(() => ctx.fiber.dispose());
  new SystemPrompt(ctx, {});
  const tools = new ToolRuntime(ctx);
  // The provider needs the capability at construction; remove this public service
  // provision before registration to simulate a missing composition dependency.
  const removePolicy = ctx.provide('sandboxPolicy', {
    defaultMode: 'workspace-write',
    resolve: () => { throw new Error('missing-policy fixture must not resolve policy'); },
  });
  const fs = new SandboxedFileSystem(ctx, { cwd: process.cwd(), diffBasisMaxBytes: 1024 });
  assert.equal(fs.sandboxMode, 'workspace-write');
  await removePolicy();
  assert.equal(ctx.get('sandboxPolicy'), undefined);
  await assert.rejects(registerPreciseEditTool(ctx), /precise_edit_sandbox_policy_unavailable/);
  assert.equal(tools.get('edit_precise'), undefined);
  assert.equal(tools.get('edit_precise_versions'), undefined);
});

test('precise tools reject unadvertised escalation fields without writes or recovery', async t => {
  const f = await fixture(t);
  await f.read();
  const files = await readdir(f.root);
  for (const [name, args] of [['edit_precise', f.editArgs], ['edit_precise_versions', f.recoverArgs]]) {
    const parameters = f.tools.get(name).parameters.properties;
    assert.equal(Object.hasOwn(parameters, 'sandbox_permissions'), false);
    assert.equal(Object.hasOwn(parameters, 'justification'), false);
    for (const fields of [
      { sandbox_permissions: 'danger-full-access' },
      { justification: 'Synthetic request for wider access' },
      { sandbox_permissions: 'workspace-write', justification: '' },
    ]) {
      const result = await f.execute(name, { ...args, ...fields });
      assert.equal(result.isError, true, JSON.stringify(result));
      assert.match(JSON.stringify(result), /precise_edit_invalid_arguments|unknown|unexpected|additional/i);
    }
  }
  assert.equal(await readFile(f.path, 'utf8'), original);
  assert.deepEqual(await readdir(f.root), files);
});
