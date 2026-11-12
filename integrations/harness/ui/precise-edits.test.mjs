import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { test } from 'node:test';
import { planPreciseEdits } from './precise-edits.mjs';

const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const edit = (old_text, new_text, extra = {}) => ({ old_text, new_text, ...extra });

test('explicit line spans preserve BOM, outer separators and final newline state', () => {
  const range = (start_line, end_line, new_text) => ({mode:'replace_lines', start_line, end_line, new_text});
  const before = Buffer.from('\ufeffalpha\r\n\r\ncharlie\r\nlast');
  const result = planPreciseEdits(before, [range(1, 1, 'A'), range(2, 2, 'B'), edit('charlie', 'C'), range(4, 4, 'D')]);
  assert.equal(result.after.toString(), '\ufeffA\r\nB\r\nC\r\nD');
  assert.equal(planPreciseEdits(Buffer.from('old\r\n'), [range(1, 1, 'new')]).after.toString(), 'new\r\n');
  assert.throws(() => planPreciseEdits(Buffer.from('old\n'), [range(2, 2, 'new')]), /precise_edit_invalid_range/);
  assert.throws(() => planPreciseEdits(before, [range(1, 3, 'X'), edit('charlie', 'C')]), /precise_edit_overlap/);
  assert.throws(() => planPreciseEdits(before, [range(2, 2, 'X'), range(2, 2, 'Y')]), /precise_edit_overlap/);
  assert.throws(() => planPreciseEdits(before, [{...range(1, 2, 'X'), old_text:'alpha'}]), /precise_edit_invalid_edit/);
  assert.throws(() => planPreciseEdits(before, [{mode:'replace_lines',start_line:1,new_text:'X'}]), /precise_edit_invalid_range/);
});
const run = (text, edits) => planPreciseEdits(Buffer.from(text, 'utf8'), edits);

function rejects(bytes, edits, code, index) {
  const original = bytes instanceof Uint8Array ? Buffer.from(bytes) : undefined;
  assert.throws(() => planPreciseEdits(bytes, edits), error => {
    assert.equal(error.code, `precise_edit_${code}`);
    assert.equal(error.message, error.code);
    assert.equal(error.edit_index, index);
    assert.deepEqual(Object.keys(error).sort(), index === undefined ? ['code'] : ['code', 'edit_index']);
    return true;
  });
  if (original) assert.deepEqual(Buffer.from(bytes), original);
}

function verifySpans(before, result) {
  assert(Buffer.isBuffer(result.after));
  assert.equal(result.before_sha256, hash(before));
  assert.equal(result.after_sha256, hash(result.after));
  let oldCursor = 0, newCursor = 0;
  for (const change of result.changes) {
    assert.deepEqual(Object.keys(change).sort(), ['end_byte', 'end_line', 'new_text', 'old_text', 'start_byte', 'start_line']);
    assert(change.start_byte >= oldCursor);
    assert(change.end_byte > change.start_byte);
    const gap = change.start_byte - oldCursor;
    assert.deepEqual(result.after.subarray(newCursor, newCursor + gap), before.subarray(oldCursor, change.start_byte));
    newCursor += gap;
    assert.deepEqual(before.subarray(change.start_byte, change.end_byte), Buffer.from(change.old_text));
    const replacement = Buffer.from(change.new_text);
    assert.deepEqual(result.after.subarray(newCursor, newCursor + replacement.length), replacement);
    const lineAt = offset => 1 + before.subarray(0, offset).filter(byte => byte === 10).length;
    assert.equal(change.start_line, lineAt(change.start_byte));
    assert.equal(change.end_line, lineAt(change.end_byte - 1));
    oldCursor = change.end_byte;
    newCursor += replacement.length;
  }
  assert.deepEqual(result.after.subarray(newCursor), before.subarray(oldCursor));
}

test('exact byte preservation for BOM, mixed newlines, whitespace and Unicode', () => {
  const before = Buffer.from('\ufeff  alpha\t\r\n\u041f\u0440\u0438\u0432\u0435\u0442 \ud83d\ude80 cafe\u0301\nlast\rbare  \r\n');
  const original = Buffer.from(before);
  const result = planPreciseEdits(before, [edit('cafe\u0301', 'CAF\u00c9'), edit('alpha', '\ud83d\ude03\tnew\r\nline')]);
  assert.equal(result.after.toString(), '\ufeff  \ud83d\ude03\tnew\r\nline\t\r\n\u041f\u0440\u0438\u0432\u0435\u0442 \ud83d\ude80 CAF\u00c9\nlast\rbare  \r\n');
  assert.deepEqual(before, original);
  verifySpans(before, result);
});

