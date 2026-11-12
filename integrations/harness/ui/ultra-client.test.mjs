import assert from 'node:assert/strict';
import test from 'node:test';
import { createUltraClient } from './ultra-client.mjs';
const id = '10000000-0000-4000-8000-000000000001';
const reply = value => ({ ok: true, json: async () => value });

test('definitive input rejection returns immediately without a phantom job cleanup', async () => {
  const paths = [];
  const client = createUltraClient({ endpoint: 'http://krea-ultra:8097/', key: 'test', sleep: () => assert.fail('unexpected wait'),
    fetcher: async url => { paths.push(url.pathname); return { ok: false, status: 400, text: async () => 'invalid image size' }; } });
  await assert.rejects(client.generate({ prompt: 'test' }, [], { id, save: () => {} }), /invalid image size/);
  assert.deepEqual(paths, ['/v1/jobs']);
});

test('unknown creation outcome is cancelled and reconciled, not replayed', async () => {
  const paths = [];
  const client = createUltraClient({ endpoint: 'http://krea-ultra:8097/', key: 'test', sleep: async () => {}, fetcher: async url => {
    paths.push(url.pathname);
    if (url.pathname === '/v1/jobs') throw new Error('network lost');
    if (url.pathname.endsWith('/cancel')) return reply({});
    return reply({ state: 'cancelled' });
  } });
  await assert.rejects(client.generate({ prompt: 'test' }, [], { id, save: () => {} }), /network lost/);
  assert.deepEqual(paths, ['/v1/jobs', `/v1/jobs/${id}/cancel`, `/v1/jobs/${id}`]);
});

test('user cancellation returns without waiting for background GPU reconciliation', async () => {
  const paths=[], controller=new AbortController();
  const client=createUltraClient({endpoint:'http://krea-ultra:8097/',key:'test',sleep:async()=>assert.fail('must not wait after cancellation'),fetcher:async url=>{
    paths.push(url.pathname);
    if(url.pathname==='/v1/jobs')return reply({state:'running'});
    if(url.pathname.endsWith('/cancel'))return reply({state:'finalizing'});
    throw new Error('unexpected poll');
  }});
  await assert.rejects(client.generate({},[],{id,signal:controller.signal,save:()=>{},notify:()=>controller.abort()}));
  assert.deepEqual(paths,['/v1/jobs',`/v1/jobs/${id}/cancel`]);
});

test('a confirmed generation failure is reported without waiting for GPU reconciliation', async () => {
  const paths=[];
  const client=createUltraClient({endpoint:'http://krea-ultra:8097/',key:'test',sleep:async()=>assert.fail('unexpected wait'),fetcher:async url=>{
    paths.push(url.pathname);return reply({state:'finalizing_failed',error:'synthetic executor failure'});
  }});
  await assert.rejects(client.generate({},[],{id,save:()=>{}}),/synthetic executor failure.*GPU release/);
  assert.deepEqual(paths,['/v1/jobs']);
});

test('a completed job downloads its output once into the workspace', async () => {
  const client = createUltraClient({ endpoint: 'http://krea-ultra:8097/', key: 'test', fetcher: async url =>
    url.pathname.includes('/outputs/') ? { ok: true, arrayBuffer: async () => new Uint8Array([1, 2, 3]).buffer } :
      reply({ state: 'completed', elapsed_seconds: 10, outputs: [{ index: 0, width: 512, height: 528, megapixels: .257812, aspect_ratio: '32:33' }] }) });
  const result = await client.generate({ prompt: 'test' }, [], { id, save: async (job, index, data) => {
    assert.equal(job, id); assert.equal(index, 0); assert.equal(data.length, 3); return { path: '/workspace/result.png' };
  } });
  assert.equal(result.files[0].path, '/workspace/result.png');
  assert.deepEqual(result.files[0], { path: '/workspace/result.png', width: 512, height: 528, megapixels: .257812, aspect_ratio: '32:33' });
});

test('the endpoint cannot be redirected to a host or public service', () => {
  for (const endpoint of ['http://host.docker.internal:8097/', 'https://example.com/', 'http://krea-ultra:8097/?key=x'])
    assert.throws(() => createUltraClient({ endpoint, key: 'test' }), /private Ultra/);
});

test('retrieve passes the immutable job title to the file writer',async()=>{
  const client=createUltraClient({endpoint:'http://krea-ultra:8097/',key:'test',fetcher:async url=>url.pathname.includes('/outputs/')?{ok:true,arrayBuffer:async()=>new Uint8Array([1]).buffer}:reply({id,state:'completed',parameters:{workflow:'krea2-pro',output_name:'blue-vase'},outputs:[{index:0}]})});
  const result=await client.retrieve(id,{save:async(job,index,bytes,workflow,title)=>{
    assert.equal(job,id);assert.equal(index,0);assert.equal(workflow,'krea2-pro');assert.equal(title,'blue-vase');return{path:'/workspace/blue-vase--1.png'};
  }});
  assert.equal(result.files[0].path,'/workspace/blue-vase--1.png');
});
