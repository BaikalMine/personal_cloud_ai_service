import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, chmod, readFile, writeFile, rm, stat, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash, randomUUID } from 'node:crypto';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { DatabaseSync } from 'node:sqlite';
import { createChatMemoryStore } from './chat-memory-store.mjs';

const posix = process.platform !== 'win32' && typeof process.getuid === 'function';
const exec = promisify(execFile);
const hash = value => createHash('sha256').update(value).digest('hex');
const moduleUrl = new URL('./chat-memory-store.mjs', import.meta.url).href;
async function fixture(t) {
  const home = await mkdtemp(join(tmpdir(), 'chat-memory-test-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  return { home, store: createChatMemoryStore({ dshHome: home }) };
}
async function legacy(home, session, data) {
  const root = join(home, 'chat-memory');
  await mkdir(root, { mode: 0o700, recursive: true });
  const dir = join(root, hash(session));
  await mkdir(dir, { mode: 0o700 });
  await writeFile(join(dir, 'records.json'), data, { mode: 0o600 });
  return dir;
}

test('copies fact status, provenance and graph links without overwriting local memory or duplicating IDs', {skip:!posix}, async t=>{
  const {store}=await fixture(t);
  const original=await store.add('old','Synthetic character',undefined,{aliases:['alias'],sources:[{file:'draft.md',line_from:3}]});
  const first=await store.setStatus('old',original.id,'critical','normal');
  const second=await store.add('old','Synthetic setting');
  const edge=await store.link('old',first.id,second.id,'supports','Synthetic link');
  await store.add('new','Unrelated target entry');
  const preview=await store.transferPreview('old','new');
  assert.deepEqual(preview,{sourceRevision:'4',targetRevision:'1',facts:{added:2,skipped:0},links:{added:1,skipped:0},fits:true});
  assert.equal((await store.list('new')).length,1);
  const copied=await store.transfer('old','new',preview.sourceRevision,preview.targetRevision);
  assert.deepEqual(copied,{...preview,targetRevision:'2'});
  assert.deepEqual(await store.get('new',first.id),first);
  assert.deepEqual((await store.graph('new',{id:first.id})).edges,[edge]);
  assert.equal(await store.revision('old'),'4');
  await store.setStatus('new',first.id,'normal','critical');
  await assert.rejects(store.transfer('old','new',copied.sourceRevision,copied.targetRevision),/conflict/);
  await store.update('new',first.id,'Local edit',first.text);
  await store.unlink('new',edge.id);
  const localEdge=await store.link('new',first.id,second.id,'supports','Local link note');
  const repeated=await store.transferPreview('old','new');
  assert.deepEqual(repeated.facts,{added:0,skipped:2});
  assert.deepEqual(repeated.links,{added:0,skipped:1});
  assert.deepEqual(await store.transfer('old','new',repeated.sourceRevision,repeated.targetRevision),repeated);
  const localFact=await store.get('new',first.id);
  assert.equal(localFact.text,'Local edit');
  assert.equal(Object.hasOwn(localFact,'status'),false);
  assert.deepEqual(await store.get('old',first.id),first);
  assert.deepEqual((await store.graph('new',{id:first.id})).edges,[localEdge]);
});

test('invalidates stale previews on either side and rejects over-quota transfers atomically', {skip:!posix}, async t=>{
  const {home,store}=await fixture(t);
  const first=await store.add('old','Original synthetic entry');
  let preview=await store.transferPreview('old','new');
  const second=await store.add('old','Second synthetic entry');
  await store.link('old',first.id,second.id,'related');
  await assert.rejects(store.transfer('old','new',preview.sourceRevision,preview.targetRevision),/conflict/);
  preview=await store.transferPreview('old','new');
  const target=await store.add('new','Existing synthetic entry');
  await assert.rejects(store.transfer('old','new',preview.sourceRevision,preview.targetRevision),/conflict/);
  const db=new DatabaseSync(join(home,'chat-memory','records.sqlite'));
  db.prepare('UPDATE records SET bytes=? WHERE id=?').run(50*1024*1024-512,target.id);
  db.close();
  preview=await store.transferPreview('old','new');
  assert.equal(preview.fits,false);
  await assert.rejects(store.transfer('old','new',preview.sourceRevision,preview.targetRevision),/limit/);
  assert.deepEqual(await store.list('new'),[target]);
  assert.equal((await store.status('new')).links,0);
  assert.equal(await store.revision('new'),preview.targetRevision);
  await assert.rejects(store.transferPreview('old','old'),/invalid_target_session/);
  await assert.rejects(store.transfer('old','new','3'),/invalid_revision/);
});

test('session revision changes atomically with mutations but not failed or replayed writes', {skip:!posix}, async t=>{
  const {home,store}=await fixture(t), second=createChatMemoryStore({dshHome:home});
  assert.equal(await store.revision('owner'),'0');
  const requestId=randomUUID(), first=await store.add('owner','Original synthetic fact',requestId);
  assert.equal(await second.revision('owner'),'1');
  await store.add('owner',first.text,requestId);
  assert.equal(await store.revision('owner'),'1');
  await assert.rejects(store.add('owner','Different replay',requestId),/conflict/);
  await assert.rejects(store.update('owner',first.id,'New fact','stale'),/conflict/);
  await assert.rejects(store.add('owner','x'.repeat(16385)),/invalid_text/);
  assert.equal(await store.revision('owner'),'1');
  assert.equal(await store.revision('other'),'0');
  const updated=await second.update('owner',first.id,'New fact',first.text);
  assert.equal(await store.revision('owner'),'2');
  const snapshot=await store.page('owner',0,50,'',true);
  assert.deepEqual(snapshot,{records:[updated],nextCursor:null,revision:'2'});
  assert.equal(Object.hasOwn(await store.page('owner'),'revision'),false);
  assert.equal(await store.delete('other',first.id),false);
  assert.equal(await store.revision('other'),'0');
  assert.equal(await store.delete('owner',first.id),true);
  assert.equal(await store.revision('owner'),'3');
  assert.equal(await store.delete('owner',first.id),false);
  assert.equal(await store.revision('owner'),'3');
  assert.deepEqual(await store.page('owner',0,50,'',true),{records:[],nextCursor:null,revision:'3'});
  await assert.rejects(store.page('owner',0,50,'','yes'),/invalid_revision_flag/);
});

test('validates arguments', async () => {
  assert.throws(() => createChatMemoryStore(), /invalid_dsh_home/);
  assert.throws(() => createChatMemoryStore({ dshHome: 'relative' }), /invalid_dsh_home/);
  assert.throws(() => createChatMemoryStore({ dshHome: tmpdir(), lockTimeoutMs: -1 }), /invalid_lock_timeout/);
  const store = createChatMemoryStore({ dshHome: tmpdir() });
  await assert.rejects(store.add('session', ''), /invalid_text/);
  await assert.rejects(store.add('session', 'x'.repeat(16385)), /invalid_text/);
  for (const id of [null, '', 'invalid', '00000000-0000-1000-8000-000000000000', 'AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA']) {
    await assert.rejects(store.add('session', 'text', id), /invalid_id/);
  }
  await assert.rejects(store.list(''), /invalid_session_id/);
  await assert.rejects(store.delete('session', '../bad'), /invalid_id/);
  const id = randomUUID();
  await assert.rejects(store.setStatus('session', '../bad', 'normal', 'normal'), /invalid_id/);
  await assert.rejects(store.setStatus('session', id, 'urgent', 'normal'), /invalid_status/);
  await assert.rejects(store.setStatus('session', id, 'critical'), /invalid_expected_status/);
  await assert.rejects(store.moveFact('session', '../bad', 'up', '0'), /invalid_id/);
  await assert.rejects(store.moveFact('session', id, 'first', '0'), /invalid_direction/);
  await assert.rejects(store.moveFact('session', id, 'up'), /invalid_revision/);
  await assert.rejects(store.moveFact('session', id, 'up', '01'), /invalid_revision/);
});

test('fact status defaults to normal, persists independently of edits, and bumps revision only on change', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const original = await store.add('one', 'synthetic', undefined, { aliases:['alias'], sources:[{file:'draft.md'}] });
  assert.equal(Object.hasOwn(original, 'status'), false);
  assert.deepEqual(await store.setStatus('one', original.id, 'normal', 'normal'), original);
  assert.equal(await store.revision('one'), '1');
  await assert.rejects(store.setStatus('two', original.id, 'important', 'normal'), /chat_memory_conflict/);
  const important = await store.setStatus('one', original.id, 'important', 'normal');
  assert.deepEqual(important, { ...original, status:'important' });
  const reopened = createChatMemoryStore({ dshHome: home });
  assert.deepEqual(await reopened.get('one', original.id, '2'), important);
  await assert.rejects(reopened.setStatus('one', original.id, 'important', 'normal'), /chat_memory_conflict/);
  assert.deepEqual(await reopened.setStatus('one', original.id, 'important', 'important'), important);
  assert.equal(await reopened.revision('one'), '2');
  const edited = await reopened.update('one', original.id, 'edited', original.text);
  assert.equal(edited.status, 'important');
  const normal = await store.setStatus('one', original.id, 'normal', 'important');
  assert.deepEqual(normal, { ...original, text:edited.text, updatedAt:edited.updatedAt });
  assert.deepEqual(await reopened.page('one', 0, 50, '', true), { records:[normal], nextCursor:null, revision:'4' });
  assert.equal(await reopened.revision('two'), '0');
});

test('concurrent status changes with the same expectation have only one winner', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const original = await store.add('same', 'synthetic');
  const script = `import { createChatMemoryStore } from ${JSON.stringify(moduleUrl)}; const s=createChatMemoryStore({dshHome:process.argv[1]}); try { const record=await s.setStatus('same',process.argv[2],process.argv[3],'normal'); process.stdout.write(record.status); } catch(e) { if(e.message !== 'chat_memory_conflict') throw e; process.stdout.write('conflict'); }`;
  const results = await Promise.all(['important','critical'].map(status => exec(process.execPath, ['--input-type=module','-e',script,home,original.id,status])));
  const outcomes = results.map(result => result.stdout);
  assert.equal(outcomes.filter(result => result === 'conflict').length, 1);
  assert.deepEqual(await store.get('same', original.id, '2'), { ...original, status:outcomes.find(result => result !== 'conflict') });
});

