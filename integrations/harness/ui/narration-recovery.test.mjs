import test from 'node:test';
import assert from 'node:assert/strict';
import { registerNarrationRecovery, NARRATION_RECOVERY_TEXT, EMPTY_FINAL_RECOVERY_TEXT, reasoningOnlyCompletion } from './narration-recovery.mjs';

const request = 'Примени правки к файлу synthetic.md.';
const narration = 'Применение правок к файлу synthetic.md\nЯ заменю финальный блок текста, начиная с описания комнаты и заканчивая финальным абзацем.\nTarget: Строки 10–12.';
let userID = 0;
const user = (text=request) => ({id:`human-${++userID}`,role:'user',source:{kind:'user'},content:[{type:'text',text}]});
const freeze = value => {
  if (value && typeof value === 'object' && !Object.isFrozen(value)) {
    for (const child of Object.values(value)) freeze(child);
    Object.freeze(value);
  }
  return value;
};

async function fixture({provider='ollama-local',goal,goalError=false,injectError=false,createError=false,
  root=true,origin='user',registryError=false}={}) {
  const created = [], hooks = new Map(), rootHooks = new Map(), prompts = [], injected = [], removed = [], mutations = [], made = [];
  const controller = new AbortController(), roots = [], goals = {value:goal,error:goalError};
  const registry = {error:registryError};
  let publishing = false, seq = 0, messageID = 0;
  let step = {turn:1,step:1,signal:controller.signal,modelSelection:{provider}};
  const ctx = {
    on(name, handler) {
      if (name === 'agent/created') created.push(handler);
      else { assert.equal(name, 'llm/stream'); assert(!rootHooks.has(name)); rootHooks.set(name, handler); }
    },
    get(name) {
      assert.equal(name, 'agents');
      return {roots() { if (registry.error) throw Error('Synthetic registry failure'); return roots; }};
    },
  };
  function mutation(kind, id) {
    mutations.push({kind,id,publishing});
    assert.equal(publishing, false, 'Session publication must not mutate the inbox');
  }
  const agent = {
    status:'running', options:{provider}, model:{current:()=>({provider})}, session:{id:'synthetic-session',header:{origin}},
    inbox:{
      nextStep:[], nextTurn:[],
      remove(id) {
        mutation('remove', id);
        removed.push(id);
        for (const queue of [this.nextStep, this.nextTurn]) {
          for (let index = queue.length - 1; index >= 0; index--) {
            if (queue[index].id === id) queue.splice(index, 1);
          }
        }
      },
    },
    ctx:{
      on(name, handler, options) {
        assert.equal(hooks.has(name), false, `Duplicate hook: ${name}`);
        if (name === 'agent/pre-step') assert.deepEqual(options, {prepend:true});
        hooks.set(name, handler);
      },
      inject(_services, setup) { setup({systemPrompt:{context:value=>prompts.push(value)}}); },
      get(name) {
        assert.equal(name, 'goals');
        return {get(current) {
          assert.equal(current, agent);
          if (goals.error) throw Error('Synthetic goals failure');
          return goals.value;
        }};
      },
    },
    inject(message) {
      mutation('inject', message.id);
      if (injectError) throw Error('Synthetic inbox failure');
      injected.push(message);
      this.inbox.nextStep.push(message);
    },
  };
  if (root) roots.push(agent);
  await registerNarrationRecovery(ctx, {createMessage(input) {
    if (createError) throw Error('Synthetic message failure');
    const message = {id:`recovery-${++messageID}`,role:'user',...input};
    made.push(message);
    return message;
  }});
  await registerNarrationRecovery(ctx, {createMessage() { throw Error('Duplicate registration'); }});
  created[0]({agent});
  created[0]({agent});

  function publish(type, data, session=agent.session) {
    const event = freeze({type,seq:++seq,data});
    assert.equal(publishing, false);
    publishing = true;
    try { hooks.get('session/event')?.(session, event); }
    finally { publishing = false; }
    return event;
  }
  async function pre(messages=[user()], options={}) {
    const {decision,beforeDecision,...overrides} = options;
    step = {...step,...overrides};
    const payload = {...step,messages};
    const next = async () => {
      beforeDecision?.();
      return decision ?? {kind:'enter',messages};
    };
    const handler = hooks.get('agent/pre-step');
    return handler ? handler(payload, next) : next();
  }
  async function enter(messages=[user()], options={}) {
    const decision = await pre(messages, options);
    if (decision.kind === 'enter') {
      for (const message of decision.messages) publish('user/message', message);
    }
    return decision;
  }
  function publishAssistant(text=narration, options={}) {
    const selected = step.modelSelection?.provider;
    const message = {id:`assistant-${seq + 1}`,role:'assistant',source:{provider:selected},content:[{type:'text',text}],...options.message};
    return publish('assistant/message', {
      turn:step.turn, step:step.step, message,
      stream:[{type:'chunk',chunk:{type:'finish',reason:{kind:options.finish ?? 'stop'}}}],
      ...options.data,
    }, options.session);
  }
  const stream = frame => hooks.get('agent/assistant-stream')?.({agent,frame});
  const settle = (event, outcome={}) => stream({type:'end',outcome:{kind:'committed',eventType:event.type,seq:event.seq,...outcome}});
  function answer(text=narration, options={}) {
    const event = publishAssistant(text, options);
    settle(event);
    return event;
  }
  function status(value) {
    agent.status = value;
    hooks.get('agent/status')?.({agent,status:value});
  }
  const takeNextStep = () => agent.inbox.nextStep.splice(0);
  const modelStream = (chunks, options = {}) => rootHooks.get('llm/stream')({ provider:'ollama-local', sessionId:agent.session.id, signal:controller.signal, ...options }, async function* () { for (const chunk of chunks) yield chunk; });
  return {agent,hooks,prompts,injected,removed,mutations,made,controller,created,roots,registry,goals,modelStream,
    pre,enter,publish,publishAssistant,stream,settle,answer,status,takeNextStep};
}

