import { closeSync, fsyncSync, lstatSync, mkdirSync, openSync, readdirSync, realpathSync, renameSync, rmdirSync, unlinkSync } from 'node:fs';
import { dirname, isAbsolute, join, relative, sep } from 'node:path';
import { parseSessionFormatLogFilename } from '@deepseek-ai/dsh-session-format';
import { createChatMemoryStore } from './chat-memory-store.mjs';

const deleting = globalThis[Symbol.for('ai-gateway.archiveDeleting')] ??= new Set();
const disposeAgent = Symbol.for('ai-gateway.archiveDispose');
const validId = id => typeof id === 'string' && /^[a-zA-Z0-9_-]{1,128}$/.test(id);
const fault = code => Object.assign(new Error(code), { code });
const inspect = path => lstatSync(path, { throwIfNoEntry:false });

function directory(path) {
  const info = inspect(path);
  if (!info?.isDirectory() || info.uid !== process.getuid() || realpathSync(path) !== path) throw fault('unsafe_storage');
}

function syncDirectory(path) {
  const fd = openSync(path, 'r');
  try { fsyncSync(fd); } finally { closeSync(fd); }
}

function historyFiles(path) {
  directory(path);
  return readdirSync(path, { withFileTypes:true }).map(entry => {
    if (!entry.isFile() || (entry.name !== 'session.lock' &&
      parseSessionFormatLogFilename(entry.name.replace(/\.zstd$/, '')) === undefined)) throw fault('unsafe_storage');
    const file = join(path, entry.name);
    const info = lstatSync(file);
    if (!info.isFile() || info.uid !== process.getuid() || info.nlink !== 1) throw fault('unsafe_storage');
    return file;
  });
}

async function stageHistory(persistence, home, sessionId) {
  const root = persistence.root;
  const rootPath = relative(home, root);
  if (!rootPath || rootPath === '..' || rootPath.startsWith('..' + sep) || isAbsolute(rootPath)) throw fault('unsafe_storage');
  const quarantine = join(home, 'archive-delete');
  const staged = join(quarantine, sessionId);
  const snapshot = await persistence.stat(sessionId);
  if (inspect(quarantine)) directory(quarantine);
  if (inspect(staged)) {
    if (snapshot) throw fault('unsafe_storage');
    historyFiles(staged);
    return { path:staged };
  }
  if (!snapshot) {
    if (inspect(root)) {
      directory(root);
      // A missing catalog entry must not hide an unreadable or unknown artifact.
      for (const project of readdirSync(root, { withFileTypes:true })) {
        if (project.isDirectory() && inspect(join(root, project.name, sessionId))) throw fault('unsafe_storage');
      }
    }
    return;
  }
  const location = snapshot.location;
  if (snapshot.header.id !== sessionId || location?.kind !== 'jsonl') throw fault('unsafe_storage');
  const parts = relative(root, location.path).split(sep);
  if (parts.length !== 3 || parts[0] === '..' || parts[1] !== sessionId || join(root, ...parts) !== location.path) throw fault('unsafe_storage');
  const source = dirname(location.path);
  if (!historyFiles(source).includes(location.path)) throw fault('unsafe_storage');
  if (!inspect(quarantine)) {
    mkdirSync(quarantine, { mode:0o700 });
    syncDirectory(home);
  }
  directory(quarantine);
  const lease = await persistence.acquireLease(sessionId, undefined, source);
  try {
    if (persistence.tracker.writers.has(sessionId)) throw fault('chat_busy');
    if (!historyFiles(source).includes(location.path)) throw fault('unsafe_storage');
    // Keep a retryable copy outside the searchable session tree until metadata is cleaned.
    renameSync(source, staged);
    syncDirectory(dirname(source));
    syncDirectory(quarantine);
    return { path:staged, source };
  } finally { await lease.release(); }
}

