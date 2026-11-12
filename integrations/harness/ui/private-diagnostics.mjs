import { createHmac, randomBytes } from 'node:crypto';
import { constants } from 'node:fs';
import { mkdir, lstat, open, rename, unlink, chmod } from 'node:fs/promises';
import { join, resolve } from 'node:path';

const EVENTS = new Set(['diagnostics_started','agent_created','agent_disposed','turn_start','turn_end','step_start','step_end','request_context','assistant_message','assistant_attempt','tool_call','tool_result','goal_change','compact_start','compact_end','command_start','command_end','model_start','model_first_chunk','model_wait','model_end','model_error','agent_error','turn_stopping']);
const NUMBERS = ['turn','step','duration_ms','first_chunk_ms','idle_ms','chunks','reasoning_chunks','text_chunks','tool_call_chunks','input_tokens','output_tokens','total_tokens','cache_read_tokens','context_window','max_output_tokens','rounds_started','rounds_limit','request'];
const STOPS = new Set(['stop','length','max-tokens','tool-calls','tool_calls','error','cancelled','canceled','interrupted','completed','aborted']);
const GOALS = new Set(['active','paused','blocked','complete']);
const PURPOSES = new Set(['compaction','inference']);
const COMMANDS = new Set(['compact','goal']);
const number = value => Number.isSafeInteger(value) && value >= 0 && value <= 1e12 ? value : undefined;

// Nothing from a message, tool payload, error message or filename is serialized.
export function privateRecord(event, session, values = {}, now = Date.now()) {
  if (!EVENTS.has(event) || !/^[a-f0-9]{24}$/.test(session) || !Number.isSafeInteger(now) || now<0 || now>1e15) return null;
  const out = { schema:1, at_ms:now, session, event };
  for (const key of NUMBERS) { const value=number(values[key]); if(value!==undefined)out[key]=value; }
  for (const key of ['failure','aborted','finish_seen']) if(typeof values[key]==='boolean')out[key]=values[key];
  if (STOPS.has(values.stop)) out.stop=values.stop;
  if (GOALS.has(values.goal_state)) out.goal_state=values.goal_state;
  if (PURPOSES.has(values.purpose)) out.purpose=values.purpose;
  if (COMMANDS.has(values.command)) out.command=values.command;
  return out;
}

export function sessionEventMetadata(event) {
  const d=event.data || {}, values={turn:d.turn,step:d.step};
  const types={'turn/start':'turn_start','turn/end':'turn_end','step/start':'step_start','step/end':'step_end','request/context':'request_context','assistant/message':'assistant_message','assistant/attempt':'assistant_attempt','tool/call':'tool_call','tool/result':'tool_result','goal/change':'goal_change','compaction/start':'compact_start','compaction/end':'compact_end','command/run':'command_start','command/done':'command_end'};
  const type=types[event.type];if(!type)return null;
  if(type==='request_context')values.context_window=d.contextWindow;
  if(type==='turn_end')values.stop=d.reason?.kind;
  if(type==='goal_change'){
    values.goal_state=d.goal?.phase;values.rounds_limit=d.goal?.maxGoalRounds;values.rounds_started=d.roundsStarted;
  }
  if(type==='tool_result'||type==='compact_end')values.failure=d.error!==undefined&&d.error!==null&&d.error!==false;
  if(type==='command_start'||type==='command_end'){values.command=d.name;if(type==='command_end')values.failure=d.kind==='error';}
  if(type==='assistant_message'||type==='assistant_attempt'){
    const u=d.usage;
    if(u){values.input_tokens=u.inputTokens;values.output_tokens=u.outputTokens;values.total_tokens=u.totalTokens;values.cache_read_tokens=u.cacheReadTokens;}
    if(Array.isArray(d.stream))for(const item of d.stream){if(item.type==='chunk'&&item.chunk?.type==='finish'){values.stop=item.chunk.reason?.kind;values.failure=values.stop==='error';}}
  }
  return {event:type,values};
}

async function privateFile(path, flags) {
  const file=await open(path,flags| (constants.O_NOFOLLOW||0),0o600);
  const info=await file.stat();
  if(!info.isFile()||info.nlink!==1){await file.close();throw Error('Invalid diagnostics file');}
  await file.chmod(0o600);
  return file;
}

