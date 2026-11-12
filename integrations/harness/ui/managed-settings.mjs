import { readFile, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';

const RC5_SETTINGS_SHA256 = '0555eec2df1ca392a1167e637f287ac9963dbd2b0a230089c2a41be079c490e3';

export function isManagedSettingsPage(location, origins) {
  return Boolean(location && location.protocol === 'https:' &&
    origins.includes(location.origin) && location.pathname.startsWith('/harness/'));
}

export function parseManagedOrigins(value = '') {
  return value.split(',').filter(Boolean).map(value => {
    const url = new URL(value);
    if (url.protocol !== 'https:' || url.origin !== value || url.username || url.password || url.hostname.includes('*')) {
      throw new Error('Managed Harness settings require an exact HTTPS origin');
    }
    return url.origin;
  });
}

export async function patchManagedSettings(path, value) {
  const origins = parseManagedOrigins(value);
  const source = await readFile(path, 'utf8');
  if (createHash('sha256').update(source).digest('hex') === RC5_SETTINGS_SHA256) {
    console.log('Managed settings: verified rc.5 host settings implementation');
    return;
  }
  const original = 'const persistence = ctx.remote.$host.isLoopback ? "host" : "memory";';
  if (source.split(original).length !== 2) throw new Error('Unsupported upstream settings policy; reverify before upgrading');
  const policy = `(${isManagedSettingsPage.toString()})(globalThis.location, ${JSON.stringify(origins)})`;
  await writeFile(path, source.replace(original,
    `const persistence = (ctx.remote.$host.isLoopback || ${policy}) ? "host" : "memory";`));
  console.log(`Managed settings policy: ${origins.length} exact HTTPS origins, Harness mount only`);
}