export function registerArchivedChatDelete(ctx, { home = process.env.DSH_HOME } = {}) {
  const memory = createChatMemoryStore({ dshHome:home });
  const registry = ctx.workspaceRegistry;
  const persistence = ctx.sessionPersistence;
  async function remove(sessionId) {
    await registry.enqueueOperation(async () => {
      const state = registry.requireState();
      if (!state.archivedSessionIds.includes(sessionId)) throw fault('not_archived');
      if (persistence.name !== 'session-persistence-jsonl') throw fault('unsafe_storage');
      directory(home);
      deleting.add(sessionId);
      try {
        const active = await ctx.waterfall('workspace/session-activity', { sessionId }, () => Promise.resolve([]));
        if (active.length) throw fault('chat_busy');
        const agent = ctx.agents.get(sessionId);
        if (agent || ctx.sessions.get(sessionId)) {
          if (typeof agent?.[disposeAgent] !== 'function') throw fault('chat_busy');
          await agent[disposeAgent]();
        }
        if (ctx.agents.get(sessionId) || ctx.sessions.get(sessionId) || persistence.tracker.writers.has(sessionId)) throw fault('chat_busy');
        const staged = await stageHistory(persistence, home, sessionId);
        try {
          persistence.coldLogMemo.delete(sessionId);
          await memory.deleteSession(sessionId);
          await ctx.get('sessionProjectionCache')?.requireTable().delete(sessionId);
          try {
            // Native search reconciles its persisted and in-memory indexes on search.
            await ctx.sessionQuery.searchSessions({ query:'gatewayarchivedchatpurge', limit:1 });
          } catch (error) {
            if (error?.code !== 'SESSION_QUERY_SEARCH_DISABLED') throw error;
          }
          for (const workspace of registry.list()) {
            if (workspace.record.sessionIds.includes(sessionId)) await workspace.detachSession(sessionId);
          }
          for (const map of [registry.headers, registry.sessionPaths, registry.missingSessionPaths, registry.invalidSessionPaths]) map.delete(sessionId);
        } catch (error) {
          // Keep the archive entry selectable when cleanup failed before any log was removed.
          if (staged?.source && !inspect(staged.source)) {
            historyFiles(staged.path);
            directory(dirname(staged.source));
            renameSync(staged.path, staged.source);
            syncDirectory(dirname(staged.source));
            syncDirectory(dirname(staged.path));
          }
          throw error;
        }
        if (staged) {
          for (const file of historyFiles(staged.path)) unlinkSync(file);
          rmdirSync(staged.path);
          syncDirectory(dirname(staged.path));
        }
        await registry.setState({
          ...state,
          archivedSessionIds:state.archivedSessionIds.filter(id => id !== sessionId),
          pinnedSessionIds:state.pinnedSessionIds.filter(id => id !== sessionId),
        });
        ctx.emit('api-session/removed', sessionId);
      } finally { deleting.delete(sessionId); }
    });
  }

  ctx.effect(() => ctx.connection.fetch.register({
    path:'/api/gateway-archive/delete', methods:['POST'], requestBody:'buffered',
    async fetch(request) {
      try {
        if (request.headers.get('content-type')?.split(';')[0].trim().toLowerCase() !== 'application/json') throw fault('invalid_request');
        const body = await request.text();
        if (Buffer.byteLength(body) > 512) throw fault('invalid_request');
        const input = JSON.parse(body);
        if (!input || !validId(input.sessionId) || input.confirm !== true || Object.keys(input).length !== 2) throw fault('invalid_request');
        request.signal.throwIfAborted();
        await remove(input.sessionId);
        return Response.json({ ok:true }, { headers:{'Cache-Control':'no-store'} });
      } catch (error) {
        const code = error instanceof SyntaxError ? 'invalid_request' :
          ['invalid_request', 'not_archived', 'chat_busy', 'unsafe_storage'].includes(error?.code) ? error.code : 'delete_failed';
        const status = code === 'invalid_request' ? 400 : ['not_archived','chat_busy'].includes(code) ? 409 : 500;
        return Response.json({ ok:false, error:{ code } }, { status, headers:{'Cache-Control':'no-store'} });
      }
    },
  }), 'gateway: archived chat deletion');
}
