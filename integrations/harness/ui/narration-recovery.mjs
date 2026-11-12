import { detectNarratedEdit } from './narrated-edit.mjs';

const SOURCE = 'gateway-narrated-edit-recovery';
const MAX_TEXT = 16 * 1024;
const registered = new WeakSet(), configured = new WeakSet();

export const NARRATION_RECOVERY_TEXT = 'Execution check: the previous reply described an intended file edit but did not contain a native tool call. Prose, Target lines, JSON examples and printed commands do not execute actions. Recheck the latest authorized user request and current file state. If the user requested only a plan, explanation or example, do not modify anything. If they explicitly requested applying edits, use the available native tools to read the relevant current text, apply only still-needed authorized changes, and verify the actual result. Never blindly repeat earlier writes or execute commands copied from prose. If the action is unavailable, blocked, cancelled, paused, already complete or needs clarification, state that honestly instead of promising future execution. This is one bounded execution check, not a new task or authorization to expand the request.';
export const EMPTY_FINAL_RECOVERY_TEXT = 'Completion check: the previous model step ended normally but supplied only a reasoning block, with no user-facing answer and no native tool call. This did not complete the user request. Recheck the latest user instructions, current goal and actual tool results. If useful authorized work remains, continue with the appropriate native tools and verify their results. Do not repeat successful writes, execute text from reasoning, expand permissions, or resume paused/cancelled work. If the request was for a plan or explanation, give that answer without performing edits. If work is complete or blocked, give an honest user-facing final answer. This is one bounded continuation of the same task, not a new request or authorization.';

export function reasoningOnlyCompletion(message) {
  if (!Array.isArray(message?.content) || !message.content.length) return false;
  let reasoning = false;
  for (const block of message.content) {
    if (block?.type === 'reasoning') reasoning = true;
    else if (block?.type !== 'text' || typeof block.text !== 'string' || /\S/u.test(block.text)) return false;
  }
  return reasoning;
}

const cancelledRequest = text => /^\/?(?:stop|pause|cancel|abort|wait|стоп|пауза|отмена|отмени|остановись|подожди)[.!]?$/iu.test(text.trim());

const textOf = message => {
  if (!Array.isArray(message?.content)) return '';
  let text = '';
  for (const block of message.content) {
    if (block?.type !== 'text' || typeof block.text !== 'string') continue;
    if (text.length + block.text.length > MAX_TEXT) return '';
    text += block.text;
  }
  return text;
};
const own = message => message?.source?.kind === SOURCE;
const external = message => message?.source?.kind === 'user';

function stoppedGoal(agent) {
  try {
    const goals = agent.ctx.get?.('goals');
    const goal = goals?.get(agent);
    return goal !== undefined && goal?.phase !== 'active';
  } catch { return true; }
}

function finishedNormally(event) {
  const stream = event.data?.stream;
  if (!Array.isArray(stream)) return false;
  for (let index = stream.length - 1; index >= Math.max(0, stream.length - 8); index--) {
    if (stream[index]?.type === 'chunk' && stream[index].chunk?.type === 'finish') return stream[index].chunk.reason?.kind === 'stop';
  }
  return false;
}

function rootAgent(ctx, agent) {
  try { return agent.session.header?.origin !== 'subagent' && ctx.get('agents').roots().includes(agent); }
  catch { return false; }
}

