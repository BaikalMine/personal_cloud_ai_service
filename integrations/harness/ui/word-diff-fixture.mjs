import { execFileSync } from 'node:child_process';
import { readFileSync, readdirSync } from 'node:fs';
import vm from 'node:vm';
import { nativeDiffCore } from './patch-word-diff.mjs';

// Read immutable installed assets only. Never contact Harness APIs or user storage.
export function installedWordDiffFixture() {
  const container = process.env.HARNESS_CONTAINER || 'ai-gateway-harness';
  const directory = '/opt/harness/node_modules/@deepseek-ai/dsh-web-frontend';
  const directRoot = process.env.HARNESS_FRONTEND_ROOT;
  if (directRoot && directRoot.replace(/\/+$/, '') !== directory) throw new Error('HARNESS_FRONTEND_ROOT must be the immutable /opt/harness frontend package');
  const read = directRoot ? path => readFileSync(path, 'utf8')
    : path => execFileSync('docker', ['exec', container, 'cat', path], {
      encoding: 'utf8', maxBuffer: 32 * 1024 * 1024,
    });
  const manifest = JSON.parse(read(`${directory}/package.json`));
  const names = directRoot ? readdirSync(`${directory}/dist/assets`)
    : execFileSync('docker', ['exec', container, 'ls', `${directory}/dist/assets`], { encoding: 'utf8' }).trim().split(/\r?\n/);
  const indexes = names.filter(name => /^index-.*\.js$/.test(name));
  if (indexes.length !== 1) throw new Error('Expected one immutable frontend index');
  const source = read(`${directory}/dist/assets/${indexes[0]}`);
  const reviewDir = '/opt/harness/node_modules/@deepseek-ai/dsh-client-ui-deliverables';
  const reviewSource = read(`${reviewDir}/lib/client.js`);
  const reviewVersion = JSON.parse(read(`${reviewDir}/package.json`)).version;
  const { name, core } = nativeDiffCore(source);
  return { source, version: manifest.version, reviewSource, reviewVersion, Diff: vm.runInNewContext(`${core};${name}`) };
}
