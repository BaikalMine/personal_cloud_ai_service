import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, mkdir, readFile, writeFile, symlink, rm, stat, readdir, realpath, rename } from 'node:fs/promises';
import { createReadStream } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { tmpdir } from 'node:os';
import { basename, join } from 'node:path';
import { createWorkspaceFiles, registerWorkspaceFiles, WORKSPACE_UPLOAD_LIMIT } from './workspace-files.mjs';

test('500 MiB workspace upload streams to disk intact without base64', async t => {
  const {root,api}=await fixture(t), chunk=Buffer.alloc(1024*1024, 0x5a), hash=createHash('sha256');
  let remaining=500;
  const body=new ReadableStream({pull(controller){if(!remaining){controller.close();return;}remaining--;hash.update(chunk);controller.enqueue(chunk);}});
  const result=await api.upload({sessionId:'known-session',path:root,name:'large.bin',bytes:500*1024*1024,body});
  assert.equal((await stat(result.path)).size,500*1024*1024);
  const actual=createHash('sha256');for await (const bytes of createReadStream(result.path))actual.update(bytes);
  assert.equal(actual.digest('hex'),hash.digest('hex'));
});

test('stream uploads reject oversize, truncation, cancellation and unsafe paths without partial files', async t => {
  const {root,temporary,api}=await fixture(t);
  const upload=(extra={})=>api.upload({sessionId:'known-session',path:root,name:'file.bin',bytes:1,body:new Blob(['x']).stream(),...extra});
  await assert.rejects(upload({bytes:WORKSPACE_UPLOAD_LIMIT+1}),e=>e.code==='stream_upload_too_large');
  await assert.rejects(upload({bytes:2}),e=>e.code==='invalid_request');
  await assert.rejects(upload({bytes:0}),e=>e.code==='stream_upload_too_large');
  await assert.rejects(upload({name:'../escape'}),e=>e.code==='invalid_name');
  await assert.rejects(upload({path:temporary}),e=>e.code==='outside_workspace');
  await assert.rejects(upload({sessionId:'unknown'}),e=>e.code==='session_unavailable');
  const controller=new AbortController();
  const body=new ReadableStream({pull(stream){controller.abort();stream.enqueue(new Uint8Array([1]));stream.close();}});
  await assert.rejects(api.upload({sessionId:'known-session',path:root,name:'aborted.bin',bytes:1,body},controller.signal));
  assert.deepEqual(await readdir(root),[]);
  await upload();await assert.rejects(upload(),e=>e.code==='EEXIST');
  assert.equal(await readFile(join(root,'file.bin'),'utf8'),'x');
  assert.deepEqual(await readdir(root),['file.bin']);
});

async function fixture(t, registerUpload, { synthetic = false } = {}) {
  const temporary = await mkdtemp(join(await realpath(tmpdir()), 'harness-files-')), root = join(temporary, 'workspace');
  await mkdir(root);
  let ctx, fs;
  t.after(async () => { await ctx?.fiber.dispose(); await rm(temporary, { recursive:true, force:true }); });
  if (synthetic) {
    fs = { resolve:async path => path, processPath:path => path, async stat(path) {
      const entry = await stat(path, { bigint:true });
      return { type:entry.isDirectory() ? 'directory' : 'file', size:Number(entry.size),
        version:[entry.dev, entry.ino, entry.size, entry.mtimeNs, entry.ctimeNs].join(':') };
    } };
  } else {
    const { Context } = await import('@deepseek-ai/cordis');
    const { LocalFileSystem } = await import('@deepseek-ai/dsh-fs-local');
    ctx = new Context();
    fs = new LocalFileSystem(ctx, { cwd:root, diffBasisMaxBytes:2<<20 });
  }
  const roots = new Map([['known-session', root]]);
  const api = createWorkspaceFiles({ fs, allowedRoot:temporary, rootFor:async id => roots.get(id), registerUpload });
  const call = (action, path, values={}) => api.manage({action,sessionId:'known-session',path,...values});
  return { root, temporary, roots, fs, api, call };
}

