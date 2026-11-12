import { constants, createReadStream } from 'node:fs';
import { lstat, mkdir, realpath, rename, unlink, rmdir, writeFile, link, open, opendir, rm } from 'node:fs/promises';
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { Readable } from 'node:stream';

const TEXT_LIMIT = 1 << 20, UPLOAD_LIMIT = 20 << 20;
const COPY_DEPTH_LIMIT = 32, COPY_ENTRY_LIMIT = 10_000;
export const WORKSPACE_UPLOAD_LIMIT = 512 * 1024 * 1024;
const editable = /\.(?:txt|md|markdown|json|jsonl|yaml|yml|toml|ini|cfg|csv|tsv|log|html|htm|css|js|mjs|cjs|ts|tsx|jsx|py|go|rs|sh|bat|ps1|xml|svg|sql)$/i;
const inside = (root, path) => { const part = relative(root, path); return part === '' || part !== '..' && !part.startsWith('..' + sep) && !isAbsolute(part); };
const fault = code => Object.assign(new Error(code), { code });
const validSessionId = id => typeof id === 'string' && /^[a-zA-Z0-9_-]{1,128}$/.test(id);
const sameEntry = (a, b) => a.dev === b.dev && a.ino === b.ino;
const unchangedEntry = (a, b) => sameEntry(a, b) && a.size === b.size && a.mtimeNs === b.mtimeNs && a.ctimeNs === b.ctimeNs;
const descriptorPath = handle => `/proc/self/fd/${handle.fd}`;
const linux = process.platform === 'linux';
const readFlags = constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0) | (constants.O_NONBLOCK ?? 0);
const exactStats = { bigint:true };
const nameOf = name => {
  if (typeof name !== 'string' || !name.trim() || name.length > 220 || /[/\\\x00-\x1f]/.test(name) || name === '.' || name === '..' || name.trim() !== name) throw fault('invalid_name');
  return name;
};
function copyFolder(path = '') {
  if (path === '') return '.';
  if (typeof path !== 'string' || path.length > 4096 || /[:\\]/.test(path)) throw fault('invalid_copy_folder');
  try { return path.split('/').map(nameOf).join(sep); } catch { throw fault('invalid_copy_folder'); }
}
function generatedJob(directory) {
  const [generated, provider, job] = directory.split(sep).slice(-3);
  if (generated === 'generated' && ['krea-ultra','moss-image'].includes(provider) && /^[a-f0-9-]{36}$/.test(job)) return `${provider}/${job}`;
}
function checkCopyRetention(source, target) {
  const parent = dirname(source);
  for (let directory = parent; dirname(directory) !== directory; directory = dirname(directory)) {
    const job = generatedJob(directory);
    if (!job) continue;
    // The sweeper visits only direct files in recognized job directories.
    if (directory !== parent || generatedJob(target) !== job ||
        target.split(sep).some(part => ['.git','.venv','node_modules','services'].includes(part))) throw fault('copy_retention');
    return;
  }
}
async function requireMissing(path) {
  try { await lstat(path); throw fault('EEXIST'); } catch (error) { if (error.code !== 'ENOENT') throw error; }
}

