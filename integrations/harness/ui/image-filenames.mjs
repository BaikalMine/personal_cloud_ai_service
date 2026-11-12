import { Buffer } from 'node:buffer';

const unsafe = /[/\\:\p{Cc}\p{Bidi_Control}\u2028\u2029]/u;
const reserved = /^(?:CON|PRN|AUX|NUL|COM[1-9\u00b9\u00b2\u00b3]|LPT[1-9\u00b9\u00b2\u00b3])$/iu;
const trimEdges = value => value.replace(/^[ -]+|[ -]+$/g, '');

export function normalizeImageOutputName(value, fallback = 'image') {
  const supplied = value === undefined ? fallback : value;
  if (typeof supplied !== 'string') throw new TypeError('output_name must be a string');
  if (unsafe.test(supplied)) throw new Error('output_name must not contain path separators, controls, or bidi controls');

  const normalized = supplied.normalize('NFC').replace(/\p{Zs}+/gu, ' ').trim().replace(/\.(?:png|jpe?g|webp)$/i, '');
  if (/^\.+$/.test(normalized)) throw new Error('output_name must not be a dot-only name');
  const cleaned = trimEdges(normalized.replace(/[^\p{L}\p{N}_-]+/gu, '-').replace(/-+/g, '-'));
  let stem = '', bytes = 0;
  for (const codepoint of cleaned) {
    const length = Buffer.byteLength(codepoint, 'utf8');
    if (bytes + length > 80) break;
    stem += codepoint;
    bytes += length;
  }
  stem = trimEdges(stem);
  if (!stem) throw new Error('output_name must contain a usable filename stem');
  return reserved.test(stem) ? '_' + stem : stem;
}
