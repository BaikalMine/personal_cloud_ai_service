const { test } = require('node:test');
const assert = require('node:assert/strict');
const { validate } = require('../../static/generation-batch.js');

test('batch count rejects incomplete or invalid input without changing it', () => {
  for (const count of ['', ' ', '1', '21', '2.5', 'NaN', 'Infinity', undefined]) {
    const config = { enabled: true, mode: 'seeds', count };
    assert.equal(validate(config).valid, false, String(count));
    assert.equal(config.count, count);
  }
});
test('batch count accepts replacements and boundaries', () => {
  for (const count of ['2', '4', '5', '6', '20', 6]) {
    const result = validate({ enabled: true, mode: 'seeds', count });
    assert.equal(result.valid, true);
    assert.equal(result.count, Number(count));
  }
  assert.equal(validate({ enabled: false, count: '' }).valid, true);
});
