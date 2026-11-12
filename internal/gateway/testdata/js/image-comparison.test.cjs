const assert = require('node:assert/strict');
const test = require('node:test');
const comparison = require('../../static/image-comparison.js');

test('comparison accepts only retained same-origin input asset URLs', () => {
  const origin = 'https://gateway.test';
  const path = '/generate/draft/asset?id=11111111111111111111111111111111';
  assert.equal(comparison.originalURL(path, origin), path);
  assert.equal(comparison.originalURL(origin + path, origin), path);
  for (const value of ['https://other.test' + path, 'javascript:alert(1)', '/comfy/view?filename=secret.png',
    path + '&id=22222222222222222222222222222222', path + '&download=1', path + '#hash', '/generate/draft/asset?id=bad']) {
    assert.equal(comparison.originalURL(value, origin), '', value);
  }
});
test('curtain bounds follow visible image content at fit and while panning', () => {
  assert.deepEqual(comparison.visibleBounds({ left: 60, top: 0, width: 240, height: 600 }, 360, 600),
    { left: 60, top: 0, width: 240, height: 600 });
  assert.deepEqual(comparison.visibleBounds({ left: -140, top: -60, width: 720, height: 900 }, 360, 600),
    { left: 0, top: 0, width: 360, height: 600 });
  assert.deepEqual(comparison.visibleBounds({ left: 0, top: 150, width: 360, height: 300 }, 360, 600),
    { left: 0, top: 150, width: 360, height: 300 });
});
test('job binding requires a durable public ID', () => {
  assert.equal(comparison.validJobID('comparison-fixture-001'), true);
  for (const value of ['', '../other', 'short', 'a'.repeat(129), null]) assert.equal(comparison.validJobID(value), false);
});