test('byte offsets are not UTF-16 offsets and normalization is never implicit', () => {
  const before = Buffer.from('\ufeff\ud83d\ude80 e\u0301 \u00e9');
  const result = planPreciseEdits(before, [edit('\u00e9', 'E')]);
  assert.equal(result.changes[0].start_byte, Buffer.byteLength('\ufeff\ud83d\ude80 e\u0301 '));
  assert.equal(result.after.toString(), '\ufeff\ud83d\ude80 e\u0301 E');
  verifySpans(before, result);
  rejects(Buffer.from('e\u0301'), [edit('\u00e9', 'E')], 'not_found', 0);
});

test('matches are literal, with no regex, occurrence or replace-all behavior', () => {
  assert.equal(run('a .*[$] b', [edit('.*[$]', '$&\\n')]).after.toString(), 'a $&\\n b');
  for (const extra of [{ occurrence: 1 }, { occurrence_index: 0 }, { regex: false }, { replace_all: true }]) {
    rejects(Buffer.from('x'), [edit('x', 'y', extra)], 'invalid_edit', 0);
  }
});

test('contexts filter exact adjacent candidates, including boundaries and overlapping occurrences', () => {
  const result = run('LxR LxS TxR', [edit('x', 'y', { context_before: 'L', context_after: 'R' })]);
  assert.equal(result.after.toString(), 'LyR LxS TxR');
  assert.equal(run('aaa', [edit('aa', 'Z', { context_before: 'a' })]).after.toString(), 'aZ');
  assert.equal(run('x x', [edit('x', 'y', { context_before: 'x ' })]).after.toString(), 'x y');
  rejects(Buffer.from('L x R'), [edit('x', 'y', { context_before: 'L', context_after: 'R' })], 'not_found', 0);
  rejects(Buffer.from('x x'), [edit('x', 'y', { context_before: '', context_after: '' })], 'ambiguous', 0);
  rejects(Buffer.from('aaa'), [edit('aa', 'Z')], 'ambiguous', 0);
  rejects(Buffer.from('LxR LxR'), [edit('x', 'y', { context_before: 'L', context_after: 'R' })], 'ambiguous', 0);
});

test('inclusive line bounds contain whole matches and can disambiguate', () => {
  const text = 'x\r\nx\ny\r\nz';
  assert.equal(run(text, [edit('x', 'X', { start_line: 2 })]).after.toString(), 'x\r\nX\ny\r\nz');
  const spanning = run(text, [edit('x\ny', 'Q', { start_line: 2, end_line: 3 })]);
  assert.deepEqual([spanning.changes[0].start_line, spanning.changes[0].end_line], [2, 3]);
  assert.equal(run(text, [edit('x\r\n', '', { start_line: 1 })]).after.toString(), 'x\ny\r\nz');
  rejects(Buffer.from(text), [edit('x\ny', 'Q', { start_line: 2 })], 'not_found', 0);
  rejects(Buffer.from(text), [edit('x\r\nx', 'Q', { start_line: 2, end_line: 3 })], 'not_found', 0);
  rejects(Buffer.from(text), [edit('z', 'Q', { start_line: 5 })], 'invalid_range', 0);
  assert.equal(run('a\nb\nc', [edit('b', 'B', { start_line: 2, context_before: 'a\n', context_after: '\nc' })]).after.toString(), 'a\nB\nc');
  rejects(Buffer.from('a\nb\nc'), [edit('a\nb\nc', 'Q', { start_line: 2, end_line: 3, context_before: '' })], 'not_found', 0);
});

