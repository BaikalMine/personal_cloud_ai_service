import { test } from 'node:test';
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { registerChatMemoryHttp } from './chat-memory-http.mjs';

const ids = Array.from({length:60}, (_, i) => `00000000-0000-4000-8000-${String(i).padStart(12, '0')}`);
function fixture() {
  let route;
  const calls = [];
  const data = new Map([['active', ids.map((id, i) => ({id, text:`fact ${i}`, createdAt:'2026-01-01T00:00:00Z', privateField:'hidden'}))], ['persisted', [{id:ids[0], text:'other', createdAt:'2026-01-01T00:00:00Z'}]]]);
  const store = {
    async page(session,cursor,limit,query = '',includeRevision = false) { calls.push(['list', session]); const all=(data.get(session) ?? []).filter(record => record.text.toLowerCase().includes(query.toLowerCase())); return {records:all.slice(cursor,cursor+limit),nextCursor:cursor+limit<all.length?cursor+limit:null,...(includeRevision?{revision:'7'}:{})}; },
    async revision(session) { calls.push(['revision', session]); return '7'; },
    async delete(session, id) { calls.push(['delete', session, id]); const list = data.get(session) ?? []; const index = list.findIndex(item => item.id === id); if (index < 0) return false; list.splice(index, 1); return true; },
    async add(session,text,requestId) { calls.push(['add',session,requestId]); const list=data.get(session) ?? []; const existing=requestId && list.find(record => record.id === requestId); if (existing) { if (existing.text !== text) throw new Error('chat_memory_conflict'); return existing; } const record={id:requestId ?? randomUUID(),text,createdAt:'2026-01-01T00:00:00Z'}; list.push(record); data.set(session,list); return record; },
    async update(session,id,text,expectedText) { const record=(data.get(session) ?? []).find(item => item.id === id); if (!record || record.text !== expectedText) throw new Error('chat_memory_conflict'); record.text=text; record.updatedAt='2026-01-02T00:00:00Z'; return record; },
    async get(session,id) { return (data.get(session) ?? []).find(record => record.id === id) ?? null; },
    async setStatus(session,id,status,expectedStatus) {
      calls.push(['setStatus',session,id,status,expectedStatus]);
      const record = await this.get(session,id);
      if (!record || (record.status ?? 'normal') !== expectedStatus) throw new Error('chat_memory_conflict');
      record.status = status;
      return record;
    },
  };
  const ctx = {
    sessions:{get:id => id === 'active' ? {id} : undefined},
    workingDirectory:{get:() => '/workspace/active', defaultDirectory:'/workspace/default'},
    get:name => name === 'sessionPersistence' ? {stat:async id => id === 'persisted' ? {header:{}} : undefined} : undefined,
    effect:fn => fn(), connection:{fetch:{register:value => { route = value; }}},
  };
  registerChatMemoryHttp(ctx, {store});
  async function send(input, options = {}) {
    const response = await route.fetch(new Request('http://localhost/api/gateway-chat-memory/manage', {method:'POST', headers:{'Content-Type':options.contentType ?? 'application/json'}, body:options.raw ?? JSON.stringify(input)}));
    assert.equal(response.headers.get('cache-control'), 'no-store');
    return response.json();
  }
  return {route, send, calls, ctx, store};
}