test('cross-chat copy publishes independent bytes in the target root without overwriting', async t => {
  const {root,temporary,roots,call}=await fixture(t), target=join(temporary,'target'), folder=join(root,'nested');
  await Promise.all([mkdir(target), mkdir(folder)]);
  roots.set('target-chat',target);
  const path=join(folder,'note.bin'), bytes=Buffer.from([0,1,2,255]);
  await writeFile(path,bytes);
  const {version}=await call('inspect',path);
  const copy=()=>call('copyToChat',path,{version,targetSessionId:'target-chat'});
  const result=await copy();
  assert.deepEqual(result,{path:join(target,'note.bin'),name:'note.bin'});
  assert.deepEqual(await readFile(result.path),bytes);
  assert.deepEqual(await readFile(path),bytes);
  await writeFile(result.path,'keep');
  await assert.rejects(copy(),{code:'EEXIST'});
  assert.equal(await readFile(result.path,'utf8'),'keep');
  assert.deepEqual(await readFile(path),bytes);
  assert.deepEqual(await readdir(target),['note.bin']);
});

test('cross-chat copy rejects stale, unknown, invalid and unconfined requests without writing', async t => {
  const {root,temporary,roots,call}=await fixture(t), target=join(temporary,'target'), path=join(root,'note.txt');
  await mkdir(target);
  roots.set('target-chat',target);
  roots.set('outside-chat',tmpdir());
  await writeFile(path,'initial');
  const {version}=await call('inspect',path);
  const options={version,targetSessionId:'target-chat'};
  for (const extra of [{targetSessionId:'../target'},{destination:'elsewhere'},{name:'other.txt'}]) {
    await assert.rejects(call('copyToChat',path,{...options,...extra}),{code:'invalid_request'});
  }
  await assert.rejects(call('copyToChat',path,{...options,sessionId:'missing'}),{code:'session_unavailable'});
  await assert.rejects(call('copyToChat',path,{...options,targetSessionId:'missing'}),{code:'session_unavailable'});
  await assert.rejects(call('copyToChat',path,{...options,targetSessionId:'outside-chat'}),{code:'outside_workspace'});
  await assert.rejects(call('copyToChat',join(target,'note.txt'),options),{code:'outside_workspace'});
  await assert.rejects(call('copyToChat',root,options),{code:'copy_workspace_root'});
  await writeFile(path,'new version');
  await assert.rejects(call('copyToChat',path,options),{code:'changed'});
  assert.deepEqual(await readdir(target),[]);
});

test('current and shared-workspace chats copy only to different paths in existing safe folders', async t => {
  const {root,roots,call}=await fixture(t), folder=join(root,'notes','copies'), path=join(root,'note.txt');
  await mkdir(folder,{recursive:true});
  await writeFile(path,'source');
  await symlink(folder,join(root,'alias'),'junction');
  roots.set('shared-chat',root);
  const {version}=await call('inspect',path);
  const copy=(targetSessionId,targetFolder='')=>call('copyToChat',path,{version,targetSessionId,targetFolder});
  for (const id of ['known-session','shared-chat']) await assert.rejects(copy(id),{code:'EEXIST'});
  for (const folder of ['../elsewhere','/notes','notes//copies','notes/./copies','notes/..','notes/','notes\\copies','C:/notes']) {
    await assert.rejects(copy('known-session',folder),{code:'invalid_copy_folder'});
  }
  await assert.rejects(copy('known-session','missing'),{code:'ENOENT'});
  await assert.rejects(copy('known-session','note.txt'),{code:'not_directory'});
  await assert.rejects(copy('known-session','alias'),{code:'symlink'});
  assert.equal((await copy('known-session','notes/copies')).path,join(folder,'note.txt'));
  await assert.rejects(copy('shared-chat','notes/copies'),{code:'EEXIST'});
  assert.equal(await readFile(path,'utf8'),'source');
  assert.equal(await readFile(join(folder,'note.txt'),'utf8'),'source');
});