// Node's POSIX rename can replace an empty directory. Use Linux's exclusive rename,
// with the syscall fallback for the Alpine runtime's libc.
const exclusiveLinuxRename = `
import ctypes, errno, os, platform, sys
source, name, parent = map(os.fsencode, sys.argv[1:])
try:
    pinned = (os.readlink(b'/proc/self/fd/3') == parent and
              os.readlink(b'/proc/self/fd/4') == os.path.join(parent, source) and
              os.path.samestat(os.stat(source, dir_fd=3, follow_symlinks=False), os.fstat(4)))
except OSError:
    pinned = False
if not pinned:
    sys.exit(2)
libc = ctypes.CDLL(None, use_errno=True)
args = (ctypes.c_int(3), ctypes.c_char_p(source), ctypes.c_int(3), ctypes.c_char_p(name), ctypes.c_uint(1))
if hasattr(libc, 'renameat2'):
    result = libc.renameat2(*args)
else:
    number = {'x86_64': 316, 'aarch64': 276}.get(platform.machine())
    if number is None:
        sys.exit(3)
    result = libc.syscall(ctypes.c_long(number), *args)
if result != 0:
    sys.exit(1 if ctypes.get_errno() in (errno.EEXIST, errno.ENOTEMPTY) else 3)
`;
const exclusiveWindowsRename = `
try {
  [System.IO.Directory]::Move($env:GATEWAY_COPY_STAGE, $env:GATEWAY_COPY_DESTINATION)
  exit 0
} catch {
  exit 3
}
`;
function runPublisher(command, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, options);
    child.once('error', reject);
    child.once('exit', code => code === 0 ? resolve() : reject(fault(code === 1 ? 'EEXIST' : code === 2 ? 'changed' : 'unavailable')));
  });
}
async function publishFolder(parent, staging, target, stagedName, name) {
  if (linux) {
    await runPublisher('/usr/bin/python3', ['-I', '-c', exclusiveLinuxRename, stagedName, name, target],
      { stdio:['ignore','ignore','ignore',parent.fd,staging.fd] });
  } else if (process.platform === 'win32') {
    const destination = join(target, name);
    // The libuv rename can replace a destination file even when the source is a directory.
    const powershell = join(process.env.SystemRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe');
    try {
      await runPublisher(powershell, ['-NoProfile','-NonInteractive','-Command',exclusiveWindowsRename], {
        stdio:'ignore', windowsHide:true,
        env:{...process.env,GATEWAY_COPY_STAGE:join(target,stagedName),GATEWAY_COPY_DESTINATION:destination},
      });
    }
    catch (error) { await requireMissing(destination); throw error; }
  } else throw fault('unavailable');
}

export function createWorkspaceFiles({ fs, rootFor, allowedRoot = '/workspace', registerUpload = async () => {} }) {
  const locks = new Map();
  async function noSymlinks(root, path) {
    let current = root;
    for (const part of relative(root, path).split(sep).filter(Boolean)) {
      current = join(current, part);
      if ((await lstat(current)).isSymbolicLink()) throw fault('symlink');
    }
  }
  async function locate(sessionId, path, signal, allowRoot = false, strictRoot = false) {
    if (!validSessionId(sessionId) || typeof path !== 'string' || !path || path.length > 4096 || /[\x00-\x1f]/.test(path)) throw fault('invalid_request');
    signal?.throwIfAborted();
    const workspace = await rootFor(sessionId);
    if (!workspace) throw fault('session_unavailable');
    const allowed = await realpath(allowedRoot);
    if (strictRoot) {
      const configured = resolve(allowedRoot), candidate = resolve(workspace);
      const base = inside(configured, candidate) ? configured : allowed;
      if (!inside(base, candidate)) throw fault('outside_workspace');
      await noSymlinks(base, candidate);
    }
    const root = await realpath(workspace);
    if (!inside(allowed, root)) throw fault('outside_workspace');
    const absolutePath = resolve(root, path);
    if (!inside(root, absolutePath) || !allowRoot && absolutePath === root) throw fault('outside_workspace');
    // Mutations never traverse symlinks, even when their present target is inside the workspace.
    await noSymlinks(root, absolutePath);
    const target = await fs.resolve(absolutePath, { signal });
    if (!inside(root, fs.processPath(target))) throw fault('outside_workspace');
    const info = await fs.stat(target, signal);
    if (!info || !['file', 'directory'].includes(info.type)) throw fault('not_found');
    return { root, absolutePath, target, info };
  }
  function summary(file) {
    return { path: file.absolutePath, workspace_root: file.root, name: basename(file.absolutePath), type: file.info.type, bytes: file.info.size ?? 0,
      version: file.info.version, root: file.absolutePath === file.root,
      editable: file.info.type === 'file' && editable.test(file.absolutePath) && (file.info.size ?? Infinity) <= TEXT_LIMIT };
  }
  function version(file, expected) { if (typeof expected !== 'string' || expected !== file.info.version) throw fault('changed'); }
  async function copyTree(file, target, folder, name, request, signal) {
    const destination = join(target.absolutePath, name);
    if (inside(file.absolutePath, target.absolutePath) || inside(file.absolutePath, destination)) throw fault('copy_descendant');
    const stagedName = '.gateway-copy-' + randomUUID();
    const stagedPath = join(target.absolutePath, stagedName);
    const targetEntry = await lstat(target.absolutePath);
    if (targetEntry.isSymbolicLink()) throw fault('symlink');
    const parent = linux ? await open(target.absolutePath, readFlags | constants.O_DIRECTORY) : null;
    const targetBase = parent ? descriptorPath(parent) : target.absolutePath;
    const temporary = join(targetBase, stagedName);
    const snapshots = [], files = [];
    let stage, stageBase, stageEntry, count = 0, published = false;

    async function checkTarget() {
      const fresh = await locate(request.targetSessionId, folder, signal, true, true);
      if (fresh.root !== target.root || fresh.absolutePath !== target.absolutePath || fresh.info.type !== 'directory' ||
          !sameEntry(targetEntry, await lstat(fresh.absolutePath)) ||
          parent && (!sameEntry(targetEntry, await parent.stat()) || await realpath(targetBase) !== target.absolutePath)) throw fault('changed');
    }
    async function checkSource() {
      const fresh = await locate(request.sessionId, request.path, signal, false, true);
      if (fresh.root !== file.root || fresh.absolutePath !== file.absolutePath || fresh.info.type !== 'directory') throw fault('changed');
      version(fresh, request.version);
    }
    async function walk(source, absolute, output, subpath, depth) {
      signal?.throwIfAborted();
      if (++count > COPY_ENTRY_LIMIT || depth > COPY_DEPTH_LIMIT) throw fault('copy_limit');
      const entry = await lstat(source, exactStats);
      if (entry.isSymbolicLink()) throw fault('symlink');
      if (!entry.isDirectory() && !entry.isFile()) throw fault('copy_unsupported');
      if (!linux) await noSymlinks(file.root, absolute);
      if (depth) {
        const stagedParent = dirname(join(stagedPath, subpath));
        if (linux) {
          if (await realpath(dirname(output)) !== stagedParent) throw fault('changed');
        } else await noSymlinks(target.absolutePath, stagedParent);
      }
      const handle = linux || entry.isFile() ? await open(source, readFlags | (entry.isDirectory() ? constants.O_DIRECTORY : 0)) : null;
      let outputHandle;
      try {
        const sourceBase = linux ? descriptorPath(handle) : source;
        if ((handle && !unchangedEntry(entry, await handle.stat(exactStats))) || (linux && await realpath(sourceBase) !== absolute)) throw fault('changed');
        let outputEntry;
        if (entry.isDirectory()) {
          if (depth) await mkdir(output, { mode:0o700 });
          outputEntry = await lstat(output, exactStats);
          if (!outputEntry.isDirectory()) throw fault('changed');
          if (linux && depth) {
            outputHandle = await open(output, readFlags | constants.O_DIRECTORY);
            if (!sameEntry(outputEntry, await outputHandle.stat(exactStats)) ||
                await realpath(descriptorPath(outputHandle)) !== join(stagedPath, subpath)) throw fault('changed');
          }
          const outputBase = outputHandle ? descriptorPath(outputHandle) : output;
          for await (const child of await opendir(sourceBase)) {
            signal?.throwIfAborted();
            const childName = child.name;
            await walk(join(sourceBase, childName), join(absolute, childName), join(outputBase, childName), join(subpath, childName), depth + 1);
          }
        } else {
          checkCopyRetention(absolute, dirname(join(destination, subpath)));
          outputHandle = await open(output, 'wx', 0o600);
          await outputHandle.writeFile(handle.createReadStream({ autoClose:false, signal }), { signal });
          outputEntry = await outputHandle.stat(exactStats);
        }
        if (!unchangedEntry(entry, await lstat(source, exactStats))) throw fault('changed');
        const stagedEntry = await lstat(output, exactStats);
        if (!sameEntry(outputEntry, stagedEntry)) throw fault('changed');
        const snapshot = { absolute, sourceEntry:entry, output:depth ? join(stageBase, subpath) : stageBase, stagedEntry, subpath };
        snapshots.push(snapshot);
        if (entry.isFile()) files.push(snapshot);
      } finally { try { await outputHandle?.close(); } finally { await handle?.close(); } }
    }

    try {
      await checkSource();
      await checkTarget();
      await requireMissing(join(targetBase, name));
      signal?.throwIfAborted();
      await mkdir(temporary, { mode:0o700 });
      stageEntry = await lstat(temporary);
      if (linux) stage = await open(temporary, readFlags | constants.O_DIRECTORY);
      if (!stageEntry.isDirectory() || stage && (!sameEntry(stageEntry, await stage.stat()) || await realpath(descriptorPath(stage)) !== stagedPath)) throw fault('changed');
      stageBase = stage ? descriptorPath(stage) + '/.' : temporary;
      // The trailing dot makes lstat see the staging directory itself, not the procfs link.
      await walk(file.absolutePath, file.absolutePath, stageBase, '', 0);
      await checkSource();
      await checkTarget();
      for (const item of files) {
        signal?.throwIfAborted();
        const registration = join(stagedPath, item.subpath);
        await noSymlinks(target.absolutePath, registration);
        if (!unchangedEntry(item.stagedEntry, await lstat(registration, exactStats))) throw fault('changed');
        signal?.throwIfAborted();
        await registerUpload(registration, signal);
      }
      await checkSource();
      for (const item of snapshots) {
        signal?.throwIfAborted();
        await noSymlinks(file.root, item.absolute);
        if (!unchangedEntry(item.sourceEntry, await lstat(item.absolute, exactStats)) ||
            !unchangedEntry(item.stagedEntry, await lstat(item.output, exactStats))) throw fault('changed');
      }
      await checkTarget();
      if (!sameEntry(stageEntry, await lstat(temporary))) throw fault('changed');
      signal?.throwIfAborted();
      await publishFolder(parent, stage, target.absolutePath, stagedName, name);
      published = true;
      return { path:destination, name };
    } finally {
      try {
        if (!published && stageEntry?.isDirectory()) {
          const current = await lstat(temporary).catch(error => { if (error.code !== 'ENOENT') throw error; });
          if (current && sameEntry(stageEntry, current)) await rm(temporary, { recursive:true, force:true });
        }
      } finally { try { await stage?.close(); } finally { await parent?.close(); } }
    }
  }
  async function copyToChat(file, request, signal) {
    const { sessionId, targetSessionId, path } = request;
    if (relative(file.root, file.absolutePath) === '') throw fault('copy_workspace_root');
    version(file, request.version);
    const folder = copyFolder(request.targetFolder);
    const target = await locate(targetSessionId, folder, signal, true, true);
    if (target.info.type !== 'directory') throw fault('not_directory');
    const name = nameOf(basename(file.absolutePath));
    if (file.info.type === 'directory') return copyTree(file, target, folder, name, request, signal);
    checkCopyRetention(file.absolutePath, target.absolutePath);
    const flags = constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0);
    const source = await open(file.absolutePath, flags | (constants.O_NONBLOCK ?? 0));
    let directory;
    try {
      const opened = await source.stat();
      if (!opened.isFile()) throw fault('not_file');
      const targetEntry = await lstat(target.absolutePath);
      if (targetEntry.isSymbolicLink()) throw fault('symlink');
      // The Linux runtime pins the destination directory for staging, publication and cleanup.
      if (process.platform === 'linux') directory = await open(target.absolutePath, flags | constants.O_DIRECTORY);
      const parent = directory ? descriptorPath(directory) : target.absolutePath;
      const stagedName = '.gateway-copy-' + randomUUID();
      const temporary = join(parent, stagedName), destination = join(parent, name);
      const checkSource = async () => {
        const fresh = await locate(sessionId, path, signal, true, true);
        if (fresh.absolutePath !== file.absolutePath || fresh.root !== file.root) throw fault('changed');
        const current = await lstat(fresh.absolutePath);
        if (current.isSymbolicLink()) throw fault('symlink');
        if (!sameEntry(current, opened) || current.size !== opened.size || current.mtimeMs !== opened.mtimeMs || current.ctimeMs !== opened.ctimeMs ||
          directory && await realpath(descriptorPath(source)) !== file.absolutePath) throw fault('changed');
        version(fresh, request.version);
      };
      const checkTarget = async () => {
        const fresh = await locate(targetSessionId, folder, signal, true, true);
        if (fresh.root !== target.root || fresh.absolutePath !== target.absolutePath || fresh.info.type !== 'directory') throw fault('changed');
        const current = await lstat(fresh.absolutePath);
        if (current.isSymbolicLink()) throw fault('symlink');
        if (!sameEntry(current, targetEntry) || directory && (!sameEntry(current, await directory.stat()) || await realpath(parent) !== target.absolutePath)) throw fault('changed');
      };
      await checkSource();
      await checkTarget();
      signal?.throwIfAborted();
      try { await lstat(destination); throw fault('EEXIST'); } catch (error) { if (error.code !== 'ENOENT') throw error; }
      const staging = await open(temporary, 'wx', 0o600);
      try {
        await staging.writeFile(source.createReadStream({ autoClose:false, signal }), { signal });
        await checkSource();
        await checkTarget();
        signal?.throwIfAborted();
        // Upload retention tracks the inode, which is unchanged when the staged copy is linked.
        await registerUpload(join(target.absolutePath, stagedName));
        await checkSource();
        await checkTarget();
        const entry = await lstat(temporary);
        if (!entry.isFile() || !sameEntry(entry, await staging.stat())) throw fault('changed');
        signal?.throwIfAborted();
        // Exclusive publication leaves the source and any existing destination untouched.
        await link(temporary, destination);
        return { path:join(target.absolutePath, name), name };
      } finally {
        try { await staging.close(); } finally { await unlink(temporary).catch(error => { if (error.code !== 'ENOENT') throw error; }); }
      }
    } finally {
      try { await source.close(); } finally { await directory?.close(); }
    }
  }
  async function operation(request, signal) {
    if (!request || typeof request !== 'object' || Array.isArray(request)) throw fault('invalid_request');
    const { action, sessionId, path } = request;
    const copying = action === 'copyToChat';
    const fields = copying ? ['action','sessionId','path','version','targetSessionId','targetFolder'] : ['action','sessionId','path','version','name','text','base64'];
    if (Object.keys(request).some(key => !fields.includes(key)) || !['inspect','read','save','rename','delete','mkdir','upload','copyToChat'].includes(action)) throw fault('invalid_request');
    if (copying && !validSessionId(request.targetSessionId)) throw fault('invalid_request');
    const file = await locate(sessionId, path, signal, copying || ['inspect','mkdir','upload'].includes(action), copying);
    if (copying) return copyToChat(file, request, signal);
    if (action === 'inspect') return summary(file);
    if (action === 'read') {
      if (!summary(file).editable) throw fault('not_editable');
      return { ...summary(file), text: await fs.readText(file.target, signal, TEXT_LIMIT) };
    }
    signal?.throwIfAborted();
    if (action === 'mkdir' || action === 'upload') {
      if (file.info.type !== 'directory') throw fault('not_directory');
      const destination = join(file.absolutePath, nameOf(request.name));
      if (action === 'mkdir') await mkdir(destination);
      else {
        if (typeof request.base64 !== 'string' || request.base64.length > Math.ceil(UPLOAD_LIMIT / 3) * 4 || request.base64.length % 4) throw fault('upload_too_large');
        const bytes = Buffer.from(request.base64, 'base64');
        if (bytes.length > UPLOAD_LIMIT || bytes.toString('base64') !== request.base64) throw fault('upload_too_large');
        const temporary = join(file.absolutePath, '.gateway-upload-' + randomUUID());
        try {
          await writeFile(temporary, bytes, { flag: 'wx', mode: 0o600, signal });
          signal?.throwIfAborted();
          await link(temporary, destination);
            try { await registerUpload(destination); } catch (error) { await unlink(destination); throw error; }
        } finally { await unlink(temporary).catch(error => { if (error.code !== 'ENOENT') throw error; }); }
      }
      return { path: destination, name: request.name };
    }
    version(file, request.version);
    if (action === 'save') {
      if (!summary(file).editable || typeof request.text !== 'string' || Buffer.byteLength(request.text) > TEXT_LIMIT || request.text.includes('\0')) throw fault('not_editable');
      const result = await fs.writeText(file.target, request.text, { kind: 'replaceIfVersion', version: request.version }, signal);
      return { ...summary(file), version: result.version };
    }
    if (action === 'delete') {
      if (file.info.type === 'directory') await rmdir(file.absolutePath);
      else await unlink(file.absolutePath);
      return { path: file.absolutePath, deleted: true };
    }
    const destination = join(dirname(file.absolutePath), nameOf(request.name));
    if (destination === file.absolutePath) return summary(file);
    try { await lstat(destination); throw fault('exists'); } catch (error) { if (error.code !== 'ENOENT') throw error; }
    if (file.info.type === 'file') {
      // Exclusive publication cannot overwrite an existing destination.
      await link(file.absolutePath, destination);
      try { await unlink(file.absolutePath); } catch (error) { await unlink(destination); throw error; }
    } else await rename(file.absolutePath, destination);
    return { path: destination, name: request.name, old_path: file.absolutePath };
  }
  return {
    async upload({ sessionId, path, name, body, bytes }, signal) {
      if (!body || !Number.isSafeInteger(bytes) || bytes < 0 || bytes > WORKSPACE_UPLOAD_LIMIT) throw fault('stream_upload_too_large');
      nameOf(name);
      const pending = (locks.get(sessionId) ?? Promise.resolve()).catch(() => {}).then(async () => {
        const file = await locate(sessionId, path, signal, true);
        if (file.info.type !== 'directory') throw fault('not_directory');
        const destination = join(file.absolutePath, name), temporary = join(file.absolutePath, '.gateway-upload-' + randomUUID());
        const reader = body.getReader();
        const abortRead = () => { void reader.cancel(signal.reason).catch(() => {}); };
        signal?.addEventListener('abort',abortRead,{once:true});
        let received = 0;
        async function* chunks() {
          for (;;) {
            signal?.throwIfAborted();
            const next = await reader.read();
            if (next.done) break;
            received += next.value.byteLength;
            if (received > bytes || received > WORKSPACE_UPLOAD_LIMIT) throw fault('stream_upload_too_large');
            yield next.value;
          }
          if (received !== bytes) throw fault('invalid_request');
        }
        try {
          await writeFile(temporary, chunks(), { flag:'wx', mode:0o600, signal });
          signal?.throwIfAborted();
          // Revalidate the directory before exclusively publishing a completed file.
          await locate(sessionId, path, signal, true);
          await link(temporary, destination);
          try { await registerUpload(destination); } catch (error) { await unlink(destination); throw error; }
          return { path:destination, name, bytes:received };
        } finally {
          signal?.removeEventListener('abort',abortRead);
          await reader.cancel().catch(() => {});
          reader.releaseLock();
          await unlink(temporary).catch(error => { if (error.code !== 'ENOENT') throw error; });
        }
      });
      locks.set(sessionId, pending);
      try { return await pending; } finally { if (locks.get(sessionId) === pending) locks.delete(sessionId); }
    },
    async manage(request, signal) {
      if (['inspect','read'].includes(request?.action)) return operation(request, signal);
      const key = request?.sessionId;
      const prior = locks.get(key) ?? Promise.resolve();
      const pending = prior.catch(() => {}).then(() => operation(request, signal));
      locks.set(key, pending);
      try { return await pending; } finally { if (locks.get(key) === pending) locks.delete(key); }
    },
    async download(sessionId, path, signal, preview = false) {
      const file = await locate(sessionId, path, signal);
      if (file.info.type !== 'file') throw fault('not_file');
      if (preview) {
        if (!/\.png$/i.test(file.absolutePath)) throw fault('not_file');
        const handle = await open(file.absolutePath, 'r'), header = Buffer.alloc(8);
        try { await handle.read(header, 0, 8, 0); } finally { await handle.close(); }
        if (!header.equals(Buffer.from([137,80,78,71,13,10,26,10]))) throw fault('not_file');
      }
      return { ...summary(file), body: Readable.toWeb(createReadStream(file.absolutePath, { signal })) };
    },
  };
}