test('reasoning-only completion is detected without accessing private reasoning text', () => {
  const privateBlock = {type:'reasoning',get text() { throw Error('Private reasoning must not be inspected'); }};
  assert.equal(reasoningOnlyCompletion({content:[privateBlock]}), true);
  assert.equal(reasoningOnlyCompletion({content:[privateBlock,{type:'text',text:' \n\t'}]}), true);
  for (const content of [[],[{type:'text',text:''}],[privateBlock,{type:'tool-call'}],
    [privateBlock,{type:'text',text:'A final answer.'}], [privateBlock,{type:'image'}],
    [privateBlock,{type:'text',text:'x'.repeat(65536)}]]) {
    assert.equal(reasoningOnlyCompletion({content}), false);
  }
});

test('reasoning-only completion gets one neutral same-task check, including plans-only requests', async () => {
  for (const request of ['Apply edits to synthetic.md.', 'Explain a plan for synthetic.md.', 'Answer a synthetic question.']) {
    const f = await fixture();
    await f.enter([user(request)]);
    f.answer('',{message:{content:[{type:'reasoning',text:'Synthetic planning, not executable.'}]}});
    assert.equal(f.injected.length,1);
    assert.equal(f.injected[0].content[0].text,EMPTY_FINAL_RECOVERY_TEXT);
    assert.match(EMPTY_FINAL_RECOVERY_TEXT,/not a new request or authorization/);
    await f.enter(f.takeNextStep(),{step:2});
    f.answer('',{message:{content:[{type:'reasoning',text:'Still synthetic planning.'}]}});
    assert.equal(f.injected.length,1);
  }
});

