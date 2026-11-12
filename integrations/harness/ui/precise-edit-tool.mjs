import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { TextDecoder } from 'node:util';
import { createDocumentVault, destructiveChange } from './document-vault.mjs';
import { planPreciseEdits } from './precise-edits.mjs';

const MAX_BYTES = 16 * 1024 * 1024;
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const fault = code => Object.assign(new Error(code), { code });
const decode = bytes => new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
const locks = new WeakMap();
const prepublicationErrors = new Set(['FS_STALE_VERSION', 'FS_NOT_OBSERVED', 'FS_NOT_FOUND', 'FS_NOT_REGULAR_FILE', 'FS_PERMISSION_DENIED', 'FS_SANDBOX_DENIED', 'FS_TOO_LARGE', 'FS_ABORTED']);

export const PRECISE_EDIT_CONTEXT = `For targeted edits to existing UTF-8 documents (.md, .markdown, .txt, .rst, .org, .tex), prefer edit_precise. Read the current relevant text first. Send one or more short literal {old_text,new_text} replacements in edits; do not repeat an unchanged paragraph just to replace one word. Example: edits:[{old_text:"quietly",new_text:"silently",start_line:12,end_line:12}]. If a word repeats on the same line, add short exact context_before and/or context_after immediately adjacent to the desired occurrence. Context is a selector, not replacement text. Never guess a line number or use a vague match. No regular expressions, occurrence indexes or replace-all are supported.
Every entry addresses the ORIGINAL file snapshot, not the output of an earlier entry. Use one batch for independent changes in the same file. Overlapping or ambiguous edits fail the entire batch without a write. Adjacent edits are allowed. Keep replacements minimal; preserve all text and Markdown outside the requested spans. start_line/end_line are optional inclusive one-based bounds; when only start_line is supplied it selects that single line. Use dry_run:true to validate a complex batch without modifying the file; an optional expected_sha256 pins a previously returned source hash.
If the user wants to rewrite an entire contiguous block, read the exact current line range and use {mode:"replace_lines",start_line:78,end_line:88,new_text:"..."} without old_text or contexts. Use dry_run:true first, then submit the same edits with expected_sha256 set to before_sha256 from the preview. Both bounds are required and inclusive. The last selected line's existing newline is preserved; do not append another unless you intend an extra blank line. For precise_edit_not_found, re-read the target: use a shorter exact fragment for a small change, or the guarded range mode for an intentional full-block rewrite. Never use line mode to work around an unread or stale file.
The tool atomically publishes the planned document through the normal filesystem backend, requires a current read observation, and saves the original private document version BEFORE writing. It immediately attempts to save the new version as well. A failure to save the original blocks the edit. Inspect applied, verified and any warning before claiming success. An outcome-unknown error requires a fresh read, never blind replay. edit_precise_versions lists private versions or recovers one to a NEW file; it never overwrites the current document. Paths stay within the calling session's original workspace and versions use the current profile's private archive; no caller-supplied session or account selector exists. Do not bypass sandbox denials with another tool. Ordinary native edit/write remain available for genuinely broader edits requested by the user.`;

function serial(fs, key, task) {
  let byTarget = locks.get(fs);
  if (!byTarget) { byTarget = new Map(); locks.set(fs, byTarget); }
  const pending = (byTarget.get(key) ?? Promise.resolve()).catch(() => {}).then(task);
  byTarget.set(key, pending);
  return pending.finally(() => { if (byTarget.get(key) === pending) byTarget.delete(key); });
}

function validateArgs(args) {
  if (!args || typeof args !== 'object' || Array.isArray(args) || Object.keys(args).some(key => !['file_path', 'edits', 'dry_run', 'expected_sha256'].includes(key))) throw fault('precise_edit_invalid_arguments');
  if (typeof args.file_path !== 'string' || !args.file_path.trim() || args.file_path.length > 4096 || /[\x00-\x1f]/.test(args.file_path)) throw fault('precise_edit_invalid_path');
  if (args.dry_run !== undefined && typeof args.dry_run !== 'boolean') throw fault('precise_edit_invalid_arguments');
  if (args.expected_sha256 !== undefined && (typeof args.expected_sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(args.expected_sha256))) throw fault('precise_edit_invalid_hash');
}