test('status JSON bytes count against the shared quota and rejection rolls back the change', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const original = await store.add('one', 'synthetic');
  const filler = await store.add('two', 'filler');
  const critical = { ...original, status:'critical' };
  const criticalBytes = Buffer.byteLength(JSON.stringify(critical));
  const db = new DatabaseSync(join(home, 'chat-memory', 'records.sqlite'));
  db.prepare('UPDATE records SET bytes = ? WHERE id = ?').run(50 * 1024 * 1024 - criticalBytes - 4, filler.id);
  db.close();
  await assert.rejects(store.setStatus('one', original.id, 'important', 'normal'), /chat_memory_limit/);
  assert.deepEqual(await store.get('one', original.id, '1'), original);
  assert.deepEqual(await store.setStatus('one', original.id, 'critical', 'normal'), critical);
  await assert.rejects(store.setStatus('one', original.id, 'important', 'critical'), /chat_memory_limit/);
  assert.deepEqual(await store.get('one', original.id, '2'), critical);
  assert.deepEqual(await store.setStatus('one', original.id, 'normal', 'critical'), original);
  assert.deepEqual(await store.setStatus('one', original.id, 'critical', 'normal'), critical);
  assert.equal(await store.revision('one'), '4');
});

test('moves facts across page boundaries, preserving payloads, links, other sessions and transfer order', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  const first = await store.add('source', 'First synthetic fact');
  const foreign = await store.add('other', 'First other fact');
  const middle = await store.add('source', 'Middle synthetic fact');
  const other = await store.add('other', 'Second other fact');
  const last = await store.add('source', 'Last synthetic fact');
  const prioritized = await store.setStatus('source', middle.id, 'important', 'normal');
  const edge = await store.link('source', first.id, middle.id, 'supports');
  const preview = await store.transferPreview('source', 'target');
  const revision = preview.sourceRevision;
  assert.deepEqual(await store.page('source', 0, 1, '', true), { records:[first], nextCursor:1, revision });
  assert.equal(await store.moveFact('source', first.id, 'up', revision), false);
  assert.equal(await store.moveFact('source', last.id, 'down', revision), false);
  await assert.rejects(store.moveFact('source', foreign.id, 'up', revision), /chat_memory_conflict/);
  await assert.rejects(store.moveFact('source', randomUUID(), 'down', revision), /chat_memory_conflict/);
  assert.equal(await store.revision('source'), revision);

  assert.equal(await store.moveFact('source', first.id, 'down', revision), true);
  assert.equal(await store.revision('source'), '6');
  await assert.rejects(store.moveFact('source', last.id, 'down', revision), /chat_memory_conflict/);
  await assert.rejects(store.graph('source', { id:first.id, expectedRevision:revision }), /chat_memory_conflict/);
  const page = await store.page('source', 0, 1, '', true);
  assert.deepEqual(page, { records:[prioritized], nextCursor:1, revision:'6' });
  assert.deepEqual(await store.page('source', page.nextCursor, 1, '', true), { records:[first], nextCursor:2, revision:page.revision });
  assert.equal(await store.moveFact('source', last.id, 'up', page.revision), true);
  assert.deepEqual(await store.page('source', 1, 2, '', true), { records:[last,first], nextCursor:null, revision:'7' });
  assert.deepEqual((await store.graph('source', { id:first.id, expectedRevision:'7' })).edges, [edge]);
  assert.deepEqual(await store.page('other', 0, 50, '', true), { records:[foreign,other], nextCursor:null, revision:'2' });

  await assert.rejects(store.transfer('source', 'target', preview.sourceRevision, preview.targetRevision), /chat_memory_conflict/);
  const fresh = await store.transferPreview('source', 'target');
  await store.transfer('source', 'target', fresh.sourceRevision, fresh.targetRevision);
  assert.deepEqual(await store.list('target'), [prioritized,last,first]);
  assert.deepEqual((await store.graph('target', { id:first.id })).edges, [edge]);
});

