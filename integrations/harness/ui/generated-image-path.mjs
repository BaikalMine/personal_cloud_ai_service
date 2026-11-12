const pattern = /\/generated\/(?:krea-ultra|moss-image)\/[a-f0-9-]{36}\/(?:\d+|[\p{L}\p{N}_-]+--[1-9]\d*)\.png$/u;
export const generatedImagePattern = pattern.source;
export const generatedImageTest = `${pattern}.test(file.path)`;
export const isGeneratedImagePath = path => typeof path === 'string' && pattern.test(path);

export function upgradeGeneratedImageMatcher(source) {
  if (source.includes(generatedImageTest)) return source;
  for (const legacy of [
    String.raw`/\/generated\/(?:krea-ultra|moss-image)\/[a-f0-9-]{36}\/\d+\.png$/.test(file.path)`,
    String.raw`/\/generated\/krea-ultra\/[a-f0-9-]{36}\/\d+\.png$/.test(file.path)`,
  ]) {
    if (source.split(legacy).length === 2) return source.replace(legacy, generatedImageTest);
  }
  throw new Error('Unsupported generated-image path matcher');
}
