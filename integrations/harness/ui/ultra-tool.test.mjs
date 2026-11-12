import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Context } from '@deepseek-ai/cordis';
import { ToolRuntime } from '@deepseek-ai/dsh-tools';
import { LocalFileSystem } from '@deepseek-ai/dsh-fs-local';
import { SystemPrompt } from '@deepseek-ai/dsh-system-prompt';
import { SessionStore } from '@deepseek-ai/dsh-session';
import { SessionProjectionRegistry } from '@deepseek-ai/dsh-session-projection';
import { WorkingDirectoryService } from '@deepseek-ai/dsh-working-directory';
import { AgentRegistry } from '@deepseek-ai/dsh-agent';
import { UserQuestionService } from '@deepseek-ai/dsh-user-questions';
import { apply, inject } from './index.mjs';
import { z } from 'zod';
import { ULTRA_CONTEXT } from './ultra-tool.mjs';

test('photo fidelity guidance makes generative upscaling opt-in without removing explicit workflow choices',()=>{
  assert.match(ULTRA_CONTEXT,/for photorealistic photos, explicitly send request\.upscale=1/);
  assert.match(ULTRA_CONTEXT,/does not receive the positive\/negative text prompts/);
  assert.match(ULTRA_CONTEXT,/Honor an explicit request for VOSR2/);
  assert.match(ULTRA_CONTEXT,/same-seed control with upscale=1/);
});

