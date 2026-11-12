const { test } = require('node:test');
const assert = require('node:assert/strict');
const { validationError } = require('../../static/suggestions.js');

test('suggestions allow incomplete description only in drafts', () => {
  assert.equal(validationError({ description: '' }, 'save'), '');
  assert.ok(validationError({ description: 'short' }, 'submit'));
  assert.equal(validationError({ description: 'x'.repeat(10) }, 'submit'), '');
  assert.ok(validationError({ description: 'x'.repeat(4001) }, 'submit'));
});
test('suggestions count Unicode code points and trim decision comments', () => {
  assert.ok(validationError({ comment: '  no  ' }, 'rejected'));
  assert.equal(validationError({ comment: '\u{1F600}'.repeat(3) }, 'rejected'), '');
  assert.equal(validationError({ comment: '' }, 'accepted'), '');
  assert.ok(validationError({ comment: 'x'.repeat(1001) }, 'accepted'));
});
test('suggestions enforce the existing JSON size limit for both draft and submission', () => {
  for (const action of ['save', 'submit']) {
    assert.equal(validationError({ description: 'x'.repeat(10), fileSize: 5 * 1024 * 1024 }, action), '');
    assert.ok(validationError({ description: 'x'.repeat(10), fileSize: 5 * 1024 * 1024 + 1 }, action));
  }
});
