/**
 * Bounded lexical retrieval only: NOT semantic or multilingual embeddings.
 * No translation or stemming is inferred. An English query against a Russian
 * fact can score zero; callers must offer unfiltered browsing, not infer absence.
 *
 * Query: undefined or string, <=512 UTF-8 bytes; alternatives: <=4 strings.
 * Each variant permits <=24 non-stopword tokens (before deduplication).
 * Records: strings <=16 KiB UTF-8, rejected rather than silently truncated.
 * Invalid query/plan inputs throw TypeError or RangeError with
 * code=chat_memory_invalid_query; invalid records use chat_memory_invalid_record.
 * Plans are deeply frozen, opaque in-process values; retain the returned plan
 * rather than serializing/reconstructing it. Neither function mutates inputs.
 */

const encoder = new TextEncoder();
const planBrand = Symbol('memory lexical search plan');
const stopwords = new Set([
  'a', 'an', 'and', 'are', 'as', 'at', 'be', 'by', 'for', 'from', 'in',
  'is', 'it', 'of', 'on', 'or', 'that', 'the', 'this', 'to', 'was', 'with',
  '\u0430', '\u0431\u044b', '\u0432', '\u0432\u043e', '\u0434\u0430',
  '\u0434\u043b\u044f', '\u0434\u043e', '\u0438', '\u0438\u0437',
  '\u043a', '\u043a\u0430\u043a', '\u043d\u0430', '\u043d\u0435',
  '\u043d\u043e', '\u043e', '\u043e\u0431', '\u043e\u0442',
  '\u043f\u043e', '\u0441', '\u0441\u043e', '\u0443',
  '\u0447\u0442\u043e', '\u044d\u0442\u043e',
]);

function invalid(ErrorClass, message, code = 'chat_memory_invalid_query') {
  return Object.assign(new ErrorClass(message), { code });
}

function boundedString(value, bytes, label, code) {
  if (typeof value !== 'string') {
    throw invalid(TypeError, `${label} must be a string`, code);
  }
  // UTF-8 is never shorter than JS UTF-16 length; avoid oversized allocations.
  if (value.length > bytes || encoder.encode(value).byteLength > bytes) {
    throw invalid(RangeError, `${label} exceeds ${bytes} UTF-8 bytes`, code);
  }
}

function words(text) {
  return text.normalize('NFKC').toLowerCase().replace(/\u0451/g, '\u0435')
    .match(/[\p{L}\p{N}]+/gu) ?? [];
}

/** Compile up to five OR variants. Empty/stopword-only variants are omitted. */
export function compileMemoryQuery(query = '', alternatives = [], {maxQueryBytes=512,maxTerms=24} = {}) {
  if(!Number.isSafeInteger(maxQueryBytes)||maxQueryBytes<1||maxQueryBytes>4096||!Number.isSafeInteger(maxTerms)||maxTerms<1||maxTerms>256) {
    throw invalid(RangeError,'invalid internal query bounds');
  }
  boundedString(query, maxQueryBytes, 'query');
  if (!Array.isArray(alternatives)) {
    throw invalid(TypeError, 'alternatives must be an array of strings');
  }
  if (alternatives.length > 4) {
    throw invalid(RangeError, 'alternatives must contain at most 4 strings');
  }
  const variants = [];
  for (const value of [query, ...alternatives]) {
    boundedString(value, maxQueryBytes, 'query variant');
    const tokens = words(value);
    const content = tokens.filter(token => !stopwords.has(token));
    if (content.length > maxTerms) {
      throw invalid(RangeError, `query variant exceeds ${maxTerms} non-stopword terms`);
    }
    if (!content.length) continue;
    variants.push(Object.freeze({
      // Keep stopwords for phrase adjacency; punctuation is a word separator.
      phrase: tokens.join(' '),
      terms: Object.freeze([...new Set(content)]),
    }));
  }
  return Object.freeze({ [planBrand]: true, variants: Object.freeze(variants) });
}

/**
 * Finite numeric score: exact token phrase=300, all terms=200..201,
 * partial terms=0..100 (exclusive), no match=0. Alternatives use max, not sum.
 * Order matters only for phrase matching. Prefixes require >=4 code points
 * and the entire query term at a record word's start; no internal substrings.
 * Prefix matching is deliberately conservative, not general inflection support.
 */
export function scoreMemoryRecord(text, plan) {
  boundedString(text, 16 * 1024, 'record text', 'chat_memory_invalid_record');
  if (!plan || typeof plan !== 'object' || plan[planBrand] !== true) {
    throw invalid(TypeError, 'plan must be returned by compileMemoryQuery');
  }
  const tokens = words(text);
  const vocabulary = new Set(tokens);
  const phrase = ` ${tokens.join(' ')} `;
  let best = 0;
  for (const variant of plan.variants) {
    if (phrase.includes(` ${variant.phrase} `)) {
      best = Math.max(best, 300);
      continue;
    }
    let matched = 0;
    let exact = 0;
    for (const term of variant.terms) {
      if (vocabulary.has(term)) {
        matched++;
        exact++;
      } else if ([...term].length >= 4 && tokens.some(token => token.startsWith(term))) {
        matched++;
      }
    }
    const count = variant.terms.length;
    const score = matched === count ? 200 + exact / count : 100 * matched / count;
    best = Math.max(best, score);
  }
  return best;
}