test('copy registers the published inode for upload retention and does not publish on registration failure', async t => {
  let registered, rejectRegistration=false;
  const {root,call}=await fixture(t,async path=>{
    if (rejectRegistration) throw Error('retention unavailable');
    registered=await stat(path,{bigint:true});
  });
  const path=join(root,'upload.txt'), folder=join(root,'copies');
  await mkdir(folder);
  await writeFile(path,'upload');
  const {version}=await call('inspect',path);
  const copy=()=>call('copyToChat',path,{version,targetSessionId:'known-session',targetFolder:'copies'});
  rejectRegistration=true;
  await assert.rejects(copy(),/retention unavailable/);
  assert.deepEqual(await readdir(folder),[]);
  rejectRegistration=false;
  const output=await stat((await copy()).path,{bigint:true});
  assert.deepEqual([output.dev,output.ino,output.birthtimeNs],[registered.dev,registered.ino,registered.birthtimeNs]);
  assert.equal(await readFile(path,'utf8'),'upload');
});

test('managed generated copies stay in a matching job folder that the sweeper can reach', async t => {
  const {root,temporary,roots,call}=await fixture(t), target=join(temporary,'target');
  const job='generated/krea-ultra/5e7c8c43-5ea0-46ca-a1df-af3430ce9981', path=join(root,job,'sample--1.png');
  await Promise.all([mkdir(join(root,job),{recursive:true}), mkdir(join(target,job),{recursive:true}), mkdir(join(target,'.git',job),{recursive:true})]);
  roots.set('target-chat',target);
  await writeFile(path,'fixture');
  const {version}=await call('inspect',path);
  const copy=targetFolder=>call('copyToChat',path,{version,targetSessionId:'target-chat',targetFolder});
  await assert.rejects(copy(''),{code:'copy_retention'});
  await assert.rejects(copy(`.git/${job}`),{code:'copy_retention'});
  assert.equal((await copy(job)).path,join(target,job,'sample--1.png'));
});

test('cross-chat copy refuses symlinks in either workspace and never replaces a destination link', async t => {
  const {root,temporary,roots,call}=await fixture(t), target=join(temporary,'target'), path=join(root,'note.txt');
  await mkdir(target);
  roots.set('target-chat',target);
  await writeFile(path,'source');
  await symlink(path,join(root,'link.txt'));
  await symlink(target,join(temporary,'alias'),'junction');
  roots.set('alias-chat',join(temporary,'alias'));
  const {version}=await call('inspect',path);
  const options={version,targetSessionId:'target-chat'};
  await assert.rejects(call('copyToChat',join(root,'link.txt'),options),{code:'symlink'});
  await assert.rejects(call('copyToChat',path,{...options,targetSessionId:'alias-chat'}),{code:'symlink'});
  roots.set('source-alias',join(temporary,'alias'));
  await assert.rejects(call('copyToChat','note.txt',{...options,sessionId:'source-alias'}),{code:'symlink'});
  const destination=join(target,'note.txt');
  await symlink(path,destination);
  await assert.rejects(call('copyToChat',path,options),{code:'EEXIST'});
  assert.equal(await readFile(path,'utf8'),'source');
  assert.deepEqual(await readdir(target),['note.txt']);
});

test('cross-chat copy races publish exactly one file even from different source chats', async t => {
  const {root,temporary,roots,call}=await fixture(t), target=join(temporary,'target'), other=join(temporary,'other');
  await Promise.all([mkdir(target), mkdir(other)]);
  roots.set('target-chat',target);
  roots.set('other-chat',other);
  const sources=await Promise.all([['known-session',root,'one'],['other-chat',other,'two']].map(async ([sessionId,folder,text]) => {
    const path=join(folder,'race.txt');
    await writeFile(path,text);
    const {version}=await call('inspect',path,{sessionId});
    return {sessionId,path,version,text};
  }));
  const results=await Promise.allSettled(sources.map(({sessionId,path,version}) => call('copyToChat',path,{sessionId,version,targetSessionId:'target-chat'})));
  const winner=results.findIndex(result=>result.status==='fulfilled');
  assert.notEqual(winner,-1);
  assert.equal(results[1-winner].reason?.code,'EEXIST');
  assert.equal(await readFile(join(target,'race.txt'),'utf8'),sources[winner].text);
  assert.deepEqual(await readdir(target),['race.txt']);
});