export function registerWorkspaceFiles(ctx) {
  const files = createWorkspaceFiles({ fs: ctx.fs, allowedRoot:process.env.HARNESS_WORKSPACE || '/workspace', registerUpload:async (path, signal) => {
    if (!process.env.HARNESS_UPLOAD_RETENTION_URL) return;
    const timeout = AbortSignal.timeout(10000);
    const response = await fetch(process.env.HARNESS_UPLOAD_RETENTION_URL,{method:'POST',headers:{Authorization:`Bearer ${process.env.HARNESS_MODEL_KEY}`,'Content-Type':'application/json'},body:JSON.stringify({path}),signal:signal ? AbortSignal.any([signal,timeout]) : timeout});
    if (!response.ok) throw fault('unavailable');
  }, rootFor: async id => {
    if (ctx.get('remoteAccess')?.allows({kind:'session',id}) === false) throw fault('session_unavailable');
    const session = ctx.sessions.get(id);
    if (session) return ctx.workingDirectory.get(session);
    const header = (await ctx.get('sessionPersistence')?.stat(id))?.header;
    return header && (header.cwd ?? ctx.workingDirectory.defaultDirectory);
  } });
  const known = new Set(['invalid_name','invalid_request','session_unavailable','outside_workspace','symlink','not_found','not_editable','not_directory','changed','exists','upload_too_large','stream_upload_too_large','not_file','invalid_copy_folder','copy_retention','copy_descendant','copy_workspace_root','copy_limit','copy_unsupported']);
  const errorCode = error => ({ ENOENT:'not_found', EEXIST:'exists', ENOTEMPTY:'not_empty', EACCES:'permission', EPERM:'permission', ELOOP:'symlink', FS_STALE_VERSION:'changed', FS_NOT_TEXT:'not_editable' })[error?.code] ?? (known.has(error?.code) ? error.code : 'unavailable');
  ctx.effect(() => ctx.connection.fetch.register({ path:'/api/gateway-files/upload', methods:['POST'], requestBody:'streaming', async fetch(request) {
    try {
      if (request.headers.get('content-type')?.split(';')[0] !== 'application/octet-stream') throw fault('invalid_request');
      const query = new URL(request.url).searchParams;
      if ([...query.keys()].some(key => !['sessionId','path','name','bytes'].includes(key)) || ['sessionId','path','name','bytes'].some(key => query.getAll(key).length !== 1) || !/^\d+$/.test(query.get('bytes'))) throw fault('invalid_request');
      const value = await files.upload({ sessionId:query.get('sessionId'), path:query.get('path'), name:query.get('name'), bytes:Number(query.get('bytes')), body:request.body }, request.signal);
      return Response.json({ ok:true, value }, { headers:{'Cache-Control':'no-store'} });
    } catch (error) { return Response.json({ ok:false, error:{code:errorCode(error)} }, { status:error.code === 'stream_upload_too_large' ? 413 : 400, headers:{'Cache-Control':'no-store'} }); }
  } }), 'gateway: streaming workspace upload');
  ctx.effect(() => ctx.connection.fetch.register({ path:'/api/gateway-files/manage', methods:['POST'], requestBody:'buffered', async fetch(request) {
    try {
      if (request.headers.get('content-type')?.split(';')[0] !== 'application/json') throw fault('invalid_request');
      const body = await request.text();
      if (body.length > (29 << 20)) throw fault('upload_too_large');
      return Response.json({ ok:true, value:await files.manage(JSON.parse(body), request.signal) }, { headers:{'Cache-Control':'no-store'} });
    } catch (error) { return Response.json({ ok:false, error:{code:errorCode(error)} }, { headers:{'Cache-Control':'no-store'} }); }
  } }), 'gateway: workspace file operations');
  ctx.effect(() => ctx.connection.fetch.register({ path: '/api/gateway-files/download', methods: ['GET'], requestBody: 'buffered', async fetch(request) {
    try {
      const query = new URL(request.url).searchParams;
      if ([...query.keys()].some(key => !['sessionId','path'].includes(key)) || query.getAll('sessionId').length !== 1 || query.getAll('path').length !== 1) throw fault('invalid_request');
      const file = await files.download(query.get('sessionId'), query.get('path'), request.signal);
      const filename = encodeURIComponent(file.name).replace(/['()*]/g, value => '%' + value.charCodeAt(0).toString(16).toUpperCase());
      return new Response(file.body, { headers: { 'Content-Type':'application/octet-stream', 'Content-Length':String(file.bytes), 'Content-Disposition':"attachment; filename*=UTF-8''" + filename, 'Cache-Control':'no-store', 'X-Content-Type-Options':'nosniff' } });
    } catch (error) { return new Response(errorCode(error), { status: 400, headers: { 'Cache-Control':'no-store' } }); }
  } }), 'gateway: workspace file download');
  ctx.effect(() => ctx.connection.fetch.register({ path:'/api/gateway-files/preview', methods:['GET'], requestBody:'buffered', async fetch(request) {
    try {
      const query = new URL(request.url).searchParams;
      if ([...query.keys()].some(key => !['sessionId','path'].includes(key)) || query.getAll('sessionId').length !== 1 || query.getAll('path').length !== 1) throw fault('invalid_request');
      const file = await files.download(query.get('sessionId'), query.get('path'), request.signal, true);
      return new Response(file.body, { headers:{'Content-Type':'image/png','Content-Length':String(file.bytes),'Content-Disposition':'inline','Cache-Control':'private, no-cache','X-Content-Type-Options':'nosniff'} });
    } catch (error) { return new Response(errorCode(error), { status:400, headers:{'Cache-Control':'no-store'} }); }
  } }), 'gateway: workspace PNG preview');
}