test('memory transfer is limited to two authorized sessions, with explicit direction and revisions', async () => {
  const {send,calls,ctx,store}=fixture();
  const get=ctx.get;
  ctx.get=name=>name==='remoteAccess'?{allows:({kind,id})=>kind==='session'&&id!=='forbidden'}:get(name);
  store.transferPreview=async (...args)=>{calls.push(['preview',...args]);return {fits:true};};
  store.transfer=async (...args)=>{calls.push(['copy',...args]);return {fits:true};};
  const preview={action:'transferPreview',sessionId:'active',otherSessionId:'persisted',direction:'import'};
  const commit={...preview,action:'transfer',sourceRevision:'7',targetRevision:'12'};
  assert.equal((await send(preview)).ok,true);
  assert.equal((await send({...commit,direction:'export'})).ok,true);
  for(const input of [
    {...commit,sourceRevision:undefined},{...commit,targetRevision:12},{...commit,sourceRevision:'00'},
    {...preview,otherSessionId:'active'},{...preview,otherSessionId:'../persisted'},
    {...preview,direction:'move'},{...preview,records:[]},
  ]) assert.equal((await send(input)).error.code,'invalid_request');
  assert.equal((await send({...preview,otherSessionId:'unknown'})).error.code,'session_unavailable');
  ctx.sessions.get=id=>({id});
  assert.equal((await send({...commit,otherSessionId:'forbidden'})).error.code,'session_unavailable');
  assert.equal((await send({...preview,sessionId:'forbidden'})).error.code,'session_unavailable');
  assert.deepEqual(calls,[['preview','persisted','active'],['copy','active','persisted','7','12']]);
});

test('registers the POST route and paginates only the resolved session', async () => {
  const {route, send} = fixture();
  assert.equal(route.path, '/api/gateway-chat-memory/manage');
  assert.deepEqual(route.methods, ['POST']);
  assert.equal(route.requestBody, 'buffered');
  const first = await send({action:'list', sessionId:'active', limit:50});
  assert.equal(first.value.records.length, 50);
  assert.equal(first.value.nextCursor, 50);
  assert.deepEqual(Object.keys(first.value.records[0]), ['id','text','createdAt']);
  const second = await send({action:'list', sessionId:'active', cursor:first.value.nextCursor});
  assert.equal(second.value.records.length, 10);
  assert.equal(second.value.nextCursor, null);
  assert.equal((await send({action:'list', sessionId:'persisted'})).value.records[0].text, 'other');
  assert.deepEqual((await send({action:'list', sessionId:'active', cursor:999})).value, {records:[], nextCursor:null});
});

test('revision polling exposes only a scoped token and list snapshots opt into that token', async () => {
  const {send,calls}=fixture();
  assert.deepEqual((await send({action:'revision',sessionId:'active'})).value,{revision:'7'});
  assert.deepEqual(calls,[['revision','active']]);
  const page=await send({action:'list',sessionId:'active',limit:1,includeRevision:true});
  assert.equal(page.value.revision,'7');assert.equal(page.value.records.length,1);
  assert.equal(Object.hasOwn((await send({action:'list',sessionId:'active',limit:1})).value,'revision'),false);
  assert.equal((await send({action:'revision',sessionId:'missing'})).error.code,'session_unavailable');
  for(const extra of [{id:ids[0]},{query:'private'},{includeRevision:true},{cursor:0}]) {
    assert.equal((await send({action:'revision',sessionId:'active',...extra})).error.code,'invalid_request');
  }
  for(const includeRevision of [null,0,'true']) {
    assert.equal((await send({action:'list',sessionId:'active',includeRevision})).error.code,'invalid_request');
  }
});

test('add forwards an optional UUID and retries a lost response without duplicates', async () => {
  const {send, calls} = fixture();
  const requestId = randomUUID();
  const request = {action:'add',sessionId:'persisted',text:'synthetic retry',requestId};
  const first = await send(request);
  const retry = await send(request);
  assert.deepEqual(retry, first);
  assert.equal(retry.value.record.id, requestId);
  assert.equal((await send({action:'list',sessionId:'persisted',query:'synthetic retry'})).value.records.length, 1);
  assert.equal((await send({...request,text:'different'})).error.code, 'conflict');
  const other = await send({...request,sessionId:'active',text:'different'});
  assert.equal(other.value.record.id, requestId);
  assert.equal(other.value.record.text, 'different');
  assert.deepEqual(calls.filter(call => call[0] === 'add').map(call => call[2]), [requestId,requestId,requestId,requestId]);
  const withoutId = await send({action:'add',sessionId:'persisted',text:'without id'});
  assert.ok(withoutId.value.record.id);
  assert.equal(calls.at(-1)[2], undefined);
});