test('cross-chat copy cannot publish into a replaced target directory', {skip:process.platform !== 'linux'}, async t => {
  const {root,temporary,roots,fs,call}=await fixture(t), target=join(temporary,'target'), moved=join(temporary,'moved');
  const path=join(root,'note.txt');
  await mkdir(target);
  await writeFile(path,'fixture');
  roots.set('target-chat',target);
  const {version}=await call('inspect',path);
  const resolve=fs.resolve.bind(fs);
  let targetLookups=0;
  fs.resolve=async (path, options) => {
    if (path === target && ++targetLookups === 3) {
      await rename(target,moved);
      await mkdir(target);
    }
    return resolve(path,options);
  };
  await assert.rejects(call('copyToChat',path,{version,targetSessionId:'target-chat'}),{code:'changed'});
  assert.deepEqual(await readdir(target),[]);
  assert.deepEqual(await readdir(moved),[]);
  assert.equal(await readFile(path,'utf8'),'fixture');
});

async function folderFixture(t, registerUpload) {
  const fixtureResult = await fixture(t, registerUpload, { synthetic:true });
  const {root,temporary,roots,api,call} = fixtureResult;
  const source = join(root,'tree'), target = join(temporary,'target');
  await Promise.all([mkdir(source),mkdir(target)]);
  roots.set('target-chat',target);
  const copy = async (options = {}, signal) => {
    const {version} = await call('inspect',source);
    return api.manage({action:'copyToChat',sessionId:'known-session',path:source,version,targetSessionId:'target-chat',...options},signal);
  };
  return { ...fixtureResult, source, target, copy };
}

test('folder copy preserves nested binary files, empty directories and registered inodes', async t => {
  const registered = [];
  const {source,target,copy} = await folderFixture(t, async path => {
    const entries = await readdir(join(target,'copies'));
    assert.equal(entries.length,1);
    assert.match(entries[0],/^\.gateway-copy-/);
    registered.push(await stat(path,{bigint:true}));
  });
  const payload = Buffer.from([0,255,123,0,42]);
  await mkdir(join(source,'nested','empty'),{recursive:true});
  await writeFile(join(source,'nested','data.bin'),payload);
  await mkdir(join(target,'copies'));
  const result = await copy({targetFolder:'copies'});
  assert.deepEqual(result,{path:join(target,'copies','tree'),name:'tree'});
  assert.deepEqual(await readdir(join(result.path,'nested','empty')),[]);
  assert.deepEqual(await readFile(join(result.path,'nested','data.bin')),payload);
  assert.equal(registered.length,1);
  const copied = await stat(join(result.path,'nested','data.bin'),{bigint:true});
  assert.deepEqual([copied.dev,copied.ino,copied.birthtimeNs],[registered[0].dev,registered[0].ino,registered[0].birthtimeNs]);
  await writeFile(join(result.path,'nested','data.bin'),'independent');
  assert.deepEqual(await readFile(join(source,'nested','data.bin')),payload);
  assert.deepEqual(await readdir(join(target,'copies')),['tree']);
});

test('folder copy never replaces an existing file or directory, including late conflicts', async t => {
  let conflict;
  const {source,target,copy} = await folderFixture(t, async () => { if (conflict) await conflict(); });
  const destination = join(target,'tree');
  await writeFile(join(source,'data'),'source');
  await mkdir(destination);
  await writeFile(join(destination,'keep'),'original');
  await assert.rejects(copy(),{code:'EEXIST'});
  assert.deepEqual(await readdir(destination),['keep']);
  assert.equal(await readFile(join(destination,'keep'),'utf8'),'original');
  await rm(destination,{recursive:true});
  conflict = () => mkdir(destination);
  await assert.rejects(copy(),{code:'EEXIST'});
  assert.deepEqual(await readdir(destination),[]);
  await rm(destination,{recursive:true});
  conflict = () => writeFile(destination,'keep');
  await assert.rejects(copy(),{code:'EEXIST'});
  assert.equal(await readFile(destination,'utf8'),'keep');
  assert.deepEqual(await readdir(target),['tree']);
  assert.equal(await readFile(join(source,'data'),'utf8'),'source');
});