test('newline ownership, bare CR, empty final line and trailing-newline preservation', () => {
  for (const text of ['a', 'a\n', 'a\r\n', 'a\n\r\n', 'a\rb']) {
    const before = Buffer.from(text);
    const result = planPreciseEdits(before, [edit('a', 'long')]);
    assert.deepEqual(result.after, Buffer.concat([Buffer.from('long'), before.subarray(1)]));
    verifySpans(before, result);
  }
  const result = run('a\r\nb\nc', [edit('\r\n', '', { start_line: 1 }), edit('\n', '!', { start_line: 2 })]);
  assert.deepEqual(result.changes.map(c => [c.start_line, c.end_line]), [[1, 1], [2, 2]]);
  assert.equal(run('a\rb', [edit('b', 'B', { start_line: 1 })]).after.toString(), 'a\rB');
  rejects(Buffer.from('a\n'), [edit('a', 'b', { start_line: 2 })], 'not_found', 0);
  rejects(Buffer.alloc(0), [edit('x', 'y')], 'not_found', 0);
});

test('reversed edits are byte-sorted, adjacent edits work, and snapshots are original', () => {
  const before = Buffer.from('abcDEFghi');
  const edits = Object.freeze([Object.freeze(edit('DEF', 'abc')), Object.freeze(edit('abc', 'longer'))]);
  const result = planPreciseEdits(before, edits);
  assert.equal(result.after.toString(), 'longerabcghi');
  assert.deepEqual(result.changes.map(c => c.start_byte), [0, 3]);
  verifySpans(before, result);
  rejects(Buffer.from('one'), [edit('one', 'two'), edit('two', 'three')], 'not_found', 1);
  assert.equal(run('abc', [edit('a', ''), edit('b', ''), edit('c', '')]).after.length, 0);
  const contextual = run('abc', [edit('a', 'X'), edit('b', 'Y', { context_before: 'a', context_after: 'c' })]);
  assert.equal(contextual.after.toString(), 'XYc');
});

test('overlaps, containment, duplicate spans and later failures are atomic', () => {
  const before = Buffer.from('abcdef');
  for (const edits of [
    [edit('abc', 'X'), edit('cde', 'Y')],
    [edit('abcdef', 'X'), edit('bc', 'Y')],
    [edit('abc', 'X'), edit('abc', 'Y')],
  ]) rejects(before, edits, 'overlap', 1);
  rejects(before, [edit('cde', 'Y'), edit('abc', 'X')], 'overlap', 0);
  rejects(before, [edit('a', 'X'), edit('absent', 'Y')], 'not_found', 1);
  rejects(Buffer.from('a b b'), [edit('a', 'X'), edit('b', 'Y')], 'ambiguous', 1);
});

test('Uint8Array views respect offsets and input/output storage is independent', () => {
  const storage = Buffer.from('!abc?');
  const input = new Uint8Array(storage.buffer, storage.byteOffset + 1, 3);
  const result = planPreciseEdits(input, [edit('b', 'B')]);
  assert.equal(result.after.toString(), 'aBc');
  assert.equal(result.before_sha256, hash(Buffer.from('abc')));
  storage.fill(33);
  assert.equal(result.after.toString(), 'aBc');
  result.after.fill(63);
  assert.equal(storage.toString(), '!!!!!');
});

test('rejects malformed UTF-8 rather than substituting characters', () => {
  for (const bytes of [[0xff], [0x80], [0xc0, 0xaf], [0xe0, 0x80, 0x80], [0xed, 0xa0, 0x80],
    [0xf0, 0x80, 0x80, 0x80], [0xf4, 0x90, 0x80, 0x80], [0xe2, 0x82], [0xc2], [0xf0, 0x9f, 0x98]]) {
    rejects(Buffer.from(bytes), [edit('x', 'y')], 'invalid_utf8');
  }
  rejects(Buffer.from('x\0y'), [edit('x', 'X')], 'invalid_text');
  assert.equal(run('\ufffd', [edit('\ufffd', 'valid')]).after.toString(), 'valid');
});