test('deletes only an explicit ID in the requested session', async () => {
  const {send} = fixture();
  assert.deepEqual((await send({action:'delete', sessionId:'persisted', id:ids[1]})).value, {deleted:false});
  assert.deepEqual((await send({action:'delete', sessionId:'active', id:ids[0]})).value, {deleted:true});
  assert.equal((await send({action:'list', sessionId:'persisted'})).value.records.length, 1);
});

test('adds, searches and updates a scoped fact with conflict detection', async () => {
  const {send, store} = fixture();
  const added = await send({action:'add',sessionId:'persisted',text:'Привет Synthetic'});
  const id = added.value.record.id;
  const found = await send({action:'list',sessionId:'persisted',query:'ПРИВЕТ',limit:1});
  assert.equal(found.value.records[0].id, id);
  const update = await send({action:'update',sessionId:'persisted',id,text:'updated',expectedText:'Привет Synthetic'});
  assert.equal(update.value.record.createdAt, added.value.record.createdAt);
  assert.equal(update.value.record.updatedAt, '2026-01-02T00:00:00Z');
  assert.equal((await send({action:'update',sessionId:'persisted',id,text:'bad',expectedText:'stale'})).error.code, 'conflict');
  assert.equal((await send({action:'update',sessionId:'active',id,text:'bad',expectedText:'updated'})).error.code, 'conflict');
  store.add = async () => { throw new Error('chat_memory_limit'); };
  assert.equal((await send({action:'add',sessionId:'active',text:'valid'})).error.code, 'limit');
});

test('setStatus returns a sanitized fact and shares its status view with get and list', async () => {
  const {send,calls,store} = fixture();
  const request = {action:'setStatus',sessionId:'active',id:ids[0],status:'critical',expectedStatus:'normal'};
  const normal = {id:ids[0],text:'fact 0',createdAt:'2026-01-01T00:00:00Z'};
  const critical = {...normal,status:'critical'};
  assert.deepEqual(await send(request), {ok:true,value:{record:critical}});
  assert.deepEqual(calls, [['setStatus','active',ids[0],'critical','normal']]);
  const get = {action:'get',sessionId:'active',id:ids[0]};
  assert.deepEqual((await send(get)).value, {record:critical});
  assert.deepEqual((await send({action:'list',sessionId:'active',limit:1})).value.records, [critical]);
  assert.deepEqual(await send(request), {ok:false,error:{code:'conflict'}});
  assert.deepEqual((await send({...request,status:'normal',expectedStatus:'critical'})).value, {record:normal});
  assert.deepEqual((await send(get)).value, {record:normal});
  assert.equal((await send({...request,sessionId:'persisted',id:ids[1]})).error.code, 'conflict');
  store.setStatus = async () => { throw new Error('chat_memory_limit'); };
  assert.deepEqual(await send(request), {ok:false,error:{code:'limit'}});
});

test('setStatus requires a resolved session, a UUID, and both exact enum values', async () => {
  const {send,calls} = fixture();
  const request = {action:'setStatus',sessionId:'active',id:ids[0],status:'important',expectedStatus:'normal'};
  for (const extra of [
    {id:undefined}, {id:'invalid'}, {status:undefined}, {status:'Important'}, {status:null},
    {expectedStatus:undefined}, {expectedStatus:'urgent'}, {expectedStatus:['normal']}, {text:'synthetic'},
  ]) assert.equal((await send({...request,...extra})).error.code, 'invalid_request');
  assert.equal((await send({...request,sessionId:'missing'})).error.code, 'session_unavailable');
  assert.deepEqual(calls, []);
});

test('moveFact dispatches a scoped revision CAS and returns only the moved flag', async () => {
  const {send,calls,store} = fixture();
  store.moveFact = async (session,id,direction,expectedRevision) => {
    calls.push([session,id,direction,expectedRevision]);
    if (expectedRevision !== '7' || id !== ids[0]) throw new Error('chat_memory_conflict');
    return direction === 'down';
  };
  const request = {action:'moveFact',sessionId:'active',id:ids[0],direction:'down',expectedRevision:'7'};
  assert.deepEqual(await send(request), {ok:true,value:{moved:true}});
  assert.deepEqual(await send({...request,direction:'up'}), {ok:true,value:{moved:false}});
  assert.deepEqual(await send({...request,expectedRevision:'6'}), {ok:false,error:{code:'conflict'}});
  assert.deepEqual(await send({...request,sessionId:'persisted',id:ids[1]}), {ok:false,error:{code:'conflict'}});
  assert.deepEqual(calls, [
    ['active',ids[0],'down','7'], ['active',ids[0],'up','7'], ['active',ids[0],'down','6'], ['persisted',ids[1],'down','7'],
  ]);
});