test('folder copy rejects self, descendant and symlink destinations, and nested symlinks', async t => {
  const {root,roots,source,target,copy} = await folderFixture(t);
  const nested = join(source,'nested');
  await mkdir(nested);
  roots.set('shared-chat',root);
  roots.set('descendant-chat',nested);
  for (const options of [{targetSessionId:'shared-chat'},{targetSessionId:'shared-chat',targetFolder:'tree'},
    {targetSessionId:'shared-chat',targetFolder:'tree/nested'},{targetSessionId:'descendant-chat'}]) {
    await assert.rejects(copy(options),{code:'copy_descendant'});
  }
  await symlink(nested,join(target,'alias'),'junction');
  await assert.rejects(copy({targetFolder:'alias'}),{code:'symlink'});
  await symlink(target,join(nested,'alias'),'junction');
  await assert.rejects(copy(),{code:'symlink'});
  assert.deepEqual(await readdir(target),['alias']);
});

test('folder copy rejects unsupported entries and excessive nesting without publishing', async t => {
  const {source,target,copy} = await folderFixture(t);
  if (process.platform === 'linux') {
    const fifo = join(source,'pipe');
    execFileSync('mkfifo',[fifo]);
    await assert.rejects(copy(),{code:'copy_unsupported'});
    await rm(fifo);
  }
  await mkdir(join(source,...Array(33).fill('d')),{recursive:true});
  await assert.rejects(copy(),{code:'copy_limit'});
  assert.deepEqual(await readdir(target),[]);
});

test('folder copy keeps managed output in a sweeper-visible job folder', async t => {
  const {root,source,target,copy,call} = await folderFixture(t);
  const job = 'generated/moss-image/5e7c8c43-5ea0-46ca-a1df-af3430ce9981';
  const origin = join(source,job), path = join(origin,'sample--1.png');
  await mkdir(origin,{recursive:true});
  await writeFile(path,'managed');
  const {version} = await call('inspect',path);
  assert.equal((await copy()).path,join(target,'tree'));
  assert.equal(await readFile(join(target,'tree',job,'sample--1.png'),'utf8'),'managed');
  await mkdir(join(root,'copies'));
  const {version:jobVersion} = await call('inspect',origin);
  await assert.rejects(call('copyToChat',origin,{version:jobVersion,targetSessionId:'known-session',targetFolder:'copies'}),{code:'copy_retention'});
  await mkdir(join(target,'.git'));
  await assert.rejects(copy({targetFolder:'.git'}),{code:'copy_retention'});
  await assert.rejects(call('copyToChat',path,{version,targetSessionId:'target-chat'}),{code:'copy_retention'});
  await mkdir(join(origin,'nested'));
  await writeFile(join(origin,'nested','sample--2.png'),'managed');
  await mkdir(join(target,'copies'));
  await assert.rejects(copy({targetFolder:'copies'}),{code:'copy_retention'});
  assert.deepEqual(await readdir(join(target,'.git')),[]);
  assert.deepEqual(await readdir(join(target,'copies')),[]);
});

test('folder copy cleans staged data after cancellation, retention failure and late source changes', async t => {
  const controller = new AbortController();
  let onRegister, registrations = 0;
  const {source,target,copy} = await folderFixture(t, async path => { registrations++; await onRegister?.(path); });
  await mkdir(join(source,'nested'));
  const paths = [join(source,'nested','a.bin'),join(source,'nested','b.bin')];
  for (const path of paths) await writeFile(path,'source');
  onRegister = () => { controller.abort(); };
  await assert.rejects(copy({},controller.signal),{name:'AbortError'});
  assert.equal(registrations,1);
  assert.deepEqual(await readdir(target),[]);
  onRegister = () => { throw Error('retention unavailable'); };
  await assert.rejects(copy(),/retention unavailable/);
  assert.deepEqual(await readdir(target),[]);
  for (const path of paths) assert.equal(await readFile(path,'utf8'),'source');
  let first;
  onRegister = async path => {
    if (!first) first = paths.find(sourcePath => basename(sourcePath) === basename(path));
    else await writeFile(first,'changed after staging');
  };
  await assert.rejects(copy(),{code:'changed'});
  assert.deepEqual(await readdir(target),[]);
  assert.equal(await readFile(first,'utf8'),'changed after staging');
});

