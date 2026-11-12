// Embedded next to native DiffText; uses the already-imported primitives namespace.
export function createReviewWordDiff(core) {
  const ready = core && ['annotate', 'render', 'rowStyle'].every(key => typeof core[key] === 'function');
  return {
    prepare(rendered, hunkRows) {
      if (!ready) return rendered;
      const groups = rendered.hunks.map(hunkRows), flat = [];
      for (const rows of groups) flat.push(...rows, { kind: 'gap', text: '' });
      const annotated = core.annotate(flat);
      let offset = 0;
      return { ...rendered, hunks: rendered.hunks.map((hunk, index) => {
        const gatewayWordRows = annotated.slice(offset, offset + groups[index].length);
        offset += groups[index].length + 1;
        return { ...hunk, gatewayWordRows };
      }) };
    },
    render: (row, jsx, spans) => core.render(row, jsx, spans),
    rowStyle: row => ready && row ? core.rowStyle(row) : undefined,
  };
}

const marker = '/* gateway-review-word-diff:0.2.2-rc.1 */';
const ending = '/* end-gateway-review-word-diff */';
const injection = `${marker}\nconst gatewayReviewWordDiff=(${createReviewWordDiff.toString()})(_deepseek_ai_dsh_client_ui_primitives.gatewayWordDiff);\n${ending}\n\t\t`;
const rules = [
  ['function DiffText({ text, spans }) {', 'function DiffText({ text, spans, wordRow }) {'],
  ['children: spans === void 0 ? text : spans.map((span, index)', 'children: wordRow?.wordParts ? gatewayReviewWordDiff.render(wordRow, react_jsx_runtime.jsx, spans) : spans === void 0 ? text : spans.map((span, index)'],
  ['function hunkRows(hunk) {\n', 'function hunkRows(hunk) {\n\t\t\tif (hunk.gatewayWordRows) return hunk.gatewayWordRows;\n'],
  ['text: row.text,\n\t\t\t\tkind: "del"', 'text: row.text,\n\t\t\t\twordParts: row.wordParts,\n\t\t\t\tkind: "del"'],
  ['text: row.text,\n\t\t\t\tkind: "add"', 'text: row.text,\n\t\t\t\twordParts: row.wordParts,\n\t\t\t\tkind: "add"'],
  ['() => renderedHunks(diff.hunks), [diff.hunks]', '() => gatewayReviewWordDiff.prepare(renderedHunks(diff.hunks), hunkRows), [diff.hunks]'],
  ['text: cell?.text ?? "",\n', 'text: cell?.text ?? "",\n\t\t\t\t\t\t\t\t\twordRow: cell,\n'],
  ['text: row.left?.text ?? "",\n', 'text: row.left?.text ?? "",\n\t\t\t\t\t\t\t\t\t\twordRow: row.left,\n'],
  ['text: row.right?.text ?? "",\n', 'text: row.right?.text ?? "",\n\t\t\t\t\t\t\t\t\t\twordRow: row.right,\n'],
  ['text: row.text,\n\t\t\t\t\t\t\t\t\t\tspans:', 'text: row.text,\n\t\t\t\t\t\t\t\t\t\twordRow: row,\n\t\t\t\t\t\t\t\t\t\tspans:'],
  ['"data-diff-line": row.kind,', '"data-diff-line": row.kind,\n\t\t\t\t\t\t\t\tstyle: gatewayReviewWordDiff.rowStyle(row),'],
  ['FileDiff_module_css_default[cell.kind]}`,', 'FileDiff_module_css_default[cell.kind]}`,\n\t\t\t\t\t\t\t\tstyle: gatewayReviewWordDiff.rowStyle(cell),'],
  ['FileDiff_module_css_default[row.left.kind]}`,', 'FileDiff_module_css_default[row.left.kind]}`,\n\t\t\t\t\t\t\t\t\tstyle: gatewayReviewWordDiff.rowStyle(row.left),'],
  ['FileDiff_module_css_default[row.right.kind]}`,', 'FileDiff_module_css_default[row.right.kind]}`,\n\t\t\t\t\t\t\t\t\tstyle: gatewayReviewWordDiff.rowStyle(row.right),'],
];
const fail = detail => { throw new Error(`Unsupported review word diff: ${detail}`); };
function once(source, before, after) {
  if (source.split(before).length !== 2) fail(`anchor ${before}`);
  return source.replace(before, after);
}

// Used by immutable-asset tests to compare only the supported native surface.
export function unpatchWordReviewSource(source) {
  if (!source.includes(marker)) {
    if (source.includes('gatewayReviewWordDiff') || source.includes(ending)) fail('partial patch');
    return source;
  }
  source = once(source, injection, '');
  for (const [before, after] of rules) source = once(source, after, before);
  return source;
}

export function patchWordReviewSource(source, version = '0.2.2-rc.1') {
  if (version !== '0.2.2-rc.1') fail(`version ${version}`);
  source = unpatchWordReviewSource(source);
  for (const anchor of ['function SplitColumns({ hunks, highlights }) {', 'function TextDiff({ diff, split, wrap, t }) {', 'function splitRows(hunk) {']) {
    if (source.split(anchor).length !== 2) fail(anchor);
  }
  for (const [before, after] of rules) source = once(source, before, after);
  return once(source, 'function DiffText({ text, spans, wordRow }) {', injection + 'function DiffText({ text, spans, wordRow }) {');
}
