import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, dirname, join } from 'node:path';
import test from 'node:test';
import { Context } from '@deepseek-ai/cordis';
import { AgentRegistry } from '@deepseek-ai/dsh-agent';
import { LocalFileSystem } from '@deepseek-ai/dsh-fs-local';
import * as ObservationPolicy from '@deepseek-ai/dsh-fs-observation-policy';
import { SessionStore } from '@deepseek-ai/dsh-session';
import { SessionProjectionRegistry } from '@deepseek-ai/dsh-session-projection';
import { SystemPrompt } from '@deepseek-ai/dsh-system-prompt';
import * as nativeToolFS from '@deepseek-ai/dsh-tool-fs';
import { NativeToolExecutor } from '@deepseek-ai/dsh-tool-executor-native';
import { ToolRuntime } from '@deepseek-ai/dsh-tools';
import { WorkingDirectoryService } from '@deepseek-ai/dsh-working-directory';
import { registerDocumentSafety } from './document-safety.mjs';
import { createDocumentVault } from './document-vault.mjs';
import { registerPreciseEditTool } from './precise-edit-tool.mjs';

const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const replacement = (old_text, new_text, selector = {}) => ({ old_text, new_text, ...selector });

function successful(result) {
  assert.equal(result?.isError, false, JSON.stringify(result));
  assert.equal(result.content.length, 1);
  assert.equal(result.content[0].type, 'text');
  return JSON.parse(result.content[0].text);
}

function failed(result, code) {
  assert.equal(result?.isError, true, JSON.stringify(result));
  assert.match(JSON.stringify(result), code);
}

async function snapshot(directory) {
  const rows = [];
  async function visit(path, relative = '') {
    let entries;
    try { entries = await readdir(path, { withFileTypes: true }); }
    catch (error) { if (error.code === 'ENOENT') return; throw error; }
    for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
      const next = join(path, entry.name), name = join(relative, entry.name);
      assert.equal(entry.isSymbolicLink(), false, 'only synthetic regular files and directories are allowed');
      if (entry.isDirectory()) {
        rows.push({ directory: name });
        await visit(next, name);
      } else {
        assert(entry.isFile());
        const bytes = await readFile(next), info = await stat(next);
        rows.push({ file: name, bytes: bytes.toString('base64'), mtime: info.mtimeMs });
      }
    }
  }
  await visit(directory);
  return rows;
}

function verifyUnchanged(before, after, edits) {
  const spans = edits.map(edit => {
    const old = Buffer.from(edit.old_text), start = before.indexOf(old);
    assert(start >= 0);
    assert.equal(before.indexOf(old, start + 1), -1, 'oracle expects unique synthetic fragments');
    return { start, end: start + old.length, next: Buffer.from(edit.new_text) };
  }).sort((a, b) => a.start - b.start);
  let oldCursor = 0, newCursor = 0;
  for (const span of spans) {
    const gap = span.start - oldCursor;
    assert.deepEqual(after.subarray(newCursor, newCursor + gap), before.subarray(oldCursor, span.start));
    newCursor += gap;
    assert.deepEqual(after.subarray(newCursor, newCursor + span.next.length), span.next);
    oldCursor = span.end;
    newCursor += span.next.length;
  }
  assert.deepEqual(after.subarray(newCursor), before.subarray(oldCursor));
}

