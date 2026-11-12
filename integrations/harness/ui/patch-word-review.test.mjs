import assert from 'node:assert/strict';
import { before, test } from 'node:test';
import vm from 'node:vm';
import { createWordDiff } from './word-diff.mjs';
import { createReviewWordDiff, patchWordReviewSource, unpatchWordReviewSource } from './patch-word-review.mjs';
import { installedWordDiffFixture } from './word-diff-fixture.mjs';

let source, version, words;
before(() => {
  const fixture = installedWordDiffFixture();
  source = unpatchWordReviewSource(fixture.reviewSource);
  version = fixture.reviewVersion;
  words = createWordDiff(fixture.Diff);
});
const jsx = (type, props, key) => typeof type === 'function' ? type(props) : { type, props, key };
const textOf = node => Array.isArray(node) ? node.map(textOf).join('') : node && typeof node === 'object'
  ? textOf(node.props.children) : node == null || node === false ? '' : String(node);
function nodes(node, predicate) {
  if (Array.isArray(node)) return node.flatMap(child => nodes(child, predicate));
  if (!node || typeof node !== 'object') return [];
  return [...(predicate(node) ? [node] : []), ...nodes(node.props.children, predicate)];
}
function renderer(core = words) {
  const patched = patchWordReviewSource(source, version);
  new vm.Script(patched);
  const start = patched.indexOf('const MAX_RENDERED_LINES = 5e3;');
  const end = patched.indexOf('\n\t\t//#endregion', patched.indexOf('function TextDiff(', start));
  const style = { color: '#abcdef', fontStyle: 'italic' };
  return vm.runInNewContext(`${patched.slice(start, end)};({ TextDiff, DiffText, hunkRows, splitRows })`, {
    react_jsx_runtime: { jsx, jsxs: jsx },
    react: { useMemo: fn => fn(), useRef: value => ({ current: value }) },
    _deepseek_ai_dsh_client_ui_primitives: { gatewayWordDiff: core, languageForPath: () => 'text',
      useCodeHighlighter: () => text => text.split('\n').map(line => [{ text: line.slice(0, 7), style }, { text: line.slice(7), style }]) },
    FileDiff_module_css_default: new Proxy({}, { get: (_, key) => key }),
  });
}
const hunk = (oldText, newText) => ({ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1, lines: ['-' + oldText, '+' + newText] });

test('Review patch is idempotent, syntax-valid, exactly reversible and rejects incompatible sources', () => {
  const after = patchWordReviewSource(source, version);
  new vm.Script(after);
  assert.equal(patchWordReviewSource(after, version), after);
  assert.equal(unpatchWordReviewSource(after), source);
  assert.throws(() => patchWordReviewSource(source, 'bad'), /version/);
  assert.throws(() => patchWordReviewSource(source.replace('function splitRows(hunk)', 'function other(hunk)')), /splitRows/);
  assert.throws(() => patchWordReviewSource(after.replace('wordRow: cell,', '')), /anchor/);
  assert.throws(() => patchWordReviewSource(source + source), /SplitColumns/);
});

for (const [split, wrap] of [[false, false], [false, true], [true, false], [true, true]]) {
  test(`actual Review renderer highlights unified/split=${split}, wrap=${wrap}, with literal text and syntax preserved`, () => {
    const { TextDiff } = renderer();
    const prefix = 'Shared \u041f\u0440\u0438\u0432\u0435\u0442 \ud83d\udc69\ud83c\udffd\u200d\ud83d\udcbb <tag> & ';
    const oldText = prefix + 'before end', newText = prefix + 'after end';
    const result = TextDiff({ diff: { before: true, after: true, path: 'synthetic.txt', hunks: [hunk(oldText, newText)] }, split, wrap, t: key => key });
    assert.equal(result.props['data-review-view'], split ? 'split' : 'unified');
    const del = nodes(result, node => node.props['data-word-change'] === 'del');
    const add = nodes(result, node => node.props['data-word-change'] === 'add');
    assert.deepEqual(del.map(textOf), ['before']);
    assert.deepEqual(add.map(textOf), ['after']);
    const texts = nodes(result, node => node.props.className === 'text');
    assert.deepEqual(texts.map(textOf), [oldText, newText]);
    assert(texts.every(node => node.props['data-diff-code'] === ''));
    assert(nodes(result, node => node.props.style?.fontStyle === 'italic').length > 2);
    assert.equal(del[0].props.style.color, 'var(--dsw-alias-label-primary)');
    assert(nodes(del[0], node => node.props.style?.fontStyle === 'italic').every(node => node.props.style.color === 'var(--dsw-alias-label-primary)'));
    assert.equal(nodes(result, node => node.type === 'tag').length, 0);
    assert.equal(nodes(result, node => node.props.dangerouslySetInnerHTML).length, 0);
  });
}

test('Review uses one global annotation call, never pairs across hunks, and leaves served hunks immutable', () => {
  let calls = 0;
  const helper = createReviewWordDiff({ ...words, annotate: rows => { calls++; return words.annotate(rows); } });
  const { hunkRows, splitRows } = renderer();
  const input = { hunks: [hunk('a old', 'a new'), { ...hunk('alone old', 'unused'), lines: ['-alone old'] },
    { ...hunk('unused', 'alone new'), lines: ['+alone new'] }], truncated: false };
  const prepared = helper.prepare(input, hunkRows);
  assert.equal(calls, 1);
  assert.equal(prepared.truncated, false);
  assert.equal(prepared.hunks[0].gatewayWordRows[0].wordParts.find(p => p.changed).text, 'old');
  assert.equal(prepared.hunks[1].gatewayWordRows[0].wordParts, undefined);
  assert.equal(prepared.hunks[2].gatewayWordRows[0].wordParts, undefined);
  assert(input.hunks.every(h => !h.gatewayWordRows));
  assert.equal(splitRows(prepared.hunks[0])[0].left.wordParts[1].text, 'old');
});

test('missing shared export falls back to native syntax rendering in all layouts', () => {
  const { TextDiff } = renderer(undefined);
  // Default arguments would select words, so exercise the explicit absent namespace too.
  const absent = renderer(null);
  for (const Component of [TextDiff, absent.TextDiff]) {
    const result = Component({ diff: { before: true, after: true, path: 'a.txt', hunks: [hunk('a old', 'a new')] }, split: true, wrap: false, t: x => x });
    assert.deepEqual(nodes(result, node => node.props.className === 'text').map(textOf), ['a old', 'a new']);
  }
  const helper = createReviewWordDiff(undefined), input = { hunks: [] };
  assert.equal(helper.prepare(input, () => { throw new Error('not called'); }), input);
  assert.equal(helper.rowStyle({ kind: 'add' }), undefined);
});
