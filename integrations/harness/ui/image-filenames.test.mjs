import { test } from 'node:test';
import assert from 'node:assert/strict';
import { normalizeImageOutputName } from './image-filenames.mjs';

test('omitted names use a normalized fallback, while explicit empty names are rejected', () => {
  assert.equal(normalizeImageOutputName(), 'image');
  assert.equal(normalizeImageOutputName(undefined), 'image');
  assert.equal(normalizeImageOutputName(undefined, ' Portrait final.PNG '), 'Portrait-final');
  assert.equal(normalizeImageOutputName(undefined, 'NUL.webp'), '_NUL');
  assert.equal(normalizeImageOutputName('chosen', '../unused'), 'chosen');
  for (const value of ['', '   ', '.png', '?!*']) assert.throws(() => normalizeImageOutputName(value), /usable filename stem/);
  assert.throws(() => normalizeImageOutputName(undefined, ''), /usable filename stem/);
  assert.throws(() => normalizeImageOutputName(undefined, '../unsafe'), /path separators/);
});

test('Unicode letters and numbers use NFC, preserving case and replacing spaces with hyphens', () => {
  assert.equal(normalizeImageOutputName('  Cafe\u0301   portrait_02--final.JPEG '), 'Caf\u00e9-portrait_02-final');
  assert.equal(normalizeImageOutputName('\u041f\u043e\u0440\u0442\u0440\u0435\u0442 \u0432 \u0441\u0430\u0434\u0443.png'), '\u041f\u043e\u0440\u0442\u0440\u0435\u0442-\u0432-\u0441\u0430\u0434\u0443');
  assert.equal(normalizeImageOutputName('\u732b\u306e\u8096\u50cf \uff12.webp'), '\u732b\u306e\u8096\u50cf-\uff12');
  assert.equal(normalizeImageOutputName('one\u00a0\u3000two'), 'one-two');
  assert.equal(normalizeImageOutputName('one - -- ? two'), 'one-two');
  assert.equal(normalizeImageOutputName('a?b*c|d"e<f>g'), 'a-b-c-d-e-f-g');
  assert.equal(normalizeImageOutputName('.hidden-name'), 'hidden-name');
});

test('only a trailing known image extension is stripped, and the result is a stem', () => {
  for (const extension of ['png', 'PNG', 'jpg', 'JPG', 'jpeg', 'JPEG', 'webp', 'WEBP']) {
    assert.equal(normalizeImageOutputName('portrait.' + extension), 'portrait');
  }
  assert.equal(normalizeImageOutputName('portrait.png.backup'), 'portrait-png-backup');
  assert.equal(normalizeImageOutputName('portrait.jpg.png'), 'portrait-jpg');
  assert.equal(normalizeImageOutputName('portrait.txt'), 'portrait-txt');
});

test('paths, traversal, all C0/C1 controls, and bidi controls are rejected before trimming', () => {
  for (const value of ['../image', '..\\image', '/tmp/image', 'C:portrait', 'https://example.com/image', 'folder/image.png', '.', '..', '...', ' . . ']) {
    assert.throws(() => normalizeImageOutputName(value));
  }
  for (let code = 0; code <= 0x9f; code++) {
    if (code > 0x1f && code < 0x7f) continue;
    assert.throws(() => normalizeImageOutputName(String.fromCodePoint(code) + 'portrait'), /controls/);
  }
  for (const code of [0x61c, 0x200e, 0x200f, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069, 0x2028, 0x2029]) {
    assert.throws(() => normalizeImageOutputName('portrait' + String.fromCodePoint(code)), /controls/);
  }
});

test('invalid types are not coerced, including invalid fallback types', () => {
  for (const value of [null, false, true, 0, 42, NaN, 1n, [], {}, new String('name'), Symbol('name'), () => 'name']) {
    assert.throws(() => normalizeImageOutputName(value), TypeError);
    assert.throws(() => normalizeImageOutputName(undefined, value), TypeError);
  }
});

test('Windows device names are escaped, including superscript device digits', () => {
  const names = ['CON', 'PRN', 'AUX', 'NUL'];
  for (let i = 1; i <= 9; i++) names.push('COM' + i, 'LPT' + i);
  for (const digit of ['\u00b9', '\u00b2', '\u00b3']) names.push('COM' + digit, 'LPT' + digit);
  for (const name of names) {
    assert.equal(normalizeImageOutputName(name), '_' + name);
    assert.equal(normalizeImageOutputName(' ' + name.toLowerCase() + '.png '), '_' + name.toLowerCase());
  }
  for (const name of ['COM0', 'LPT0', 'COM10', 'LPT10', 'CONnect', '_CON', 'AUXiliary']) assert.equal(normalizeImageOutputName(name), name);
});

test('the 80-byte bound truncates at codepoint boundaries, not UTF-16 units', () => {
  for (const [unit, count] of [['a', 80], ['\u00e9', 40], ['\u732b', 26], ['\u{10400}', 20]]) {
    const result = normalizeImageOutputName(unit.repeat(100) + '.png');
    assert.equal(result, unit.repeat(count));
    assert.ok(Buffer.byteLength(result, 'utf8') <= 80);
    assert.equal(Buffer.from(result, 'utf8').toString('utf8'), result);
    assert.ok(!/[\uD800-\uDBFF]$/.test(result));
  }
  assert.equal(normalizeImageOutputName('a'.repeat(79) + '\u{10400}'), 'a'.repeat(79));
  assert.equal(normalizeImageOutputName('a'.repeat(79) + ' trailing'), 'a'.repeat(79));
});

test('normalization is deterministic and idempotent; equivalent names may collide', () => {
  for (const [left, right] of [['Cafe\u0301.png', 'Caf\u00e9.webp'], ['one?two', 'one*two'], ['one   two', 'one\u00a0two'], ['one two', 'one--two'], ['CON', '_CON'], ['a'.repeat(80) + 'left', 'a'.repeat(80) + 'right']]) {
    assert.equal(normalizeImageOutputName(left), normalizeImageOutputName(right));
  }
  for (const value of [' portrait.jpeg ', 'Cafe\u0301', 'CON', 'COM\u00b9', '\u732b'.repeat(100), 'one?two', 'one - two', 'one\u00a0two', '_CON', 'sample__02']) {
    const stem = normalizeImageOutputName(value);
    assert.equal(normalizeImageOutputName(stem), stem);
    assert.match(stem, /^[\p{L}\p{N}_-]+$/u);
    assert.ok(!stem.includes('--'));
    assert.equal(stem, stem.normalize('NFC'));
    assert.ok(Buffer.byteLength(stem, 'utf8') <= 80);
  }
});