test('folder copy cleans the pinned stage when the destination parent is replaced', {skip:process.platform !== 'linux'}, async t => {
  const {source,target,temporary,copy} = await folderFixture(t, async () => {
    await rename(target,join(temporary,'moved'));
    await mkdir(target);
  });
  await writeFile(join(source,'data'),'source');
  await assert.rejects(copy(),{code:'changed'});
  assert.deepEqual(await readdir(target),[]);
  assert.deepEqual(await readdir(join(temporary,'moved')),[]);
  assert.equal(await readFile(join(source,'data'),'utf8'),'source');
});

test('workspace manage route authorizes both chat IDs, including persisted chats', async t => {
  const {root,temporary,fs,call}=await fixture(t), path=join(root,'note.txt'), target=join(temporary,'target');
  await mkdir(target);
  await writeFile(path,'fixture');
  const {version}=await call('inspect',path), routes=new Map(), denied=new Set(), lookedUp=[];
  const previous=process.env.HARNESS_WORKSPACE;
  process.env.HARNESS_WORKSPACE=temporary;
  try {
    registerWorkspaceFiles({fs, sessions:new Map([['known-session',{cwd:root}]]), workingDirectory:{get:session=>session.cwd},
      get:key=>key === 'sessionPersistence' ? {stat:async id=>{ lookedUp.push(id); return id === 'target-chat' ? {header:{cwd:target}} : undefined; }} :
        key === 'remoteAccess' ? {allows:({kind,id})=>kind === 'session' && !denied.has(id)} : undefined, effect:fn=>fn(),
      connection:{fetch:{register:route=>routes.set(route.path,route)}}});
  } finally {
    if (previous === undefined) delete process.env.HARNESS_WORKSPACE;
    else process.env.HARNESS_WORKSPACE=previous;
  }
  const route=routes.get('/api/gateway-files/manage');
  const copy=async () => (await route.fetch(new Request('http://localhost/api/gateway-files/manage',{
    method:'POST', headers:{'Content-Type':'application/json'},
    body:JSON.stringify({action:'copyToChat',sessionId:'known-session',targetSessionId:'target-chat',path,version}),
  }))).json();
  for (const id of ['known-session','target-chat']) {
    denied.add(id);
    assert.deepEqual(await copy(),{ok:false,error:{code:'session_unavailable'}});
    denied.delete(id);
  }
  assert.deepEqual(lookedUp,[]);
  assert.deepEqual(await readdir(target),[]);
  assert.equal((await copy()).ok,true);
  assert.equal(await readFile(join(target,'note.txt'),'utf8'),'fixture');
});

test('workspace text edits are versioned and preserve concurrent changes', async t => {
  const { root,call } = await fixture(t); const path=join(root,'chapter.md');
  await writeFile(path,'# Before\n');
  const before=await call('read',path);assert.equal(before.text,'# Before\n');
  const saved=await call('save',path,{version:before.version,text:'# After\n'});
  assert.notEqual(saved.version,before.version);assert.equal(await readFile(path,'utf8'),'# After\n');
  await assert.rejects(call('save',path,{version:before.version,text:'stale'}),e=>e.code==='FS_STALE_VERSION'||e.code==='changed');
  assert.equal(await readFile(path,'utf8'),'# After\n');
});

test('cancelling a stalled active stream releases its reader and leaves no temporary file',async t=>{
  const {root,api}=await fixture(t),controller=new AbortController();let cancelled=false;
  const body=new ReadableStream({start(stream){stream.enqueue(new Uint8Array([1]));},cancel(){cancelled=true;}});
  const pending=api.upload({sessionId:'known-session',path:root,name:'partial.bin',bytes:1000,body},controller.signal);
  await new Promise(resolve=>setTimeout(resolve,30));controller.abort();
  await assert.rejects(pending);assert.equal(cancelled,true);assert.deepEqual(await readdir(root),[]);
});

