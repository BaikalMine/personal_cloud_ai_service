import { readFile, writeFile, rename } from 'node:fs/promises';

const target = '/data/dsh/profiles/web/cordis.patch.yml';
const temporary = `${target}.restore-${process.pid}.tmp`;
await writeFile(temporary, await readFile('/restore/cordis.patch.yml'), { mode: 0o600, flag: 'wx' });
await rename(temporary, target);
console.log('Original Harness model profile restored');