test('strict edit schema, text validation, and sanitized errors', () => {
  for (const value of [null, 'x', 2, {}, new ArrayBuffer(3), new Uint16Array(2), new DataView(new ArrayBuffer(1))]) {
    rejects(value, [edit('x', 'y')], 'invalid_bytes');
  }
  for (const value of [undefined, null, {}, [], Array(65).fill(edit('x', 'y'))]) {
    rejects(Buffer.from('x'), value, 'invalid_edits');
  }
  for (const value of [null, [], 2, 'x', {}, { old_text: 'x' }, { new_text: 'y' },
    edit('x', 'y', { extra: 'PRIVATE' }), edit('x', 'y', { [Symbol('PRIVATE')]: true }),
    Object.create({ old_text: 'x', new_text: 'y' }), { get old_text() { throw Error('PRIVATE'); }, new_text: 'y' }]) {
    rejects(Buffer.from('x'), [value], 'invalid_edit', 0);
  }
  rejects(Buffer.from('x'), Array(1), 'invalid_edit', 0);
  for (const key of ['old_text', 'new_text', 'context_before', 'context_after']) {
    for (const value of [null, undefined, 4, {}, '\ud800', '\udc00', 'a\ud800b', '\udc00\ud800', '\0']) {
      rejects(Buffer.from('x'), [{ ...edit('x', 'y'), [key]: value }], 'invalid_text', 0);
    }
  }
  rejects(Buffer.from('x'), [edit('', 'y')], 'invalid_text', 0);
  rejects(Buffer.from('PRIVATE'), [edit('PRIVATE', 'PRIVATE')], 'unchanged', 0);
  const hostile = new Proxy({}, { ownKeys() { throw Error('PRIVATE'); } });
  rejects(Buffer.from('x'), [hostile], 'invalid_input');
});

test('line bounds reject invalid, missing, reversed or out-of-file values', () => {
  for (const bounds of [{ end_line: 1 }, { start_line: 2, end_line: 1 }, { start_line: 1, end_line: 3 },
    ...[0, -1, 1.5, NaN, Infinity, '1', null, undefined, Number.MAX_SAFE_INTEGER + 1].flatMap(value => [
      { start_line: value }, { start_line: 1, end_line: value },
    ])]) rejects(Buffer.from('x\ny'), [edit('x', 'X', bounds)], 'invalid_range', 0);
});

test('array iterator overrides cannot skip edit validation or change the edit count', () => {
  const requests = [edit('x', 'y')];
  requests[Symbol.iterator] = function* () { yield edit('missing', 'z'); };
  assert.equal(run('x', requests).after.toString(), 'y');
  const invalid = [null];
  invalid[Symbol.iterator] = function* () {};
  rejects(Buffer.from('x'), invalid, 'invalid_edit', 0);
});

test('aggregate edit strings count all fields in UTF-8 bytes, with exact limits', () => {
  const limit = 128 * 1024;
  assert.equal(run('x', [edit('x', 'a'.repeat(limit - 1))]).after.length, limit - 1);
  rejects(Buffer.from('x'), [edit('x', 'a'.repeat(limit))], 'strings_too_large', 0);
  assert.equal(run('x', [edit('x', '\u00e9'.repeat((limit - 2) / 2) + 'a')]).after.length, limit - 1);
  rejects(Buffer.from('x'), [edit('x', '\u00e9'.repeat(limit / 2))], 'strings_too_large', 0);
  rejects(Buffer.from('x'), [edit('x', 'y', { context_before: 'a'.repeat(limit - 2), context_after: 'b' })], 'strings_too_large', 0);
  rejects(Buffer.from('xy'), [edit('x', 'a'.repeat(limit - 3)), edit('y', 'bb')], 'strings_too_large', 1);
  const context = 'a'.repeat(limit - 2);
  assert.equal(run(context + 'x', [edit('x', 'y', { context_before: context })]).after.toString(), context + 'y');
});

test('64 edits and the 16 MiB input ceiling are inclusive', () => {
  const tokens = Array.from({ length: 64 }, (_, i) => `<${i}>`);
  const before = Buffer.from(tokens.join(''));
  const result = planPreciseEdits(before, tokens.map((token, i) => edit(token, `[${i}]`)).reverse());
  assert.equal(result.changes.length, 64);
  verifySpans(before, result);
  const large = Buffer.alloc(16 * 1024 * 1024, 97);
  large[large.length - 1] = 98;
  const planned = planPreciseEdits(large, [edit('b', 'B')]);
  assert.equal(planned.after.length, large.length);
  verifySpans(large, planned);
  rejects(Buffer.alloc(16 * 1024 * 1024 + 1, 97), [edit('a', 'b')], 'file_too_large');
});

