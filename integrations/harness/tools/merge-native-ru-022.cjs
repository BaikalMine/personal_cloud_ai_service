const fs = require('node:fs');
const path = require('node:path');
const { parse } = require('acorn');
const { createHash } = require('node:crypto');
const { dictionaries } = require('./extract-locales.cjs');

const managed = namespace => namespace.startsWith('gateway') || ['mnemon', 'dsh-context', 'skills-manager'].includes(namespace);
const parameters = text => [...text.matchAll(/\{([a-zA-Z0-9_]+)\}/g)].map(match => match[1]).sort().join(',');
function walk(node, visit) {
  if (!node || typeof node !== 'object') return;
  if (node.type) visit(node);
  for (const value of Object.values(node)) {
    if (Array.isArray(value)) value.forEach(item => walk(item, visit));
    else if (value && typeof value === 'object') walk(value, visit);
  }
}
function loadOverrides(ui) {
  const result = Object.create(null);
  for (const name of fs.readdirSync(ui).filter(name => /^ru-.*\.json$/.test(name)).sort()) {
    for (const [namespace, entries] of Object.entries(JSON.parse(fs.readFileSync(path.join(ui, name), 'utf8')))) {
      if (managed(namespace)) continue;
      result[namespace] ??= Object.create(null);
      for (const [key, value] of Object.entries(entries)) {
        if (typeof value !== 'string' || Object.hasOwn(result[namespace], key)) throw new Error(`Invalid override ${namespace}.${key}`);
        result[namespace][key] = value;
      }
    }
  }
  return result;
}
function locate(source, expected) {
  const ast = parse(source, { ecmaVersion:'latest', sourceType:'module' });
  const bindings = new Map(), matches = [];
  walk(ast, node => {
    if (node.type === 'VariableDeclarator' && node.id.type === 'Identifier') {
      const all = bindings.get(node.id.name) ?? [];
      all.push(node.init); bindings.set(node.id.name, all);
    }
  });
  function resolve(node) {
    const seen = new Set();
    while (node?.type === 'Identifier') {
      if (seen.has(node.name) || bindings.get(node.name)?.length !== 1) throw new Error(`Ambiguous binding ${node.name}`);
      seen.add(node.name); node = bindings.get(node.name)[0];
    }
    return node;
  }
  walk(ast, node => {
    const call = node.callee;
    if (node.type !== 'CallExpression' || call?.type !== 'MemberExpression' || call.property.name !== 'register' ||
        (call.object?.property?.name !== 'locale' && call.object?.name !== 'locale')) return;
    let name;
    try { const value = resolve(node.arguments[0]); if (value?.type === 'Literal') name = value.value; } catch { return; }
    if (!expected.has(name)) return;
    const langs = resolve(node.arguments[1]);
    if (langs?.type !== 'ObjectExpression') throw new Error(`Unsupported locale registration ${name}`);
    const ru = langs.properties.filter(p => p.type === 'Property' && !p.computed && (p.key.name ?? p.key.value) === 'ru');
    if (ru.length !== 1) throw new Error(`Missing or ambiguous RU registration ${name}`);
    const object = resolve(ru[0].value);
    if (object?.type !== 'ObjectExpression') throw new Error(`Unsupported RU object ${name}`);
    matches.push({ name, object });
  });
  return matches;
}
function planMerge(modules, ui) {
  const overrides = loadOverrides(ui), pending = new Map(), found = new Set(), verifiedFiles = new Set();
  const expected = new Set(Object.keys(overrides));
  const root = path.join(modules, '@deepseek-ai');
  for (const name of fs.readdirSync(root).sort()) {
    const file = path.join(root, name, 'lib/client.js');
    if (!fs.existsSync(file)) continue;
    const source = fs.readFileSync(file, 'utf8');
    const english = dictionaries(source, 'en');
    if (!Object.keys(english).some(namespace => expected.has(namespace))) continue;
    const manifest = JSON.parse(fs.readFileSync(path.join(root, name, 'package.json'), 'utf8'));
    if (manifest.version !== '0.2.2-rc.1') throw new Error(`Unsupported native package ${name}: ${manifest.version}`);
    const russian = dictionaries(source, 'ru'), edits = [];
    const locations = locate(source, expected);
    if (locations.length) verifiedFiles.add(file);
    for (const { name:namespace, object } of locations) {
      if (found.has(namespace)) throw new Error(`Duplicate native namespace ${namespace}`);
      found.add(namespace);
      for (const [key, text] of Object.entries(overrides[namespace])) {
        if (english[namespace]?.[key] !== undefined && parameters(english[namespace][key]) !== parameters(text)) throw new Error(`Placeholder mismatch ${namespace}.${key}`);
      }
      const marker = `/* gateway-native-ru-022:${namespace} */`;
      if (source.includes(marker)) {
        if (!Object.entries(overrides[namespace]).every(([key, text]) => russian[namespace]?.[key] === text)) throw new Error(`Existing merge differs in ${namespace}`);
        continue;
      }
      const prior = source.slice(object.start + 1, object.end - 1).trimEnd();
      const comma = object.properties.length && !prior.endsWith(',') ? ',' : '';
      edits.push({ start:object.end - 1, end:object.end - 1, value:`${comma}\n${marker} ...${JSON.stringify(overrides[namespace])}\n` });
    }
    let updated = source;
    for (const edit of edits.sort((a,b) => b.start - a.start)) updated = updated.slice(0, edit.start) + edit.value + updated.slice(edit.end);
    if (edits.length) {
      const merged = dictionaries(updated, 'ru');
      for (const { name:namespace } of locations) {
        const expectedValues = { ...russian[namespace], ...overrides[namespace] };
        if (JSON.stringify(merged[namespace]) !== JSON.stringify(expectedValues)) throw new Error(`Merge verification failed ${namespace}`);
      }
      pending.set(file, updated);
    }
  }
  const missing = [...expected].filter(namespace => !found.has(namespace));
  if (missing.length) throw new Error(`Missing native namespaces: ${missing.join(', ')}`);
  return { pending, verifiedFiles, namespaces:found.size, keys:Object.values(overrides).reduce((sum, entries) => sum + Object.keys(entries).length, 0) };
}
function applyMerge(modules, ui) {
  const plan = planMerge(modules, ui);
  for (const [file, source] of plan.pending) fs.writeFileSync(file, source);
  const hash = file => createHash('sha256').update(fs.readFileSync(file)).digest('hex');
  const inputs = Object.fromEntries(fs.readdirSync(ui).filter(name => /^ru-.*\.json$/.test(name)).sort().map(name => [name, hash(path.join(ui, name))]));
  const files = Object.fromEntries([...plan.verifiedFiles].sort().map(file => [path.relative(modules, file).split(path.sep).join('/'), hash(file)]));
  fs.writeFileSync(path.join(ui, '.native-ru-022-proof.json'), JSON.stringify({ version:1, inputs, files }) + '\n');
  return { files:plan.pending.size, namespaces:plan.namespaces, keys:plan.keys };
}
if (require.main === module) {
  if (!process.argv[2] || !process.argv[3]) throw new Error('Supply node_modules root and reconciled staged UI directory');
  console.log(JSON.stringify(applyMerge(path.resolve(process.argv[2]), path.resolve(process.argv[3]))));
}
module.exports = { planMerge, applyMerge };
