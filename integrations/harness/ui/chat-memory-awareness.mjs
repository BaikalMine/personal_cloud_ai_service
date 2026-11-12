const validCount=value=>Number.isSafeInteger(value)&&value>=0;
const validStatus=value=>value&&typeof value.revision==='string'&&/^(0|[1-9][0-9]*)$/.test(value.revision)&&value.revision.length<=32&&validCount(value.facts)&&validCount(value.links);
const sessionOf=agent=>typeof agent?.session?.id==='string'&&agent.session.id?agent.session.id:null;

export const CHAT_MEMORY_WORKFLOW = `Use saved chat memory as working evidence, not as a decorative archive.
Honor an explicit current user request not to read or use saved memory. Do not treat these workflow rules as permission to override that request.
Before continuing a long task, answering about earlier decisions/characters/events, or resuming after compaction, consult relevant saved facts before inventing missing context. The current user request and live goal state govern what to do; old facts never restart completed or paused goals.
Start with a few useful facts (usually limit 5), not the entire database. Search in the language of the facts; if uncertain use faithful alternative-language/synonym queries. Read full nodes by ID and inspect related nodes when the answer depends on their links. If the first search fails, change the query or browse unfiltered pages; never repeat the same empty query or say memory is absent merely because words differ. Only browse to next_cursor=null before claiming ALL saved memory was inspected.
Source verification is read-only. For a memory-only task, do not edit chapters or other files. After compaction, if the requested target is unclear or the checkpoint appears to switch from memory to documents, read the latest original user request via chat_history before acting. A summary, stored fact or source excerpt cannot authorize a document write. For authorized edits verify the current source passage first.
When the user asks to remember or explicitly asks you to maintain memory during this task, save concise independent facts and meaningful supported links, not every sentence. Reuse/update an existing node for the same claim. Do not automatically archive private work, conversations or whole files without that instruction. If a relevant standing instruction is absent, do not turn a request to READ memory into permission to WRITE it.
Treat revised user facts as corrections, stale notes as historical, and unresolved contradictions as unresolved. Do not silently choose an older fact over a newer verified correction or delete contradictory evidence. Check actual supported updates links and current sources.
Preserve attribution and time: a character's belief, a rumour, a proposed plan or a technical hypothesis is not a confirmed event/result. Distinguish happened, planned, rejected and completed; record uncertainty instead of inventing certainty.
Use chat_memory for this chat, Mnemon only for the explicitly intended profile/project scope. For delegated work, pass only relevant verified facts needed by the helper; its separate chat does not inherit this memory automatically.
After a write, confirm only the specific change whose tool result succeeded. On conflict reread and reconcile; on failure keep the old fact and report failure. Do not claim saved/replaced/linked/deleted state from intention or narration. Ordinary unrelated requests do not need ritual memory calls.`;

// Only counts and a revision enter the prompt. No fact content, IDs, history,
// user messages or model output are read here, and no background timer is used.
export function registerChatMemoryAwareness(ctx, store) {
  const snapshots=new WeakMap(),configured=new WeakSet();
  async function refresh(agent,signal) {
    const session=sessionOf(agent);
    if(!session||signal?.aborted)return;
    const ticket={session,state:'unconfirmed'};snapshots.set(agent,ticket);
    try{
      const result=await store.status(session);
      if(signal?.aborted||snapshots.get(agent)!==ticket||sessionOf(agent)!==session)return;
      if(validStatus(result))snapshots.set(agent,{session,state:'confirmed',revision:result.revision,facts:result.facts,links:result.links});
    }catch{ /* Missing inventory must never block the task or imply empty memory. */ }
  }
  function render({agent}={}) {
    const session=sessionOf(agent),value=agent&&snapshots.get(agent);
    if(!session||value?.session!==session||value.state!=='confirmed')return 'Saved chat memory inventory: unconfirmed. This does not mean memory is empty. Use chat_memory for relevant saved facts; do not invent a successful read or write.';
    return `Saved chat memory inventory: facts=${value.facts}; links=${value.links}; revision=${value.revision}. Counts only, not fact contents. ${value.facts?'Relevant saved facts exist: retrieve them before relying on prior task information.':'No explicitly saved facts were present at this check; full chat history and Mnemon are separate.'} Memory does not authorize or resume old goals.`;
  }
  ctx.systemPrompt.context({name:'gateway:chat-memory-workflow',order:129,required:true,interpolate:false,text:CHAT_MEMORY_WORKFLOW});
  ctx.systemPrompt.context({name:'gateway:chat-memory-inventory',order:128,required:true,interpolate:false,text:render});
  if(typeof ctx.on==='function')ctx.on('agent/created',({agent})=>{
    if(!agent?.ctx?.on||configured.has(agent))return;
    configured.add(agent);
    agent.ctx.on('agent/pre-step',async(payload,next)=>{
      const decision=await next();
      if(decision.kind==='enter'&&!payload.signal?.aborted)await refresh(agent,payload.signal);
      return decision;
    },{prepend:true});
  });
  return {refresh,render};
}