test('moves use exact 64-bit slots and roll back an interrupted swap', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const first = await store.add('one', 'First synthetic fact');
  const foreign = await store.add('two', 'Other synthetic fact');
  const last = await store.add('one', 'Last synthetic fact');
  const slot = 2n ** 53n + 1n;
  const db = new DatabaseSync(join(home, 'chat-memory', 'records.sqlite'));
  t.after(() => db.close());
  const setSlot = db.prepare('UPDATE records SET seq = ? WHERE id = ?');
  setSlot.run(slot, first.id);
  setSlot.run(slot + 1n, foreign.id);
  setSlot.run(slot + 2n, last.id);
  const readSlots = db.prepare('SELECT seq, id FROM records ORDER BY seq');
  readSlots.setReadBigInts(true);
  const slots = () => readSlots.all().map(({seq,id}) => [seq,id]);
  const before = slots();
  db.exec('CREATE TRIGGER ignore_final_swap BEFORE UPDATE OF seq ON records WHEN OLD.seq < 0 BEGIN SELECT RAISE(IGNORE); END');
  await assert.rejects(store.moveFact('one', first.id, 'down', '2'), /chat_memory_conflict/);
  assert.deepEqual(slots(), before);
  assert.equal(await store.revision('one'), '2');
  db.exec('DROP TRIGGER ignore_final_swap');
  assert.equal(await store.moveFact('one', first.id, 'down', '2'), true);
  assert.deepEqual(slots(), [[slot,last.id],[slot + 1n,foreign.id],[slot + 2n,first.id]]);
  assert.deepEqual(await store.page('one', 0, 50, '', true), { records:[last,first], nextCursor:null, revision:'3' });
});

