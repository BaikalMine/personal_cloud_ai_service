import assert from 'node:assert/strict';
import test from 'node:test';
import vm from 'node:vm';
import {generatedImageTest, isGeneratedImagePath, upgradeGeneratedImageMatcher} from './generated-image-path.mjs';

test('native deliverables and recovery recognize named and legacy PNGs only', () => {
  const prefix='/workspace/generated/krea-ultra/10000000-0000-4000-8000-000000000001/';
  for (const name of ['1.png','blue-vase--1.png','\u0441\u0438\u043d\u044f\u044f-\u0432\u0430\u0437\u0430--2.png']) {
    const path=prefix+name;
    assert(isGeneratedImagePath(path));
    assert(vm.runInNewContext(generatedImageTest,{file:{path}}));
    assert(isGeneratedImagePath(path.replace('krea-ultra','moss-image')));
  }
  for(const name of ['../blue--1.png','nested/a--1.png','blue--1.svg','blue--0.png','script.png','blue\\vase--1.png'])assert(!isGeneratedImagePath(prefix+name));
  const old=String.raw`x=/\/generated\/(?:krea-ultra|moss-image)\/[a-f0-9-]{36}\/\d+\.png$/.test(file.path);`;
  const upgraded=upgradeGeneratedImageMatcher(old);
  assert(upgraded.includes(generatedImageTest));
  assert.equal(upgradeGeneratedImageMatcher(upgraded),upgraded);
  assert.throws(()=>upgradeGeneratedImageMatcher('unrecognized'),/Unsupported/);
});
