import { Buffer, isUtf8 } from 'node:buffer';
import { createHash } from 'node:crypto';

const MAX_BYTES = 16 * 1024 * 1024;
const MAX_STRINGS = 128 * 1024;
const FIELDS = new Set(['mode', 'old_text', 'new_text', 'start_line', 'end_line', 'context_before', 'context_after']);

class PreciseEditError extends Error {}

function fail(code, editIndex) {
  const error = new PreciseEditError(`precise_edit_${code}`);
  error.code = error.message;
  if (editIndex !== undefined) error.edit_index = editIndex;
  throw error;
}

function validText(text) {
  for (let i = 0; i < text.length; i++) {
    const unit = text.charCodeAt(i);
    if (unit === 0) return false;
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = text.charCodeAt(++i);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
    } else if (unit >= 0xdc00 && unit <= 0xdfff) return false;
  }
  return true;
}

function validateEdits(edits) {
  if (!Array.isArray(edits) || edits.length < 1 || edits.length > 64) fail('invalid_edits');
  let stringBytes = 0;
  return Array.from({ length: edits.length }, (_, index) => {
    const edit = edits[index];
    if (!edit || typeof edit !== 'object' || Array.isArray(edit)) fail('invalid_edit', index);
    const descriptors = Object.getOwnPropertyDescriptors(edit);
    const fields = Reflect.ownKeys(descriptors);
    if (fields.some(key => !FIELDS.has(key) || !Object.hasOwn(descriptors[key], 'value')) ||
        !Object.hasOwn(descriptors, 'new_text')) {
      fail('invalid_edit', index);
    }
    const value = Object.fromEntries(fields.map(key => [key, descriptors[key].value]));
    if (Object.hasOwn(value, 'mode')) {
      if (value.mode !== 'replace_lines' || ['old_text', 'context_before', 'context_after'].some(key => Object.hasOwn(value, key))) {
        fail('invalid_edit', index);
      }
      if (!Object.hasOwn(value, 'start_line') || !Object.hasOwn(value, 'end_line')) fail('invalid_range', index);
    } else if (!Object.hasOwn(value, 'old_text')) fail('invalid_edit', index);
    for (const key of ['old_text', 'new_text', 'context_before', 'context_after']) {
      if (!Object.hasOwn(value, key)) continue;
      const text = value[key];
      if (typeof text !== 'string' || (key === 'old_text' && text.length === 0)) fail('invalid_text', index);
      if (text.length > MAX_STRINGS || (stringBytes += Buffer.byteLength(text, 'utf8')) > MAX_STRINGS) {
        fail('strings_too_large', index);
      }
      if (!validText(text)) fail('invalid_text', index);
    }
    if (value.old_text === value.new_text) fail('unchanged', index);
    for (const key of ['start_line', 'end_line']) {
      if (Object.hasOwn(value, key) && (!Number.isSafeInteger(value[key]) || value[key] < 1)) {
        fail('invalid_range', index);
      }
    }
    if (Object.hasOwn(value, 'end_line') && !Object.hasOwn(value, 'start_line')) fail('invalid_range', index);
    if (value.start_line !== undefined) {
      value.end_line ??= value.start_line;
      if (value.end_line < value.start_line) fail('invalid_range', index);
    }
    return { ...value, index };
  });
}

function lineStarts(bytes) {
  let count = 1;
  for (const byte of bytes) if (byte === 10) count++;
  const starts = new Uint32Array(count);
  let line = 1;
  for (let i = 0; i < bytes.length; i++) if (bytes[i] === 10) starts[line++] = i + 1;
  return starts;
}

function lineAt(starts, offset) {
  let low = 0, high = starts.length;
  while (low < high) {
    const mid = low + Math.floor((high - low) / 2);
    if (starts[mid] <= offset) low = mid + 1;
    else high = mid;
  }
  return low;
}

const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');