test('competing moves from one revision have only one winner', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const first = await store.add('one', 'First synthetic fact');
  const middle = await store.add('one', 'Middle synthetic fact');
  const last = await store.add('one', 'Last synthetic fact');
  const script = `import { createChatMemoryStore } from ${JSON.stringify(moduleUrl)}; const s=createChatMemoryStore({dshHome:process.argv[1]}); try { process.stdout.write(String(await s.moveFact('one',process.argv[2],process.argv[3],'3'))); } catch(e) { if(e.message !== 'chat_memory_conflict') throw e; process.stdout.write('conflict'); }`;
  const results = await Promise.all(['up','down'].map(direction => exec(process.execPath, ['--input-type=module','-e',script,home,middle.id,direction])));
  assert.deepEqual(results.map(result => result.stdout).sort(), ['conflict','true']);
  const records = results[0].stdout === 'true' ? [middle,first,last] : [first,last,middle];
  assert.deepEqual(await store.page('one', 0, 50, '', true), { records, nextCursor:null, revision:'4' });
});

test('idempotent add survives a lost response, is scoped and bypasses quota on replay', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const id = randomUUID();
  const original = await store.add('first', 'synthetic', id);
  const other = await store.add('second', 'other synthetic', id);
  assert.equal(original.id, id);
  assert.equal(other.id, id);
  const reopened = createChatMemoryStore({ dshHome: home });
  const db = new DatabaseSync(join(home, 'chat-memory', 'records.sqlite'));
  db.prepare('UPDATE records SET bytes = ? WHERE session = ? AND id = ?').run(50 * 1024 * 1024, hash('second'), id);
  db.close();
  assert.deepEqual(await reopened.add('first', 'synthetic', id), original);
  await assert.rejects(reopened.add('first', 'different', id), /chat_memory_conflict/);
  await assert.rejects(reopened.add('first', 'new record'), /chat_memory_limit/);
  assert.deepEqual(await reopened.list('first'), [original]);
  assert.deepEqual(await reopened.list('second'), [other]);
});

