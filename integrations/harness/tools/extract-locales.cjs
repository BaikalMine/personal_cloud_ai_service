const fs = require('node:fs');
const path = require('node:path');
const { parse } = require('acorn');

function walk(node, visit) {
  if (!node || typeof node !== 'object') return;
  if (node.type) visit(node);
  for (const value of Object.values(node)) {
    if (Array.isArray(value)) value.forEach(child => walk(child, visit));
    else if (value && typeof value === 'object') walk(value, visit);
  }
}

function dictionaries(source, language = 'en') {
  const ast = parse(source, { ecmaVersion: 'latest', sourceType: 'module' });
  const bindings = new Map();
  walk(ast, node => {
    if (node.type === 'VariableDeclarator' && node.id.type === 'Identifier') bindings.set(node.id.name, node.init);
  });
  function value(node, seen = new Set()) {
    if (!node) return undefined;
    if (node.type === 'Literal') return node.value;
    if (node.type === 'TemplateLiteral' && node.expressions.length === 0) return node.quasis[0].value.cooked;
    if (node.type === 'Identifier' && !seen.has(node.name)) return value(bindings.get(node.name), new Set([...seen, node.name]));
    if (node.type === 'MemberExpression') {
      const object = value(node.object, seen);
      const key = node.computed ? value(node.property, seen) : node.property.name;
      return object && typeof object === 'object' && Object.hasOwn(object, key) ? object[key] : undefined;
    }
    if (node.type === 'ArrayExpression') return node.elements.map(item => value(item, seen));
    if (node.type === 'CallExpression' && node.callee?.type === 'MemberExpression' && node.callee.property.name === 'join') {
      const items = value(node.callee.object, seen);
      const separator = value(node.arguments[0], seen);
      if (Array.isArray(items) && items.every(item => typeof item === 'string') && typeof separator === 'string') return items.join(separator);
    }
    if (node.type === 'ObjectExpression') {
      const result = {};
      for (const item of node.properties) {
        if (item.type === 'SpreadElement') {
          const spread = value(item.argument, seen);
          if (!spread || typeof spread !== 'object') return undefined;
          Object.assign(result, spread);
        } else {
          const key = item.computed ? value(item.key, seen) : item.key.name ?? item.key.value;
          if (key === undefined) return undefined;
          result[key] = value(item.value, seen);
        }
      }
      return result;
    }
    return undefined;
  }
  const result = {};
  walk(ast, node => {
    const callee = node.callee;
    if (node.type !== 'CallExpression' || callee?.type !== 'MemberExpression' || callee.property.name !== 'register' ||
      (callee.object?.property?.name !== 'locale' && callee.object?.name !== 'locale')) return;
    const ns = value(node.arguments[0]);
    const entries = value(node.arguments[1])?.[language];
    if (typeof ns !== 'string' || !entries) return;
    if (!Object.values(entries).every(v => typeof v === 'string')) throw new Error(`Unsupported locale expression in ${ns}`);
    if (result[ns] && JSON.stringify(result[ns]) !== JSON.stringify(entries)) throw new Error(`Conflicting namespace ${ns}`);
    result[ns] = entries;
  });
  return result;
}

if (require.main === module) {
  const root = path.resolve(process.argv[2]);
  const output = {};
  for (const name of fs.readdirSync(root)) {
    const file = path.join(root, name, 'lib/client.js');
    if (fs.existsSync(file)) Object.assign(output, dictionaries(fs.readFileSync(file, 'utf8'), process.argv[4] || 'en'));
  }
  fs.writeFileSync(process.argv[3], JSON.stringify(output, null, 2) + '\n');
  console.log(`Extracted ${Object.keys(output).length} namespaces, ${Object.values(output).reduce((n, dict) => n + Object.keys(dict).length, 0)} strings.`);
}
module.exports = { dictionaries };
