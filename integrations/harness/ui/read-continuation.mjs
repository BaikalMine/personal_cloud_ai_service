// Cursors count UTF-16 code units, as JavaScript strings do. Returned cursors
// never split a surrogate pair; line numbering retains the upstream contract.
export function readColumn(value = 0) {
  if (!Number.isSafeInteger(value) || value < 0) throw new Error('column_offset must be a non-negative safe integer');
  return value;
}

export async function buildReadWindow(chunks, request) {
  const {offset, limit, maxLineLength, maxBytes, signal} = request;
  const column = readColumn(request.columnOffset);
  const lines = [];
  let totalLines = 0, outputBytes = 0, lineLength = 0, buffer = '', lastChar = '';
  let stopped = false, nextOffset = 0, nextColumnOffset = 0;
  const capacity = Math.min(maxLineLength, maxBytes) + 2;
  const startColumn = () => totalLines + 1 === offset ? column : 0;
  function append(segment) {
    if (!stopped && totalLines + 1 >= offset && lines.length < limit) {
      const skip = Math.max(0, startColumn() - lineLength);
      if (buffer.length < capacity) buffer += segment.slice(skip, skip + capacity - buffer.length);
    }
    lineLength += segment.length;
    if (segment.length) lastChar = segment.at(-1);
  }
  function flush() {
    totalLines++;
    const logicalLength = lineLength - (lastChar === '\r' ? 1 : 0);
    if (!stopped && totalLines >= offset) {
      const from = totalLines === offset ? column : 0;
      if (from > logicalLength) throw new Error('column_offset is beyond the requested line');
      if (lines.length >= limit) {
        stopped = true; nextOffset = totalLines;
      } else {
        const remainingBytes = maxBytes - outputBytes - (lines.length ? 1 : 0);
        const available = Math.max(0, logicalLength - from);
        const candidate = buffer.slice(0, available);
        if (candidate.length && /[\uDC00-\uDFFF]/.test(candidate[0])) throw new Error('column_offset splits a Unicode character; use a returned continuation cursor');
        let used = 0, bytes = 0;
        for (const char of candidate) {
          const size = Buffer.byteLength(char);
          if (used + char.length > maxLineLength || bytes + size > remainingBytes) break;
          used += char.length; bytes += size;
        }
        if (remainingBytes < 0 || used === 0 && available > 0) {
          if (!lines.length) throw new Error('Configured read budget cannot fit one Unicode character');
          stopped = true; nextOffset = totalLines; nextColumnOffset = from;
        } else {
          lines.push({number:totalLines, text:candidate.slice(0,used)});
          outputBytes += bytes + (lines.length > 1 ? 1 : 0);
          if (used < available) {
            stopped = true; nextOffset = totalLines; nextColumnOffset = from + used;
          }
        }
      }
    }
    lineLength = 0; buffer = ''; lastChar = '';
  }
  for await (const chunk of chunks) {
    signal?.throwIfAborted();
    let start = 0, newline;
    while ((newline = chunk.indexOf('\n',start)) !== -1) {
      append(chunk.slice(start,newline)); flush(); start = newline + 1;
    }
    append(chunk.slice(start));
  }
  if (lineLength > 0) flush();
  signal?.throwIfAborted();
  if (offset > totalLines && !(offset === 1 && totalLines === 0 && column === 0)) throw new Error('offset is outside the file');
  return {lines,totalLines,columnOffset:column,complete:!stopped,nextOffset,nextColumnOffset};
}

export function formatReadWindow(path, value) {
  // Old stored tool results have no completeness fields; never certify them.
  const footer = value.complete === true
    ? `(End of file - total ${value.totalLines} lines. Requested range fully read.)`
    : value.complete === false
      ? `(PARTIAL READ. Continue with offset=${value.nextOffset}, column_offset=${value.nextColumnOffset}. Do not treat this fragment as the complete document.)`
      : '(Historical read result: completeness was not recorded. Re-read the file before revising it.)';
  const column = value.columnOffset ?? 0;
  const label = column ? `(First returned line starts at column_offset=${column}.)\n` : '';
  return `<path>${path}</path>\n<type>file</type>\n<content>\n${label}${value.lines.map(line=>`${line.number}: ${line.text}`).join('\n')}\n\n${footer}\n</content>`;
}