test('a repeated reasoning-only stop becomes an explicit stream failure after admitted recovery', async () => {
  const f = await fixture(); await f.enter();
  const chunks=[{type:'reasoning-delta',get text(){throw Error('No reasoning inspection');}},{type:'finish',reason:{kind:'stop'}}];
  assert.equal((await Array.fromAsync(f.modelStream(chunks))).length,2,'first attempt is eligible for one recovery');
  f.answer('',{message:{content:[{type:'reasoning',text:'Synthetic private planning.'}]}});
  await f.enter(f.takeNextStep(),{step:2});
  await assert.rejects(async()=>Array.fromAsync(f.modelStream(chunks)),error=>error.code==='empty_assistant_completion');
  for (const tail of [[{type:'text-delta',text:'Final answer.'}], [{type:'block-start',blockType:'tool-call'}]]) {
    assert.equal((await Array.fromAsync(f.modelStream([chunks[0],...tail,chunks[1]]))).length,3);
  }
  assert.equal((await Array.fromAsync(f.modelStream(chunks,{purpose:'compaction'}))).length,2);
  assert.equal((await Array.fromAsync(f.modelStream(chunks,{sessionId:'another-session'}))).length,2);
  assert.equal((await Array.fromAsync(f.modelStream(chunks,{signal:new AbortController().signal}))).length,2);
});

test('reasoning-only recovery respects pause, cancellation, correction and descendant lifecycle', async () => {
  const content=[{type:'reasoning',text:'Synthetic planning.'}];
  for (const command of ['Stop','Pause','Cancel','Стоп','Пауза','Подожди']) {
    const f=await fixture();await f.enter([user(command)]);f.answer('',{message:{content}});assert.equal(f.injected.length,0);
    const batch=await fixture();await batch.enter([user(),user(command)]);batch.answer('',{message:{content}});assert.equal(batch.injected.length,0);
    await batch.enter([],{step:2});batch.answer('',{message:{content}});assert.equal(batch.injected.length,0);
  }
  const paused=await fixture({goal:{phase:'paused'}});await paused.enter();paused.answer('',{message:{content}});assert.equal(paused.injected.length,0);
  const f=await fixture();await f.enter();
  f.hooks.get('agent/disposed')({agent:{session:{id:'child'}}});
  f.hooks.get('agent/status')({agent:{},status:'idle'});
  f.answer('',{message:{content}});assert.equal(f.injected.length,1);
  await f.enter(f.takeNextStep(),{step:2});
  f.agent.inbox.nextTurn.push(user('Stop'));
  assert.equal((await Array.fromAsync(f.modelStream([{type:'reasoning-delta'},{type:'finish',reason:{kind:'stop'}}]))).length,2);
  f.agent.inbox.nextTurn.length=0;f.controller.abort();
  assert.equal((await Array.fromAsync(f.modelStream([{type:'reasoning-delta'},{type:'finish',reason:{kind:'stop'}}]))).length,2);
});

test('registration is idempotent and the execution-evidence prompt is provider scoped', async () => {
  const f = await fixture();
  assert.equal(f.created.length, 1);
  assert.equal(f.prompts.length, 1);
  assert.equal(f.hooks.size, 5);
  assert.equal(f.prompts[0].name, 'gateway:execution-evidence');
  assert.equal(f.prompts[0].required, true);
  assert.equal(f.prompts[0].interpolate, false);
  assert.match(f.prompts[0].text({agent:f.agent}), /actual native tool calls/);
  assert.equal(f.prompts[0].text(), '');
  assert.equal(f.prompts[0].text({agent:{model:{current:()=>({provider:'other'})}}}), '');
});

