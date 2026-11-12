import { createChatMemoryStore, isChatMemoryStatus } from './chat-memory-store.mjs';
import { normalizeMemorySources } from './chat-memory-sources.mjs';

const MAX_RECORD_BYTES = 16 * 1024;
const MAX_BODY_BYTES = 2 * MAX_RECORD_BYTES * 6 + 4096;
const MAX_QUERY_BYTES = 4096;
const SESSION_ID = /^[a-zA-Z0-9_-]{1,128}$/;
const RECORD_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const RELATIONS = ['related','supports','contradicts','depends_on','updates','part_of'];
const recordView = ({id,text,createdAt,updatedAt,aliases,sources,status}) => ({
  id,text,createdAt,...(updatedAt===undefined?{}:{updatedAt}),...(aliases?.length?{aliases}:{}),...(sources?.length?{sources}:{}),
  ...(status!=='normal'&&isChatMemoryStatus(status)?{status}:{}),
});
const fault = code => Object.assign(new Error(code), { code });
const validSessionId = value => typeof value === 'string' && SESSION_ID.test(value);
const validRevision = value => typeof value === 'string' && value.length <= 32 && /^(0|[1-9][0-9]*)$/.test(value);
const transferFields = ['action','sessionId','otherSessionId','direction'];

export function registerChatMemoryHttp(ctx, { store } = {}) {
  store ??= createChatMemoryStore({ dshHome:process.env.DSH_HOME });
  async function checkSession(id, transferring) {
    if (transferring && ctx.get('remoteAccess')?.allows({kind:'session',id}) === false) throw fault('session_unavailable');
    const session = ctx.sessions.get(id);
    const header = session ? null : (await ctx.get('sessionPersistence')?.stat(id))?.header;
    const workspace = session ? await ctx.workingDirectory.get(session) : header && (header.cwd ?? ctx.workingDirectory.defaultDirectory);
    if (!workspace) throw fault('session_unavailable');
  }
  ctx.effect(() => ctx.connection.fetch.register({
    path:'/api/gateway-chat-memory/manage', methods:['POST'], requestBody:'buffered',
    async fetch(request) {
      try {
        if (request.headers.get('content-type')?.split(';')[0].trim().toLowerCase() !== 'application/json') throw fault('invalid_request');
        const body = await request.text();
        if (Buffer.byteLength(body) > MAX_BODY_BYTES) throw fault('invalid_request');
        let input;
        try { input = JSON.parse(body); } catch { throw fault('invalid_request'); }
        if (!input || typeof input !== 'object' || Array.isArray(input) ||
          !['list','revision','usage','delete','add','update','setStatus','moveFact','get','graph','graphOverview','link','unlink','transferPreview','transfer'].includes(input.action) || !validSessionId(input.sessionId)) throw fault('invalid_request');
        const allowed = { list:['action','sessionId','limit','cursor','query','includeRevision'], revision:['action','sessionId'], usage:['action','sessionId'], delete:['action','sessionId','id'],
          add:['action','sessionId','text','requestId','aliases','sources'], update:['action','sessionId','id','text','expectedText','aliases','expectedAliases','sources','expectedSources'],
          setStatus:['action','sessionId','id','status','expectedStatus'],
          moveFact:['action','sessionId','id','direction','expectedRevision'],
          get:['action','sessionId','id','expectedRevision'],graph:['action','sessionId','id','cursor','limit','expectedRevision'],
          graphOverview:['action','sessionId','page','expectedRevision'],
          link:['action','sessionId','fromId','toId','relation','note'],unlink:['action','sessionId','id'],
          transferPreview:transferFields,transfer:[...transferFields,'sourceRevision','targetRevision'] }[input.action];
        if (Object.keys(input).some(key => !allowed.includes(key))) throw fault('invalid_request');
        const isTransfer = input.action === 'transferPreview' || input.action === 'transfer';
        if (isTransfer && (!validSessionId(input.otherSessionId) || input.sessionId === input.otherSessionId || !['import','export'].includes(input.direction))) throw fault('invalid_request');
        if (input.action === 'transfer' && ![input.sourceRevision,input.targetRevision].every(validRevision)) throw fault('invalid_request');
        if (['delete','update','setStatus','moveFact','get','unlink'].includes(input.action) && (typeof input.id !== 'string' || !RECORD_ID.test(input.id))) throw fault('invalid_request');
        if (input.action === 'setStatus' && ![input.status,input.expectedStatus].every(isChatMemoryStatus)) throw fault('invalid_request');
        if (input.action === 'moveFact' && (!['up','down'].includes(input.direction) || !validRevision(input.expectedRevision))) throw fault('invalid_request');
        if (input.action==='graph'&&input.id!==undefined&&(typeof input.id!=='string'||!RECORD_ID.test(input.id)))throw fault('invalid_request');
        if (['get','graph','graphOverview'].includes(input.action)&&input.expectedRevision!==undefined&&!validRevision(input.expectedRevision))throw fault('invalid_request');
        if (input.action==='graphOverview'&&(!Number.isSafeInteger(input.page)||input.page<0||input.page>1_000_000||
          (input.page>0&&input.expectedRevision===undefined)))throw fault('invalid_request');
        if(input.action==='link'&&([input.fromId,input.toId].some(id=>typeof id!=='string'||!RECORD_ID.test(id))||input.fromId===input.toId||!RELATIONS.includes(input.relation===undefined?'related':input.relation)||(input.note!==undefined&&(typeof input.note!=='string'||Buffer.byteLength(input.note)>256))))throw fault('invalid_request');
        if (input.action === 'add' && input.requestId !== undefined && (typeof input.requestId !== 'string' || !RECORD_ID.test(input.requestId))) throw fault('invalid_request');
        const validText = value => typeof value === 'string' && !!value.trim() && Buffer.byteLength(value) <= MAX_RECORD_BYTES;
        if (['add','update'].includes(input.action) && !validText(input.text)) throw fault('invalid_request');
        if (input.action === 'update' && !validText(input.expectedText)) throw fault('invalid_request');
        if(['add','update'].includes(input.action)&&input.aliases!==undefined&&(!Array.isArray(input.aliases)||input.aliases.length>12||input.aliases.some(value=>typeof value!=='string'||!value.trim()||Buffer.byteLength(value.trim())>80)||input.aliases.reduce((n,value)=>n+Buffer.byteLength(value.trim()),0)>1024))throw fault('invalid_request');
        if(input.action==='update'&&input.aliases!==undefined&&(!Array.isArray(input.expectedAliases)||input.expectedAliases.length>12||input.expectedAliases.some(value=>typeof value!=='string'||!value.trim()||Buffer.byteLength(value)>80)))throw fault('invalid_request');
        if(input.action==='update'&&input.expectedAliases!==undefined&&input.aliases===undefined)throw fault('invalid_request');
        if(['add','update'].includes(input.action)){
          try{
            if(input.sources!==undefined)input.sources=normalizeMemorySources(input.sources);
            if(input.action==='update'&&input.sources!==undefined)input.expectedSources=normalizeMemorySources(input.expectedSources);
            if(input.action==='update'&&input.expectedSources!==undefined&&input.sources===undefined)throw Error('invalid_sources');
          }catch{throw fault('invalid_request');}
        }
        if (input.action === 'list' && input.query !== undefined && (typeof input.query !== 'string' || Buffer.byteLength(input.query) > MAX_QUERY_BYTES)) throw fault('invalid_request');
        if (input.action === 'list' && input.includeRevision !== undefined && typeof input.includeRevision !== 'boolean') throw fault('invalid_request');
        const limit = input.limit === undefined ? 50 : input.limit, cursor = input.cursor === undefined ? 0 : input.cursor;
        if (input.action === 'list' && (!Number.isSafeInteger(limit) || limit < 1 || limit > 50 || !Number.isSafeInteger(cursor) || cursor < 0 || cursor > Number.MAX_SAFE_INTEGER - limit)) throw fault('invalid_request');
        const graphLimit=input.limit===undefined?20:input.limit;
        if (input.action === 'graph' && (!Number.isSafeInteger(graphLimit)||graphLimit<1||graphLimit>20||!Number.isSafeInteger(cursor)||cursor<0||cursor>Number.MAX_SAFE_INTEGER-20))throw fault('invalid_request');

        request.signal?.throwIfAborted();
        await checkSession(input.sessionId,isTransfer);
        if (isTransfer) await checkSession(input.otherSessionId,true);
        request.signal?.throwIfAborted();
        let value;
        if (isTransfer) {
          const [source,target] = input.direction === 'import' ? [input.otherSessionId,input.sessionId] : [input.sessionId,input.otherSessionId];
          value = input.action === 'transferPreview' ? await store.transferPreview(source,target) :
            await store.transfer(source,target,input.sourceRevision,input.targetRevision);
        }
        else if (input.action === 'revision') value = { revision:await store.revision(input.sessionId) };
        else if (input.action === 'usage') value = await store.usage(input.sessionId);
        else if(input.action==='get'){const record=await store.get(input.sessionId,input.id,input.expectedRevision);value={record:record?recordView(record):null};}
        else if(input.action==='graph')value=await store.graph(input.sessionId,{id:input.id,cursor,limit:graphLimit,expectedRevision:input.expectedRevision});
        else if(input.action==='graphOverview')value=await store.graphOverview(input.sessionId,input);
        else if(input.action==='link')value={edge:await store.link(input.sessionId,input.fromId,input.toId,input.relation,input.note)};
        else if(input.action==='unlink')value={unlinked:await store.unlink(input.sessionId,input.id)};
        else if (input.action === 'delete') value = { deleted:await store.delete(input.sessionId, input.id) };
        else if (input.action === 'moveFact') value = { moved:await store.moveFact(input.sessionId,input.id,input.direction,input.expectedRevision) };
        else if (input.action === 'setStatus') {
          const record = await store.setStatus(input.sessionId, input.id, input.status, input.expectedStatus);
          value = { record:recordView(record) };
        }
        else if (input.action === 'add' || input.action === 'update') {
          const metadata={...(input.aliases===undefined?{}:{aliases:input.aliases,...(input.action==='update'?{expectedAliases:input.expectedAliases}:{})}),...(input.sources===undefined?{}:{sources:input.sources,...(input.action==='update'?{expectedSources:input.expectedSources}:{})})};
          const options=Object.keys(metadata).length?[metadata]:[];
          const record = input.action === 'add' ? await store.add(input.sessionId, input.text, input.requestId,...options) : await store.update(input.sessionId, input.id, input.text, input.expectedText,...options);
          value = { record:recordView(record) };
        }
        else {
          const page = await store.page(input.sessionId, cursor, limit, input.query ?? '', input.includeRevision === true);
          value = { records:page.records.map(recordView), nextCursor:page.nextCursor, ...(input.includeRevision === true ? {revision:page.revision} : {}) };
        }
        return Response.json({ ok:true, value }, { headers:{'Cache-Control':'no-store'} });
      } catch (error) {
        const code = ['invalid_request','session_unavailable'].includes(error?.code) ? error.code :
          error?.message === 'chat_memory_conflict' ? 'conflict' : error?.message === 'chat_memory_limit' ? 'limit' : 'unavailable';
        return Response.json({ ok:false, error:{ code } }, { headers:{'Cache-Control':'no-store'} });
      }
    },
  }), 'gateway: chat memory operations');
}
