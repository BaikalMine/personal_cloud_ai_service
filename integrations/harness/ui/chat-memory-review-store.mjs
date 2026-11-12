import { randomUUID } from 'node:crypto';
import { normalizeMemorySources } from './chat-memory-sources.mjs';

const ttl = 30 * 86400_000;
const historyLimit = 4 * 1024 * 1024;
const uuid = /^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/;
const priorities = ['normal','important','critical'];
export const reviewRelations = ['related','supports','contradicts','depends_on','updates','part_of'];
const same = (a,b) => JSON.stringify(a) === JSON.stringify(b);
const reject = code => { throw new Error('review_' + code); };
const amount = rows => rows.reduce((sum,row)=>sum+Buffer.byteLength(row.json)+1,0);

export function initMemoryReview(db) {
  db.exec(`CREATE TABLE IF NOT EXISTS memory_reviews(
    session TEXT NOT NULL, id TEXT NOT NULL, expires INTEGER NOT NULL, created INTEGER NOT NULL,
    state TEXT NOT NULL, payload TEXT NOT NULL, reserve INTEGER NOT NULL, PRIMARY KEY(session,id));
    CREATE INDEX IF NOT EXISTS memory_reviews_state_expires ON memory_reviews(state,expires)`);
  db.prepare("UPDATE memory_reviews SET payload='',reserve=0,state='expired' WHERE state='applied' AND expires<=?").run(Date.now());
}

export function reviewReserve(db, now = Date.now()) {
  return db.prepare("SELECT COALESCE(SUM(reserve),0) AS bytes FROM memory_reviews WHERE state='applied' AND expires>?").get(now).bytes;
}

function rowsFor(db, session, ids) {
  const marks=ids.map(()=>'?').join(',');
  const facts=db.prepare(`SELECT seq,json FROM records WHERE session=? AND id IN (${marks}) ORDER BY seq`).all(session,...ids);
  const edges=db.prepare(`SELECT seq,json FROM relations WHERE session=? AND (from_id IN (${marks}) OR to_id IN (${marks})) ORDER BY seq LIMIT 97`).all(session,...ids,...ids);
  if(edges.length>96)reject('too_many_links');
  return {facts,edges};
}

export function reviewSelection(db,session,ids) {
  if(!Array.isArray(ids)||ids.length<2||ids.length>12||ids.some(id=>typeof id!=='string'||!uuid.test(id))||new Set(ids).size!==ids.length)reject('selection');
  const rows=rowsFor(db,session,ids);
  if(rows.facts.length!==ids.length)reject('stale');
  return {facts:rows.facts.map(row=>JSON.parse(row.json)),edges:rows.edges.map(row=>JSON.parse(row.json))};
}

function plan(db,session,ids,proposal) {
  reviewSelection(db,session,ids);
  const before=rowsFor(db,session,ids);
  const after=structuredClone(before);
  if(proposal.kind==='merge'){
    const facts=before.facts.map(row=>JSON.parse(row.json));
    const [keep]=facts;
    if(facts.some(fact=>fact.status==='critical'))reject('critical');
    if(typeof proposal.text!=='string'||!proposal.text.trim()||Buffer.byteLength(proposal.text)>16*1024)reject('text');
    const aliases=[...new Set(facts.flatMap(fact=>fact.aliases??[]))];
    if(aliases.length>12||aliases.reduce((n,a)=>n+Buffer.byteLength(a),0)>1024)reject('metadata_limit');
    const sources=[...new Map(facts.flatMap(fact=>fact.sources??[]).map(source=>[JSON.stringify(source),source])).values()];
    try{normalizeMemorySources(sources);}catch{reject('metadata_limit');}
    if(aliases.length)keep.aliases=aliases;
    if(sources.length)keep.sources=sources;
    const priority=Math.max(...facts.map(fact=>Math.max(0,priorities.indexOf(fact.status))));
    if(priority)keep.status=priorities[priority];
    keep.text=proposal.text;keep.updatedAt=new Date().toISOString();
    after.facts=[{seq:before.facts[0].seq,json:JSON.stringify(keep)}];
    const dedup=new Set();
    after.edges=before.edges.map(row=>{
      const edge=JSON.parse(row.json);
      const inner=ids.includes(edge.fromId)&&ids.includes(edge.toId);
      if(inner&&edge.relation!=='related')reject('link_conflict');
      if(inner)return null;
      if(ids.includes(edge.fromId))edge.fromId=keep.id;
      if(ids.includes(edge.toId))edge.toId=keep.id;
      if(edge.relation==='related'&&edge.fromId>edge.toId)[edge.fromId,edge.toId]=[edge.toId,edge.fromId];
      const key=JSON.stringify([edge.fromId,edge.toId,edge.relation]);
      if(dedup.has(key))reject('link_conflict');
      dedup.add(key);return {seq:row.seq,json:JSON.stringify(edge)};
    }).filter(Boolean);
  }else if(proposal.kind==='link'){
    let {fromId,toId,relation}=proposal;
    if(fromId===toId||!ids.includes(fromId)||!ids.includes(toId)||!reviewRelations.includes(relation))reject('link');
    if(relation==='related'&&fromId>toId)[fromId,toId]=[toId,fromId];
    if(before.edges.some(row=>{const e=JSON.parse(row.json);return e.fromId===fromId&&e.toId===toId&&e.relation===relation;}))reject('link_conflict');
    after.edges.push({seq:null,json:JSON.stringify({id:randomUUID(),fromId,toId,relation,note:'',createdAt:new Date().toISOString()})});
  }else reject('proposal');
  return {ids,before,after};
}