test('concurrent idempotent adds return one record and conflicting adds have one winner', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  await store.list('seed');
  const script = `import { createChatMemoryStore } from ${JSON.stringify(moduleUrl)}; const s=createChatMemoryStore({dshHome:process.argv[1]}); try { const record=await s.add('same',process.argv[3],process.argv[2]); process.stdout.write(JSON.stringify({record})); } catch(e) { if(e.message !== 'chat_memory_conflict') throw e; process.stdout.write(JSON.stringify({error:e.message})); }`;
  const run = (id, text) => exec(process.execPath, ['--input-type=module', '-e', script, home, id, text]).then(result => JSON.parse(result.stdout));
  const id = randomUUID();
  const same = await Promise.all(Array.from({ length: 6 }, () => run(id, 'same text')));
  for (const result of same) assert.deepEqual(result, same[0]);
  assert.equal(same[0].record.id, id);
  const conflictId = randomUUID();
  const competing = await Promise.all(['first', 'second'].map(text => run(conflictId, text)));
  assert.equal(competing.filter(result => result.error === 'chat_memory_conflict').length, 1);
  assert.equal(competing.filter(result => result.record).length, 1);
  const records = await store.list('same');
  assert.equal(records.length, 2);
  assert.deepEqual(records.find(record => record.id === conflictId), competing.find(result => result.record).record);
});

test('Windows fails closed', { skip: posix }, async t => {
  const { store, home } = await fixture(t);
  await assert.rejects(store.add('synthetic', 'text'), /private_permissions_unsupported/);
  await assert.rejects(stat(join(home, 'chat-memory')), { code: 'ENOENT' });
});

test('records persist, are isolated and private, and delete by id', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const first = await store.add('../synthetic/session', 'Synthetic one');
  const second = await store.add('../synthetic/session', 'Synthetic two');
  await store.add('another', 'Other session');
  const reopened = createChatMemoryStore({ dshHome: home });
  assert.deepEqual(await reopened.list('../synthetic/session'), [first, second]);
  assert.equal((await reopened.list('another')).length, 1);
  assert.equal((await stat(join(home, 'chat-memory'))).mode & 0o777, 0o700);
  assert.equal((await stat(join(home, 'chat-memory', 'records.sqlite'))).mode & 0o777, 0o600);
  assert.equal(await reopened.delete('../synthetic/session', first.id), true);
  assert.equal(await reopened.delete('../synthetic/session', first.id), false);
  assert.deepEqual(await reopened.list('../synthetic/session'), [second]);
});

test('pages are bounded and remain scoped to the current session', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  for (let i = 0; i < 55; i++) await store.add('first', `synthetic ${i}`);
  await store.add('second', 'private other');
  const first = await store.page('first', 0, 50), second = await store.page('first', first.nextCursor, 50);
  assert.equal(first.records.length, 50); assert.equal(first.nextCursor, 50);
  assert.equal(second.records.length, 5); assert.equal(second.nextCursor, null);
  assert.equal(first.records.some(record => record.text === 'private other'), false);
  await assert.rejects(store.page('first', 0, 51), /invalid_page/);
});

