import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

export async function ensurePythonEnvironment() {
  const run = promisify(execFile);
  // System pip is supplied by the image; installations go into the persistent venv.
  await run('/usr/bin/python3', ['-m', 'venv', '--system-site-packages', '--without-pip', '/workspace/.venv'], {
    timeout: 30000, maxBuffer: 65536,
  });
  await run('/workspace/.venv/bin/python3', ['-m', 'pip', '--version'], {
    timeout: 10000, maxBuffer: 65536,
  });
}
