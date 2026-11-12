import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import { fixture, nodes, button, tick } from './chat-memory-navigation.test.mjs';

const source = await readFile(new URL('./chat-memory-client.js', import.meta.url), 'utf8');
const helpers = vm.runInNewContext(source + '\n({createChatMemoryLive,chatMemoryReadPages,chatMemoryToolSignal,chatMemoryScrollAnchor,chatMemoryRestoreScroll,chatMemoryValidRevision})', { AbortController, setTimeout, clearTimeout });
const records = count => Array.from({ length:count }, (_, index) => ({ id:`synthetic-${index}`, text:`Synthetic fact ${index}` }));
const texts = (view, tree) => nodes(tree, node => node.type === view.MarkdownText).map(node => node.props.text);
const response = value => ({ ok:true, json:async () => ({ ok:true, value }) });
async function open(options) { const view = fixture(options); view.render(); await tick(); view.render(); await view.advance(); return view; }
function clock() {
  const tasks = new Map(); let id = 0;
  return { tasks, schedule:(callback, delay) => { tasks.set(++id, { callback, delay }); return id; }, unschedule:id => tasks.delete(id),
    async fire() { assert.equal(tasks.size, 1); const [id, task] = [...tasks][0]; tasks.delete(id); task.callback(); await tick(); } };
}

test('revision strings remain opaque above safe integer range and across restore', async () => {
  assert.equal(helpers.chatMemoryValidRevision('9007199254740993'), true);
  for (const invalid of [0, '', '01', '-1', '1.1']) assert.equal(helpers.chatMemoryValidRevision(invalid), false);
  const view = await open({ records:records(1), revision:'9007199254740993' });
  view.data([{ id:'synthetic-restored', text:'Synthetic restored fact' }], '0');
  await view.advance(3000);
  assert.deepEqual(texts(view, view.render()), ['Synthetic restored fact']);
  view.unmount('chat-a');
});

test('unchanged revisions only use cheap checks; tool notifications coalesce without token polling', async () => {
  const view = await open({ records:records(2) });
  const lists = view.requests.filter(body => body.action === 'list').length;
  await view.advance(9000);
  assert.equal(view.requests.filter(body => body.action === 'list').length, lists);
  const before = view.requests.length;
  for (let seq=1; seq<20; seq++) view.emit('chat-a', { type:'assistant/message', seq, data:{ text:'Synthetic token' } });
  await view.advance(); assert.equal(view.requests.length, before);
  view.emit('chat-a', { type:'tool/call', seq:20, data:{ private:'not inspected' } });
  view.emit('chat-a', { type:'tool/result', seq:21, data:{ private:'not inspected' } });
  await view.advance(); assert.equal(view.requests.length, before+1);
  assert.deepEqual(view.requests.at(-1), { action:'revision', sessionId:'chat-a' });
  view.unmount('chat-a');
});

test('partial loaded depth survives revision changes, pagination and explicit refresh', async () => {
  const view = await open({ records:records(121) });
  let tree=view.render(); button(tree,'Load more').props.onClick(); await tick(); tree=view.render();
  assert.equal(texts(view,tree).length,100);
  view.data(records(123).map(record => ({...record,text:`${record.text} changed`})), '1');
  await view.advance(3000); tree=view.render();
  assert.equal(texts(view,tree).length,100);
  assert.match(texts(view,tree)[99],/changed/);
  button(tree,'Refresh').props.onClick(); await tick(); tree=view.render();
  assert.equal(texts(view,tree).length,100);
  button(tree,'Load more').props.onClick(); await tick(); tree=view.render();
  assert.equal(texts(view,tree).length,123);
  view.data(records(151), '2'); await view.advance(3000); tree=view.render();
  assert.equal(texts(view,tree).length,151,'Fully loaded lists follow new tail pages');
  view.unmount('chat-a');
});

test('a fully loaded 50-record list follows a new 51st record', async () => {
  const view = await open({ records:records(50) });
  view.data(records(51),'1'); await view.advance(3000);
  assert.equal(texts(view,view.render()).length,51);
  assert.equal(view.requests.at(-1).cursor,50);
  view.unmount('chat-a');
});

