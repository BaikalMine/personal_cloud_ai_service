import assert from 'node:assert/strict';
import test from 'node:test';
import { createImageModelChoice, imageModelChoices } from './image-model-choice.mjs';

const models = [{ name:'Krea2\\cyberrealisticKrea2_v40_3245336.safetensors' }, { name:'Krea2/krea2TurboFP8.safetensors' }];
const exec = () => ({ callId:'model-choice-fixture', agent:{ session:{} }, signal:new AbortController().signal });

test('selection is human-confirmed, reused only for the same task, and can be changed',async()=>{
  const call=exec(), questions=[];
  const choice=createImageModelChoice({ catalog:async()=>({models}),ask:async request=>{
    questions.push(request);
    return {answers:[{id:request.questions[0].id,selected:[request.questions[0].options[1].label]}]};
  } });
  assert.equal(await choice.resolve(call),models[1].name);
  assert.equal(await choice.resolve(call),models[1].name);
  assert.equal(questions.length,1);
  assert.equal(questions[0].agent,call.agent);
  assert.deepEqual(questions[0].wait,{callId:call.callId,timed:false});
  assert.equal(questions[0].questions[0].options.length,2);
  assert(!questions[0].questions[0].options[0].label.includes('3245336'));
  await choice.resolve(call,undefined,true);
  assert.equal(questions.length,2);
  choice.clear(call.agent.session);
  await choice.resolve(call);
  assert.equal(questions.length,3);
});

test('model waiting is observable and always cleared after a failed question',async()=>{
  const call=exec(), waiting=[];
  const choice=createImageModelChoice({catalog:async()=>({models}),onWaiting:(exec,active,id)=>waiting.push({exec,active,id}),ask:async()=>{throw new Error('disconnected');}});
  await assert.rejects(choice.resolve(call),/disconnected/);
  assert.deepEqual(waiting.map(item=>item.active),[true,false]);
  assert(waiting.every(item=>item.exec===call&&item.id===call.callId));
});

test('LLM preferred model does not replace human choice; unavailable cached model is offered again',async()=>{
  const call=exec();let installed=models,asks=0;
  const choice=createImageModelChoice({catalog:async()=>({models:installed}),ask:async request=>{
    asks++;return {answers:[{id:request.questions[0].id,selected:[request.questions[0].options.at(-1).label]}]};
  }});
  assert.equal(await choice.resolve(call,models[1].name),models[0].name);
  installed=[models[1]];
  assert.equal(await choice.resolve(call),models[1].name);
  assert.equal(asks,2);
});

test('skipped, invalid, aborted and late choices never fall back to an automatic model',async()=>{
  const call=exec();
  const skipped=createImageModelChoice({catalog:async()=>({models}),ask:async()=>({answers:[]})});
  assert.equal(await skipped.resolve(call),undefined);
  const invalid=createImageModelChoice({catalog:async()=>({models}),ask:async request=>({answers:[{id:request.questions[0].id,selected:['unknown model']}]})});
  await assert.rejects(invalid.resolve(call),/installed catalog/);
  const controller=new AbortController();controller.abort();
  await assert.rejects(skipped.resolve({...call,signal:controller.signal}));
  let finish;
  const late=createImageModelChoice({catalog:async()=>({models}),ask:request=>new Promise(resolve=>{finish=()=>resolve({answers:[{id:request.questions[0].id,selected:[request.questions[0].options[0].label]}]});})});
  const pending=late.resolve(call);
  await new Promise(resolve=>setImmediate(resolve));
  late.clear(call.agent.session);finish();
  await assert.rejects(pending,/finished task/);
});

test('concurrent image requests share one model question',async()=>{
  const call=exec();let asks=0,finish;
  const choice=createImageModelChoice({catalog:async()=>({models}),ask:request=>new Promise(resolve=>{asks++;finish=()=>resolve({answers:[{id:request.questions[0].id,selected:[request.questions[0].options[0].label]}]});})});
  const a=choice.resolve(call),b=choice.resolve(call);
  await new Promise(resolve=>setImmediate(resolve));finish();
  assert.equal(await a,models[0].name);assert.equal(await b,models[0].name);assert.equal(asks,1);
  assert.throws(()=>imageModelChoices([]));
});

test('skipping one shared question stops every waiting image request',async()=>{
  const call=exec();let asks=0,finish;
  const choice=createImageModelChoice({catalog:async()=>({models}),ask:()=>new Promise(resolve=>{
    asks++;finish=()=>resolve({answers:[]});
  })});
  const a=choice.resolve(call),b=choice.resolve(call);
  await new Promise(resolve=>setImmediate(resolve));finish();
  assert.equal(await a,undefined);assert.equal(await b,undefined);assert.equal(asks,1);
});
