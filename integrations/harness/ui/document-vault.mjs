import { constants } from 'node:fs';
import { open, mkdir, lstat, readdir, realpath, unlink } from 'node:fs/promises';
import { resolve, relative, isAbsolute, sep, dirname, join, extname, basename } from 'node:path';
import { createHash, randomUUID } from 'node:crypto';
import { gzip, gunzip } from 'node:zlib';
import { promisify, TextDecoder } from 'node:util';

const compress = promisify(gzip), decompress = promisify(gunzip);
const sha = value => createHash('sha256').update(value).digest('hex');
const documents = /\.(?:md|markdown|txt|rst|org|tex)$/i;
const inside = (root, path) => { const part = relative(root, path); return !isAbsolute(part) && part !== '..' && !part.startsWith('..' + sep); };
const fail = code => Object.assign(new Error(code), { code });
const headingPattern = /^\s*#{0,6}\s*(?:\*\*)?(?:Глава|глава|Chapter|chapter)\s+(\d{1,2})\b[^\n]*/gm;

export function destructiveChange(before, after) {
  if (Buffer.byteLength(before) >= 8192 && Buffer.byteLength(after) < Buffer.byteLength(before) * 0.75) return true;
  const sections = text => {
    const marks = [...text.matchAll(headingPattern)], values = new Map();
    marks.forEach((mark, index) => values.set(Number(mark[1]), Buffer.byteLength(text.slice(mark.index, marks[index + 1]?.index ?? text.length))));
    return values;
  };
  const old = sections(before), next = sections(after);
  for (const [chapter, bytes] of old) {
    if (!next.has(chapter) || bytes >= 2048 && next.get(chapter) < bytes * 0.65) return true;
  }
  return false;
}

