import { createHash } from 'node:crypto';
import { compileMemoryQuery, scoreMemoryRecord } from './chat-memory-search.mjs';

const PAGE_BYTES=112*1024, MAX_CURSOR_BYTES=2048;
const REVISION=/^(0|[1-9][0-9]*)$/;
const UUID=/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const hash=value=>createHash('sha256').update(value).digest('hex');
const fail=code=>{throw new Error(`chat_memory_${code}`);};
const scope=sessionId=>hash(sessionId);
const encode=value=>Buffer.from(JSON.stringify(value)).toString('base64url');
function decode(value,sessionId,kind,queryHash){
  if(typeof value!=='string'||!value||Buffer.byteLength(value)>MAX_CURSOR_BYTES||!/^[A-Za-z0-9_-]+$/.test(value))fail('invalid_cursor');
  let cursor;
  try{const bytes=Buffer.from(value,'base64url');if(bytes.toString('base64url')!==value)fail('invalid_cursor');cursor=JSON.parse(bytes.toString('utf8'));}catch{fail('invalid_cursor');}
  if(!cursor||typeof cursor!=='object'||Array.isArray(cursor)||cursor.v!==1||cursor.scope!==scope(sessionId)||cursor.kind!==kind||
    typeof cursor.revision!=='string'||!REVISION.test(cursor.revision)||cursor.query!==queryHash)fail('invalid_cursor');
  return cursor;
}
function fact(record){
  if(!record||typeof record.id!=='string'||typeof record.text!=='string'||Buffer.byteLength(record.text)>16384)fail('invalid_record');
  return {id:record.id,text:record.text,createdAt:record.createdAt,...(record.updatedAt===undefined?{}:{updatedAt:record.updatedAt}),...(record.aliases?.length?{aliases:record.aliases}:{}),...(record.sources?.length?{sources:record.sources}:{}),
    ...(['important','critical'].includes(record.status)?{status:record.status}:{})};
}
function size(record){return Buffer.byteLength(JSON.stringify(record))+1;}
function pageOf(candidates,limit){
  const rows=[];let bytes=0;
  for(const candidate of candidates){
    const count=size(candidate.record);
    if(count>PAGE_BYTES)fail('record_too_large');
    if(rows.length>=limit||bytes+count>PAGE_BYTES)break;
    rows.push(candidate);bytes+=count;
  }
  return rows;
}
const order=(a,b)=>b.score-a.score||a.seq-b.seq;
function boundedInsert(rows,candidate,capacity){
  let lo=0,hi=rows.length;
  while(lo<hi){const mid=(lo+hi)>>>1;if(order(rows[mid],candidate)<=0)lo=mid+1;else hi=mid;}
  if(lo>=capacity)return;
  rows.splice(lo,0,candidate);if(rows.length>capacity)rows.pop();
}

