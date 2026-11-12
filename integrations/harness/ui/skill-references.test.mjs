import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,mkdir,writeFile,rm,symlink} from 'node:fs/promises';
import {join} from 'node:path';
import {tmpdir} from 'node:os';
import {loadStoryReferences} from './skill-references.mjs';
async function fixture(fn){const root=await mkdtemp(join(tmpdir(),'skill-refs-'));try{await mkdir(join(root,'references'));await fn(root);}finally{await rm(root,{recursive:true,force:true});}}
test('both managed skills receive a complete reference larger than the read tool limit',async()=>fixture(async root=>{
 const body='Synthetic reference.\n'.repeat(6500)+'END_OF_REFERENCE_SENTINEL';
 await writeFile(join(root,'references/example.md'),body);
 for(const name of ['story-writing','story-writing-local']){
 const text=await loadStoryReferences(name,root,'Read `references/example.md` and `references/example.md`.');
 assert.ok(text.includes(body));assert.equal(text.split('## Loaded reference:').length,2);assert.ok(text.includes('"complete":true'));}
}));
test('other skills remain untouched',async()=>assert.equal(await loadStoryReferences('other','/not-present','Original'),'Original'));
test('missing and oversized files fail instead of silently truncating',async()=>fixture(async root=>{
 await assert.rejects(loadStoryReferences('story-writing',root,'`references/missing.md`'));
 await writeFile(join(root,'references/large.md'),'x'.repeat(256*1024));
 await assert.rejects(loadStoryReferences('story-writing',root,'`references/large.md`'),/budget/);
}));
test('traversal and symlink references are rejected',async()=>fixture(async root=>{
 await assert.rejects(loadStoryReferences('story-writing',root,'`references/../secret.md`'),/Unsafe/);
 await writeFile(join(root,'outside.md'),'synthetic');await symlink(join(root,'outside.md'),join(root,'references/link.md'));
 await assert.rejects(loadStoryReferences('story-writing',root,'`references/link.md`'),/escaped/);
}));
test('aborted loading is never returned as success',async()=>fixture(async root=>{
 const c=new AbortController();c.abort();await assert.rejects(loadStoryReferences('story-writing',root,'`references/test.md`',c.signal));
}));
test('invalid UTF-8 and nested reference instructions do not extend the manifest',async()=>fixture(async root=>{
 await writeFile(join(root,'references/a.md'),Buffer.from([255]));await assert.rejects(loadStoryReferences('story-writing',root,'`references/a.md`'));
 await writeFile(join(root,'references/a.md'),'Nested `references/secret.md`');
 const text=await loadStoryReferences('story-writing',root,'`references/a.md`');assert.equal(text.split('## Loaded reference:').length,2);
}));
