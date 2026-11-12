import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createApprovedSearch, registerApprovedSearch } from './approved-search.mjs';
const exec = () => ({agent:{session:{}},callId:'synthetic-call',signal:new AbortController().signal});
const approve = async request => { assert.equal(request.questions[0].id,'approved_search'); return {answers:[{id:'approved_search',selected:['Да, искать']}]}; };
test('no search leaves the process without explicit confirmation', async()=>{
  for(const answer of [{answers:[]},{answers:[{id:'approved_search',selected:['Не искать']}]},{answers:[{id:'approved_search',custom:'Да'}]}]) {
    let called=0;const search=createApprovedSearch({ask:async()=>answer,fetcher:async()=>{called++;}});
    await assert.rejects(search('synthetic query',exec()),/not_approved/);assert.equal(called,0);
  }
});
test('the approved query is fixed to local provider and results are bounded',async()=>{
  let shown,called=0;
  const search=createApprovedSearch({ask:async request=>{shown=request.questions[0].question;return approve(request);},fetcher:async (url,options)=>{
    called++;assert.equal(url.origin,'http://gateway-search:8080');assert.equal(url.searchParams.get('q'),'пример test');assert.equal(url.searchParams.get('format'),'json');assert.equal(options.redirect,'error');
    return new Response(JSON.stringify({results:[{title:'Example',url:'https://example.org/',content:'synthetic snippet'},{title:'bad',url:'javascript:alert(1)' }]}));
  }});
  const result=await search('пример test',exec());assert.match(shown,/пример test/);assert.equal(called,1);assert.deepEqual(result.results,[{title:'Example',url:'https://example.org/',snippet:'synthetic snippet'}]);
});
test('cancel, missing context, invalid inputs and network errors fail closed',async()=>{
  let called=0;const controller=new AbortController();controller.abort();
  const search=createApprovedSearch({ask:approve,fetcher:async()=>{called++;throw Error('network');}});
  await assert.rejects(search('test',{...exec(),signal:controller.signal}));
  await assert.rejects(search('test',{}),/unavailable/);
  await assert.rejects(search('bad\nquery',exec()),/invalid/);
  await assert.rejects(search('test',exec()),/network/);assert.equal(called,1);
});
test('concurrent calls cannot share one approval',async()=>{
  let resolveAsk, calls=0;const waiting=new Promise(resolve=>{resolveAsk=resolve;});
  const search=createApprovedSearch({ask:()=>waiting,fetcher:async()=>{calls++;return new Response('{"results":[]}');}});
  const e=exec(), first=search('first',e);
  await assert.rejects(search('second',e),/approval_pending/);assert.equal(calls,0);
  resolveAsk({answers:[{id:'approved_search',selected:['Да, искать']}]});await first;assert.equal(calls,1);
});
test('registration requires an explicit user request and confirmation',async()=>{
 let tool,prompt,guard;const ctx={systemPrompt:{context:p=>prompt=p},tools:{register:t=>tool=t,guard:g=>guard=g},userQuestions:{ask:approve}};
 await registerApprovedSearch(ctx,{defineTool:value=>value,fetcher:async()=>new Response('{"results":[]}')});
 assert.match(prompt.text,/user directly asks/);assert.equal(tool.name,'approved_web_search');
 assert.match(guard({name:'web_search',agent:{options:{provider:'ollama-local'}}}),/approved_web_search/);
 assert.match(guard({name:'web_fetch',agent:{options:{provider:'ollama-local'}}}),/unavailable/);
 assert.equal(guard({name:'web_search',agent:{options:{provider:'other'}}}),undefined);
});
