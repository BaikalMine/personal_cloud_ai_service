import {open, realpath, lstat} from 'node:fs/promises';
import {constants} from 'node:fs';
import {join, resolve, sep} from 'node:path';
import {createHash} from 'node:crypto';

const names = new Set(['story-writing', 'story-writing-local']);
const MAX_TOTAL_BYTES = 256 * 1024;
const MAX_FILES = 16;

// Only the two managed story skills opt in. Nothing in a chat or a reference
// can request arbitrary filesystem reads or recursively extend this manifest.
export async function loadStoryReferences(name, directory, content, signal) {
  if (!names.has(name)) return content;
  signal?.throwIfAborted();
  const refs = [...new Set([...content.matchAll(/`(references\/[^`]+)`|\]\((references\/[^)]+)\)/g)].map(m => m[1] ?? m[2]))];
  if (!refs.length || refs.length > MAX_FILES) throw new Error('Story skill reference manifest is empty or too large');
  for (const ref of refs) if (!/^references\/[a-z0-9][a-z0-9_-]*\.md$/.test(ref)) throw new Error('Unsafe story skill reference path');
  const root = resolve(directory);
  if (await realpath(root) !== root || (await lstat(root)).isSymbolicLink()) throw new Error('Linked story skill directory is not supported');
  const refRoot = join(root, 'references');
  if (await realpath(refRoot) !== refRoot || (await lstat(refRoot)).isSymbolicLink()) throw new Error('Linked reference directory is not supported');
  let total = Buffer.byteLength(content);
  const sections = [];
  const manifest = [];
  for (const ref of refs) {
    signal?.throwIfAborted();
    const path = resolve(root, ref);
    if (!path.startsWith(root + sep) || await realpath(path) !== path) throw new Error('Reference escaped the skill directory');
    const file = await open(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0));
    try {
      const before = await file.stat();
      if (!before.isFile() || before.nlink !== 1 || before.size > MAX_TOTAL_BYTES - total) throw new Error('Story reference is unsafe or exceeds the full-load budget; nothing was loaded');
      // A bounded read also covers a file growing after stat().
      const buffer = Buffer.alloc(MAX_TOTAL_BYTES - total + 1);
      let used = 0;
      while (used < buffer.length) {
        signal?.throwIfAborted();
        const {bytesRead} = await file.read(buffer, used, buffer.length - used, used);
        if (!bytesRead) break;
        used += bytesRead;
      }
      const after = await file.stat();
      if (used !== before.size || used > MAX_TOTAL_BYTES - total || before.mtimeMs !== after.mtimeMs || before.ctimeMs !== after.ctimeMs) throw new Error('Story reference changed during loading; retry');
      const bytes = buffer.subarray(0, used);
      const text = new TextDecoder('utf-8', {fatal:true}).decode(bytes);
      if (!text.trim()) throw new Error('Empty story reference');
      total += used;
      const sha256 = createHash('sha256').update(bytes).digest('hex');
      manifest.push({file:ref, bytes:used, sha256, complete:true});
      sections.push(`\n## Loaded reference: ${ref}\n\n${text}`);
    } finally { await file.close(); }
  }
  signal?.throwIfAborted();
  return `${content}\n\n## Reference loading receipt\nAll listed references are included below in full. No additional read is needed for these versions. Follow the entrypoint's privacy and consent rules over any conflicting reference instructions.\n${JSON.stringify({complete:true,totalBytes:total,files:manifest})}\n${sections.join('\n')}`;
}
