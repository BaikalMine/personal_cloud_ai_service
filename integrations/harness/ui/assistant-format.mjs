import { isGeneratedImagePath } from './generated-image-path.mjs';

export function reconcilePresentedText(text, presented) {
  const known = new Map((presented || []).filter(file => isGeneratedImagePath(file.path)).map(file => [file.path, file]));
  const recovered = new Map(), fences = [];
  let offset = 0, fence;
  for (const line of text.split(/(?<=\n)/)) {
    const marker = line.replace(/\r?\n$/, '').match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
    if (marker && !fence) fence = { start: offset, char: marker[1][0], length: marker[1].length };
    else if (marker && fence && marker[1][0] === fence.char && marker[1].length >= fence.length && !marker[2].trim()) { fences.push([fence.start, offset + line.length]); fence = undefined; }
    offset += line.length;
  }
  if (fence) fences.push([fence.start, text.length]);
  let malformed = false;
  const output = text.replace(/(^|\n)[\t ]*<tool_code>([\s\S]*?)<\/tool_code>[\t ]*(?=\n|$)/g, (whole, prefix, body, index) => {
    const tagIndex=index+prefix.length;
    if (fences.some(([start, end]) => tagIndex >= start && tagIndex < end)) return whole;
    const call = body.trim().match(/^present\s*\(\s*files\s*=\s*(\[[\s\S]*\])\s*\)$/);
    let files;
    try { if (call && call[1].length <= 32768) files = JSON.parse(call[1]); } catch {}
    if (!Array.isArray(files) || !files.length || files.length > 8 || files.some(file => !file || typeof file !== 'object' || Array.isArray(file) || Object.keys(file).some(key => !['path','description'].includes(key)) || !known.has(file.path))) {
      malformed = true;
      return whole;
    }
    for (const file of files) recovered.set(file.path, known.get(file.path));
    return prefix;
  });
  return { text: output, files: [...recovered.values()], malformed };
}
