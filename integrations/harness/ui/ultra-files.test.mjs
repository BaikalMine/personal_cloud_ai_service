import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, mkdir, readdir, readFile, writeFile, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { saveUltraImage } from './ultra-files.mjs';
import { createUltraUploads } from './ultra-uploads.mjs';

test('PNG delivery is durable, scoped to cwd, idempotent, and never overwrites another file', async t => {
  const root=await mkdtemp(join(tmpdir(),'ultra-files-'));t.after(()=>rm(root,{recursive:true,force:true}));
  const id='1a8ecc8b-3651-4ee4-a187-443440db6611',bytes=Buffer.from('test PNG contents');
  const file=await saveUltraImage(root,id,0,bytes);
  assert.equal(file.path,join(root,'generated','krea-ultra',id,'krea2-image--1.png'));
  assert.deepEqual(await readFile(file.path),bytes);
  await saveUltraImage(root,id,0,bytes);
  await assert.rejects(saveUltraImage(root,id,0,Buffer.from('different')),/different contents/);
  assert.deepEqual(await readdir(join(root,'generated','krea-ultra',id)),['krea2-image--1.png']);
  await assert.rejects(saveUltraImage(root,'../../escape',0,bytes),/identity/);
  const aborted=AbortSignal.abort();await assert.rejects(saveUltraImage(root,id,1,bytes,aborted));
  await assert.rejects(readFile(join(root,'generated','krea-ultra',id,'krea2-image--2.png')),e=>e.code==='ENOENT');
});

test('named outputs stay idempotent across retrieve, renaming and legacy files',async t=>{
  const root=await mkdtemp(join(tmpdir(),'ultra-names-'));t.after(()=>rm(root,{recursive:true,force:true}));
  const id='1a8ecc8b-3651-4ee4-a187-443440db6611',bytes=Buffer.from('same PNG');
  const first=await saveUltraImage(root,id,0,bytes,undefined,'krea-ultra','blue ceramic vase.png');
  assert.equal(first.file_name,'blue-ceramic-vase--1.png');
  assert.equal(first.description,'blue ceramic vase');
  const recovered=await saveUltraImage(root,id,0,bytes,undefined,'krea-ultra','another-name');
  assert.equal(recovered.path,first.path);
  assert.deepEqual(await readdir(join(root,'generated','krea-ultra',id)),[first.file_name]);
  const oldId='2a8ecc8b-3651-4ee4-a187-443440db6611';
  const folder=join(root,'generated','krea-ultra',oldId);await mkdir(folder);
  await writeFile(join(folder,'1.png'),bytes);
  const legacy=await saveUltraImage(root,oldId,0,bytes,undefined,'krea-ultra','new-name');
  assert.equal(legacy.path,join(folder,'1.png'));
  assert.deepEqual(await readdir(folder),['1.png']);
  await assert.rejects(saveUltraImage(root,id,1,bytes,undefined,'krea-ultra','../escape'),/output_name/);
});

test('PNG delivery refuses symlink folders and destinations',async t=>{
  const root=await mkdtemp(join(tmpdir(),'ultra-links-'));t.after(()=>rm(root,{recursive:true,force:true}));
  const cwd=join(root,'chat'),outside=join(root,'outside');await mkdir(cwd);await mkdir(outside);
  await symlink(outside,join(cwd,'generated'));
  await assert.rejects(saveUltraImage(cwd,'1a8ecc8b-3651-4ee4-a187-443440db6611',0,Buffer.from('png')),/Unsafe/);
  assert.deepEqual(await readdir(outside),[]);
  await rm(join(cwd,'generated'));const file=await saveUltraImage(cwd,'1a8ecc8b-3651-4ee4-a187-443440db6611',0,Buffer.from('png'));
  await writeFile(join(outside,'keep.png'),'png');await rm(file.path);await symlink(join(outside,'keep.png'),file.path);
  await assert.rejects(saveUltraImage(cwd,'1a8ecc8b-3651-4ee4-a187-443440db6611',0,Buffer.from('png')),/different contents/);
  assert.equal(await readFile(join(outside,'keep.png'),'utf8'),'png');
});

test('upload reuse is byte-based, task-scoped, bounded and expiring',async()=>{
  let calls=0,now=0;const cache=createUltraUploads({call:async()=>({id:`upload-${++calls}`}),now:()=>now});
  const first={agent:{session:{}}},second={agent:{session:{}}};
  const a=await cache.upload(Buffer.from('one'),first);
  assert.deepEqual(await cache.upload(Buffer.from('one'),first),a);assert.equal(calls,1);
  await cache.upload(Buffer.from('changed'),first);assert.equal(calls,2);
  await cache.upload(Buffer.from('one'),second);assert.equal(calls,3);
  now=600001;await cache.upload(Buffer.from('one'),first);assert.equal(calls,4);
  cache.clear(first.agent.session);await cache.upload(Buffer.from('one'),first);assert.equal(calls,5);
  for(let i=0;i<17;i++)await cache.upload(Buffer.from(`extra-${i}`),first);
  await cache.upload(Buffer.from('one'),first);assert.equal(calls,23);
});
