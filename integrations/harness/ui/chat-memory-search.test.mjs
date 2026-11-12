import test from 'node:test';
import assert from 'node:assert/strict';
import { compileMemoryQuery, scoreMemoryRecord } from './chat-memory-search.mjs';

const score = (text, query, alternatives) =>
  scoreMemoryRecord(text, compileMemoryQuery(query, alternatives));
const queryError = ErrorClass => error =>
  error instanceof ErrorClass && error.code === 'chat_memory_invalid_query';
const recordError = ErrorClass => error =>
  error instanceof ErrorClass && error.code === 'chat_memory_invalid_record';

test('normalizes Russian case and yo/e, including decomposed Unicode', () => {
  assert.equal(score('\u041e\u043d \u043b\u044e\u0431\u0438\u0442 \u0435\u0436\u0438\u043a\u043e\u0432.', '\u0401\u0416\u0418\u041a\u041e\u0412'), 300);
  assert.equal(score('\u0451\u043b\u043a\u0430', '\u0435\u0308\u043b\u043a\u0430'), 300);
});

test('normalizes NFKC compatibility forms and numeric tokens', () => {
  assert.equal(score('Build 42 uses ffi', '\uff22\uff35\uff29\uff2c\uff24 \uff14\uff12'), 300);
  assert.equal(score('ffi', '\ufb03'), 300);
  assert.equal(score('build 142', '42'), 0);
});

test('exact phrase outranks all terms out of order, which outrank partial terms', () => {
  const plan = compileMemoryQuery('red green blue');
  const exact = scoreMemoryRecord('A red green blue notebook', plan);
  const all = scoreMemoryRecord('blue notebook green red', plan);
  const partial = scoreMemoryRecord('red notebook', plan);
  assert.ok(exact > all && all > partial && partial > 0);
  assert.equal(exact, 300);
  assert.equal(scoreMemoryRecord('yellow notebook', plan), 0);
});

test('phrase matching uses whole adjacent tokens, not raw substrings', () => {
  assert.equal(score('scat dogma', 'cat dog'), 0);
  assert.equal(score('red, green! blue', 'red green blue'), 300);
  assert.ok(score('red other green blue', 'red green blue') < 300);
  assert.ok(score('state art', 'state of the art') < 300);
  assert.equal(score('state of the art', 'state of the art'), 300);
});

test('matches bounded word prefixes without claiming general stemming', () => {
  const prefix = score('\u043f\u0440\u043e\u0435\u043a\u0442\u0430\u043c\u0438', '\u043f\u0440\u043e\u0435\u043a\u0442');
  assert.ok(prefix >= 200 && prefix < 300);
  assert.ok(score('programming', 'program') > 0);
  assert.equal(score('deprogramming', 'program'), 0);
  assert.equal(score('catalog', 'cat'), 0);
  assert.equal(score('program', 'programming'), 0);
  assert.equal(score('\u0440\u0430\u0431\u043e\u0442\u044b', '\u0440\u0430\u0431\u043e\u0442\u0430'), 0);
});

test('prefix minimum counts Unicode code points, not UTF-16 units', () => {
  const astral = '\u{10428}';
  assert.equal(score(`${astral.repeat(3)}x`, astral.repeat(3)), 0);
  assert.ok(score(`${astral.repeat(4)}x`, astral.repeat(4)) > 0);
});

test('treats regex syntax as separators with no dynamic regular expressions', () => {
  assert.equal(score('anything', '.* [ ] ^ $ \\'), 0);
  assert.equal(score('alpha beta', 'alpha.*(beta)+'), 300);
  assert.equal(score('unrelated', '(alpha|beta){100000}'), 0);
});

test('alternatives are OR/max and never accumulate unrelated partial coverage', () => {
  const plan = compileMemoryQuery('red green', ['blue yellow']);
  assert.equal(scoreMemoryRecord('red blue', plan), 50);
  assert.equal(scoreMemoryRecord('blue yellow', plan), 300);
  assert.equal(scoreMemoryRecord('red green blue yellow', plan), 300);
  assert.equal(score('red', 'red green', ['red green']), 50);
});

test('explicit English/Russian alternatives work without automatic translation', () => {
  const russian = '\u043b\u044e\u0431\u0438\u0442 \u043a\u043e\u0444\u0435';
  assert.equal(score(russian, 'likes coffee'), 0);
  assert.equal(score('likes coffee', russian), 0);
  assert.equal(score(russian, 'likes coffee', [russian]), 300);
  assert.equal(score('likes coffee', russian, ['likes coffee']), 300);
});

test('empty and stopword-only queries and records produce zero, not NaN', () => {
  for (const query of [undefined, '', '   ', 'and the of', '\u0438 \u0432 \u043d\u0430', '...']) {
    assert.deepEqual(compileMemoryQuery(query).variants, []);
    assert.equal(score('real facts', query), 0);
  }
  assert.equal(score('', 'facts'), 0);
  assert.equal(score('!!!', 'facts'), 0);
  assert.equal(score('coffee', undefined, ['coffee']), 300);
});

