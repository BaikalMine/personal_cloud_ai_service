(function(root,factory){
 const api=factory();
 if(typeof module==='object'&&module.exports) module.exports=api;
 if(root) root.ModelMaintenance=api;
})(typeof window!=='undefined'?window:null,function(){
 'use strict';
 function create({check,save=()=>{},onState=()=>{},initial=null}){
  let state={kind:'loras',pending:[],total:0,done:0,updates:0,status:'idle',error:''},running=false,paused=false;
  if(initial&&['loras','checkpoints'].includes(initial.kind)&&Array.isArray(initial.pending)&&initial.pending.length<=10000&&initial.pending.every(x=>typeof x==='string'&&x.length<=16384)
    &&['total','done','updates'].every(k=>Number.isInteger(initial[k])&&initial[k]>=0)&&initial.updates<=initial.done&&initial.done+initial.pending.length===initial.total){
   state={...state,kind:initial.kind,pending:initial.pending,total:initial.total,done:initial.done,updates:initial.updates,status:initial.pending.length?'paused':'completed'};
  }
  const snapshot=()=>({...state,pending:[...state.pending],running});
  const publish=()=>{save(snapshot());onState(snapshot());};
  async function run(){
   if(running||!state.pending.length)return;
   running=true;paused=false;state.status='running';state.error='';publish();
   try{
    while(state.pending.length&&!paused){
     let result;
     try{result=await check(state.pending[0]);}
     catch(error){state.error=error.message||'Проверка не завершена.';paused=true;break;}
     state.pending.shift();state.done++;if(result.update)state.updates++;
     publish();
    }
    state.status=state.pending.length?'paused':'completed';
   }finally{running=false;publish();}
  }
  function start(kind,tokens){
   if(running||!['loras','checkpoints'].includes(kind)||tokens.length>10000) return false;
   state={kind,pending:[...new Set(tokens)],total:0,done:0,updates:0,status:'paused',error:''};
   state.total=state.pending.length;
   if(!state.total)state.status='completed';
   publish();return true;
  }
  function pause(){paused=true;if(state.pending.length)state.status='paused';publish();}
  return {start,run,pause,snapshot,refresh:publish};
 }
 return {create};
});