async function fixture(t, { protection = true, userMode = false } = {}) {
  const home = await mkdtemp(join(tmpdir(), 'synthetic-precise-sdk-'));
  const root = join(home, 'workspace'), privateHome = join(home, 'private');
  const archive = join(privateHome, 'document-versions');
  const envKeys = ['HARNESS_DOCUMENT_PROTECTION', 'HARNESS_USER_MODE', 'HARNESS_WORKSPACE', 'DSH_HOME'];
  const oldEnv = Object.fromEntries(envKeys.map(key => [key, process.env[key]]));
  let ctx, vault;
  const leave = [], agents = [];
  t.after(async () => {
    try {
      for (const exit of leave.reverse()) exit();
      await ctx?.fiber.dispose();
      for (const agent of agents) await agent.ctx.fiber.dispose();
      await vault?.flush();
    } finally {
      for (const [key, value] of Object.entries(oldEnv)) {
        if (value === undefined) delete process.env[key];
        else process.env[key] = value;
      }
      assert.equal(dirname(home), tmpdir());
      assert.match(basename(home), /^synthetic-precise-sdk-/);
      await rm(home, { recursive: true, force: true });
    }
  });
  await mkdir(root);
  await mkdir(privateHome);
  Object.assign(process.env, {
    HARNESS_DOCUMENT_PROTECTION: String(protection), HARNESS_USER_MODE: String(userMode),
    HARNESS_WORKSPACE: root, DSH_HOME: privateHome,
  });
  ctx = new Context();
  new SystemPrompt(ctx, {});
  const sessions = new SessionStore(ctx);
  await ctx.plugin(SessionProjectionRegistry);
  await ctx.plugin(AgentRegistry);
  new LocalFileSystem(ctx, { cwd: root, diffBasisMaxBytes: 2 << 20 });
  await ctx.plugin(WorkingDirectoryService, { defaultDirectory: root });
  const tools = new ToolRuntime(ctx);
  tools.setExecutor(new NativeToolExecutor(ctx));
  for (const name of ['synthetic-precise-first', 'synthetic-precise-second']) {
    const agent = { ctx: new Context(), session: sessions.create(name, { meta: { cwd: root } }) };
    agent.id = agent.session.id;
    agents.push(agent);
    leave.push(ctx.agents.enter(agent, undefined));
  }
  await ctx.plugin(ObservationPolicy);
  await ctx.plugin(nativeToolFS, { readImage: false, applyPatch: false });
  // Register safety while the workspace is empty, so its initial tree scan
  // cannot mask a missing edit_precise checkpoint or a dry-run side effect.
  let safetyVault;
  await ctx.plugin({
    inject: ['systemPrompt', 'tools', 'fs'],
    async apply(pluginCtx) {
      await registerDocumentSafety(pluginCtx, { onVault: value => { safetyVault = value; } });
    },
  });
  if (protection && !userMode) assert.ok(safetyVault, 'admin fixture must use the shared document-safety vault');
  const realVault = safetyVault ?? createDocumentVault({ root, archive });
  const checkpoints = [];
  vault = {
    ...realVault,
    async checkpoint(path) {
      checkpoints.push(path);
      return realVault.checkpoint(path);
    },
  };
  await registerPreciseEditTool(ctx, { root, archive, vault });
  let sequence = 0;
  async function execute(agent, name, args, timeout = 10000) {
    const outcome = await tools.execute({
      agent, signal: AbortSignal.timeout(timeout),
      calls: [{ type: 'tool-call', id: `synthetic-precise-${++sequence}`, name, arguments: JSON.stringify(args) }],
    });
    assert.equal(outcome.results.length, 1);
    return outcome.results[0].result;
  }
  const [first, second] = agents;
  return {
    ctx, root, archive, first, second, checkpoints, execute,
    read: (agent, path) => execute(agent, 'read', { file_path: path }),
    edit: (agent, path, edits, options = {}) => execute(agent, 'edit_precise', { file_path: path, edits, ...options }),
    versions: async (path, agent = first) => successful(await execute(agent, 'edit_precise_versions', { action: 'list', file_path: path })).versions,
    disk: async () => ({ workspace: await snapshot(root), archive: await snapshot(archive) }),
  };
}

