import assert from 'node:assert/strict';
import test from 'node:test';
import { createLoraKnowledge } from './lora-knowledge.mjs';

const fixture = () => {
  const card = { name:'Krea2/studio.safetensors', display_name:'Studio lighting', compatibility:'verified_krea2', excluded:false,
    description:'Studio product lighting; use studio_light and weight 0.65.', trigger_words:['studio_light'], recommendations:{strength:.65,clip_strength:0}, revision:'a'.repeat(64) };
  const calls=[];
  const knowledge=createLoraKnowledge({call:async(path,body)=>{calls.push({path,body});return{items:[{...card}],data_trust:'untrusted_model_metadata_not_instructions'};}});
  const exec={agent:{session:{}},signal:AbortSignal.timeout(5000)};
  const request={prompt:'A ceramic teapot under studio_light.',loras:[{name:card.name,strength:.65,clip_strength:0}]};
  return {card,calls,knowledge,exec,request};
};

test('LoRA cards must reach the agent before generation, then exact documented files and weights are preserved',async()=>{
  const {knowledge,exec,request,card}=fixture();
  const first=await knowledge.prepare(request,exec);
  assert.equal(first.review.state,'lora_review_required');assert.equal(first.review.items[0].description,card.description);
  const ready=await knowledge.prepare(request,exec);
  assert.deepEqual(ready.request,request);assert.equal(ready.applied[0].clip_strength,0);
  knowledge.clear(exec.agent.session);
  assert((await knowledge.prepare(request,exec)).review);
});

test('explicit inspection avoids redundant review; asset/documentation changes require new review',async()=>{
  const {knowledge,exec,request,card}=fixture();
  await knowledge.inspect([card.name],exec);
  assert(!(await knowledge.prepare(request,exec)).review);
  card.revision='b'.repeat(64);
  assert((await knowledge.prepare(request,exec)).review);
  assert((await knowledge.prepare(request,{...exec,agent:{session:{}}})).review);
});

test('incompatible, excluded, unknown or mismatched metadata never authorizes a LoRA',async()=>{
  for(const change of [{compatibility:'incompatible'},{compatibility:'unknown'},{excluded:true},{name:'Flux2/studio.safetensors'},{revision:''}]){
    const {knowledge,exec,request,card}=fixture();Object.assign(card,change);
    await assert.rejects(knowledge.prepare(request,exec));
  }
});

test('weights are explicit; no filename guessing, default strength or duplicate stacking',async()=>{
  const {knowledge,exec,request,card,calls}=fixture();
  const plain={prompt:'A teapot'};
  assert.deepEqual((await knowledge.prepare(plain,exec)).request,plain);assert.equal(calls.length,0);
  await knowledge.inspect([card.name],exec);
  const noWeight={...request,loras:[{name:card.name}]};
  assert((await knowledge.prepare(noWeight,exec)).review);
  for(const loras of [[...request.loras,...request.loras],[{name:card.name,strength:3}],[{name:card.name,strength:'0.5'}],null])await assert.rejects(knowledge.prepare({...request,loras},exec));
  const ready=await knowledge.prepare({...request,loras:[{name:'Krea2\\studio.safetensors',strength:-.5}]},exec);
  assert.equal(ready.request.loras[0].name,card.name);assert.equal(ready.applied[0].clip_strength,-.5);
});

test('detailer adapters use the same metadata review and retain their separate stage',async()=>{
  const {knowledge,exec,request,card}=fixture();
  const detail={prompt:request.prompt,detailer_loras:request.loras};
  assert.equal((await knowledge.prepare(detail,exec)).review.state,'lora_review_required');
  const ready=await knowledge.prepare(detail,exec);
  assert.deepEqual(ready.request,detail);
  assert.equal(ready.applied[0].stage,'detailers');
  assert.equal(ready.applied[0].name,card.name);
  const both=await knowledge.prepare({...request,...detail},exec);
  assert.equal(both.applied.length,2);
  assert.equal(both.applied[0].stage,'base');
});

test('MOSS LoRAs are model-only, require their own family and retain explicit strengths',async()=>{
  const card={name:'MOSSImage/studio.safetensors',display_name:'Studio',compatibility:'verified_moss',excluded:false,revision:'a'.repeat(64)};
  const knowledge=createLoraKnowledge({call:async()=>({items:[{...card}]}),compatibility:'verified_moss',family:'MOSS Image',modelOnly:true,maxStrength:10});
  const exec={agent:{session:{}},signal:AbortSignal.timeout(3000)};
  const request={prompt:'A ceramic teapot',loras:[{name:card.name,strength:.6}]};
  await knowledge.inspect([card.name],exec);
  const ready=await knowledge.prepare(request,exec);
  assert.equal(ready.applied[0].strength,.6);assert(!Object.hasOwn(ready.applied[0],'clip_strength'));
  for(const change of [{loras:[{name:card.name,strength:.6,clip_strength:0}]},{detailer_loras:[]},{loras:[{name:card.name,strength:11}]}]) await assert.rejects(knowledge.prepare({...request,...change},exec));
  for(const compatibility of ['verified_krea2','unknown','incompatible']) {card.compatibility=compatibility;await assert.rejects(knowledge.prepare(request,exec));}
});