test('many repeated candidates remain ambiguous or resolve with full contexts', () => {
  rejects(Buffer.from('a'.repeat(100000)), [edit('a', 'b')], 'ambiguous', 0);
  const text = 'a'.repeat(100000) + 'END';
  const result = run(text, [edit('a', 'X', { context_after: 'a'.repeat(20000) + 'END' })]);
  assert.equal(result.changes[0].start_byte, 79999);
  verifySpans(Buffer.from(text), result);
});

test('seeded randomized edits preserve every unselected byte and independent oracle output', () => {
  let seed = 0x51a7beef;
  const random = n => {
    seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
    return seed % n;
  };
  const alphabet = [' ', '\t', '\n', '\r\n', '\r', '\ud83d\ude80', '\u0416', 'e\u0301', '\u00e9', 'abc', '\ufeff'];
  const noise = () => Array.from({ length: random(12) }, () => alphabet[random(alphabet.length)]).join('');
  for (let trial = 0; trial < 300; trial++) {
    const pieces = [], expected = [], edits = [];
    const count = 1 + random(20);
    for (let i = 0; i < count; i++) {
      const gap = noise(), old = `<T${trial}:${i}>`, replacement = noise();
      pieces.push(gap, old);
      expected.push(gap, replacement);
      edits.push(edit(old, replacement));
    }
    const tail = noise();
    pieces.push(tail);
    expected.push(tail);
    for (let i = edits.length - 1; i > 0; i--) {
      const j = random(i + 1);
      [edits[i], edits[j]] = [edits[j], edits[i]];
    }
    const before = Buffer.from(pieces.join(''));
    const snapshot = Buffer.from(before);
    const result = planPreciseEdits(before, edits);
    assert.deepEqual(result.after, Buffer.from(expected.join('')));
    assert.deepEqual(before, snapshot);
    verifySpans(before, result);
  }
});

test('seeded candidate oracle verifies context, ranges, ambiguity and overlapping matches', () => {
  let seed = 0x93ed40a1;
  const random = n => {
    seed = (Math.imul(seed, 1103515245) + 12345) >>> 0;
    return seed % n;
  };
  const alphabet = ['a', 'b', 'a', '\n', '\r\n', '\ud83d\ude80', 'e\u0301', '\ufeff'];
  for (let trial = 0; trial < 1000; trial++) {
    const text = Array.from({ length: 5 + random(30) }, () => alphabet[random(alphabet.length)]).join('');
    const points = Array.from(text);
    const start = random(points.length), length = 1 + random(4);
    const old = points.slice(start, start + length).join('');
    const request = edit(old, 'NEW');
    const prefix = points.slice(0, start).join('');
    const suffix = points.slice(start + length).join('');
    if (random(2)) request.context_before = Array.from(prefix).slice(-1 - random(3)).join('');
    if (random(2)) request.context_after = Array.from(suffix).slice(0, 1 + random(3)).join('');
    const totalLines = text.split('\n').length;
    if (random(2)) {
      request.start_line = 1 + random(totalLines);
      if (random(2)) request.end_line = request.start_line + random(totalLines - request.start_line + 1);
    }
    // This oracle uses UTF-16 positions and literal slicing, independently of
    // the planner's contextual byte needle and binary-searched line table.
    const candidates = [];
    for (let pos = 0; pos <= text.length - old.length; pos++) {
      if (text.slice(pos, pos + old.length) !== old) continue;
      if (!text.slice(0, pos).endsWith(request.context_before ?? '')) continue;
      if (!text.slice(pos + old.length).startsWith(request.context_after ?? '')) continue;
      const firstLine = text.slice(0, pos).split('\n').length;
      const lastLine = text.slice(0, pos + old.length - 1).split('\n').length;
      if (request.start_line !== undefined &&
          (firstLine < request.start_line || lastLine > (request.end_line ?? request.start_line))) continue;
      candidates.push(pos);
    }
    const before = Buffer.from(text);
    if (candidates.length !== 1) {
      rejects(before, [request], candidates.length ? 'ambiguous' : 'not_found', 0);
    } else {
      const result = planPreciseEdits(before, [request]);
      assert.equal(result.changes[0].start_byte, Buffer.byteLength(text.slice(0, candidates[0])));
      assert.equal(result.after.toString(), text.slice(0, candidates[0]) + 'NEW' + text.slice(candidates[0] + old.length));
      verifySpans(before, result);
    }
  }
});
