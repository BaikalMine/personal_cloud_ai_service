const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const script = path.resolve(__dirname, '../../../../scripts/check-js-syntax.cjs');

function fixture(t, files) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'gateway syntax '));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  for (const [name, source] of Object.entries(files)) fs.writeFileSync(path.join(directory, name), source);
  return directory;
}
function check(directory) {
  return spawnSync(process.execPath, [script, directory], { encoding: 'utf8' });
}
test('CI syntax gate fails on an invalid first file even when the last file is valid', t => {
  const result = check(fixture(t, { 'a-broken.js': 'const = ;', 'z-valid.js': 'const value = 1;' }));
  assert.equal(result.status, 1);
  assert.match(result.stderr, /a-broken\.js/);
  assert.match(result.stderr, /SyntaxError/);
});
test('CI syntax gate checks later files and paths containing spaces', t => {
  const result = check(fixture(t, { 'a-valid.js': 'const value = 1;', 'z broken.js': 'const = ;' }));
  assert.equal(result.status, 1);
  assert.match(result.stderr, /z broken\.js/);
});
test('CI syntax gate accepts valid files but refuses an empty input directory', t => {
  assert.equal(check(fixture(t, { 'valid.js': 'const value = 1;', 'ignored.txt': 'not JavaScript' })).status, 0);
  const empty = check(fixture(t, {}));
  assert.equal(empty.status, 1);
  assert.match(empty.stderr, /No JavaScript files/);
});