test('moveFact rejects invalid input and unresolved sessions before calling the store', async () => {
  const {send,calls,store} = fixture();
  store.moveFact = async (...args) => { calls.push(args); return false; };
  const request = {action:'moveFact',sessionId:'active',id:ids[0],direction:'up',expectedRevision:'7'};
  for (const extra of [
    {id:undefined}, {id:'invalid'}, {direction:undefined}, {direction:'UP'}, {direction:['up']},
    {expectedRevision:undefined}, {expectedRevision:7}, {expectedRevision:'07'}, {expectedRevision:'1'.repeat(33)}, {cursor:0},
  ]) assert.equal((await send({...request,...extra})).error.code, 'invalid_request');
  assert.equal((await send({...request,sessionId:'missing'})).error.code, 'session_unavailable');
  assert.deepEqual(calls, []);
});

test('rejects invalid input and unknown sessions before calling the store', async () => {
  const {send, calls} = fixture();
  for (const input of [null, [], {}, {action:'list',sessionId:'../bad'}, {action:'list',sessionId:'active',limit:51}, {action:'list',sessionId:'active',limit:0}, {action:'list',sessionId:'active',limit:null}, {action:'list',sessionId:'active',cursor:null}, {action:'list',sessionId:'active',cursor:-1}, {action:'list',sessionId:'active',cursor:'0'}, {action:'list',sessionId:'active',id:ids[0]}, {action:'delete',sessionId:'active'}, {action:'delete',sessionId:'active',id:'*'}, {action:'delete',sessionId:'active',id:ids[0],limit:1}]) {
    assert.deepEqual(await send(input), {ok:false,error:{code:'invalid_request'}});
  }
  assert.equal((await send(null, {raw:'{'})).error.code, 'invalid_request');
  assert.equal((await send(null, {raw:' '.repeat(4097)})).error.code, 'invalid_request');
  assert.equal((await send({action:'add',sessionId:'active',text:'x'.repeat(16385)})).error.code, 'invalid_request');
  for (const requestId of [null, '', 42, '00000000-0000-1000-8000-000000000000', 'AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA']) {
    assert.equal((await send({action:'add',sessionId:'active',text:'valid',requestId})).error.code, 'invalid_request');
  }
  assert.equal((await send({action:'delete',sessionId:'active',id:ids[0],requestId:randomUUID()})).error.code, 'invalid_request');
  assert.equal((await send({action:'update',sessionId:'active',id:ids[0],text:'x'})).error.code, 'invalid_request');
  assert.equal((await send({action:'list',sessionId:'active',query:'x'.repeat(4097)})).error.code, 'invalid_request');
  assert.equal((await send(null, {raw:' '.repeat(200705)})).error.code, 'invalid_request');
  assert.equal((await send({}, {contentType:'text/plain'})).error.code, 'invalid_request');
  assert.equal((await send({action:'list',sessionId:'missing'})).error.code, 'session_unavailable');
  assert.deepEqual(calls, []);
});

test('requires a usable resolved workspace and sanitizes failures', async () => {
  const {ctx, store} = fixture();
  let route;
  ctx.connection.fetch.register = value => { route = value; };
  ctx.workingDirectory.get = () => '';
  store.page = async () => { throw new Error('secret path or data'); };
  registerChatMemoryHttp(ctx, {store});
  const send = async sessionId => (await route.fetch(new Request('http://localhost/', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({action:'list',sessionId})}))).json();
  assert.equal((await send('active')).error.code, 'session_unavailable');
  assert.deepEqual(await send('persisted'), {ok:false,error:{code:'unavailable'}});
});
