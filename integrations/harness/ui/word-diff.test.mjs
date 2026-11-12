import assert from 'node:assert/strict';
import { before, test } from 'node:test';
import vm from 'node:vm';
import { createWordDiff } from './word-diff.mjs';
import { installedWordDiffFixture } from './word-diff-fixture.mjs';

let Diff, words;
before(() => { ({ Diff } = installedWordDiffFixture()); words = createWordDiff(Diff); });
const pair = (oldText, newText) => words.annotate([{ kind: 'del', text: oldText }, { kind: 'add', text: newText }]);
const changed = row => row.wordParts.filter(part => part.changed).map(part => part.text).join('');
function exact(rows) {
  for (const row of rows) if (row.wordParts) assert.equal(row.wordParts.map(p => p.text).join(''), row.text);
}

test('actual bundled jsdiff core highlights a partial replacement, not the whole paragraph', () => {
  const rows = pair('The old word remains here.', 'The new word remains here.');
  assert.equal(changed(rows[0]), 'old');
  assert.equal(changed(rows[1]), 'new');
  exact(rows);
});

test('partial insertion and deletion preserve every space and punctuation mark', () => {
  for (const [a, b] of [['alpha  beta\tend', 'alpha  bright beta\tend'], ['value: (old), end!', 'value: (), end!']]) {
    const rows = pair(a, b);
    exact(rows);
    assert(rows.every(row => row.wordParts));
    assert(rows.some(row => changed(row).length > 0));
  }
  const rows = pair('a b', 'a  b');
  assert.equal(changed(rows[0]), ' ');
  assert.equal(changed(rows[1]), '  ');
});

test('Cyrillic, combining marks and emoji sequences remain intact', () => {
  const oldEmoji = '\ud83d\udc69\ud83c\udffd\u200d\ud83d\udcbb';
  const newEmoji = '\ud83d\udc69\ud83c\udffb\u200d\ud83d\udcbb';
  const rows = pair(`\u041f\u0440\u0438\u0432\u0435\u0442 ${oldEmoji} cafe\u0301!`, `\u041f\u0440\u0438\u0432\u0435\u0442 ${newEmoji} cafe\u0301?`);
  exact(rows);
  assert(changed(rows[0]).includes(oldEmoji));
  assert(changed(rows[1]).includes(newEmoji));
  assert(rows[0].wordParts.some(p => !p.changed && p.text.includes('cafe\u0301')));
});

test('repeated words retain shared text and highlight the local edit', () => {
  const rows = pair('one two one two one two end', 'one two one NEW two one two end');
  exact(rows);
  assert.equal(changed(rows[0]), '');
  assert.equal(changed(rows[1]), 'NEW ');
});

test('synthetic long paragraph still gets a small inline highlight', () => {
  const prefix = 'Shared paragraph words, '.repeat(180), suffix = ' tail'.repeat(100);
  const rows = pair(prefix + 'before' + suffix, prefix + 'after' + suffix);
  exact(rows);
  assert.equal(changed(rows[0]), 'before');
  assert.equal(changed(rows[1]), 'after');
});

test('unpaired rows and hunk/path boundaries remain native and input is not mutated', () => {
  const rows = [{ kind: 'del', text: 'old' }, { kind: 'gap', text: '...' }, { kind: 'add', text: 'new' },
    { kind: 'path', text: 'file' }, { kind: 'del', text: 'a old' }, { kind: 'del', text: 'extra' }, { kind: 'add', text: 'a new' }];
  const result = words.annotate(rows);
  for (const index of [0, 1, 2, 3, 5]) assert.equal(result[index], rows[index]);
  assert.equal(changed(result[4]), 'old');
  assert.equal(changed(result[6]), 'new');
  assert(rows.every(row => !row.wordParts));
});

test('character, token, edit-distance, pair-count and total-work limits retain native text', () => {
  for (const [a, b] of [['a'.repeat(24001), 'b'], ['x '.repeat(2000), 'y '.repeat(2000)],
    ['a '.repeat(200), 'b '.repeat(200)]]) {
    const rows = pair(a, b);
    assert(rows.every(row => !row.wordParts));
    assert.equal(rows[0].text, a);
    assert.equal(rows[1].text, b);
  }
  const rows = Array.from({ length: 140 }, () => [{ kind: 'del', text: 'a old' }, { kind: 'add', text: 'a new' }, { kind: 'context', text: '' }]).flat();
  const result = words.annotate(rows);
  assert.equal(result.filter(row => row.wordParts).length, 256);
  const large = Array.from({ length: 20 }, () => [{ kind: 'del', text: 'a'.repeat(6000) + ' old' }, { kind: 'add', text: 'a'.repeat(6000) + ' new' }, { kind: 'gap', text: '' }]).flat();
  assert(words.annotate(large).filter(row => row.wordParts).length <= 30);
});

test('missing Unicode segmentation falls back without splitting emoji', () => {
  const factory = vm.runInNewContext(`(${createWordDiff.toString()})`, { Intl: {} });
  const rows = [{ kind: 'del', text: 'old' }, { kind: 'add', text: 'new' }];
  assert.equal(factory(Diff).annotate(rows)[0], rows[0]);
});

test('global 40ms budget stops remaining rows after a real native-core highlight', () => {
  let elapsed = 0, calls = 0;
  class TimedDiff extends Diff {
    diffWithOptionsObj(...args) {
      calls++;
      const result = super.diffWithOptionsObj(...args);
      elapsed = 41;
      return result;
    }
  }
  const factory = vm.runInNewContext(`(${createWordDiff.toString()})`, { performance: { now: () => elapsed } });
  const rows = [{ kind: 'del', text: 'a old' }, { kind: 'add', text: 'a new' }, { kind: 'context', text: '' },
    { kind: 'del', text: 'b old' }, { kind: 'add', text: 'b new' }];
  const result = factory(TimedDiff).annotate(rows);
  assert.equal(changed(result[0]), 'old');
  assert.equal(changed(result[1]), 'new');
  assert.equal(result[3], rows[3]);
  assert.equal(result[4], rows[4]);
  assert.equal(calls, 1);
  exact(result);
});

test('React gets literal text children, never raw HTML or extra copy text', () => {
  const rows = pair('safe <img src=x onerror=alert(1)> & old', 'safe <img src=x onerror=alert(1)> & new');
  const jsx = (type, props, key) => ({ type, props, key });
  for (const row of rows) {
    const children = words.render(row, jsx);
    assert.equal(children.map(child => typeof child === 'string' ? child : child.props.children).join(''), row.text);
    for (const child of children.filter(c => typeof c !== 'string')) {
      assert.equal(child.type, 'span');
      assert.equal(child.props['data-word-change'], row.kind);
      assert.equal(typeof child.props.children, 'string');
      assert.equal(child.props.dangerouslySetInnerHTML, undefined);
      assert.equal(child.props['aria-label'], undefined);
    }
    assert.equal(words.rowStyle(row).color, 'var(--dsw-alias-label-primary)');
  }
});
