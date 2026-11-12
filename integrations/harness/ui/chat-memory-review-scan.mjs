import { createHash } from 'node:crypto';

const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('base64url');
const groupSize = 12;
const relations=new Set(['related','supports','contradicts','depends_on','updates','part_of']);

export async function scanMemoryReview(store,sessionId,kind,signal,{includeFacts=false}={}) {
  const buckets=new Map(), entries=new Map();
  const facts=[];
  let scanned=0, excluded=0;
  function add(key,id){
    let ids=buckets.get(key);
    if(!ids)buckets.set(key,ids=new Set());
    ids.add(id);
  }
  const {revision,total}=await store.scan(sessionId,record=>{
    signal?.throwIfAborted();
    scanned++;
    if(includeFacts)facts.push(record);
    if(kind==='merge'&&record.status==='critical'){excluded++;return;}
    const sources=record.sources??[];
    if(kind==='merge')add('exact:'+hash([record.text,sources]),record.id);
    for(const source of sources){
      // Candidate generation only; preview verifies every source before asking the model.
      const location=source.line_from?['lines',Math.floor((source.line_from-1)/12)]:
        source.anchor?['anchor',source.anchor]:null;
      if(location)add('source:'+hash([source.file,source.chapter??null,source.sha256??null,location]),record.id);
    }
    entries.set(record.id,Array.from(record.text.trim().replace(/\s+/g,' ')).slice(0,140).join(''));
  });
  signal?.throwIfAborted();
  const groups=[], emitted=new Set(), exactIds=new Set();
  function emit(ids,reason) {
    if(ids.length<2)return;
    const key=[...ids].sort().join(',');
    if(emitted.has(key))return;
    emitted.add(key);groups.push({ids,reason,label:entries.get(ids[0])});
  }
  function emitBatches(ids,reason) {
    for(let i=0;i<ids.length;i+=groupSize){
      const batch=ids.slice(i,i+groupSize);
      if(i&&batch.length===1)batch.unshift(ids[i-1]);
      emit(batch,reason);
    }
  }
  for(const [key,bucket] of buckets){
    if(!key.startsWith('exact:')||bucket.size<2)continue;
    const ids=[...bucket];
    ids.forEach(id=>exactIds.add(id));
    emitBatches(ids,'exact');
  }
  for(const [key,bucket] of buckets){
    if(!key.startsWith('source:')||bucket.size<2)continue;
    const ids=[...bucket].filter(id=>!exactIds.has(id));
    if(kind==='link'){
      for(let i=1;i<ids.length;i++)emit(ids.slice(i-1,i+1),'source');
    }else{
      emitBatches(ids,'source');
    }
  }
  return {revision,total,scanned,excluded,groups,facts};
}

export function* memoryReviewBatches(facts) {
  const topic=fact=>fact.sources?.[0]?.file??'';
  const ordered=[...facts].sort((a,b)=>topic(a).localeCompare(topic(b)));
  let batch=[], bytes=0;
  for(const fact of ordered){
    const size=Buffer.byteLength(JSON.stringify(memoryReviewModelFact(fact)));
    if(batch.length&&(batch.length===8||bytes+size>14*1024)){yield batch;batch=[];bytes=0;}
    batch.push(fact);bytes+=size;
  }
  if(batch.length)yield batch;
}

export function memoryReviewModelFact({id,text,status}) {
  return {id,text:text.replace(/[\x00-\x08\x0b-\x1f\x7f]/g,' '),status:status??'normal'};
}

export function semanticReviewGroups(answer,facts,kind) {
  if(!Array.isArray(answer?.groups)||answer.groups.length>8)throw new Error('review_model_response');
  const allowed=new Map(facts.map(fact=>[fact.id,fact]));
  return answer.groups.flatMap(group=>{
    const ids=group?.ids;
    if(!Array.isArray(ids)||ids.length<2||ids.length>(kind==='link'?2:12)||new Set(ids).size!==ids.length||
      !ids.every(id=>allowed.has(id)&&(kind!=='merge'||allowed.get(id).status!=='critical')))return [];
    const note=typeof group.reason==='string'?group.reason.trim().slice(0,500):'';
    let proposal;
    if(kind==='merge'&&typeof group.text==='string'&&group.text.trim()&&Buffer.byteLength(group.text)<=16*1024){
      proposal={kind,text:group.text.trim()};
    }else if(kind==='link'&&ids.includes(group.fromId)&&ids.includes(group.toId)&&group.fromId!==group.toId&&relations.has(group.relation)){
      proposal={kind,fromId:group.fromId,toId:group.toId,relation:group.relation};
    }
    return [{ids,reason:'semantic',note,proposal,label:Array.from(allowed.get(ids[0]).text.trim().replace(/\s+/g,' ')).slice(0,140).join('')}];
  });
}