export async function registerPreciseEditTool(ctx, options = {}) {
  const defineTool = options.defineTool ?? (await import('@deepseek-ai/dsh-tools')).defineTool;
  const filesystem = ctx.fs;
  const root = options.root ?? process.env.HARNESS_WORKSPACE ?? '/workspace';
  const archive = options.archive ?? (process.env.DSH_HOME && join(process.env.DSH_HOME, 'document-versions'));
  const sandbox = ctx.fs.sandboxMode === undefined ? undefined : ctx.get('sandboxPolicy');
  if (ctx.fs.sandboxMode !== undefined && !sandbox) throw fault('precise_edit_sandbox_policy_unavailable');
  let vault = options.vault;
  function versions() {
    if (vault) return vault;
    if (!archive) throw fault('precise_edit_versions_unavailable');
    return vault ??= (options.createVault ?? createDocumentVault)({ root, archive });
  }
  async function locate(filePath, exec) {
    if (!exec?.agent?.session || !exec.signal) throw fault('precise_edit_session_unavailable');
    exec.signal.throwIfAborted();
    const cwd = await ctx.workingDirectory.ensure(exec.agent, exec.signal);
    const resolveOptions = { cwd, signal: exec.signal };
    const workspace = await ctx.fs.resolve(root, { signal: exec.signal });
    const originalDirectory = exec.agent.session.header?.cwd ?? ctx.workingDirectory.defaultDirectory ?? cwd;
    const directory = await ctx.fs.resolve(originalDirectory, { signal: exec.signal });
    const currentDirectory = await ctx.fs.resolve(cwd, { signal: exec.signal });
    const target = await ctx.fs.resolve(filePath, resolveOptions);
    if (!ctx.fs.contains(workspace, directory) || !ctx.fs.contains(directory, currentDirectory) || !ctx.fs.contains(directory, target) || target.targetKey === directory.targetKey) throw fault('precise_edit_outside_workspace');
    const path = ctx.fs.processPath(target);
    if (!versions().supported(path)) throw fault('precise_edit_unsupported_document');
    return { target, path };
  }
  const summary = value => { const { diffs, ...result } = value; return result; };
  const definition = defineTool({
    name: 'edit_precise',
    description: 'Apply 1-64 literal replacements or explicitly selected whole-line rewrites to one existing UTF-8 document in a single atomic batch. Read first. For a small edit use short old_text and new_text. For a full block use mode replace_lines, both line numbers, and new_text, without old_text. Preview a range with dry_run and pass before_sha256 as expected_sha256 to commit. Unselected bytes and private versions are preserved.',
    parameters: {
      file_path: { type: 'string', required: true, description: 'Existing document path in this chat workspace. Supply before edits.' },
      edits: { type: 'array', required: true, items: { type: 'object', additionalProperties: false, properties: {
        mode: { type: 'string', enum: ['replace_lines'], description: 'Explicit whole-line rewrite; omit for literal matching.' },
        old_text: { type: 'string', description: 'Required for literal matching. Small nonempty exact text; omit for replace_lines.' },
        new_text: { type: 'string', required: true, description: 'Literal replacement. In replace_lines the original final line ending is kept, even if new_text is empty.' },
        start_line: { type: 'integer', description: 'Inclusive one-based first line from the current read. Required for replace_lines.' },
        end_line: { type: 'integer', description: 'Inclusive last line, required for replace_lines. In literal mode start_line alone selects one line.' },
        context_before: { type: 'string', description: 'Optional short exact text directly before the match. Selector only; not changed.' },
        context_after: { type: 'string', description: 'Optional short exact text directly after the match. Selector only; not changed.' },
      } } },
      dry_run: { type: 'boolean', description: 'Validate and preview the batch without writing or creating versions. Default false.' },
      expected_sha256: { type: 'string', description: 'Optional original-file SHA-256 from a previous dry run. Rejects changed content.' },
    },
    output: {
      schema: { type: 'object', additionalProperties: true },
      render: (_args, value) => [{ type: 'text', text: JSON.stringify(summary(value)) }],
      presentationMeta: (_args, value) => ({ path: value.path, diffs: value.dry_run ? [] : value.diffs }),
    },
    async execute(args, exec) {
      validateArgs(args);
      const { target, path } = await locate(args.file_path, exec);
      return serial(filesystem, target.targetKey, async () => {
        exec.signal.throwIfAborted();
        // Do not turn this internal read into an observation: the agent must have read first.
        const intent = await ctx.waterfall('fs/edit-intent', target, exec, () => undefined);
        if (!intent?.version) throw fault('precise_edit_observation_required');
        const info = await ctx.fs.stat(target, exec.signal);
        if (!info || info.type !== 'file' || info.version !== intent.version) throw fault('FS_STALE_VERSION');
        if ((info.size ?? Infinity) > MAX_BYTES) throw fault('precise_edit_file_too_large');
        const before = Buffer.from(await ctx.fs.readBytes(target, exec.signal, MAX_BYTES));
        const afterRead = await ctx.fs.stat(target, exec.signal);
        if (afterRead?.version !== info.version) throw fault('FS_STALE_VERSION');
        if (args.expected_sha256 !== undefined && args.expected_sha256 !== hash(before)) throw fault('FS_STALE_VERSION');
        const plan = planPreciseEdits(before, args.edits);
        if (args.dry_run !== true && args.expected_sha256 === undefined && args.edits.some(edit => edit.mode === 'replace_lines')) {
          throw fault('precise_edit_range_requires_preview_hash');
        }
        if (plan.after.length > MAX_BYTES) throw fault('precise_edit_file_too_large');
        if (plan.after_sha256 === plan.before_sha256) throw fault('precise_edit_unchanged');
        const result = {
          path, applied: false, verified: true, dry_run: args.dry_run === true, replacements: plan.changes.length,
          before_sha256: plan.before_sha256, after_sha256: plan.after_sha256,
          changed_ranges: plan.changes.map(change => ({ start_line: change.start_line, end_line: change.end_line, old_bytes: change.end_byte - change.start_byte, new_bytes: Buffer.byteLength(change.new_text) })),
          diffs: plan.changes.map(change => ({ path, oldText: change.old_text, newText: change.new_text })),
        };
        if (args.dry_run === true) return result;
        const policy = sandbox ? await sandbox.resolve({ session: exec.agent.session }) : undefined;
        const saved = await versions().checkpoint(path);
        if (!saved || saved.version !== plan.before_sha256 || !saved.bytes.equals(before)) throw fault('FS_STALE_VERSION');
        const text = decode(plan.after);
        if (destructiveChange(decode(before), text)) throw fault('protected_document_truncation');
        // Preserve existing write-intent hooks, including document protection, without model-generated full-file arguments.
        const writeIntent = await ctx.waterfall('fs/write-intent', target, { ...exec, arguments: { ...args, content: text } }, () => undefined);
        if (writeIntent?.kind !== 'replaceIfVersion' || writeIntent.version !== intent.version) throw fault('FS_STALE_VERSION');
        exec.signal.throwIfAborted();
        let outcome;
        try {
          outcome = await ctx.fs.writeText(target, text, writeIntent, exec.signal, policy);
        } catch (error) {
          if (prepublicationErrors.has(error?.code)) throw error;
          // A failed acknowledgement is not permission to replay a potentially committed edit.
          let currentHash;
          try { currentHash = hash(await ctx.fs.readBytes(target, undefined, MAX_BYTES)); } catch {}
          if (currentHash === plan.before_sha256) throw error;
          if (currentHash !== plan.after_sha256) throw fault('precise_edit_outcome_unknown_read_before_retry');
          result.warning = 'publication_confirmation_lost_read_before_further_edits';
        }
        result.applied = true;
        result.before_version = saved.version;
        let current, currentInfo;
        try {
          currentInfo = await ctx.fs.stat(target);
          current = Buffer.from(await ctx.fs.readBytes(target, undefined, MAX_BYTES));
          const finalInfo = await ctx.fs.stat(target);
          result.verified = currentInfo?.version === finalInfo?.version && (!outcome || outcome.version === finalInfo?.version) && hash(current) === plan.after_sha256;
          if (result.verified) ctx.emit('fs/observed', target, { kind: 'present', version: finalInfo.version }, exec);
        } catch { result.verified = false; }
        if (!result.verified) {
          result.warning = 'file_changed_after_publication_read_before_further_edits';
          result.after_version_saved = false;
          return result;
        }
        try {
          const savedAfter = await versions().checkpoint(path);
          result.after_version_saved = savedAfter?.version === plan.after_sha256;
        } catch { result.after_version_saved = false; }
        if (!result.after_version_saved) result.warning = 'edit_applied_original_version_saved_new_version_snapshot_unavailable';
        return result;
      });
    },
    presentCall(args) {
      return { card: 'diff', title: `${args.dry_run ? 'Preview' : 'Edit'} ${args.file_path}`, diffs: Array.isArray(args.edits) ? args.edits.filter(edit => edit?.mode !== 'replace_lines').map(edit => ({ path: args.file_path, oldText: edit.old_text || null, newText: edit.new_text ?? '' })) : [], locations: [{ path: args.file_path }] };
    },
    presentResult(args, result) {
      if (result.isError || !Array.isArray(result.meta?.diffs) || result.meta.diffs.length === 0) return;
      return { card: 'diff', title: `Edit ${args.file_path}`, diffs: result.meta.diffs };
    },
  });
  ctx.tools.register(definition);
  ctx.systemPrompt.context({ name: 'gateway:precise-edits', order: 125, required: true, interpolate: false, text: PRECISE_EDIT_CONTEXT });
  ctx.tools.register(defineTool({
    name: 'edit_precise_versions', description: 'Page through private saved versions of a text document in this chat workspace, or recover one to a NEW file without overwriting the original. Lists 20 versions by default.',
    parameters: { action: { type: 'string', enum: ['list', 'recover'], required: true }, file_path: { type: 'string', required: true }, version: { type: 'string', description: 'Exact SHA-256 version ID from list; required for recover.' }, cursor: { type: 'integer', description: 'List offset, initially 0; use next_cursor for more.' }, limit: { type: 'integer', description: 'List page size, 1-50; default 20.' } },
    output: { schema: { type: 'object', additionalProperties: true }, render: (_args, value) => [{ type: 'text', text: JSON.stringify(value) }] },
    async execute(args, exec) {
      if (!args || Object.keys(args).some(key => !['action', 'file_path', 'version', 'cursor', 'limit'].includes(key)) || !['list', 'recover'].includes(args.action)) throw fault('precise_edit_invalid_arguments');
      validateArgs({ file_path: args.file_path });
      if (args.action === 'list' && (args.version !== undefined || !Number.isSafeInteger(args.cursor ?? 0) || (args.cursor ?? 0) < 0 || !Number.isInteger(args.limit ?? 20) || (args.limit ?? 20) < 1 || (args.limit ?? 20) > 50)) throw fault('precise_edit_invalid_arguments');
      if (args.action === 'recover' && (args.cursor !== undefined || args.limit !== undefined || typeof args.version !== 'string' || !/^[a-f0-9]{64}$/.test(args.version))) throw fault('precise_edit_invalid_arguments');
      const { path } = await locate(args.file_path, exec);
      if (args.action === 'list') {
        const all = await versions().list(path), cursor = args.cursor ?? 0, limit = args.limit ?? 20;
        return { versions: all.slice(cursor, cursor + limit), total: all.length, next_cursor: cursor + limit < all.length ? cursor + limit : null };
      }
      exec.signal.throwIfAborted();
      if (sandbox) {
        const policy = await sandbox.resolve({ session: exec.agent.session });
        if (policy.mode !== 'danger-full-access') {
          if (policy.mode !== 'workspace-write' || typeof policy.workspaceRoot !== 'string') throw fault('FS_SANDBOX_DENIED');
          const granted = await ctx.fs.resolve(policy.workspaceRoot, { signal: exec.signal });
          const target = await ctx.fs.resolve(path, { signal: exec.signal });
          if (!ctx.fs.contains(granted, target)) throw fault('FS_SANDBOX_DENIED');
        }
      }
      return versions().recover(path, args.version);
    },
  }));
}