test('rename and deletion require a current version and never overwrite a destination', async t => {
  const { root,call }=await fixture(t);const path=join(root,'note.txt');await writeFile(path,'one');
  const old=await call('inspect',path);await writeFile(path,'a new version');
  await assert.rejects(call('delete',path,{version:old.version}),e=>e.code==='changed');
  const current=await call('inspect',path);await writeFile(join(root,'exists.txt'),'keep');
  await assert.rejects(call('rename',path,{version:current.version,name:'exists.txt'}),e=>e.code==='exists');
  assert.equal(await readFile(join(root,'exists.txt'),'utf8'),'keep');
  const renamed=await call('rename',path,{version:current.version,name:'renamed.txt'});
  assert.equal(await readFile(renamed.path,'utf8'),'a new version');
  const info=await call('inspect',renamed.path);await call('delete',renamed.path,{version:info.version});
  await assert.rejects(readFile(renamed.path),e=>e.code==='ENOENT');
});

test('root, parent traversal, symlinks and unknown sessions cannot be mutated', async t => {
  const { root,temporary,api,call }=await fixture(t);await writeFile(join(temporary,'outside.txt'),'keep');
  for(const path of [root,'../outside.txt',join(temporary,'outside.txt')])await assert.rejects(call('delete',path,{version:'irrelevant'}),e=>e.code==='outside_workspace');
  await symlink(join(temporary,'outside.txt'),join(root,'link.txt'));
  await assert.rejects(call('inspect',join(root,'link.txt')),e=>e.code==='symlink');
  await assert.rejects(api.manage({action:'inspect',sessionId:'unknown-session',path:root}),e=>e.code==='session_unavailable');
  assert.equal(await readFile(join(temporary,'outside.txt'),'utf8'),'keep');
});

test('folder deletion is non-recursive and uploads are bounded exclusive publications', async t => {
  const { root,call,api }=await fixture(t);
  const folder=await call('mkdir',root,{name:'notes'});await writeFile(join(folder.path,'keep.txt'),'keep');
  const info=await call('inspect',folder.path);
  await assert.rejects(call('delete',folder.path,{version:info.version}),e=>e.code==='ENOTEMPTY'||e.code==='EEXIST');
  assert.equal(await readFile(join(folder.path,'keep.txt'),'utf8'),'keep');
  await call('upload',root,{name:'uploaded.bin',base64:Buffer.from([0,1,255]).toString('base64')});
  await assert.rejects(call('upload',root,{name:'uploaded.bin',base64:'eA=='}),e=>e.code==='EEXIST');
  assert.deepEqual(await readFile(join(root,'uploaded.bin')),Buffer.from([0,1,255]));
  await assert.rejects(call('upload',root,{name:'bad',base64:'!bad!'}),e=>e.code==='upload_too_large');
  for(const name of ['../bad','a/b','a\\b','..',' bad'])await assert.rejects(call('mkdir',root,{name}),e=>e.code==='invalid_name');
  const binary=await api.download('known-session',join(root,'uploaded.bin'));
  assert.deepEqual(Buffer.from(await new Response(binary.body).arrayBuffer()),Buffer.from([0,1,255]));
});

test('inline image preview is scoped, PNG-only and never serves active content',async t=>{
  const {root,temporary,api}=await fixture(t),png=Buffer.from([137,80,78,71,13,10,26,10]);
  await writeFile(join(root,'image.png'),png);await writeFile(join(root,'fake.png'),'<script>bad</script>');
  const result=await api.download('known-session',join(root,'image.png'),undefined,true);
  assert.deepEqual(Buffer.from(await new Response(result.body).arrayBuffer()),png);
  await assert.rejects(api.download('known-session',join(root,'fake.png'),undefined,true),e=>e.code==='not_file');
  await writeFile(join(temporary,'outside.png'),png);
  await assert.rejects(api.download('known-session',join(temporary,'outside.png'),undefined,true),e=>e.code==='outside_workspace');
  await assert.rejects(api.download('unknown-session',join(root,'image.png'),undefined,true),e=>e.code==='session_unavailable');
});