test('publication is read-only; only the matching committed assistant-stream end injects', async () => {
  const f = await fixture(), human = user();
  await f.enter([human]);
  const event = f.publishAssistant();
  assert.equal(Object.isFrozen(event.data.message.content), true);
  assert.deepEqual(f.mutations, []);
  assert.deepEqual(f.injected, []);
  f.stream({type:'chunk'});
  f.settle(event, {kind:'rejected'});
  f.settle(event, {eventType:'user/message'});
  f.settle(event, {seq:event.seq + 1});
  assert.deepEqual(f.mutations, []);
  f.settle(event);
  assert.equal(f.injected.length, 1);
  const reminder = f.injected[0];
  assert.equal(typeof reminder.id, 'string');
  assert.deepEqual(reminder.source, {kind:'gateway-narrated-edit-recovery',request_id:human.id,turn:1});
  assert.deepEqual(reminder.content, [{type:'text',text:NARRATION_RECOVERY_TEXT}]);
  assert.ok(!NARRATION_RECOVERY_TEXT.includes('synthetic.md'));
  assert.ok(!NARRATION_RECOVERY_TEXT.includes('10–12'));
  assert.match(NARRATION_RECOVERY_TEXT, /never blindly repeat/i);
  assert.deepEqual(f.mutations, [{kind:'inject',id:reminder.id,publishing:false}]);
  f.settle(event);
  assert.equal(f.injected.length, 1, 'Repeated settlement must not inject twice');
});

test('a repeated narration stays one shot after the recovery message is consumed', async () => {
  const f = await fixture();
  await f.enter();
  f.answer();
  const [reminder] = f.takeNextStep();
  const decision = await f.enter([reminder], {step:2});
  assert.deepEqual(decision.messages, [reminder]);
  f.answer();
  await f.enter([], {step:3});
  f.answer();
  assert.equal(f.injected.length, 1);
  f.status('idle');
  assert.deepEqual(f.removed, [], 'An admitted reminder is no longer pending cleanup');
});

test('only an actually admitted human message id grants authority', async () => {
  const missing = await fixture(), human = user();
  await missing.pre([human]);
  missing.answer();
  assert.equal(missing.injected.length, 0, 'Pre-step input alone is not durable admission');
  missing.publish('user/message', {...human,id:'not-admitted'});
  missing.answer();
  assert.equal(missing.injected.length, 0, 'Matching text with the wrong id is not authority');
  missing.publish('user/message', human, {id:'foreign-session'});
  missing.answer();
  assert.equal(missing.injected.length, 0);
  missing.publish('user/message', human);
  missing.answer();
  assert.equal(missing.injected[0].source.request_id, human.id);

  const filtered = await fixture(), omitted = user();
  await filtered.pre([omitted], {decision:{kind:'enter',messages:[]}});
  filtered.publish('user/message', omitted);
  filtered.answer();
  assert.equal(filtered.injected.length, 0, 'Only ids in the final enter decision are admitted');
});

test('goal/internal messages cannot authorize or replenish the one-shot budget', async () => {
  for (const source of [{kind:'goal'},{kind:'system'},{kind:'tool'},{},undefined]) {
    const f = await fixture();
    await f.enter([{...user(),source}]);
    f.answer();
    assert.equal(f.injected.length, 0, JSON.stringify(source));
  }
  const f = await fixture();
  await f.enter();
  f.answer();
  await f.enter(f.takeNextStep(), {step:2});
  f.answer();
  await f.enter([{...user(),source:{kind:'goal'}}], {turn:2,step:1});
  f.answer();
  assert.equal(f.injected.length, 1);
  const fresh = user();
  await f.enter([fresh], {turn:3,step:1});
  f.answer();
  assert.equal(f.injected.length, 2);
  assert.equal(f.injected[1].source.request_id, fresh.id);
  assert.equal(f.injected[1].source.turn, 3);
});

test('all admitted human text is considered and the latest admitted id is attributed', async () => {
  const f = await fixture(), first = user('Примени правки к файлу synthetic.md'), last = user('Внеси правки в файл synthetic.md');
  await f.enter([first,last]);
  f.answer();
  assert.equal(f.injected.length, 1);
  assert.equal(f.injected[0].source.request_id, last.id);
  const corrected = await fixture();
  await corrected.enter([user(),user('Stop')]);
  corrected.answer();
  assert.equal(corrected.injected.length, 0);
});