test('search filters all session records before pagination, including Unicode', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  for (let i = 0; i < 7; i++) await store.add('first', i % 2 ? `other ${i}` : `ПрИвЕт ${i}`);
  await store.add('second', 'ПРИВЕТ secret');
  const first = await store.page('first', 0, 2, 'привет');
  const second = await store.page('first', first.nextCursor, 2, 'привет');
  assert.deepEqual(first.records.map(r => r.text), ['ПрИвЕт 0','ПрИвЕт 2']);
  assert.deepEqual(second.records.map(r => r.text), ['ПрИвЕт 4','ПрИвЕт 6']);
  assert.equal(second.nextCursor, null);
  assert.deepEqual((await store.page('first', 0, 2, 'not found')).records, []);
  await assert.rejects(store.page('first', 0, 1, 'x'.repeat(4097)), /invalid_query/);
});

test('update preserves identity and creation time, detects stale text and stays scoped', { skip: !posix }, async t => {
  const { store } = await fixture(t);
  const original = await store.add('first', 'old');
  await assert.rejects(store.update('second', original.id, 'wrong', 'old'), /chat_memory_conflict/);
  const changed = await store.update('first', original.id, 'new', 'old');
  assert.equal(changed.id, original.id);
  assert.equal(changed.createdAt, original.createdAt);
  assert.ok(Number.isFinite(Date.parse(changed.updatedAt)));
  await assert.rejects(store.update('first', original.id, 'stale', 'old'), /chat_memory_conflict/);
  assert.deepEqual(await store.list('first'), [changed]);
  assert.deepEqual(await store.list('second'), []);
});

test('concurrent updates with the same expected text have only one winner', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const original = await store.add('same', 'original');
  const script = `import { createChatMemoryStore } from ${JSON.stringify(moduleUrl)}; const s=createChatMemoryStore({dshHome:process.argv[1]}); try { await s.update('same',process.argv[2],process.argv[3],'original'); process.stdout.write('ok'); } catch(e) { if(e.message !== 'chat_memory_conflict') throw e; process.stdout.write('conflict'); }`;
  const results = await Promise.all(['one','two'].map(text => exec(process.execPath, ['--input-type=module','-e',script,home,original.id,text])));
  assert.deepEqual(results.map(result => result.stdout).sort(), ['conflict','ok']);
  assert.ok(['one','two'].includes((await store.list('same'))[0].text));
});

test('update quota rejection is atomic and shrinking an entry frees quota', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const first = await store.add('one', 'original');
  const filler = await store.add('two', 'filler');
  const db = new DatabaseSync(join(home, 'chat-memory', 'records.sqlite'));
  db.prepare('UPDATE records SET bytes = ? WHERE id = ?').run(50 * 1024 * 1024 - 250, filler.id);
  db.close();
  await assert.rejects(store.update('one', first.id, 'x'.repeat(500), 'original'), /chat_memory_limit/);
  assert.deepEqual(await store.list('one'), [first]);
  await store.update('two', filler.id, 'small', 'filler');
  await store.add('two', 'x'.repeat(200));
});

test('imports every legacy session atomically, ignoring abandoned locks', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const record = { id: randomUUID(), text: 'legacy', createdAt: new Date().toISOString(), extra: 'preserved' };
  const data = JSON.stringify([record]);
  const dir = await legacy(home, 'old', data);
  await mkdir(join(dir, '.lock'));
  await legacy(home, 'broken', '{broken');
  await assert.rejects(store.list('old'), /chat_memory_invalid_file/);
  await writeFile(join(home, 'chat-memory', hash('broken'), 'records.json'), '[]');
  assert.deepEqual(await store.list('old'), [record]);
  await store.delete('old', record.id);
  assert.deepEqual(await createChatMemoryStore({ dshHome: home }).list('old'), []);
  assert.equal(await readFile(join(dir, 'records.json'), 'utf8'), data);
});

test('concurrent processes do not lose records', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const script = `import { createChatMemoryStore } from ${JSON.stringify(moduleUrl)}; const s = createChatMemoryStore({dshHome:process.argv[1]}); for(let i=0;i<20;i++) await s.add(i%2?'same':'other', process.argv[2] + ':' + i);`;
  await Promise.all(Array.from({ length: 4 }, (_, i) => exec(process.execPath, ['--input-type=module', '-e', script, home, String(i)])));
  const records = [...await store.list('same'), ...await store.list('other')];
  assert.equal(records.length, 80);
  assert.equal(new Set(records.map(record => record.text)).size, 80);
  assert.equal(await store.revision('same'), '40');
  assert.equal(await store.revision('other'), '40');
});