/**
 * Pure, synchronous planner. No IO, persistence, or normalization is performed.
 * Input: Buffer/Uint8Array, <=16 MiB, valid UTF-8 without NUL; 1..64 edits.
 * All supplied edit strings together may occupy at most 128 KiB in UTF-8.
 * Bounds are inclusive and one-based. Literal bounds must contain every byte
 * of old_text; replace_lines instead selects the entire bounded span, except
 * for the leading BOM and the final line's terminator.
 * start_line alone selects one line; end_line alone is invalid. LF starts a
 * new line (also in CRLF); newline bytes belong to the preceding line, bare CR
 * is ordinary content. Contexts are exact adjacent text, not part of the span,
 * and may extend beyond the line bounds. Empty contexts impose no restriction.
 * All matches use the original snapshot; overlapping spans fail, adjacency is OK.
 * Changes are byte-sorted, end_byte is exclusive, and line metadata describes
 * original matched bytes. The returned Buffer is independently owned.
 * Errors have identical sanitized message/code and optional zero-based edit_index:
 * precise_edit_{invalid_bytes,file_too_large,invalid_utf8,invalid_edits,
 * invalid_edit,invalid_text,strings_too_large,unchanged,invalid_range,
 * not_found,ambiguous,overlap,invalid_input}. No partial result is returned.
 * Callers own version storage and must verify before_sha256 before committing.
 */
export function planPreciseEdits(bytes, edits) {
  try {
    return plan(bytes, edits);
  } catch (error) {
    if (error instanceof PreciseEditError) throw error;
    fail('invalid_input');
  }
}

function plan(bytes, edits) {
  if (!(bytes instanceof Uint8Array)) fail('invalid_bytes');
  if (bytes.byteLength > MAX_BYTES) fail('file_too_large');
  const before = Buffer.from(bytes);
  if (!isUtf8(before)) fail('invalid_utf8');
  if (before.includes(0)) fail('invalid_text');
  const validated = validateEdits(edits);
  const starts = lineStarts(before);
  let rangeBytes = 0;
  const matches = validated.map(edit => {
    let lower = 0, upper = before.length;
    if (edit.start_line !== undefined) {
      if (edit.start_line > starts.length || edit.end_line > starts.length) fail('invalid_range', edit.index);
      lower = starts[edit.start_line - 1];
      upper = edit.end_line === starts.length ? before.length : starts[edit.end_line];
    }
    if (edit.mode === 'replace_lines') {
      if (edit.end_line === starts.length && starts[edit.end_line - 1] === before.length) fail('invalid_range', edit.index);
      if (lower === 0 && before[0] === 0xef && before[1] === 0xbb && before[2] === 0xbf) lower = 3;
      if (edit.end_line < starts.length) {
        upper--;
        if (upper > lower && before[upper - 1] === 13) upper--;
      }
      rangeBytes += upper - lower;
      if (rangeBytes > MAX_STRINGS) fail('strings_too_large', edit.index);
      const old = before.toString('utf8', lower, upper);
      if (old === edit.new_text) fail('unchanged', edit.index);
      return {
        start_byte: lower, end_byte: upper, start_line: edit.start_line, end_line: edit.end_line,
        old_text: old, new_text: edit.new_text, index: edit.index,
      };
    }
    const old = Buffer.from(edit.old_text, 'utf8');
    const prefix = Buffer.from(edit.context_before ?? '', 'utf8');
    const suffix = Buffer.from(edit.context_after ?? '', 'utf8');
    // Search the complete contextual needle to avoid rescanning long contexts
    // at every occurrence of a short, repeated old_text.
    const needle = Buffer.concat([prefix, old, suffix]);
    let cursor = Math.max(0, lower - prefix.length), found = -1;
    while (cursor <= upper - old.length - prefix.length) {
      const hit = before.indexOf(needle, cursor);
      if (hit < 0) break;
      const start = hit + prefix.length;
      if (start + old.length > upper) break;
      if (found !== -1) fail('ambiguous', edit.index);
      found = start;
      cursor = hit + 1;
    }
    if (found === -1) fail('not_found', edit.index);
    return {
      start_byte: found,
      end_byte: found + old.length,
      start_line: lineAt(starts, found),
      end_line: lineAt(starts, found + old.length - 1),
      old_text: edit.old_text,
      new_text: edit.new_text,
      index: edit.index,
    };
  }).sort((a, b) => a.start_byte - b.start_byte || a.index - b.index);
  for (let i = 1; i < matches.length; i++) {
    if (matches[i].start_byte === matches[i - 1].start_byte || matches[i].start_byte < matches[i - 1].end_byte) fail('overlap', matches[i].index);
  }
  const chunks = [];
  let cursor = 0;
  for (const match of matches) {
    chunks.push(before.subarray(cursor, match.start_byte), Buffer.from(match.new_text, 'utf8'));
    cursor = match.end_byte;
  }
  chunks.push(before.subarray(cursor));
  const after = Buffer.concat(chunks);
  return {
    after,
    changes: matches.map(({ index, ...change }) => change),
    before_sha256: sha256(before),
    after_sha256: sha256(after),
  };
}