test('pending nextStep or nextTurn human work prevents injection at settlement', async () => {
  for (const queue of ['nextStep','nextTurn']) {
    const f = await fixture();
    await f.enter();
    const event = f.publishAssistant();
    const correction = user('Stop');
    f.agent.inbox[queue].push(correction);
    f.settle(event);
    assert.deepEqual(f.injected, [], queue);
    assert.deepEqual(f.agent.inbox[queue], [correction]);
    assert.deepEqual(f.mutations, []);
  }
});

test('a human nextTurn queued after injection filters out the drained reminder', async () => {
  const f = await fixture();
  await f.enter();
  f.answer();
  const [reminder] = f.takeNextStep(), correction = user('Только объясни правки.');
  f.agent.inbox.nextTurn.push(correction);
  const original = freeze({kind:'enter',messages:[reminder]});
  const decision = await f.enter([reminder], {step:2,decision:original});
  assert.deepEqual(decision.messages, []);
  assert.deepEqual(original.messages, [reminder], 'The downstream decision is not mutated');
  assert.deepEqual(f.agent.inbox.nextTurn, [correction]);
  f.answer();
  assert.equal(f.injected.length, 1);
  await f.enter(f.agent.inbox.nextTurn.splice(0), {turn:2,step:1});
  f.answer();
  assert.equal(f.injected.length, 1);
});

test('an admitted correction after injection wins over a co-delivered reminder', async () => {
  const f = await fixture();
  await f.enter();
  f.answer();
  const [reminder] = f.takeNextStep(), correction = user('Только объясни правки.');
  const decision = await f.enter([reminder,correction], {step:2});
  assert.deepEqual(decision.messages, [correction]);
  f.answer();
  assert.equal(f.injected.length, 1);
});

test('a correction admitted on another route clears local authority in the same turn', async () => {
  const f = await fixture();
  await f.enter();
  const correction = user('Stop. Only explain the changes.');
  const decision = await f.enter([correction], {step:2,modelSelection:{provider:'other'}});
  assert.deepEqual(decision.messages, [correction]);
  f.answer();
  assert.deepEqual(f.injected, []);
  await f.enter([], {step:3,modelSelection:{provider:'ollama-local'}});
  f.answer();
  assert.deepEqual(f.injected, [], 'Returning to local must not revive the earlier edit request');
  const fresh = user();
  await f.enter([fresh], {step:4});
  f.answer();
  assert.equal(f.injected.length, 1, 'A new admitted local human request can authorize recovery');
  assert.equal(f.injected[0].source.request_id, fresh.id);
});

test('a local reminder never leaks to an accepted nonlocal step', async () => {
  for (const withCorrection of [false,true]) {
    const f = await fixture();
    await f.enter();
    f.answer();
    const [reminder] = f.takeNextStep(), correction = user('Only explain the changes.');
    const incoming = withCorrection ? [reminder,correction] : [reminder];
    const original = freeze({kind:'enter',messages:incoming});
    const decision = await f.enter(incoming, {step:2,modelSelection:{provider:'other'},decision:original});
    assert.deepEqual(decision.messages, withCorrection ? [correction] : []);
    assert.deepEqual(original.messages, incoming);
    await f.enter([], {step:3,modelSelection:{provider:'ollama-local'}});
    f.answer();
    assert.equal(f.injected.length, 1);
  }
});

test('rejected reminder restoration is removed on idle without dropping human work', async () => {
  const f = await fixture();
  await f.enter();
  f.answer();
  const [reminder] = f.takeNextStep(), correction = user('Stop');
  const decision = await f.pre([reminder], {step:2,decision:{kind:'reject'}});
  assert.equal(decision.kind, 'reject');
  // The runtime restores drained input on rejection, then publishes idle status.
  f.agent.inbox.nextStep.push(reminder);
  f.agent.inbox.nextTurn.push(correction);
  f.status('idle');
  assert.deepEqual(f.removed, [reminder.id]);
  assert.deepEqual(f.agent.inbox.nextStep, []);
  assert.deepEqual(f.agent.inbox.nextTurn, [correction]);
  assert.equal(f.mutations.every(value => !value.publishing), true);
  f.agent.inbox.nextTurn.length = 0;
  f.status('running');
  await f.enter([], {turn:2,step:1});
  f.answer();
  assert.equal(f.injected.length, 1, 'Idle also clears the old user authority');
});