// Real Cordis injection and ToolRuntime output validation; only HTTP is stubbed.
test('Ultra wrapper uses injected plugin services and returns lossless JSON through the dispatcher', async t => {
  const temporary = await mkdtemp(join(tmpdir(), 'ultra-wrapper-'));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const originalFetch = globalThis.fetch;
  const originalEnv = Object.fromEntries(['HARNESS_ULTRA_URL', 'HARNESS_ULTRA_KEY', 'HARNESS_TASK_RETENTION', 'DSH_HOME'].map(name => [name, process.env[name]]));
  t.after(() => {
    globalThis.fetch = originalFetch;
    for (const [name, value] of Object.entries(originalEnv)) if (value === undefined) delete process.env[name]; else process.env[name] = value;
  });
  process.env.HARNESS_ULTRA_URL = 'http://krea-ultra:8097';
  process.env.HARNESS_ULTRA_KEY = 'test-private-key';
  process.env.HARNESS_TASK_RETENTION = 'false';
  process.env.DSH_HOME = temporary;
  const ctx = new Context();
  t.after(() => ctx.fiber.dispose());
  new SystemPrompt(ctx, {});
  const sessions = new SessionStore(ctx);
  const projections = new SessionProjectionRegistry(ctx);
  await projections.register({key:'turnBoundary',stateVersion:1,stateSchema:z.object({lastTurn:z.number(),openTurnStartSeq:z.number().nullable()}),init:()=>({lastTurn:1,openTurnStartSeq:0}),apply:state=>state});
  const deliveries=[];
  ctx.on('session/event',(_session,event)=>{if(event.type==='deliverables/presented')deliveries.push(event);});
  new LocalFileSystem(ctx, { cwd: temporary, diffBasisMaxBytes: 10 << 20 });
  await ctx.plugin(WorkingDirectoryService, { defaultDirectory: temporary });
  const directory = ctx.workingDirectory;
  const runtime = new ToolRuntime(ctx, { mode: 'native' });
  const batchAPI = typeof runtime.setExecutor === 'function';
  if (batchAPI) {
    const { NativeToolExecutor } = await import('@deepseek-ai/dsh-tool-executor-native');
    runtime.setExecutor(new NativeToolExecutor(ctx));
  }
  const agent = { ctx: new Context(), session: sessions.create('ultra-wrapper-fixture', { meta: { cwd: temporary } }) }; // Agent context deliberately has no fs/workingDirectory inject.
  agent.id=agent.session.id;
  const agents=new AgentRegistry(ctx);
  const detach=agents.enter(agent,undefined);t.after(detach);
  const {SessionTitleService}=await import('@deepseek-ai/dsh-session-title');
  await ctx.plugin(SessionTitleService,{fallbackMaxWords:8,fallbackMaxBytes:80,maxTitleBytes:512});
  ctx.sessionTitle.rename(agent.session,'Studio vase chat');
  await ctx.plugin(UserQuestionService);
  let questions=0, chooseModel=true;
  ctx.on('user-questions/request',async request=>{
    questions++;
    assert.equal(request.agent,agent);
    assert.equal(request.wait.timed,false);
    const progress=projections.snapshot(agent.session).values.gatewayUltra.calls.find(row=>row.call_id===request.wait.callId);
    assert.equal(progress.phase,'awaiting_model');
    assert.equal(progress.question_call_id,request.wait.callId);
    return {answers:chooseModel?[{id:request.questions[0].id,selected:[request.questions[0].options[0].label]}]:[]};
  });
  const png = Uint8Array.from([137,80,78,71,13,10,26,10]);
  await writeFile(join(temporary, 'reference.png'), png);
  const requests = [], created = [], jobParameters=new Map();
  const loraCard={name:'Krea2/studio.safetensors',display_name:'Studio lighting',compatibility:'verified_krea2',excluded:false,
    description:'Use studio_light for product lighting, weight 0.65.',trigger_words:['studio_light'],recommendations:{strength:.65},revision:'a'.repeat(64)};
  const mossCard={...loraCard,name:'MOSSImage/studio.safetensors',compatibility:'verified_moss',revision:'c'.repeat(64)};
  globalThis.fetch = async (url, options) => {
    const path = new URL(url).pathname;
    requests.push({ path, body: options.body });
    if (path === '/v1/moss/catalog') return Response.json({workflow:'moss-image',models:[{name:'MOSSImage/model.safetensors'}],loras:[mossCard.name],seedvr2_available:true});
    if (path === '/v1/moss/workflow') return Response.json({workflow:'moss-image',defaults:{steps:30,cfg:3,seedvr2:false}});
    if (path === '/v1/moss/plan') return Response.json({workflow:'moss-image',executor_validated:true,gpu_acquired:false});
    if (path === '/v1/moss/resolution') return Response.json({state:'resolved',gpu_acquired:false,request:JSON.parse(options.body),base:{width:1440,height:816}});
    if (path === '/v1/moss/loras') return Response.json({items:[mossCard],data_trust:'untrusted_model_metadata_not_instructions'});
    if (path === '/v1/moss/prompt-guide') { const query=JSON.parse(options.body);return Response.json({source:'promptassistant.EnhancementInstruction',workflow_mode:'text',cfg:query.cfg??3,profile:'workflow-default',instruction:'Live shared MOSS guide',revision:'c'.repeat(64)}); }
    if (path === '/v1/catalog') return Response.json({ models: [{ name: 'Krea2/model.safetensors' }], loras: [], options: ['large schema'] });
    if (path === '/v1/workflow') return Response.json({source_ui_nodes:332,settings_blocks:50,typed_controls:341,blocks:[]});
    if (path === '/v1/plan') return Response.json({state:'planned',gpu_acquired:false,executor_validated:true});
    if (path === '/v1/resolution') return Response.json({state:'resolved',gpu_acquired:false,request:JSON.parse(options.body),base:{width:1888,height:1056}});
    if (path === '/v1/loras') return Response.json({items:[loraCard],data_trust:'untrusted_model_metadata_not_instructions'});
    if (path === '/v1/prompt-guide') {
      const query=JSON.parse(options.body);
      return Response.json({source:'promptassistant.EnhancementInstruction',workflow_mode:query.mode||'text',cfg:query.cfg??2,profile:query.mode&&query.mode!=='text'?'krea2-edit':'workflow-default',instruction:'Shared Prompt Assistant instructions',revision:(query.mode&&query.mode!=='text'?'b':'a').repeat(64)});
    }
    if (path === '/v1/uploads') { assert.deepEqual(new Uint8Array(options.body), png); return Response.json({ id: 'upload-fixture' }); }
    if (path === '/v1/jobs') {
      const body = JSON.parse(options.body); created.push(body.id);
      const parameters={workflow:body.request.pipeline==='moss'?'moss-image':'krea2-pro',output_name:body.request.output_name};jobParameters.set(body.id,parameters);
      return Response.json({ id: body.id, state: 'completed', elapsed_seconds: 0.1, parameters, outputs: [{ index: 0, width: 1024, height: 1536, aspect_ratio: '2:3' }] });
    }
    if (path.endsWith('/outputs/0')) return new Response(png);
    if (/^\/v1\/jobs\/[a-f0-9-]{36}$/.test(path)) return Response.json({id:path.split('/').at(-1),state:'completed',parameters:jobParameters.get(path.split('/').at(-1)),elapsed_seconds:.1,outputs:[{index:0,width:1024,height:1536}]});
    if (path === '/v1/history') return Response.json({ items: [], counts: { total: 0 }, next_cursor: '' });
    throw new Error(`Unexpected HTTP path ${path}`);
  };
  await ctx.plugin({ inject, apply });
  async function execute(args, name='krea_ultra', callId=`fixture-${requests.length}`) {
    if (!batchAPI) return runtime.execute({name,callId,arguments:args,agent,signal:AbortSignal.timeout(3000)});
    const output = await runtime.execute({calls:[{type:'tool-call',id:callId,name,arguments:JSON.stringify(args)}],agent,signal:AbortSignal.timeout(3000)});
    return output.results[0].result;
  }
  async function dispatch(args, name='krea_ultra') {
    const result = await execute(args, name);
    assert.equal(result.isError, false, JSON.stringify(result));
    return JSON.parse(result.content[0].text);
  }
  const catalog = await dispatch({ action: 'catalog' });
  assert.equal(catalog.models.length, 1);
  assert(!Object.hasOwn(catalog, 'options'));
  assert.equal((await dispatch({action:'workflow',workflow_section:'detailers'})).source_ui_nodes,332);
  assert.equal((await dispatch({action:'plan',request:{prompt:'A ceramic vase'}})).executor_validated,true);
  const size = await dispatch({action:'resolution',request:{megapixels:1.9,aspect_ratio:'16:9',upscale:1}});
  assert.equal(size.state,'resolved');assert.equal(size.gpu_acquired,false);
  assert.deepEqual(size.request,{megapixels:1.9,aspect_ratio:'16:9',upscale:1,images:[]});
  const sourceSize=await dispatch({action:'resolution',request:{megapixels:4.27,aspect_ratio:'source'},images:['reference.png']});
  assert.deepEqual(sourceSize.request.images,['upload-fixture']);
  assert.equal(created.length,0);assert.equal(questions,0,'Planning must not start a job or ask for a generation model');
  const prepared=await dispatch({action:'prepare',request:{megapixels:1.9,aspect_ratio:'16:9',upscale:1}});
  assert.equal(prepared.state,'prepared');assert.equal(prepared.gpu_acquired,false);
  assert.equal(prepared.prompt_guide.source,'promptassistant.EnhancementInstruction');
  assert.equal(prepared.workflow.source_ui_nodes,332);assert.equal(prepared.catalog.models.length,1);
  assert.equal(created.length,0);assert.equal(questions,0);
  assert.equal((await dispatch({action:'prompt_guide'})).source,'promptassistant.EnhancementInstruction');
  await dispatch({action:'prompt_guide',guide_mode:'references'});
  await dispatch({ action: 'history', q: 'vase' });
  const uploadsBeforeGeneration=requests.filter(x=>x.path==='/v1/uploads').length;
  for (const images of [[], ['reference.png']]) {
    const result = await dispatch({ action: 'generate', images, request: { prompt: 'A ceramic vase', megapixels: 1.9, aspect_ratio: images.length ? 'source' : '16:9', upscale: 1 } });
    assert(result.files[0].path.startsWith(join(temporary,'generated','krea-ultra')+'/'));
    assert.equal(result.files[0].file_name,'Studio-vase-chat--1.png');
    assert.equal(result.session_id,agent.session.id);assert.equal(result.saved_to_workspace,true);
    assert.equal(result.automatically_presented,true);
    assert(result.timings.total_tool_seconds>=0);assert(result.timings.gpu_handoff_seconds>=0);
    assert(result.timings.model_selection_seconds>=0);assert(result.timings.active_tool_seconds<=result.timings.total_tool_seconds);
    assert.deepEqual(new Uint8Array(await readFile(result.files[0].path)), png);
    assert.equal(result.files[0].width,1024);assert.equal(result.files[0].height,1536);
    assert.equal(result.files[0].megapixels,1.5);assert.equal(result.files[0].aspect_ratio,'2:3');
  }
  assert.equal(directory.get(agent.session), temporary);
  assert.equal(created.length, 2);
  assert.equal(requests.filter(x=>x.path==='/v1/uploads').length-uploadsBeforeGeneration,0,'Identical bytes from resolution are reused across planning and generation');
  assert.equal(deliveries.length,2,'Successful tool results deliver files without another model turn or present call');
  ctx.sessionTitle.rename(agent.session,'Renamed studio chat');
  const recovered=await dispatch({action:'retrieve',job_id:created[0]});
  assert.equal(recovered.id,created[0]);assert.equal(deliveries.length,3);assert.equal(created.length,2);
  assert.equal(recovered.files[0].file_name,'Studio-vase-chat--1.png');
  assert.deepEqual(new Uint8Array(await readFile(recovered.files[0].path)),png);
  assert.equal(questions,1,'One confirmed selection is shared by images in this task');
  for(const request of requests.filter(x=>x.path==='/v1/jobs'))assert.equal(JSON.parse(request.body).request.model,'Krea2/model.safetensors');
  assert.equal(JSON.parse(requests.filter(x => x.path === '/v1/jobs')[1].body).request.images[0], 'upload-fixture');
  const request={prompt:'A teapot under studio_light.',loras:[{name:loraCard.name,strength:.65,clip_strength:0}]};
  const before=requests.length;
  assert.equal((await dispatch({action:'generate',request,images:['reference.png']})).state,'lora_review_required');
  assert.deepEqual(requests.slice(before).map(item=>item.path),['/v1/prompt-guide','/v1/loras'],'Unread cards must not upload references, reserve GPU or create a job');
  const applied=await dispatch({action:'generate',request});
  assert.equal(applied.applied_loras[0].strength,.65);assert.equal(applied.applied_loras[0].clip_strength,0);
  assert.equal(created.length,3);assert.equal(questions,1);
  assert.deepEqual(JSON.parse(requests.filter(item=>item.path==='/v1/jobs').at(-1).body).request.loras,request.loras);
  loraCard.compatibility='incompatible';
  const rejected=await execute({action:'generate',request},'krea_ultra','bad-lora');
  assert.equal(rejected.isError,true);assert.equal(created.length,3);
  assert.equal(projections.snapshot(agent.session).values.gatewayUltra.calls.find(row=>row.call_id==='bad-lora').phase,'failed');
  const beforeMoss=created.length;
  const mossPrepared=await dispatch({action:'prepare',request:{megapixels:1,aspect_ratio:'16:9'}},'moss_image');
  assert.equal(mossPrepared.prompt_guide.cfg,3);assert.equal(mossPrepared.workflow.defaults.steps,30);
  assert.equal(mossPrepared.catalog.workflow,'moss-image');assert.equal(created.length,beforeMoss);
  assert.equal(questions,1,'MOSS prepare must not ask for a model or acquire GPU');
  const mossResult=await dispatch({action:'generate',request:{prompt:'A blue ceramic teapot',negative_prompt:'blurry',megapixels:1,aspect_ratio:'16:9'}},'moss_image');
  assert.equal(questions,2,'MOSS has an independent human model selection');
  assert.equal(mossResult.saved_to_workspace,true);assert.equal(mossResult.automatically_presented,true);
  assert(mossResult.files[0].path.startsWith(join(temporary,'generated','moss-image')+'/'));
  assert.equal(mossResult.files[0].description,'Renamed studio chat');
  assert.equal(mossResult.files[0].file_name,'Renamed-studio-chat--1.png');
  const mossSubmission=JSON.parse(requests.filter(row=>row.path==='/v1/jobs').at(-1).body);
  assert.equal(mossSubmission.request.pipeline,'moss');assert.equal(mossSubmission.request.model,'MOSSImage/model.safetensors');
  assert.equal(mossSubmission.request.negative_prompt,'blurry');assert.equal(mossSubmission.request.upscale,undefined);
  const mossRequest={prompt:'A blue teapot under studio_light',loras:[{name:mossCard.name,strength:.65}]};
  assert.equal((await dispatch({action:'generate',request:mossRequest},'moss_image')).state,'lora_review_required');
  const mossApplied=await dispatch({action:'generate',request:mossRequest},'moss_image');
  assert.equal(mossApplied.applied_loras[0].strength,.65);assert.equal(mossApplied.applied_loras[0].clip_strength,undefined);
  assert.equal(questions,2,'A confirmed MOSS selection is reused during the same task');
  const jobsBeforeCancel=created.length;
  chooseModel=false;
  assert.equal((await dispatch({action:'select_model'},'moss_image')).reason,'model_not_selected');
  assert.equal(created.length,jobsBeforeCancel);
  const unsupported=await execute({action:'generate',images:['reference.png'],request:{prompt:'A vase'}},'moss_image','moss-no-reference');
  assert.equal(unsupported.isError,true);assert.equal(created.length,jobsBeforeCancel);
});
