import { lstat, mkdir, open, readFile, readdir, realpath, link, unlink } from 'node:fs/promises';
import { join, relative, isAbsolute, sep } from 'node:path';
import { randomUUID } from 'node:crypto';
import { normalizeImageOutputName } from './image-filenames.mjs';

const inside = (root, path) => { const part = relative(root, path); return part === '' || part !== '..' && !part.startsWith('..' + sep) && !isAbsolute(part); };

export async function saveUltraImage(cwd, id, index, body, signal, folder = 'krea-ultra', outputName) {
  if (!['krea-ultra', 'moss-image'].includes(folder)) throw new Error('Invalid image pipeline folder');
  if (!/^[a-f0-9-]{36}$/.test(id) || !Number.isSafeInteger(index) || index < 0 || index > 63) throw new Error('Invalid Ultra output identity');
  const defaultStem = folder === 'moss-image' ? 'moss-image' : 'krea2-image';
  const stem = normalizeImageOutputName(outputName, defaultStem);
  signal?.throwIfAborted();
  const root = await realpath(cwd);
  let directory = root;
  for (const part of ['generated', folder, id]) {
    directory = join(directory, part);
    await mkdir(directory, { mode: 0o700 }).catch(error => { if (error.code !== 'EEXIST') throw error; });
    const info = await lstat(directory);
    if (info.isSymbolicLink() || !info.isDirectory() || !inside(root, await realpath(directory))) throw new Error('Unsafe Ultra output directory');
  }
  const suffix = `--${index + 1}.png`;
  const existing = (await readdir(directory)).filter(name => name === `${index + 1}.png` || name.endsWith(suffix));
  if (existing.length > 1) throw new Error('Multiple Ultra outputs exist for the same index');
  const filename = existing[0] ?? `${stem}${suffix}`;
  const path = join(directory, filename), temporary = join(directory, `.saving-${randomUUID()}`);
  try {
    const handle = await open(temporary, 'wx', 0o600);
    try { await handle.writeFile(body, { signal }); await handle.sync(); } finally { await handle.close(); }
    signal?.throwIfAborted();
    try { await link(temporary, path); }
    catch (error) {
      if (error.code !== 'EEXIST') throw error;
      const info = await lstat(path);
      if (!info.isFile() || info.isSymbolicLink() || !Buffer.from(await readFile(path)).equals(Buffer.from(body))) throw new Error('Ultra output already exists with different contents');
    }
  } finally { await unlink(temporary).catch(error => { if (error.code !== 'ENOENT') throw error; }); }
  const savedStem = filename.endsWith(suffix) ? filename.slice(0, -suffix.length) : defaultStem;
  return { path, file_name: filename, description: savedStem !== defaultStem ? savedStem.replace(/[-_]+/g, ' ') + (index ? ` (${index + 1})` : '') : `${folder === 'moss-image' ? 'MOSS Image' : 'Krea2 Ultra'} ${index + 1}` };
}