test('a killed transaction releases its lock and rolls back', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const original = await store.add('s', 'committed');
  const path = join(home, 'chat-memory', 'records.sqlite');
  const script = `import { DatabaseSync } from 'node:sqlite'; const db=new DatabaseSync(process.argv[1]); db.exec('BEGIN IMMEDIATE'); db.prepare('DELETE FROM records').run(); process.stdout.write('ready\\n'); setInterval(()=>{},1000);`;
  const child = spawn(process.execPath, ['--input-type=module', '-e', script, path], { stdio: ['ignore', 'pipe', 'pipe'] });
  await new Promise((resolve, reject) => { child.stdout.once('data', resolve); child.once('error', reject); child.once('exit', code => reject(new Error(`child exited ${code}`))); });
  child.kill('SIGKILL');
  await new Promise(resolve => child.once('exit', resolve));
  assert.deepEqual(await store.list('s'), [original]);
  assert.equal(await store.revision('s'), '1');
  await store.add('s', 'after crash');
  assert.equal((await store.list('s')).length, 2);
  assert.equal(await store.revision('s'), '2');
});

test('account quota is shared across sessions and deletion frees space', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  const first = await store.add('one', 'first');
  await store.add('two', 'second');
  const db = new DatabaseSync(join(home, 'chat-memory', 'records.sqlite'));
  // Simulate an account near the boundary without writing 50 MiB per test run.
  db.prepare('UPDATE records SET bytes = ? WHERE id = ?').run(50 * 1024 * 1024 - 100, first.id);
  db.close();
  const revision = await store.revision('two');
  await assert.rejects(store.add('two', 'x'.repeat(200)), /chat_memory_limit/);
  assert.equal(await store.revision('two'), revision);
  assert.equal((await store.list('one')).length, 1);
  assert.equal((await store.list('two')).length, 1);
  assert.equal(await store.delete('one', first.id), true);
  await store.add('two', 'x'.repeat(200));
});

test('real 50 MiB boundary is enforced across concurrent writers', { skip: !posix, timeout: 30000 }, async t => {
  const { home, store } = await fixture(t);
  await store.list('seed');
  const db = new DatabaseSync(join(home, 'chat-memory', 'records.sqlite'));
  const insert = db.prepare('INSERT INTO records(session, id, json, bytes) VALUES (?, ?, ?, ?)');
  let used = 0;
  let count = 0;
  const text = 'x'.repeat(16000);
  db.exec('BEGIN');
  while (used < 50 * 1024 * 1024 - 20000) {
    const record = { id: randomUUID(), text, createdAt: '2026-01-01T00:00:00.000Z' };
    const json = JSON.stringify(record);
    insert.run(hash('seed'), record.id, json, Buffer.byteLength(json));
    used += Buffer.byteLength(json) + 1;
    count++;
  }
  used += 1;
  db.exec('COMMIT');
  db.close();
  const remaining = 50 * 1024 * 1024 - used;
  assert.ok(remaining > 1000 && remaining < 20000);
  const script = `import { createChatMemoryStore } from ${JSON.stringify(moduleUrl)}; const s=createChatMemoryStore({dshHome:process.argv[1]}); try { await s.add(process.argv[2], 'y'.repeat(Number(process.argv[3]))); process.stdout.write('ok'); } catch(e) { if(e.message !== 'chat_memory_limit') throw e; process.stdout.write('limit'); }`;
  const results = await Promise.all(['one', 'two'].map(session => exec(process.execPath, ['--input-type=module', '-e', script, home, session, String(Math.ceil(remaining / 2))])));
  assert.deepEqual(results.map(result => result.stdout).sort(), ['limit', 'ok']);
  assert.equal((await store.list('seed')).length, count);
  assert.equal((await store.list('one')).length + (await store.list('two')).length, 1);
});

test('rejects insecure homes and database links', { skip: !posix }, async t => {
  const { home, store } = await fixture(t);
  await chmod(home, 0o755);
  await assert.rejects(store.list('s'), /insecure_directory/);
  await chmod(home, 0o700);
  await mkdir(join(home, 'chat-memory'), { mode: 0o700 });
  await symlink(join(home, 'missing'), join(home, 'chat-memory', 'records.sqlite'));
  await assert.rejects(store.add('s', 'x'));
});
