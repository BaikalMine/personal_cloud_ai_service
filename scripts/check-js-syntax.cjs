const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const directory = path.resolve(process.argv[2] || path.join(__dirname, '../internal/gateway/static'));
try {
  const files = fs.readdirSync(directory, { withFileTypes: true })
    .filter(entry => entry.isFile() && entry.name.endsWith('.js'))
    .map(entry => entry.name).sort();
  if (!files.length) throw new Error(`No JavaScript files in ${directory}`);
  for (const file of files) {
    const result = spawnSync(process.execPath, ['--check', path.join(directory, file)], { stdio: 'inherit' });
    if (result.error) throw result.error;
    if (result.status !== 0) {
      process.exitCode = result.status || 1;
      break;
    }
  }
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