test('reminders cannot cross a turn or signal boundary', async () => {
  for (const options of [{turn:2},{signal:new AbortController().signal}]) {
    const f = await fixture();
    await f.enter();
    f.answer();
    const decision = await f.enter(f.takeNextStep(), {step:2,...options});
    assert.deepEqual(decision.messages, []);
    f.answer();
    assert.equal(f.injected.length, 1);
  }
  const staleAuthority = await fixture();
  await staleAuthority.enter();
  await staleAuthority.enter([], {turn:2,step:1});
  staleAuthority.answer();
  assert.equal(staleAuthority.injected.length, 0, 'Previous-turn admission does not authorize a new turn');
});

test('aborted or rejected pre-step input never grants authority', async () => {
  for (const kind of ['reject','abort-before','abort-during']) {
    const f = await fixture(), human = user();
    if (kind === 'abort-before') f.controller.abort();
    await f.pre([human], {
      ...(kind === 'reject' ? {decision:{kind:'reject'}} : {}),
      ...(kind === 'abort-during' ? {beforeDecision:()=>f.controller.abort()} : {}),
    });
    f.publish('user/message', human);
    f.answer();
    assert.equal(f.injected.length, 0, kind);
    f.status('idle');
    f.status('running');
    await f.enter([], {turn:2,step:1,signal:new AbortController().signal});
    f.answer();
    assert.equal(f.injected.length, 0, `${kind} must not leave latent authority`);
  }
});

test('an abort between publication and stream end suppresses the candidate', async () => {
  const f = await fixture();
  await f.enter();
  const event = f.publishAssistant();
  f.controller.abort();
  f.settle(event);
  assert.deepEqual(f.mutations, []);
});

test('stale turn, step, session, and superseded stream candidates are ignored', async () => {
  for (const options of [{data:{turn:99}},{data:{step:99}},{session:{id:'foreign-session'}}]) {
    const f = await fixture();
    await f.enter();
    f.answer(narration, options);
    assert.equal(f.injected.length, 0);
  }
  const f = await fixture();
  await f.enter();
  const stale = f.publishAssistant();
  await f.enter([], {step:2,signal:new AbortController().signal});
  f.settle(stale);
  assert.equal(f.injected.length, 0, 'A new pre-step invalidates the previous candidate');
});

test('subagents, non-root agents and unavailable roots registries are excluded', async () => {
  for (const options of [{origin:'subagent'},{root:false},{registryError:true}]) {
    const f = await fixture(options);
    assert.equal(f.hooks.size, 0);
    assert.equal(f.prompts.length, 0);
    await f.enter();
    f.answer();
    assert.deepEqual(f.mutations, []);
  }
  for (const change of [f=>f.roots.splice(0),f=>{f.registry.error = true;}]) {
    const f = await fixture();
    await f.enter();
    const event = f.publishAssistant();
    change(f);
    f.settle(event);
    assert.deepEqual(f.injected, [], 'Root membership is checked again at settlement');
  }
});

test('paused, completed, blocked and unreadable goals cannot trigger recovery', async () => {
  for (const phase of ['paused','complete','blocked']) {
    const f = await fixture({goal:{phase}});
    await f.enter();
    f.answer();
    assert.deepEqual(f.injected, [], phase);
  }
  const unreadable = await fixture({goalError:true});
  await unreadable.enter();
  unreadable.answer();
  assert.deepEqual(unreadable.injected, []);
  const active = await fixture({goal:{phase:'active'}});
  await active.enter();
  active.answer();
  assert.equal(active.injected.length, 1);
});