test('mid-page changes discard the entire snapshot and retry later with a bounded attempt', async () => {
  const view = await open({ records:records(121) });
  button(view.render(),'Load more').props.onClick(); await tick(); view.render();
  const changed=records(121).map(record=>({...record,text:`${record.text} new`}));
  view.respond(body => response(body.action==='revision'?{revision:'1'}:{records:changed.slice(body.cursor,body.cursor+50),nextCursor:body.cursor<100?body.cursor+50:null,revision:body.cursor===0?'1':'2'}));
  const before=view.requests.length;
  await view.advance(3000);
  assert.equal(view.requests.length-before,3,'One revision check and one two-page attempt');
  assert.equal(texts(view,view.render())[0],'Synthetic fact 0');
  view.respond(undefined); view.data(changed,'2');
  await view.advance(6000);
  assert.equal(texts(view,view.render())[0],'Synthetic fact 0 new');
  view.unmount('chat-a');
});

test('revision changes during manual pagination replace rather than mix old/new pages', async () => {
  const view=await open({records:records(121)});
  view.data(records(121).map(record=>({...record,text:`${record.text} new`})),'1');
  button(view.render(),'Load more').props.onClick(); await tick();
  const displayed=texts(view,view.render());
  assert.equal(displayed.length,100); assert.ok(displayed.every(text=>text.endsWith(' new')));
  view.unmount('chat-a');
});

test('late background pages cannot replace newer manual pagination', async () => {
  const view=await open({records:records(121)});
  let release, heldSignal;
  view.respond((body,options)=>body.action==='revision'?response({revision:'1'}):new Promise(resolve=>{release=()=>resolve(response({records:[{id:'synthetic-stale',text:'Synthetic stale response'}],nextCursor:null,revision:'1'}));heldSignal=options.signal;}));
  await view.advance(3000); assert.ok(release);
  view.respond(undefined); view.data(records(121),'2');
  button(view.render(),'Load more').props.onClick(); await tick();
  assert.equal(heldSignal.aborted,true);
  release(); await tick();
  const displayed=texts(view,view.render());
  assert.equal(displayed.length,100); assert.ok(!displayed.includes('Synthetic stale response'));
  view.unmount('chat-a');
});

test('background list changes leave the editor draft and original CAS baseline intact', async () => {
  const view=await open({records:records(1)});
  let tree=view.render();button(tree,'Edit entry').props.onClick();tree=view.render();
  nodes(tree,node=>node.type==='textarea')[0].props.onChange({target:{value:'Synthetic unsaved draft'}});view.render();
  view.data([{id:'synthetic-0',text:'Synthetic model update'}],'1');await view.advance(3000);tree=view.render();
  assert.equal(nodes(tree,node=>node.type==='textarea')[0].props.value,'Synthetic unsaved draft');
  assert.deepEqual(texts(view,tree),['Synthetic model update']);
  nodes(tree,node=>node.type==='form')[0].props.onSubmit({preventDefault(){}});await tick();
  assert.equal(view.requests.find(body=>body.action==='update').expectedText,'Synthetic fact 0');
  view.unmount('chat-a');
});

test('selection deferral does not advance applied revision and selectionchange reconciles immediately', async () => {
  const view=await open({records:records(1)});
  const pane={contains:()=>true,getBoundingClientRect:()=>({top:0}),querySelectorAll:()=>[],scrollTop:30,scrollLeft:0};
  nodes(view.render(),node=>node.props?.className==='gateway-chat-memory-inner')[0].props.ref.current=pane;
  view.select({isCollapsed:false,rangeCount:1,anchorNode:{},focusNode:{}});
  view.data([{id:'synthetic-0',text:'Synthetic selected update'}],'1');
  const before=view.requests.filter(body=>body.action==='list').length;
  await view.advance(3000);
  assert.equal(view.requests.filter(body=>body.action==='list').length,before);
  assert.deepEqual(texts(view,view.render()),['Synthetic fact 0']);
  view.select(null);view.listeners.get('selectionchange')();await view.advance();
  assert.deepEqual(texts(view,view.render()),['Synthetic selected update']);
  view.unmount('chat-a');
});

