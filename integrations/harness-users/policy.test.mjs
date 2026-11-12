import test from 'node:test';
import assert from 'node:assert/strict';
import {userIdentity,equalSecret,scopedIdentity,publicHeaders} from './policy.mjs';
import {UploadRetention} from './retention.mjs';
import {inspectStorage} from './storage-worker.mjs';
import {mkdtemp,mkdir,writeFile,rename,readFile,rm,lstat,symlink} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';

test('tenant identities cannot alias each other or accept path/header injection',()=>{
  const binds=new Set(),uids=new Set();
  for(let id=1;id<=60000;id++){const user=userIdentity(String(id));assert(!binds.has(user.bind));binds.add(user.bind);assert(!uids.has(user.uid));uids.add(user.uid);}
  for(const value of ['01','0','60001','-1','1/../../2','1\n','1e2',undefined,['1']])assert.throws(()=>userIdentity(value));
  assert.notEqual(scopedIdentity(1,'same-session'),scopedIdentity(2,'same-session'));
  assert.throws(()=>scopedIdentity(1,''));
});
test('browser credentials never select an underlying tenant session',()=>{
  assert.deepEqual(publicHeaders({cookie:'other-user',authorization:'admin','x-harness-user':'2','x-forwarded-host':'private','x-ai-harness-session':'forged','idempotency-key':'old',host:'forged',accept:'text/html'},'public.example','dsh-auth-own=secret'),{accept:'text/html',host:'public.example',cookie:'dsh-auth-own=secret'});
  assert(equalSecret('a'.repeat(64),'a'.repeat(64)));assert(!equalSecret('b'.repeat(64),'a'.repeat(64)));assert(!equalSecret('x','x'));
});
test('upload expiry survives restart and rename without erasing chat history',async()=>{
  const root=await mkdtemp(join(tmpdir(),'harness-retention-'));let ledger;
  try{
    const native=join(root,'native'),workspace=join(root,'workspace');await mkdir(native);await mkdir(workspace);
    const upload=join(workspace,'large.bin');await writeFile(upload,'uploaded bytes');await writeFile(join(workspace,'generated.md'),'keep result');await writeFile(join(root,'chat.json'),'keep history');
    await symlink(join(root,'chat.json'),join(workspace,'outside-link'));
    const inode=(await inspectStorage({},[],{native,workspace})).find(x=>x.path===upload).inode;
    let now=1000;ledger=new UploadRetention(join(root,'ledger.sqlite'),{now:()=>now});ledger.register(1,inode);const expiry=ledger.next(1,inode);
    now+=23*3600000;ledger.register(1,inode);assert.equal(ledger.next(1,inode),expiry);assert.deepEqual(ledger.expired(1),[]);
    ledger.close();ledger=new UploadRetention(join(root,'ledger.sqlite'),{now:()=>now});await rename(upload,join(workspace,'renamed.bin'));
    now=expiry;assert.deepEqual(ledger.expired(2),[]);assert.deepEqual(ledger.expired(1),[inode]);
    await inspectStorage({},ledger.expired(1),{native,workspace});await assert.rejects(lstat(join(workspace,'renamed.bin')),{code:'ENOENT'});
    assert.equal(await readFile(join(workspace,'generated.md'),'utf8'),'keep result');assert.equal(await readFile(join(root,'chat.json'),'utf8'),'keep history');
  }finally{ledger?.close();await rm(root,{recursive:true,force:true});}
});