export async function createPrivateDiagnostics(directory, { maxBytes=1024*1024, warn=()=>{} }={}) {
  directory=resolve(directory);await mkdir(directory,{recursive:true,mode:0o700});
  const info=await lstat(directory);if(!info.isDirectory()||info.isSymbolicLink())throw Error('Invalid diagnostics directory');
  await chmod(directory,0o700);
  const keyPath=join(directory,'session-key');let key;
  try{const file=await privateFile(keyPath,constants.O_CREAT|constants.O_EXCL|constants.O_WRONLY);try{key=randomBytes(32);await file.writeFile(key);}finally{await file.close();}}
  catch(error){if(error.code!=='EEXIST')throw error;const file=await privateFile(keyPath,constants.O_RDONLY);try{key=await file.readFile();}finally{await file.close();}}
  if(key.length!==32)throw Error('Invalid diagnostics key');
  let queue=Promise.resolve(),disabled=false;
  const path=join(directory,'events.jsonl');
  const label=id=>createHmac('sha256',key).update(typeof id==='string'?id:'unscoped').digest('hex').slice(0,24);
  const emit=(event,id,values)=>{
    if(disabled)return;
    const record=privateRecord(event,label(id),values);if(!record)return;
    const line=JSON.stringify(record)+'\n';
    queue=queue.then(async()=>{
      let size=0;
      try{const stat=await lstat(path);if(!stat.isFile()||stat.isSymbolicLink()||stat.nlink!==1)throw Error('Invalid diagnostics log');size=stat.size;}catch(error){if(error.code!=='ENOENT')throw error;}
      if(size+Buffer.byteLength(line)>maxBytes){
        for(const suffix of ['.2','.1']){try{const stat=await lstat(path+suffix);if(!stat.isFile()||stat.isSymbolicLink()||stat.nlink!==1)throw Error('Invalid diagnostics rotation file');}catch(error){if(error.code!=='ENOENT')throw error;}}
        try{await unlink(path+'.2');}catch(error){if(error.code!=='ENOENT')throw error;}
        try{await rename(path+'.1',path+'.2');}catch(error){if(error.code!=='ENOENT')throw error;}
        await rename(path,path+'.1');
      }
      const file=await privateFile(path,constants.O_CREAT|constants.O_APPEND|constants.O_WRONLY);
      try{await file.writeFile(line);}finally{await file.close();}
    }).catch(()=>{if(!disabled){disabled=true;warn('Private diagnostics disabled: storage unavailable');}});
  };
  return {emit,label,flush:()=>queue};
}

export async function registerPrivateDiagnostics(ctx) {
  if(process.env.HARNESS_PRIVATE_DIAGNOSTICS!=='true'||process.env.HARNESS_USER_MODE==='true')return;
  let log;
  try{log=await createPrivateDiagnostics(join(process.env.DSH_HOME,'private-diagnostics'),{warn:message=>ctx.logger.warn(message)});}
  catch{ctx.logger.warn('Private diagnostics unavailable');return;}
  log.emit('diagnostics_started','runtime');
  ctx.effect(()=>()=>log.flush(),'gateway: private metadata diagnostics');
  ctx.on('agent/created',({agent})=>{
    const id=agent.session.id;
    log.emit('agent_created',id);
    agent.ctx.on('session/event',(session,event)=>{if(session!==agent.session)return;try{const metadata=sessionEventMetadata(event);if(metadata)log.emit(metadata.event,id,metadata.values);}catch{/* Diagnostics must not interrupt the agent. */}});
    agent.ctx.on('agent/turn-stopping',()=>log.emit('turn_stopping',id));
    agent.ctx.on('agent/error',()=>log.emit('agent_error',id,{failure:true}));
    agent.ctx.on('agent/disposed',({agent:disposed})=>{if(disposed===agent)log.emit('agent_disposed',id);});
  });
  let counter=0;
  ctx.on('llm/stream',(options,next)=>(async function*(){
    const id=options.sessionId,request=++counter,start=Date.now(),purpose=options.purpose==='compaction'?'compaction':'inference';
    let chunks=0,first,last=start;
    const metadata={reasoning_chunks:0,text_chunks:0,tool_call_chunks:0,finish_seen:false};
    log.emit('model_start',id,{request,purpose,max_output_tokens:options.maxTokens});
    const waiting=setInterval(()=>log.emit('model_wait',id,{request,purpose,chunks,duration_ms:Date.now()-start,idle_ms:Date.now()-last,aborted:options.signal?.aborted===true}),60000);
    waiting.unref();
    try{
      for await(const chunk of next()){
        chunks++;
        // Inspect only discriminants; even hostile metadata must not affect streaming.
        try{
          switch(chunk?.type){
            case 'reasoning-delta':metadata.reasoning_chunks=Math.min(metadata.reasoning_chunks+1,1e12);break;
            case 'text-delta':metadata.text_chunks=Math.min(metadata.text_chunks+1,1e12);break;
            case 'tool-call-delta':metadata.tool_call_chunks=Math.min(metadata.tool_call_chunks+1,1e12);break;
            case 'finish':{
              metadata.finish_seen=true;delete metadata.stop;
              const stop=chunk.reason?.kind;
              if(STOPS.has(stop))metadata.stop=stop;
              break;
            }
          }
        }catch{/* Diagnostics must not interrupt the stream. */}
        last=Date.now();
        if(first===undefined){first=Date.now()-start;log.emit('model_first_chunk',id,{request,purpose,first_chunk_ms:first});}
        yield chunk;
      }
      log.emit('model_end',id,{request,purpose,chunks,...metadata,duration_ms:Date.now()-start,aborted:options.signal?.aborted===true});
    }catch(error){
      log.emit('model_error',id,{request,purpose,chunks,...metadata,duration_ms:Date.now()-start,failure:true,aborted:options.signal?.aborted===true});
      throw error;
    }finally{clearInterval(waiting);}
  })());
}