test('ordinary caret/selection events do not trigger revision requests',async()=>{
  const view=await open({records:records(1)}),before=view.requests.length;
  for(let index=0;index<20;index++)view.listeners.get('selectionchange')();
  await view.advance();assert.equal(view.requests.length,before);view.unmount('chat-a');
});

test('selection beginning during a held snapshot defers commit until cleared',async()=>{
  const view=await open({records:records(1)});let release;
  const pane={contains:()=>true,getBoundingClientRect:()=>({top:0}),querySelectorAll:()=>[],scrollTop:0,scrollLeft:0};
  nodes(view.render(),node=>node.props?.className==='gateway-chat-memory-inner')[0].props.ref.current=pane;
  view.respond(body=>body.action==='revision'?response({revision:'1'}):new Promise(resolve=>{release=()=>resolve(response({records:[{id:'synthetic-0',text:'Synthetic held update'}],nextCursor:null,revision:'1'}));}));
  await view.advance(3000);view.select({isCollapsed:false,rangeCount:1,anchorNode:{},focusNode:{}});release();await tick();
  assert.deepEqual(texts(view,view.render()),['Synthetic fact 0']);
  view.respond(undefined);view.data([{id:'synthetic-0',text:'Synthetic held update'}],'1');view.select(null);view.listeners.get('selectionchange')();await view.advance();
  assert.deepEqual(texts(view,view.render()),['Synthetic held update']);view.unmount('chat-a');
});

test('suspending an in-flight multi-page read prevents further pages even if fetch ignores abort',async()=>{
  const view=await open({records:records(121)});button(view.render(),'Load more').props.onClick();await tick();view.render();
  let release;
  view.respond(body=>body.action==='revision'?response({revision:'1'}):new Promise(resolve=>{release=()=>resolve(response({records:records(50),nextCursor:50,revision:'1'}));}));
  await view.advance(3000);const count=view.requests.length;view.visible(false);view.render();release();await tick();await view.advance(30000);
  assert.equal(view.requests.length,count);assert.equal(texts(view,view.render()).length,100);view.unmount('chat-a');
});

test('native hidden tabs, non-fact modes, document hiding and page departure suspend all reads', async () => {
  const view=await open({records:records(1)});
  async function paused(){const count=view.requests.length;await view.advance(30000);assert.equal(view.requests.length,count);}
  view.visible(false);view.render();await paused();
  view.data(records(2),'1');view.visible(true);view.render();await view.advance();assert.equal(texts(view,view.render()).length,2);
  button(view.render(),'Summary').props.onClick();view.render();await paused();
  button(view.render(),'Mnemon').props.onClick();view.render();await paused();
  button(view.render(),'Chat facts').props.onClick();view.render();await view.advance();
  view.document.visibilityState='hidden';view.listeners.get('visibilitychange')();await paused();
  view.document.visibilityState='visible';view.listeners.get('visibilitychange')();await view.advance();
  view.listeners.get('pagehide')();view.render();await paused();
  view.listeners.get('pageshow')();await view.advance();
  view.unmount('chat-a');const count=view.requests.length;await view.advance(30000);assert.equal(view.requests.length,count);
  for(const name of ['focus','visibilitychange','selectionchange','pagehide','pageshow'])assert.equal(view.listeners.has(name),false,name);
});

test('an initially hidden keep-mounted tab does not fetch until opened', async () => {
  const view=fixture({visible:false});view.render();await view.advance(30000);assert.equal(view.requests.length,0);
  view.visible(true);view.render();await tick();view.render();assert.equal(view.requests.length,1);
  view.unmount('chat-a');
});

