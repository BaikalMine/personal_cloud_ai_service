import { join } from 'node:path';
import { createDocumentVault } from './document-vault.mjs';

export async function registerDocumentSafety(ctx, { onVault } = {}) {
  if (process.env.HARNESS_DOCUMENT_PROTECTION !== 'true' || process.env.HARNESS_USER_MODE === 'true') return;
  const vault = createDocumentVault({ root: process.env.HARNESS_WORKSPACE || '/workspace', archive: join(process.env.DSH_HOME, 'document-versions') });
  await vault.checkpointTree();
  ctx.systemPrompt.context({ name: 'gateway:document-protection', order: 127, required: true, interpolate: false,
    text: 'Preserve existing user-authored documents. Keep long stories chapter-by-chapter in separate files and maintain a canonical assembled copy only after verifying all unchanged chapters remain. Never replace omitted sections with placeholders, summaries, "unchanged" markers or invented reconstructions. Before revising a long manuscript, keep its full baseline and apply targeted edits. If protected_document_truncation blocks an operation, save a NEW review file; never bypass the protection with Bash, Python or another tool. document_versions lists private automatic checkpoints and restores only to a separate file. Unresolved reconstruction gaps must be reported, not silently filled. Explicit user confirmation is required before replacing their canonical manuscript with a recovered candidate. Transfer large document bodies with the native write/edit file tools or stdin/file-based scripts. Never put a large manuscript inside python -c, node -e or any other process argument: operating systems enforce per-argument size limits (spawn E2BIG).' });
  ctx.on('fs/write-intent', async (target, exec, next) => {
    const intent = await next();
    const path = ctx.fs.processPath(target);
    if (vault.supported(path) && typeof exec.arguments?.content === 'string') await vault.beforeWrite(path, exec.arguments.content);
    return intent;
  }, { prepend: true });
  ctx.on('fs/edit-intent', async (target, exec, next) => {
    const intent = await next();
    if (exec.name === 'edit_precise') return intent;
    const path = ctx.fs.processPath(target);
    if (vault.supported(path)) await vault.beforeEdit(path, exec.arguments);
    return intent;
  }, { prepend: true });
  const shell = exec => ['bash', 'Bash', 'run_code'].includes(exec.name);
  ctx.on('tools/pre-execute', async (exec, next) => {
    const decision = await next();
    if (decision.kind === 'allow' && shell(exec)) await vault.checkpointTree();
    return decision;
  }, { prepend: true });
  ctx.on('tools/post-execute', async (exec, _result, next) => {
    if (shell(exec)) await vault.checkpointTree();
    return next();
  }, { prepend: true });
  ctx.tools.register({
    name: 'document_versions',
    description: 'List private automatic versions of a workspace text document or recover one to a NEW file. Never overwrites the original.',
    parameters: { action: { type: 'string', enum: ['list', 'recover'], required: true }, path: { type: 'string', required: true }, version: { type: 'string' } },
    output: { schema: {}, render: (_args, value) => [{ type: 'text', text: JSON.stringify(value) }] },
    async execute(args) {
      if (args.action === 'list') return { versions: await vault.list(args.path) };
      if (args.action === 'recover') return vault.recover(args.path, args.version);
      throw new Error('invalid_document_version_action');
    },
  });
  let pending = Promise.resolve();
  const timer = setInterval(() => { pending = pending.then(() => vault.checkpointTree()).catch(() => ctx.logger.warn('Document checkpoint unavailable; inspect private archive status')); }, 60000);
  timer.unref?.();
  ctx.effect(() => async () => { clearInterval(timer); await pending; await vault.flush(); }, 'gateway: private document versions');
  onVault?.(vault);
}