test('removes stopwords and deduplicates terms without mutating alternatives', () => {
  const alternatives = Object.freeze(['the blue blue']);
  const plan = compileMemoryQuery('the red and red', alternatives);
  assert.deepEqual(plan.variants.map(variant => variant.terms), [['red'], ['blue']]);
  assert.deepEqual(alternatives, ['the blue blue']);
  assert.ok(score('green red', 'the red and green') >= 200);
});

test('plan and all exposed nested values are immutable', () => {
  const plan = compileMemoryQuery('red green', ['blue']);
  assert.ok(Object.isFrozen(plan));
  assert.ok(Object.isFrozen(plan.variants));
  for (const variant of plan.variants) {
    assert.ok(Object.isFrozen(variant));
    assert.ok(Object.isFrozen(variant.terms));
  }
  assert.throws(() => { plan.variants = []; }, TypeError);
  assert.throws(() => plan.variants.push({}), TypeError);
  assert.throws(() => { plan.variants[0].phrase = 'other'; }, TypeError);
  assert.throws(() => plan.variants[0].terms.push('other'), TypeError);
});

test('rejects invalid query types without coercion', () => {
  const hostile = { toString() { throw new Error('must not coerce'); } };
  for (const value of [null, 1, true, {}, [], new String('red'), hostile]) {
    assert.throws(() => compileMemoryQuery(value), queryError(TypeError));
    assert.throws(() => compileMemoryQuery('red', [value]), queryError(TypeError));
  }
  for (const alternatives of [null, 'red', {}, 1]) {
    assert.throws(() => compileMemoryQuery('red', alternatives), queryError(TypeError));
  }
  assert.throws(() => compileMemoryQuery('', Array(1)), queryError(TypeError));
});

test('enforces at most four alternatives, including empty alternatives', () => {
  assert.equal(compileMemoryQuery('red', ['blue', 'green', 'yellow', 'black']).variants.length, 5);
  assert.throws(() => compileMemoryQuery('', ['', '', '', '', '']), queryError(RangeError));
});

test('enforces 512 UTF-8 bytes per variant before normalization', () => {
  for (const value of ['x'.repeat(512), '\u044f'.repeat(256), '\u{1f600}'.repeat(128)]) {
    assert.doesNotThrow(() => compileMemoryQuery(value));
    assert.throws(() => compileMemoryQuery(`${value}x`), queryError(RangeError));
    assert.throws(() => compileMemoryQuery('', [`${value}x`]), queryError(RangeError));
  }
  assert.throws(() => compileMemoryQuery('\u044f'.repeat(257)), queryError(RangeError));
  assert.doesNotThrow(() => compileMemoryQuery('\ufb03'.repeat(170)));
});

test('enforces 24 non-stopword terms per variant, counting repeated terms', () => {
  const terms = Array.from({ length: 24 }, (_, index) => `term${index}`).join(' ');
  assert.equal(compileMemoryQuery(terms).variants[0].terms.length, 24);
  assert.throws(() => compileMemoryQuery(`${terms} extra`), queryError(RangeError));
  assert.throws(() => compileMemoryQuery('', [`${terms} extra`]), queryError(RangeError));
  assert.throws(() => compileMemoryQuery(Array(25).fill('red').join(' ')), queryError(RangeError));
  assert.doesNotThrow(() => compileMemoryQuery(`${terms} the and of`));
});

test('enforces 16 KiB record bytes, including long multibyte Unicode', () => {
  const plan = compileMemoryQuery('\u044f'.repeat(256));
  const text = '\u044f'.repeat(8192);
  assert.equal(scoreMemoryRecord(text, plan), 200);
  assert.throws(() => scoreMemoryRecord(`${text}x`, plan), recordError(RangeError));
  assert.equal(scoreMemoryRecord('\u{1f600}'.repeat(4096), plan), 0);
  assert.throws(() => scoreMemoryRecord('\u{1f600}'.repeat(4097), plan), recordError(RangeError));
  assert.equal(scoreMemoryRecord('x'.repeat(16384), plan), 0);
  assert.throws(() => scoreMemoryRecord('x'.repeat(16385), plan), recordError(RangeError));
});

test('rejects invalid record and plan types instead of coercing them', () => {
  const plan = compileMemoryQuery('red');
  for (const text of [undefined, null, 12, {}, ['red'], new String('red')]) {
    assert.throws(() => scoreMemoryRecord(text, plan), recordError(TypeError));
  }
  for (const value of [undefined, null, {}, [], 'red', { variants: [] }]) {
    assert.throws(() => scoreMemoryRecord('red', value), queryError(TypeError));
  }
});

test('all valid scores remain finite, nonnegative, deterministic and bounded', () => {
  for (const query of ['', 'red', 'red green', '\u043a\u043e\u0444\u0435', '\ufdfa']) {
    const plan = compileMemoryQuery(query, ['blue yellow']);
    for (const text of ['', 'red green', 'green red blue', '\u043a\u043e\u0444\u0435', '\ufdfa'.repeat(100)]) {
      const value = scoreMemoryRecord(text, plan);
      assert.ok(Number.isFinite(value) && value >= 0 && value <= 300);
      assert.equal(scoreMemoryRecord(text, plan), value);
    }
  }
});