test('SDK 0.2.2 precise edits use native observation, CAS and private versions', { concurrency: false }, async t => {
  await t.test('unread, cross-agent and stale observations cannot edit or create backups', async t => {
    const f = await fixture(t), path = join(f.root, 'synthetic-observation.md');
    const original = Buffer.from('Synthetic original\r\nalpha\n');
    await writeFile(path, original);
    const edits = [replacement('alpha', 'beta')], initial = await f.disk();
    failed(await f.edit(f.first, path, edits), /FS_NOT_OBSERVED|precise_edit_observation_required|requires reading/i);
    assert.deepEqual(await f.disk(), initial);
    assert.equal((await f.read(f.first, path)).isError, false);
    failed(await f.edit(f.second, path, edits), /FS_NOT_OBSERVED|precise_edit_observation_required|requires reading/i);
    assert.deepEqual(await f.disk(), initial);
    const external = Buffer.from('Synthetic external revision, deliberately different size\r\nalpha\n');
    await writeFile(path, external);
    const changed = await f.disk();
    failed(await f.edit(f.first, path, edits), /FS_STALE_VERSION|changed since/i);
    assert.deepEqual(await f.disk(), changed);
    assert.equal((await f.read(f.first, path)).isError, false);
    failed(await f.edit(f.first, path, edits, { expected_sha256: hash(original) }), /FS_STALE_VERSION|changed since/i);
    assert.deepEqual(await f.disk(), changed);
    assert.equal(f.checkpoints.length, 0);
  });

  await t.test('two reversed independent edits preserve BOM, CRLF, mixed endings and every unselected byte', async t => {
    const f = await fixture(t), path = join(f.root, 'synthetic-exact.md');
    const before = Buffer.from('\ufeff  Heading\r\nalpha\tkept  \n\u0416 \ud83d\ude80 cafe\u0301\r\nlast\rbare  \n');
    const edits = [replacement('cafe\u0301', 'CAF\u00c9'), replacement('alpha', 'longer\tword')];
    await writeFile(path, before);
    assert.equal((await f.read(f.first, path)).isError, false);
    const result = await f.edit(f.first, path, edits), value = successful(result);
    const after = await readFile(path);
    assert.deepEqual(after, Buffer.from('\ufeff  Heading\r\nlonger\tword\tkept  \n\u0416 \ud83d\ude80 CAF\u00c9\r\nlast\rbare  \n'));
    verifyUnchanged(before, after, edits);
    assert.equal(value.applied, true);
    assert.equal(value.verified, true);
    assert.equal(value.replacements, 2);
    assert.equal(value.before_sha256, hash(before));
    assert.equal(value.after_sha256, hash(after));
    assert.deepEqual(value.changed_ranges.map(range => [range.start_line, range.end_line]), [[2, 2], [3, 3]]);
    assert.equal(value.before_version, hash(before));
    assert.equal(value.after_version_saved, true);
    assert.equal(value.warning, undefined);
    assert.deepEqual(f.checkpoints, [path, path]);
  });

  await t.test('native executor accepts a guarded block rewrite without literal old_text', async t => {
    const f = await fixture(t), path = join(f.root, 'synthetic-block.md');
    const lines = Array.from({length:90}, (_, i) => `Synthetic line ${i + 1}  \t`);
    const before = Buffer.from(lines.join('\r\n'));
    const edits = [{mode:'replace_lines',start_line:78,end_line:88,new_text:'Rewritten block\r\nSecond paragraph'}];
    await writeFile(path, before);
    assert.equal((await f.read(f.first, path)).isError, false);
    failed(await f.edit(f.first, path, edits), /precise_edit_range_requires_preview_hash/);
    const preview = successful(await f.edit(f.first, path, edits, {dry_run:true}));
    assert.equal(preview.before_sha256, hash(before));
    assert.deepEqual(await readFile(path), before);
    assert.deepEqual(f.checkpoints, []);
    const result = successful(await f.edit(f.first, path, edits, {expected_sha256:preview.before_sha256}));
    assert.equal(result.verified, true);
    assert.equal(result.after_version_saved, true);
    assert.deepEqual(await readFile(path), Buffer.from([...lines.slice(0,77), edits[0].new_text, ...lines.slice(88)].join('\r\n')));
    assert.deepEqual(f.checkpoints, [path, path]);
  });

  await t.test('overlap and a last-edit miss reject the entire batch without any file writes', async t => {
    const f = await fixture(t), path = join(f.root, 'synthetic-atomic.md');
    await writeFile(path, 'Synthetic abcdef\r\nkeep all bytes\n');
    assert.equal((await f.read(f.first, path)).isError, false);
    for (const [edits, code] of [
      [[replacement('abc', 'X'), replacement('bcde', 'Y')], /precise_edit_overlap/],
      [[replacement('abc', 'X'), replacement('absent synthetic fragment', 'Y')], /precise_edit_not_found/],
    ]) {
      const before = await f.disk();
      failed(await f.edit(f.first, path, edits), code);
      assert.deepEqual(await f.disk(), before);
      assert.equal(f.checkpoints.length, 0);
    }
  });

  await t.test('dry-run uses the safety hook without creating backups and pins a later commit', async t => {
    const f = await fixture(t), path = join(f.root, 'synthetic-preview.md');
    const before = Buffer.from('\ufeffpreview\r\nalpha\nbeta\r\n');
    const after = Buffer.from('\ufeffpreview\r\nALPHA\nbeta\r\n');
    const edits = [replacement('alpha', 'ALPHA')];
    await writeFile(path, before);
    assert.equal((await f.read(f.first, path)).isError, false);
    const initial = await f.disk();
    const previewResult = await f.edit(f.first, path, edits, { dry_run: true });
    const preview = successful(previewResult);
    assert.equal(preview.dry_run, true);
    assert.equal(preview.applied, false);
    assert.equal(preview.verified, true);
    assert.equal(preview.before_sha256, hash(before));
    assert.equal(preview.after_sha256, hash(after));
    assert.deepEqual(previewResult.meta.diffs, []);
    assert.deepEqual(await f.disk(), initial);
    assert.deepEqual(await f.versions(path), []);
    assert.equal(f.checkpoints.length, 0);
    const committed = successful(await f.edit(f.first, path, edits, { expected_sha256: preview.before_sha256 }));
    assert.equal(committed.applied, true);
    assert.deepEqual(await readFile(path), after);
    assert.equal(f.checkpoints.length, 2);
  });

  await t.test('before and after versions recover to distinct new files without overwriting the original', async t => {
    const f = await fixture(t), path = join(f.root, 'synthetic-recover.md');
    const before = Buffer.from('\ufeffsynthetic\r\nold\nend  \r\n');
    const after = Buffer.from('\ufeffsynthetic\r\nnew\nend  \r\n');
    await writeFile(path, before);
    assert.equal((await f.read(f.first, path)).isError, false);
    assert.equal(successful(await f.edit(f.first, path, [replacement('old', 'new')])).after_version_saved, true);
    const versions = await f.versions(path);
    assert.deepEqual(versions.map(version => version.version).sort(), [hash(before), hash(after)].sort());
    assert(versions.every(version => Number.isInteger(version.bytes) && !Object.hasOwn(version, 'content_base64')));
    const recovered = [];
    for (const bytes of [before, after]) {
      const value = successful(await f.execute(f.first, 'edit_precise_versions', {
        action: 'recover', file_path: path, version: hash(bytes),
      }));
      assert.equal(value.original_modified, false);
      assert.equal(value.version, hash(bytes));
      assert.equal(value.bytes, bytes.length);
      assert.equal(dirname(value.path), f.root);
      assert.notEqual(value.path, path);
      assert(!recovered.includes(value.path));
      assert.deepEqual(await readFile(value.path), bytes);
      assert.deepEqual(await readFile(path), after);
      recovered.push(value.path);
    }
  });

  await t.test('user mode with document protection disabled still saves both own versions', async t => {
    const f = await fixture(t, { protection: false, userMode: true });
    const path = join(f.root, 'synthetic-user.txt');
    const before = Buffer.from('synthetic user document\r\nold\n');
    const after = Buffer.from('synthetic user document\r\nnew\n');
    await writeFile(path, before);
    assert.equal((await f.read(f.first, path)).isError, false);
    const value = successful(await f.edit(f.first, path, [replacement('old', 'new')]));
    assert.equal(value.applied, true);
    assert.equal(value.verified, true);
    assert.equal(value.before_version, hash(before));
    assert.equal(value.after_version_saved, true);
    assert.deepEqual(f.checkpoints, [path, path]);
    assert.deepEqual((await f.versions(path)).map(version => version.version).sort(), [hash(before), hash(after)].sort());
    assert.deepEqual(await readFile(path), after);
  });

  await t.test('concurrent independently observed batches have exactly one winner and one stale loser', async t => {
    const f = await fixture(t), path = join(f.root, 'synthetic-concurrent.md');
    const before = Buffer.from('synthetic alpha\r\nbeta\n');
    await writeFile(path, before);
    assert.equal((await f.read(f.first, path)).isError, false);
    assert.equal((await f.read(f.second, path)).isError, false);
    const results = await Promise.all([
      f.edit(f.first, path, [replacement('alpha', 'FIRST')]),
      f.edit(f.second, path, [replacement('beta', 'SECOND')]),
    ]);
    const winners = results.flatMap((result, index) => result.isError ? [] : [index]);
    assert.equal(winners.length, 1, JSON.stringify(results));
    const winner = winners[0], loser = 1 - winner;
    const value = successful(results[winner]);
    assert.equal(value.applied, true);
    assert.equal(value.verified, true);
    failed(results[loser], /FS_STALE_VERSION|changed since/i);
    const expected = Buffer.from(winner === 0 ? 'synthetic FIRST\r\nbeta\n' : 'synthetic alpha\r\nSECOND\n');
    assert.deepEqual(await readFile(path), expected);
    assert.equal(f.checkpoints.length, 2);
    assert.deepEqual((await f.versions(path)).map(version => version.version).sort(), [hash(before), hash(expected)].sort());
  });

  await t.test('external change after write-intent is preserved by the native compare-and-swap', async t => {
    const f = await fixture(t), path = join(f.root, 'synthetic-cas.md');
    const before = Buffer.from('synthetic original\r\nalpha\n');
    const external = Buffer.from('synthetic external change at the commit boundary\r\nalpha untouched\n');
    await writeFile(path, before);
    assert.equal((await f.read(f.first, path)).isError, false);
    let raced = false;
    f.ctx.on('fs/write-intent', async (_target, exec, next) => {
      const intent = await next();
      if (exec.name === 'edit_precise' && !raced) {
        assert.equal(intent.kind, 'replaceIfVersion');
        assert.equal(typeof intent.version, 'string');
        raced = true;
        await writeFile(path, external);
      }
      return intent;
    }, { prepend: true });
    const result = await f.edit(f.first, path, [replacement('alpha', 'beta')]);
    assert.equal(raced, true, 'race must happen after native policy supplies a CAS intent');
    failed(result, /FS_STALE_VERSION|precise_edit_outcome_unknown_read_before_retry|changed since/i);
    assert.deepEqual(await readFile(path), external);
    const versions = await f.versions(path);
    assert(versions.some(version => version.version === hash(before)), 'original was saved before attempting publication');
    assert(!versions.some(version => version.version === hash(Buffer.from('synthetic original\r\nbeta\n'))));
  });

  await t.test('rendered output and metadata stay bounded and never contain the unchanged document', async t => {
    const f = await fixture(t), path = join(f.root, 'synthetic-bounded.md');
    const sentinel = 'SYNTHETIC_UNSELECTED_BODY_MUST_NOT_APPEAR_IN_PRECISE_OUTPUT';
    const before = Buffer.from('\ufeffTARGET_A\r\n' + `${sentinel}  \ud83d\ude80\n`.repeat(2200) + 'TARGET_B\r\n');
    const edits = [replacement('TARGET_A', 'short-A'), replacement('TARGET_B', 'short-B')];
    await writeFile(path, before);
    assert.equal((await f.read(f.first, path)).isError, false);
    const result = await f.edit(f.first, path, edits), value = successful(result);
    assert.equal(value.applied, true);
    assert.equal(value.diffs, undefined, 'diff fragments belong in metadata, not model-facing JSON');
    assert.equal(value.content, undefined);
    assert.equal(value.before, undefined);
    assert.equal(value.after, undefined);
    assert.equal(value.changed_ranges.length, 2);
    assert.deepEqual(result.meta.diffs, [
      { path, oldText: 'TARGET_A', newText: 'short-A' },
      { path, oldText: 'TARGET_B', newText: 'short-B' },
    ]);
    for (const output of [JSON.stringify(result.content), JSON.stringify(result.meta)]) {
      assert(Buffer.byteLength(output) < 16 * 1024, 'bounded by edit summary, not document size');
      assert(!output.includes(sentinel));
      assert(!output.includes(before.toString('base64')));
    }
    verifyUnchanged(before, await readFile(path), edits);
  });

  await t.test('line bounds plus exact adjacent contexts disambiguate repeated words', async t => {
    const f = await fixture(t), path = join(f.root, 'synthetic-selectors.md');
    const before = Buffer.from('\ufeffred word blue\r\nred word blue; green word gold\nred word blue\r\n');
    await writeFile(path, before);
    assert.equal((await f.read(f.first, path)).isError, false);
    const initial = await f.disk();
    failed(await f.edit(f.first, path, [replacement('word', 'CHOSEN')]), /precise_edit_ambiguous/);
    assert.deepEqual(await f.disk(), initial);
    const value = successful(await f.edit(f.first, path, [replacement('word', 'CHOSEN', {
      start_line: 2, end_line: 2, context_before: 'green ', context_after: ' gold',
    })]));
    assert.equal(value.replacements, 1);
    assert.deepEqual(value.changed_ranges, [{ start_line: 2, end_line: 2, old_bytes: 4, new_bytes: 6 }]);
    assert.deepEqual(await readFile(path), Buffer.from('\ufeffred word blue\r\nred word blue; green CHOSEN gold\nred word blue\r\n'));
  });
});
