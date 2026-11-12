const test = require('node:test');
const assert = require('node:assert/strict');
const output = require('../../static/generation-output.js');
const batch = require('../../static/generation-batch.js');
const assistant = require('../../static/generation-assistant.js');

test('MOSS output plan reports optional upscale separately from base generation', () => {
  const values = {width:'1024',height:'1024',aspect_ratio:'1:1',output_megapixels:'1',dimension_multiple:'16'};
  const base = output.plan({family:'moss',templateID:'text-to-image',values});
  assert.deepEqual(base.base,{width:1024,height:1024});
  assert.deepEqual(base.final,base.base);
  const upscaled = output.plan({family:'moss',templateID:'text-to-image',values:{...values,moss_seedvr2:'true',moss_upscale_resolution:'2560'}});
  assert.deepEqual(upscaled.final,{width:2560,height:2560});
  assert.deepEqual(upscaled.processing,['SeedVR2 Sharp']);
  assert.equal(upscaled.approximate,true);
});

test('MOSS assistant request carries family context and separate negative conditioning', () => {
  const body = assistant.buildRequest({mode:'text-to-image',workflow:'moss-image',prompt:'A blue teapot',profile:'realistic',cfg:3,negative:'watermark',images:[]});
  assert.equal(body.get('generation_workflow'),'moss-image');
  assert.equal(body.get('cfg'),'3');
  assert.equal(body.get('negative_prompt'),'watermark');
  assert.ok(batch.parameterOptions({family:'moss',templateID:'text-to-image'}).some(item => item.name==='steps'));
});