const view = part => ({facts:part.facts.map(row=>JSON.parse(row.json)),edges:part.edges.map(row=>JSON.parse(row.json))});
function deltaFor(db,session,{before,after}) {
  return [['records','facts'],['relations','edges']].reduce((sum,[table,key])=>{
    const count=db.prepare(`SELECT COUNT(*) AS n FROM ${table} WHERE session=?`).get(session).n;
    const next=count-before[key].length+after[key].length;
    return sum+amount(after[key])-amount(before[key])+Number(next>0)-Number(count>0);
  },0);
}
export function prepareMemoryReview(db,session,ids,proposal) {
  const patch=plan(db,session,ids,proposal);
  return {patch,before:view(patch.before),after:view(patch.after),delta:deltaFor(db,session,patch)};
}

function writePatch(db,session,current,next) {
  for(const row of current.edges)db.prepare('DELETE FROM relations WHERE session=? AND id=?').run(session,JSON.parse(row.json).id);
  for(const row of current.facts)db.prepare('DELETE FROM records WHERE session=? AND id=?').run(session,JSON.parse(row.json).id);
  for(const row of next.facts){
    const {id}=JSON.parse(row.json);
    db.prepare('INSERT INTO records(seq,session,id,json,bytes) VALUES(?,?,?,?,?)').run(row.seq,session,id,row.json,Buffer.byteLength(row.json));
  }
  for(const row of next.edges){
    const {id,fromId,toId,relation}=JSON.parse(row.json);
    const result=db.prepare('INSERT INTO relations(seq,session,id,from_id,to_id,relation,json,bytes) VALUES(?,?,?,?,?,?,?,?)')
      .run(row.seq,session,id,fromId,toId,relation,row.json,Buffer.byteLength(row.json));
    if(row.seq===null)row.seq=Number(result.lastInsertRowid);
  }
}

export function commitMemoryReview(db,session,id,patch,quota,used) {
  const now=Date.now();
  const previous=db.prepare('SELECT state,expires FROM memory_reviews WHERE session=? AND id=?').get(session,id);
  if(previous) return {id,state:previous.state,expires:previous.expires};
  if(!same(rowsFor(db,session,patch.ids),patch.before))reject('stale');
  const delta=deltaFor(db,session,patch),reserve=Math.max(0,-delta);
  if(used()+delta+reserve>quota)reject('quota');
  const bytes=db.prepare('SELECT COALESCE(SUM(length(CAST(payload AS BLOB))),0) AS n FROM memory_reviews').get().n;
  if(bytes+Buffer.byteLength(JSON.stringify(patch))+64>historyLimit)reject('history_limit');
  writePatch(db,session,patch.before,patch.after);
  const expires=now+ttl;
  db.prepare("INSERT INTO memory_reviews VALUES(?,?,?,?,'applied',?,?)").run(session,id,expires,now,JSON.stringify(patch),reserve);
  return {id,state:'applied',expires,delta,reserve};
}

export function undoMemoryReview(db,session,id,quota,used) {
  const operation=db.prepare('SELECT state,expires,payload,reserve FROM memory_reviews WHERE session=? AND id=?').get(session,id);
  if(operation?.state==='undone')return {id,state:'undone'};
  if(operation?.state!=='applied'||operation.expires<=Date.now())reject('expired');
  const patch=JSON.parse(operation.payload);
  if(!same(rowsFor(db,session,patch.ids),patch.after))reject('undo_conflict');
  const growth=deltaFor(db,session,{before:patch.after,after:patch.before});
  if(used()-operation.reserve+growth>quota)reject('quota');
  try{writePatch(db,session,patch.after,patch.before);}
  catch(error){if(error.code==='ERR_SQLITE_ERROR')reject('undo_conflict');throw error;}
  db.prepare("UPDATE memory_reviews SET state='undone',payload='',reserve=0 WHERE session=? AND id=?").run(session,id);
  return {id,state:'undone',changed:true};
}