test('goal pause between publication and settlement, or before reminder admission, wins', async () => {
  const before = await fixture({goal:{phase:'active'}});
  await before.enter();
  const event = before.publishAssistant();
  before.goals.value = {phase:'paused'};
  before.settle(event);
  assert.deepEqual(before.injected, []);

  const after = await fixture({goal:{phase:'active'}});
  await after.enter();
  after.answer();
  after.goals.value = {phase:'paused'};
  const decision = await after.enter(after.takeNextStep(), {step:2});
  assert.deepEqual(decision.messages, []);
  after.answer();
  assert.equal(after.injected.length, 1);
});

test('plans, native tool calls, non-stop endings and interrupted messages are untouched', async () => {
  const plan = await fixture();
  await plan.enter([user('Покажи план правок к файлу synthetic.md.')]);
  plan.answer();
  assert.deepEqual(plan.injected, []);
  for (const options of [
    {message:{content:[{type:'text',text:narration},{type:'tool-call',name:'edit',arguments:'{}',id:'synthetic-tool'}]}},
    {finish:'max-tokens'},{finish:'error'},{finish:'aborted'},
    {data:{interrupted:true}},{data:{stream:[]}},{data:{stream:undefined}},
    {message:{source:{provider:'other'}}},
  ]) {
    const f = await fixture();
    await f.enter();
    f.answer(narration, options);
    assert.deepEqual(f.injected, [], JSON.stringify(options));
  }
  const foreign = await fixture({provider:'other'});
  await foreign.enter();
  foreign.answer();
  assert.deepEqual(foreign.injected, []);
});

test('idle status and turn end suppress unsettled candidates without publication writes', async () => {
  const idle = await fixture();
  await idle.enter();
  const first = idle.publishAssistant();
  idle.status('idle');
  idle.settle(first);
  assert.deepEqual(idle.mutations, []);
  idle.status('running');
  await idle.enter([], {step:2});
  idle.answer();
  assert.deepEqual(idle.injected, [], 'Idle clears authority even when the same turn resumes');

  const ended = await fixture();
  await ended.enter();
  const second = ended.publishAssistant();
  ended.publish('turn/end', {turn:1});
  ended.settle(second);
  assert.deepEqual(ended.mutations, []);
  await ended.enter([], {turn:2,step:1});
  ended.answer();
  assert.deepEqual(ended.injected, []);
});

test('turn-end publication defers pending reminder removal until idle', async () => {
  const f = await fixture();
  await f.enter();
  f.answer();
  const reminder = f.injected[0];
  f.publish('turn/end', {turn:1});
  assert.deepEqual(f.removed, []);
  assert.deepEqual(f.agent.inbox.nextStep, [reminder]);
  f.status('idle');
  assert.deepEqual(f.removed, [reminder.id]);
  assert.deepEqual(f.agent.inbox.nextStep, []);
});

test('disposal removes only its pending reminder and clears transient authority', async () => {
  const f = await fixture();
  await f.enter();
  f.answer();
  const reminder = f.injected[0], correction = user('Stop');
  f.agent.inbox.nextStep.push(correction);
  f.hooks.get('agent/disposed')({agent:f.agent});
  assert.deepEqual(f.removed, [reminder.id]);
  assert.deepEqual(f.agent.inbox.nextStep, [correction]);
  f.agent.inbox.nextStep.length = 0;
  await f.enter([], {turn:2,step:1});
  f.answer();
  assert.equal(f.injected.length, 1);
});

test('optional message construction or injection failures never break settlement or loop', async () => {
  for (const options of [{createError:true},{injectError:true}]) {
    const f = await fixture(options);
    await f.enter();
    assert.doesNotThrow(() => f.answer());
    await f.enter([], {step:2});
    assert.doesNotThrow(() => f.answer());
    assert.deepEqual(f.injected, []);
    assert.equal(f.mutations.length, options.injectError ? 1 : 0);
    assert.equal(f.made.length, options.injectError ? 1 : 0);
  }
});