for(const status of [401,403])test(`HTTP ${status} stops polling until explicit focus and preserves entries`,async()=>{
  const view=await open({records:records(1)});
  view.respond(()=>({ok:false,status,json:async()=>({ok:false})}));
  await view.advance(3000);const count=view.requests.length;await view.advance(60000);assert.equal(view.requests.length,count);
  view.emit('chat-a',{type:'tool/result',seq:1});await view.advance();assert.equal(view.requests.length,count);
  assert.deepEqual(texts(view,view.render()),['Synthetic fact 0']);
  view.respond(undefined);view.data(records(2),'1');view.listeners.get('focus')();await view.advance();
  assert.equal(texts(view,view.render()).length,2);view.unmount('chat-a');
});

test('initial authorization denial does not create a retry loop', async()=>{
  const view=fixture();view.respond(()=>({ok:false,status:401,json:async()=>({ok:false})}));
  view.render();await tick();view.render();await view.advance(60000);assert.equal(view.requests.length,1);
  view.respond(undefined);view.listeners.get('focus')();await view.advance();assert.equal(view.requests.length,2);
  view.unmount('chat-a');
});

test('scheduler coalesces, backs off failures and aborts on suspension/disposal',async()=>{
  const timer=clock();let calls=0,release,signal;
  const live=helpers.createChatMemoryLive({...timer,blocked:()=>false,refresh:async value=>{signal=value;calls++;await new Promise(resolve=>{release=resolve;});}});
  live.setActive(true);live.wake();live.wake();await timer.fire();assert.equal(calls,1);
  live.wake();live.wake();assert.equal(timer.tasks.size,0);release();await tick();
  await timer.fire();assert.equal(calls,2);live.setActive(false);assert.equal(signal.aborted,true);release();await tick();assert.equal(timer.tasks.size,0);
  live.dispose();live.setActive(true);assert.equal(timer.tasks.size,0);
  let fail=true;
  const backoff=helpers.createChatMemoryLive({...timer,blocked:()=>false,refresh:async()=>{if(fail)throw Error('Synthetic offline');}});
  backoff.setActive(true);await timer.fire();assert.equal([...timer.tasks.values()][0].delay,6000);
  backoff.wake();assert.equal([...timer.tasks.values()][0].delay,6000);
  await timer.fire();assert.equal([...timer.tasks.values()][0].delay,12000);
  fail=false;backoff.wake(true);await timer.fire();assert.equal([...timer.tasks.values()][0].delay,3000);backoff.dispose();
});

test('page collection has a finite budget and never publishes mixed revisions',async()=>{
  let calls=0;
  await assert.rejects(helpers.chatMemoryReadPages(async cursor=>{calls++;return {records:[],nextCursor:cursor+50,revision:'1'};},1,'1',true),/budget/);
  assert.equal(calls,200);
  await assert.rejects(helpers.chatMemoryReadPages(async cursor=>({records:records(1),nextCursor:cursor===0?50:null,revision:cursor===0?'1':'2'}),2),/revision changed/);
});

test('scroll anchor follows a retained row and falls back to absolute offsets if all rows disappear',()=>{
  let items,scroll=80;
  const row=(id,position)=>({getAttribute:()=>id,getBoundingClientRect:()=>({top:position-scroll,bottom:position-scroll+40})});
  items=[row('synthetic-a',60),row('synthetic-b',100),row('synthetic-c',140)];
  const pane={get scrollTop(){return scroll;},set scrollTop(value){scroll=value;},scrollLeft:7,getBoundingClientRect:()=>({top:0}),querySelectorAll:()=>items};
  const anchor=helpers.chatMemoryScrollAnchor(pane);
  items=[row('synthetic-a',110),row('synthetic-b',150),row('synthetic-c',190)];helpers.chatMemoryRestoreScroll(anchor);assert.equal(scroll,130);assert.equal(pane.scrollLeft,7);
  const second=helpers.chatMemoryScrollAnchor(pane);items=[row('synthetic-c',90)];helpers.chatMemoryRestoreScroll(second);assert.equal(scroll,30);
  const last=helpers.chatMemoryScrollAnchor(pane);items=[];scroll=0;helpers.chatMemoryRestoreScroll(last);assert.equal(scroll,30);
});
