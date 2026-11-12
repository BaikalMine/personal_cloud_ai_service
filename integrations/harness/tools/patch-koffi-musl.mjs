import { readFileSync, writeFileSync } from 'node:fs';

const directory = '/opt/harness/node_modules/@koromix/koffi-linux-x64';
const manifest = JSON.parse(readFileSync(`${directory}/package.json`, 'utf8'));
if (manifest.version !== '3.1.1') throw new Error('Unsupported Koffi version for musl loader patch');
const expected = `let native = null;
let err = null;
if (native == null) {
  try {
    native = require("./linux_x64/koffi.node");
  } catch (e) {
    err = e;
  }
}
if (native == null) {
  try {
    native = require("./musl_x64/koffi.node");
  } catch (e) {
    err = e;
  }
}
if (native == null)
  throw err;
module.exports = native;
`;
const file = `${directory}/index.js`;
if (readFileSync(file, 'utf8') !== expected) throw new Error('Koffi loader changed; refusing broad patch');
// gcompat can load the glibc addon but cannot safely initialize its C++ runtime.
writeFileSync(file, `const musl = !process.report.getReport().header.glibcVersionRuntime;
module.exports = require(musl ? './musl_x64/koffi.node' : './linux_x64/koffi.node');
`);
