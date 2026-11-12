import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

export async function patchFilesUI(root) {
  const folder = join(root, '@deepseek-ai/dsh-client-ui-sidebar-files');
  const manifest = JSON.parse(await readFile(join(folder, 'package.json'), 'utf8'));
  if (!['0.2.1-alpha.2', '0.2.1-rc.5'].includes(manifest.version)) throw new Error('Review file-row patch for the new Harness version');
  const path = join(folder, 'lib/client.js');
  let source = await readFile(path, 'utf8');
  if (source.includes('gateway.files.row.actions')) return;
  const replace = (from, to) => {
    if (source.split(from).length !== 2) throw new Error('Harness file-row patch anchor changed');
    source = source.replace(from, to);
  };
  replace('const path = childPath(parent, entry.name);', 'const path = childPath(parent, entry.name);\n\t\t\tconst entryActions = tree.renderActions(path, entry.type);');
  replace('}), expanded && (0, react_jsx_runtime.jsx)("ul", {', '}), entryActions, expanded && (0, react_jsx_runtime.jsx)("ul", {');
  const begin = source.indexOf('if (entry.type === "file") return');
  const end = source.indexOf('\n\t\t\treturn (0, react_jsx_runtime.jsx)("li", {', begin);
  if (begin < 0 || end < 0) throw new Error('Harness file-row branch changed');
  const original = source.slice(begin, end);
  const updated = original.replace('return (0, react_jsx_runtime.jsx)', 'return (0, react_jsx_runtime.jsxs)')
    .replace('children: (0, react_jsx_runtime.jsxs)("button", {', 'children: [(0, react_jsx_runtime.jsxs)("button", {')
    .replace('\n\t\t\t\t})\n\t\t\t});', '\n\t\t\t\t}), entryActions]\n\t\t\t});');
  if (updated === original || !updated.includes('entryActions]')) throw new Error('Harness file-row output changed');
  source = source.slice(0, begin) + updated + source.slice(end);
  replace('const tree = {\n\t\t\t\tstate,', 'const tree = {\n\t\t\t\trenderActions: (absolutePath, entryType) => renderSlot("gateway.files.row.actions", { absolutePath, entryType }),\n\t\t\t\tstate,');
  replace('children: { "sidebar.right.tab.files.actions": {', 'children: { "gateway.files.row.actions": { kind: "list", scope: "session" }, "sidebar.right.tab.files.actions": {');
  await writeFile(path, source);
}

if (process.argv[1]?.endsWith('patch-files-ui.mjs')) await patchFilesUI(process.argv[2] ?? '/opt/harness/node_modules');