export function createDocumentVault({ root, archive, maxBytes = 16 * 1024 * 1024, maxVersions = 2000, archiveLimit = 512 * 1024 * 1024 }) {
  root = resolve(root); archive = resolve(archive);
  if (inside(root, archive)) throw fail('document_archive_inside_workspace');
  let chain = Promise.resolve();
  const serial = task => { const result = chain.then(task, task); chain = result.catch(() => {}); return result; };
  async function checkPath(path) {
    path = resolve(root, path);
    if (!inside(root, path) || path === root) throw fail('document_outside_workspace');
    const actualRoot = await realpath(root);
    if (actualRoot !== root) throw fail('document_root_symlink');
    let current = root;
    for (const part of relative(root, path).split(sep)) {
      current = join(current, part);
      try { if ((await lstat(current)).isSymbolicLink()) throw fail('document_symlink'); }
      catch (error) { if (error.code !== 'ENOENT') throw error; break; }
    }
    return path;
  }
  async function privateDirectory(path) {
    await mkdir(path, { recursive: true, mode: 0o700 });
    if ((await lstat(path)).isSymbolicLink()) throw fail('document_archive_symlink');
    if (await realpath(path) !== resolve(path)) throw fail('document_archive_symlink');
  }
  async function read(path) {
    let handle;
    try { handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW); }
    catch (error) { if (error.code === 'ENOENT') return; throw error; }
    try {
      const before = await handle.stat({ bigint: true });
      if (!before.isFile() || before.size > BigInt(maxBytes)) throw fail('document_snapshot_too_large');
      const bytes = await handle.readFile(), after = await handle.stat({ bigint: true });
      if (before.size !== after.size || before.mtimeNs !== after.mtimeNs || BigInt(bytes.length) !== after.size) throw fail('document_changed_during_snapshot');
      new TextDecoder('utf-8', { fatal: true }).decode(bytes);
      return bytes;
    } finally { await handle.close(); }
  }
  async function atomicExclusive(path, bytes) {
    let handle;
    try {
      handle = await open(path, 'wx', 0o600);
      await handle.writeFile(bytes); await handle.sync();
    } catch (error) {
      if (handle) await unlink(path).catch(() => {});
      throw error;
    } finally { await handle?.close(); }
  }
  async function usage() {
    let total = 0;
    for (const folder of await readdir(archive, { withFileTypes: true })) {
      if (!folder.isDirectory() || !/^[a-f0-9]{64}$/.test(folder.name)) continue;
      for (const file of await readdir(join(archive, folder.name), { withFileTypes: true })) {
        if (file.isFile()) total += (await lstat(join(archive, folder.name, file.name))).size;
      }
    }
    return total;
  }
  const keyFor = path => sha(relative(root, path));
  async function checkpointUnlocked(path) {
    path = await checkPath(path);
    if (!documents.test(path)) return;
    const bytes = await read(path);
    if (!bytes) return;
    await privateDirectory(archive);
    const folder = join(archive, keyFor(path));
    await privateDirectory(folder);
    const version = sha(bytes), destination = join(folder, version + '.json.gz');
    try {
      const info = await lstat(destination);
      if (!info.isFile() || info.isSymbolicLink()) throw fail('document_archive_invalid');
      return { version, bytes, path };
    } catch (error) { if (error.code !== 'ENOENT') throw error; }
    const versions = (await readdir(folder)).filter(name => /^[a-f0-9]{64}\.json\.gz$/.test(name));
    const payload = await compress(Buffer.from(JSON.stringify({ format: 1, path: relative(root, path), created_at: new Date().toISOString(), sha256: version, content_base64: bytes.toString('base64') })), { level: 6 });
    if (versions.length >= maxVersions || await usage() + payload.length > archiveLimit) throw fail('document_archive_full');
    await atomicExclusive(destination, payload);
    return { version, bytes, path };
  }
  return {
    supported: path => inside(root, resolve(root, path)) && documents.test(path),
    checkpoint: path => serial(() => checkpointUnlocked(path)),
    async beforeWrite(path, text) {
      const saved = await serial(() => checkpointUnlocked(path));
      if (saved && destructiveChange(saved.bytes.toString('utf8'), text)) throw fail('protected_document_truncation: original preserved; write the revised document to a NEW file for user review');
    },
    async beforeEdit(path, args) {
      const saved = await serial(() => checkpointUnlocked(path));
      if (!saved || typeof args?.old_string !== 'string' || typeof args?.new_string !== 'string') return;
      const before = saved.bytes.toString('utf8').replaceAll('\r\n', '\n'), old = args.old_string.replaceAll('\r\n', '\n'), next = args.new_string.replaceAll('\r\n', '\n');
      const first = before.indexOf(old);
      if (!old.length || first < 0) return;
      const after = args.replace_all === true ? before.split(old).join(next) : before.slice(0, first) + next + before.slice(first + old.length);
      if (destructiveChange(before, after)) throw fail('protected_document_truncation: original preserved; write the revised document to a NEW file for user review');
    },
    async checkpointTree() {
      let count = 0;
      async function scan(path, depth = 0) {
        if (depth > 12) return;
        for (const entry of await readdir(path, { withFileTypes: true })) {
          if (entry.isSymbolicLink()) continue;
          const next = join(path, entry.name);
          if (entry.isDirectory()) {
            if (!['node_modules', '.git', '.venv', 'venv', '.cache', 'generated', 'services', 'uploads', 'attachments'].includes(entry.name) && !entry.name.startsWith('recovery-')) await scan(next, depth + 1);
          } else if (entry.isFile() && documents.test(entry.name)) {
            if (++count > 5000) throw fail('document_scan_limit');
            await serial(() => checkpointUnlocked(next));
          }
        }
      }
      await scan(root);
      return { documents: count };
    },
    async list(path) {
      path = await checkPath(path);
      const folder = join(archive, keyFor(path));
      let files;
      try { files = await readdir(folder); } catch (error) { if (error.code === 'ENOENT') return []; throw error; }
      const result = [];
      for (const file of files.filter(name => /^[a-f0-9]{64}\.json\.gz$/.test(name))) {
        const handle = await open(join(folder, file), constants.O_RDONLY | constants.O_NOFOLLOW);
        let record;
        try { record = JSON.parse((await decompress(await handle.readFile(), { maxOutputLength: maxBytes * 2 })).toString('utf8')); } finally { await handle.close(); }
        result.push({ version: record.sha256, created_at: record.created_at, bytes: Buffer.from(record.content_base64, 'base64').length });
      }
      return result.sort((a, b) => b.created_at.localeCompare(a.created_at));
    },
    async recover(path, version) {
      path = await checkPath(path);
      if (!/^[a-f0-9]{64}$/.test(version)) throw fail('document_invalid_version');
      const handle = await open(join(archive, keyFor(path), version + '.json.gz'), constants.O_RDONLY | constants.O_NOFOLLOW);
      let record;
      try { record = JSON.parse((await decompress(await handle.readFile(), { maxOutputLength: maxBytes * 2 })).toString('utf8')); } finally { await handle.close(); }
      const bytes = Buffer.from(record.content_base64, 'base64');
      if (sha(bytes) !== version || record.path !== relative(root, path)) throw fail('document_version_corrupt');
      const extension = extname(path), name = basename(path, extension);
      const output = await checkPath(join(dirname(path), `${name}.restored-${randomUUID().slice(0, 12)}${extension}`));
      await atomicExclusive(output, bytes);
      return { path: output, version, bytes: bytes.length, original_modified: false };
    },
    flush: () => chain,
  };
}