export async function readChatMemory(store,sessionId,args={}, {signal}={}){
  signal?.throwIfAborted();
  const limit=args.limit??30;
  if(!Number.isSafeInteger(limit)||limit<1||limit>50)fail('invalid_limit');
  if(args.id!==undefined){
    if(typeof args.id!=='string'||!UUID.test(args.id)||args.cursor!==undefined||args.query!==undefined||args.queries!==undefined)fail('invalid_read');
    const record=await store.get(sessionId,args.id);signal?.throwIfAborted();
    return {records:record?[fact(record)]:[],total:record?1:0,mode:'exact_id',next_cursor:null,truncated:false};
  }
  const query=args.query??'',alternatives=args.queries??[];
  let plan;
  try{plan=compileMemoryQuery(query,alternatives);}catch(error){if(error?.code==='chat_memory_invalid_query')fail('invalid_query');throw error;}
  const searched=Boolean(query.trim()||alternatives.some(value=>value.trim()));
  const queryHash=hash(JSON.stringify([query,alternatives]));
  const cursor=args.cursor===undefined?null:decode(args.cursor,sessionId,'facts',queryHash);
  if(cursor&&(!['browse','search','browse_fallback'].includes(cursor.mode)||!Number.isSafeInteger(cursor.seq)||cursor.seq<0||
    (cursor.mode==='search'&&(!Number.isFinite(cursor.score)||cursor.score<=0))||
    (!searched&&cursor.mode!=='browse')||(searched&&cursor.mode==='browse')))fail('invalid_cursor');
  const browse=cursor?.mode==='browse_fallback'||!searched;
  const candidates=[],fallback=[];let matched=0;
  const metadata=await store.scan(sessionId,(record,seq)=>{
    signal?.throwIfAborted();
    if(!Number.isSafeInteger(seq)||seq<1)fail('invalid_record');
    if(browse){
      if(seq>(cursor?.seq??0))candidates.push({record:fact(record),seq,score:0});
      return candidates.length<=limit;
    }
    if(!cursor&&fallback.length<=limit)fallback.push({record:fact(record),seq,score:0});
    let score=scoreMemoryRecord(record.text,plan);
    for(const alias of record.aliases??[])score=Math.max(score,scoreMemoryRecord(alias,plan));
    if(!Number.isFinite(score)||score<0)fail('invalid_score');
    if(!score)return;
    matched++;
    if(cursor&&(score>cursor.score||(score===cursor.score&&seq<=cursor.seq)))return;
    boundedInsert(candidates,{record:fact(record),seq,score},limit+1);
  },cursor?.revision);
  signal?.throwIfAborted();
  if(!metadata||typeof metadata.revision!=='string'||!REVISION.test(metadata.revision)||!Number.isSafeInteger(metadata.total)||metadata.total<0)fail('invalid_snapshot');
  if(cursor&&metadata.revision!==cursor.revision)fail('conflict');
  let mode=cursor?.mode??(searched?'search':'browse');
  let available=candidates;
  if(searched&&!cursor&&!matched){mode='browse_fallback';available=fallback;}
  const rows=pageOf(available,limit),more=available.length>rows.length,last=rows.at(-1);
  if(more&&!last)fail('record_too_large');
  const next=more?encode({v:1,kind:'facts',scope:scope(sessionId),query:queryHash,revision:metadata.revision,mode,seq:last.seq,...(mode==='search'?{score:last.score}:{})}):null;
  return {records:rows.map(row=>row.record),total:mode==='search'?matched:metadata.total,memory_total:metadata.total,
    matched_total:mode==='search'?matched:mode==='browse_fallback'?0:null,mode,revision:metadata.revision,
    query_matched:mode==='search'?matched>0:mode==='browse_fallback'?false:null,next_cursor:next,truncated:next!==null,
    ...(mode==='browse_fallback'?{notice:'No lexical matches. These are unfiltered saved facts, not search matches. Continue with next_cursor or use alternate-language queries; this does not establish that the fact is absent.'}:{})};
}

export async function readChatMemoryGraph(store,sessionId,args={}, {signal}={}){
  signal?.throwIfAborted();
  const limit=args.limit??12;
  if(!Number.isSafeInteger(limit)||limit<1||limit>20||typeof args.id!=='string'||!UUID.test(args.id))fail('invalid_graph');
  const queryHash=hash(args.id),cursor=args.cursor===undefined?null:decode(args.cursor,sessionId,'graph',queryHash);
  if(cursor&&(!Number.isSafeInteger(cursor.offset)||cursor.offset<0))fail('invalid_cursor');
  const value=await store.graph(sessionId,{id:args.id,cursor:cursor?.offset??0,limit,expectedRevision:cursor?.revision});
  signal?.throwIfAborted();
  if(!value||typeof value.revision!=='string'||!REVISION.test(value.revision)||!Array.isArray(value.nodes)||!Array.isArray(value.edges))fail('invalid_snapshot');
  if(cursor&&value.revision!==cursor.revision)fail('conflict');
  return {node_id:value.nodeId,nodes:value.nodes,edges:value.edges.map(edge=>({id:edge.id,from_id:edge.fromId,to_id:edge.toId,relation:edge.relation,note:edge.note,createdAt:edge.createdAt})),
    total:value.total,revision:value.revision,next_cursor:value.nextCursor===null?null:encode({v:1,kind:'graph',scope:scope(sessionId),query:queryHash,revision:value.revision,offset:value.nextCursor}),
    previews_only:true};
}
