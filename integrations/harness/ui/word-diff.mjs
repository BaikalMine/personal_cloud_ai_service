// Self-contained factory: embedded in the native frontend, using its jsdiff core.
export function createWordDiff(Diff) {
  const limits = { pairChars: 24000, tokens: 6000, pairs: 128, totalChars: 192000, editLength: 256 };
  const now = typeof performance === 'object' && typeof performance.now === 'function'
    ? () => performance.now() : () => Date.now();
  const segmenter = typeof Intl.Segmenter === 'function'
    ? new Intl.Segmenter('und', { granularity: 'word' }) : null;
  const engine = new Diff();
  engine.tokenize = text => Array.from(segmenter.segment(text), part => part.segment);
  const rowStyles = {
    del: { color: 'var(--dsw-alias-label-primary)', backgroundColor: 'color-mix(in srgb, var(--dsw-alias-code-diff-deleted) 45%, transparent)' },
    add: { color: 'var(--dsw-alias-label-primary)', backgroundColor: 'color-mix(in srgb, var(--dsw-alias-code-diff-added) 45%, transparent)' },
  };
  const wordStyles = {
    del: { color: 'var(--dsw-alias-label-primary)', backgroundColor: 'color-mix(in srgb, var(--dsw-alias-state-error-primary) 24%, transparent)', boxShadow: 'inset 0 -1px 0 var(--dsw-alias-state-error-primary)', boxDecorationBreak: 'clone' },
    add: { color: 'var(--dsw-alias-label-primary)', backgroundColor: 'color-mix(in srgb, var(--dsw-alias-state-success-primary) 24%, transparent)', boxShadow: 'inset 0 -1px 0 var(--dsw-alias-state-success-primary)', boxDecorationBreak: 'clone' },
  };

  function pair(oldText, newText, deadline) {
    if (!segmenter || oldText.length + newText.length > limits.pairChars) return null;
    const oldTokens = engine.tokenize(oldText), newTokens = engine.tokenize(newText);
    if (oldTokens.length + newTokens.length > limits.tokens) return null;
    const remaining = deadline - now();
    if (remaining <= 0) return null;
    // Reuse jsdiff's Myers implementation; never ignore or normalize whitespace.
    const changes = engine.diffWithOptionsObj(oldTokens, newTokens, {
      maxEditLength: limits.editLength, timeout: Math.min(12, remaining),
    });
    if (!changes) return null;
    const oldParts = [], newParts = [];
    for (const change of changes) {
      if (!change.added) oldParts.push({ text: change.value, changed: !!change.removed });
      if (!change.removed) newParts.push({ text: change.value, changed: !!change.added });
    }
    if (oldParts.map(p => p.text).join('') !== oldText || newParts.map(p => p.text).join('') !== newText) return null;
    return [oldParts, newParts];
  }

  function annotate(rows) {
    const result = rows.slice();
    const deadline = now() + 40;
    let pairs = 0, chars = 0;
    for (let i = 0; i < rows.length;) {
      if (rows[i].kind !== 'del') { i++; continue; }
      const start = i;
      while (i < rows.length && rows[i].kind === 'del') i++;
      const added = i;
      while (i < rows.length && rows[i].kind === 'add') i++;
      const count = Math.min(added - start, i - added);
      for (let j = 0; j < count; j++) {
        const oldRow = rows[start + j], newRow = rows[added + j];
        chars += oldRow.text.length + newRow.text.length;
        if (++pairs > limits.pairs || chars > limits.totalChars || now() >= deadline) return result;
        const parts = pair(oldRow.text, newRow.text, deadline);
        if (!parts) continue;
        result[start + j] = { ...oldRow, wordParts: parts[0] };
        result[added + j] = { ...newRow, wordParts: parts[1] };
      }
    }
    return result;
  }

  function render(row, jsx, spans) {
    if (!row.wordParts) return row.text;
    const syntax = spans?.map(span => span.text).join('') === row.text ? spans : undefined;
    let token = 0, offset = 0;
    function content(part) {
      if (!syntax) return part.text;
      const children = [];
      let remaining = part.text.length;
      while (remaining > 0 && token < syntax.length) {
        const span = syntax[token], size = Math.min(remaining, span.text.length - offset);
        if (size > 0) children.push(jsx('span', {
          style: part.changed ? { ...span.style, color: wordStyles[row.kind].color } : span.style,
          children: span.text.slice(offset, offset + size),
        }, children.length));
        remaining -= size;
        offset += size;
        if (offset === span.text.length) { token++; offset = 0; }
      }
      return children;
    }
    return row.wordParts.map((part, index) => part.changed
      ? jsx('span', { 'data-word-change': row.kind, style: wordStyles[row.kind], children: content(part) }, index)
      : content(part));
  }
  return { annotate, render, rowStyle: row => row.wordParts ? rowStyles[row.kind] : undefined };
}