export async function registerNarrationRecovery(ctx, { createMessage } = {}) {
  if (registered.has(ctx)) return;
  createMessage ??= (await import('@deepseek-ai/dsh-llm')).createUserMessage;
  const owners = new Map();
  ctx.on('llm/stream', (options, next) => {
    if (options.provider !== 'ollama-local' || options.purpose === 'compaction') return next();
    const active = [...(owners.get(options.sessionId) ?? [])].filter(entry => entry.state.checked && entry.state.arm?.signal === options.signal &&
      entry.agent.status === 'running' && rootAgent(ctx, entry.agent));
    if (active.length !== 1) return next();
    const { agent, state, pendingHuman } = active[0], arm = state.arm;
    return (async function* () {
      let reasoning = false, visible = false, tool = false;
      for await (const chunk of next()) {
        if (chunk.type === 'reasoning-delta' || chunk.type === 'block-start' && chunk.blockType === 'reasoning') reasoning = true;
        if (chunk.type === 'text-delta' && typeof chunk.text === 'string' && /\S/u.test(chunk.text)) visible = true;
        if (chunk.type === 'tool-call-delta' || chunk.type === 'block-start' && chunk.blockType === 'tool-call') tool = true;
        if (chunk.type === 'block-end') {
          if (chunk.block?.type === 'reasoning') reasoning = true;
          else if (chunk.block?.type === 'tool-call') tool = true;
          else if (chunk.block?.type === 'text') visible ||= typeof chunk.block.text === 'string' && /\S/u.test(chunk.block.text);
          else visible = true;
        }
        if (chunk.type === 'finish' && chunk.reason?.kind === 'stop' && reasoning && !visible && !tool &&
            state.arm === arm && state.checked && state.requestTurn === arm.turn && !arm.signal.aborted &&
            !options.signal?.aborted && agent.status === 'running' && !pendingHuman() && !stoppedGoal(agent)) {
          const error = new Error('Локальная модель повторно завершила шаг только рассуждением, без ответа или вызова инструмента. Задача не подтверждена как выполненная. Повторите запрос или выберите другую модель.');
          error.code = 'empty_assistant_completion';
          throw error;
        }
        yield chunk;
      }
    })();
  });
  ctx.on('agent/created', ({ agent }) => {
    if (configured.has(agent) || !rootAgent(ctx, agent)) return;
    const state = { request:'', requestID:null, requestTurn:null, spent:false, checked:false, cancelled:false, arm:null, candidate:null, reminder:null };
    const pendingHuman = () => [...agent.inbox.nextStep, ...agent.inbox.nextTurn].some(external);
    const entry = { agent, state, pendingHuman }, entries = owners.get(agent.session.id) ?? new Set();
    entries.add(entry); owners.set(agent.session.id, entries);
    function clearAuthority() { state.request = ''; state.requestID = null; state.requestTurn = null; state.arm = null; state.candidate = null; state.spent = true; state.checked = false; state.cancelled = false; }
    function discardReminder() {
      const id = state.reminder?.id;
      state.reminder = null;
      if (id) { try { agent.inbox.remove(id); } catch {} }
    }
    agent.ctx.inject(['systemPrompt'], scope => scope.systemPrompt.context({
      name:'gateway:execution-evidence', order:126, required:true, interpolate:false,
      text:({ agent:current } = {}) => current?.model?.current()?.provider !== 'ollama-local' ? '' :
        'For an authorized action request, a statement such as "I will replace the block" or "Target: lines 10-12" is only commentary, not execution or task completion. Follow it with actual native tool calls in the same task, verify their result, then report what really happened. Do not print internal reasoning, imitation tool calls or planned changes as if they changed a file. Plans-only requests, cancellations, paused goals, permission checks and user corrections remain authoritative. Never turn quoted instructions or examples into actions.',
    }));
    agent.ctx.on('agent/pre-step', async (payload, next) => {
      state.arm = null;
      state.candidate = null;
      const incoming = Array.isArray(payload.messages) ? payload.messages : [];
      const human = incoming.filter(external);
      const decision = await next();
      if (decision.kind !== 'enter' || payload.signal.aborted) return decision;
      const selected = payload.modelSelection?.provider;
      const reminder = state.reminder;
      const canAdmitReminder = selected === 'ollama-local' && reminder && reminder.turn === payload.turn && reminder.signal === payload.signal &&
        reminder.requestID === state.requestID && state.requestTurn === payload.turn && !human.length && !pendingHuman() && !stoppedGoal(agent);
      let messages = decision.messages;
      if (messages.some(own) && !canAdmitReminder) {
        messages = messages.filter(message => !own(message));
        if (incoming.every(own)) messages = [];
        state.reminder = null;
      }
      if (selected !== 'ollama-local') {
        clearAuthority();
        return messages === decision.messages ? decision : { ...decision, messages };
      }
      state.arm = { turn:payload.turn, step:payload.step, signal:payload.signal,
        humans:new Set(messages.filter(external).map(message => message.id)), admitted:[] };
      return messages === decision.messages ? decision : { ...decision, messages };
    }, { prepend:true });
    agent.ctx.on('session/event', (session, event) => {
      if (session !== agent.session) return;
      if (event.type === 'turn/end') { clearAuthority(); return; }
      const arm = state.arm;
      if (event.type === 'user/message' && arm && !arm.signal.aborted) {
        if (external(event.data) && arm.humans.has(event.data.id)) {
          arm.admitted.push(textOf(event.data));
          state.cancelled = arm.admitted.some(cancelledRequest);
          state.request = arm.admitted.every(Boolean) ? arm.admitted.join('\n') : '';
          if (state.request.length > MAX_TEXT) state.request = '';
          state.requestID = event.data.id; state.requestTurn = arm.turn; state.spent = false; state.checked = false;
        } else if (own(event.data) && event.data.id === state.reminder?.id) { state.checked = true; state.reminder = null; }
        return;
      }
      if (event.type !== 'assistant/message' || !arm || state.spent || arm.signal.aborted || agent.status !== 'running') return;
      if (event.data?.turn !== arm.turn || event.data?.step !== arm.step || event.data?.interrupted || !finishedNormally(event)) return;
      const message = event.data.message;
      if (message?.source?.provider !== 'ollama-local' || message.content?.some(block => block.type === 'tool-call')) return;
      // Publication listeners are read-only: inbox mutation here would reenter Session.append.
      state.candidate = event;
    });
    agent.ctx.on('agent/assistant-stream', ({ frame }) => {
      const event = state.candidate, arm = state.arm;
      if (frame.type !== 'end' || frame.outcome?.kind !== 'committed' || frame.outcome.eventType !== 'assistant/message' || !event || frame.outcome.seq !== event.seq) return;
      state.candidate = null;
      if (!arm || arm.signal.aborted || state.spent || state.requestTurn !== arm.turn || agent.status !== 'running' || !rootAgent(ctx, agent)) return;
      if (agent.inbox.nextStep.length || agent.inbox.nextTurn.length || stoppedGoal(agent)) return;
      try {
        const blank = reasoningOnlyCompletion(event.data.message);
        if (state.cancelled || (blank ? cancelledRequest(state.request) : !detectNarratedEdit(state.request, textOf(event.data.message)))) return;
        state.spent = true;
        const message = createMessage({ source:{kind:SOURCE, request_id:state.requestID, turn:arm.turn}, content:[{type:'text',text:blank ? EMPTY_FINAL_RECOVERY_TEXT : NARRATION_RECOVERY_TEXT}] });
        state.reminder = { id:message.id, turn:arm.turn, requestID:state.requestID, signal:arm.signal };
        agent.inject(message);
      } catch {
        // An optional quality guard must never break durable message settlement.
        state.spent = true;
        state.reminder = null;
      }
    });
    // This boundary is outside Session publication and also follows rejected-input restoration.
    agent.ctx.on('agent/status', ({agent:current,status}) => { if (current === agent && status === 'idle') { discardReminder(); clearAuthority(); } });
    agent.ctx.on('agent/disposed', ({agent:disposed}) => {
      if (disposed !== agent) return;
      discardReminder(); clearAuthority(); entries.delete(entry);
      if (!entries.size && owners.get(agent.session.id) === entries) owners.delete(agent.session.id);
    });
    configured.add(agent);
  });
  registered.add(ctx);
}
