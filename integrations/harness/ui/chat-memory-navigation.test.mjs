import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';

const source = await readFile(new URL('./chat-memory-client.js', import.meta.url), 'utf8');
const template = await readFile(new URL('./client.template.js', import.meta.url), 'utf8');
const russian = JSON.parse(await readFile(new URL('./ru-chat-memory.json', import.meta.url), 'utf8')).gatewayChatMemory;

export function fixture({ available = true, expanded = true, records = [], revision = '0', visible = true } = {}) {
  const registrations = [], types = [], requests = [], navigation = [], listeners = new Map(), states = new Map();
  const timers = new Map();
  let dictionary, active, cursor, nextId = 0, acceptDiscard = false, responder, now = 0, selection;
  const sources = new Map(), document = { baseURI:'http://synthetic.invalid/', visibilityState:'visible' };
  function eventSource(sessionId) {
    if(!sources.has(sessionId))sources.set(sessionId,{snapshot:{sessionId,entries:[],hasMore:false},listeners:new Set()});
    const state=sources.get(sessionId);
    return state.source??=( {subscribe:fn=>{state.listeners.add(fn);return()=>state.listeners.delete(fn);},getSnapshot:()=>state.snapshot} );
  }
  const hook = init => {
    const index = cursor++;
    return active[index] ??= init();
  };
  const memo = (factory, deps) => {
    const cell = hook(() => ({}));
    if (!cell.deps || deps.some((value, index) => !Object.is(value, cell.deps[index]))) {
      cell.value = factory(); cell.deps = deps;
    }
    return cell.value;
  };
  const React = {
    Fragment: Symbol('Fragment'),
    createElement: (type, props, ...children) => ({ type, props: { ...props, children } }),
    useState(initial) {
      const cell = hook(() => ({ value: typeof initial === 'function' ? initial() : initial }));
      return [cell.value, value => { cell.value = typeof value === 'function' ? value(cell.value) : value; }];
    },
    useRef: value => hook(() => ({ current: value })),
    useMemo: memo, useCallback: (callback, deps) => memo(() => callback, deps),
    useId: () => memo(() => `synthetic-${++nextId}`, []),
    useSyncExternalStore: (_subscribe, snapshot) => snapshot(),
    useEffect(setup, deps) {
      const cell = hook(() => ({}));
      if (!cell.deps || deps.some((value, index) => !Object.is(value, cell.deps[index]))) {
        cell.pending = setup; cell.deps = deps;
      }
    },
  };
  const ctx = {
    effect: setup => setup(),
    locale: { register: (_name, value) => { dictionary = value.en; }, bind: () => key => dictionary[key],
      subscribe: () => () => {}, getSnapshot: () => ({ active: 'en' }) },
    slots: {
      inject: (_slot, setup) => setup(), register: (options, component) => { registrations.push({ options, component }); return () => {}; },
      entriesOfSlot: name => { assert.equal(name, 'main'); return available ? [{ options: { key: 'mnemon' } }] : []; },
      subscribe: () => () => {},
    },
    sidebarRightTabs: { register: value => { types.push(value); return () => {}; } },
    sidebarRight: { toggleExpanded: () => navigation.push('collapse') },
    layout: { selectPanel: panel => navigation.push(panel) },
    sessions: {
      binding: sessionId => ({ eventSource:eventSource(sessionId) }),
      scope: sessionId => ({ get: () => ({ input: { for: () => ({ focus: () => navigation.push(`focus:${sessionId}`) }) } }) }),
    },
  };
  const MarkdownText = () => null;
  const primitives = new Proxy({ MarkdownText }, { get: (object, name) => object[name] ?? (() => null) });
  const sandbox = {
    React, TextEncoder, AbortController, URL, document,
    crypto: { randomUUID: () => `synthetic-request-${++nextId}` },
    chatMemorySummary: window => ({ state: 'ready', text: `**Synthetic summary ${window.sessionId}**` }),
    addEventListener: (name, listener) => listeners.set(name, listener), removeEventListener: name => listeners.delete(name),
    confirm: () => acceptDiscard,
    getSelection:()=>selection,
    setTimeout: (callback, delay=0) => { const id = ++nextId; timers.set(id, {callback,due:now+delay,delay}); return id; }, clearTimeout: id => timers.delete(id),
    fetch: async (url, options) => {
      assert.equal(url.href, 'http://synthetic.invalid/api/gateway-chat-memory/manage');
      assert.equal(options.credentials, 'same-origin');
      const body = JSON.parse(options.body); requests.push(body);
      if(responder)return responder(body,options);
      const cursor=body.cursor||0;
      const value = body.action === 'list' ? { records:records.slice(cursor,cursor+50), nextCursor:records.length>cursor+50?cursor+50:null, revision }
        : body.action === 'revision' ? {revision}
        : body.action === 'delete' ? { deleted: true } : { record: { id: body.id || 'synthetic-added', text: body.text } };
      return { ok: true, json: async () => ({ ok: true, value }) };
    },
  };
  vm.runInNewContext(source + '\nregisterChatMemoryUI(ctx, primitives);', { ...sandbox, ctx, primitives });
  const tab = registrations[0];
  function render(sessionId = 'chat-a') {
    const element = tab.component({ sessionId, useTabInfo: () => ({ sidebar: { expanded },tab:{visible} }) });
    active = states.get(sessionId) || [];
    states.set(sessionId, active); cursor = 0;
    const tree = element.type(element.props);
    for (const cell of active) if (cell.pending) {
      cell.cleanup?.(); const setup = cell.pending; cell.pending = undefined; cell.cleanup = setup();
    }
    return tree;
  }
  function unmount(sessionId) {
    for (const cell of states.get(sessionId) || []) cell.cleanup?.();
    states.delete(sessionId);
  }
  return { ctx, types, registrations, requests, navigation, listeners, MarkdownText, render, unmount, timers, document,
    async advance(ms=0){const end=now+ms;for(let guard=0;guard<100;guard++){const next=[...timers].filter(([,timer])=>timer.due<=end).sort((a,b)=>a[1].due-b[1].due)[0];if(!next){now=end;return;}now=next[1].due;timers.delete(next[0]);next[1].callback();await tick();}throw Error('Synthetic timer loop');},
    respond:fn=>{responder=fn;}, visible:value=>{visible=value;}, select:value=>{selection=value;}, data:(value,nextRevision)=>{records=value;revision=nextRevision;},
    emit(sessionId,event){eventSource(sessionId);const state=sources.get(sessionId);state.snapshot={...state.snapshot,entries:[...state.snapshot.entries,{type:'event',event}]};for(const listener of state.listeners)listener();},
    available: value => { available = value; }, acceptDiscard: value => { acceptDiscard = value; } };
}

export function nodes(tree, predicate) {
  if (Array.isArray(tree)) return tree.flatMap(value => nodes(value, predicate));
  if (!tree || typeof tree !== 'object') return [];
  return [...predicate(tree) ? [tree] : [], ...nodes(tree.props?.children, predicate)];
}
export function text(tree) {
  if (Array.isArray(tree)) return tree.map(text).join('');
  return tree && typeof tree === 'object' ? text(tree.props?.children) : typeof tree === 'string' ? tree : '';
}
export const button = (tree, label) => {
  const found = nodes(tree, node => node.type === 'button' && (node.props['aria-label'] === label || text(node) === label));
  assert.equal(found.length, 1, `Expected one ${label} button`);
  return found[0];
};
export const tick = () => new Promise(resolve => setImmediate(resolve));

test('one native guide card/body replaces the header launcher and conversation memory entry', () => {
  const view = fixture();
  assert.equal(view.types.length, 1);
  const type = view.types[0];
  assert.equal(type.id, 'gateway.chat-memory');
  assert.equal(type.kind, 'gateway-memory');
  assert.equal(type.keepMounted, true);
  assert.equal(type.guide.length, 1);
  assert.equal(type.guide[0].order, 15);
  assert.equal(type.guide[0].title(), 'Memory');
  assert.equal(type.guide[0].description(), 'This chat and Mnemon');
  assert.equal(typeof type.guide[0].icon, 'function');
  assert.equal(view.registrations.length, 1);
  assert.equal(view.registrations[0].options.name, 'sidebar.right.pane.tab');
  assert.equal(view.registrations[0].options.key, type.id);
  assert.doesNotMatch(source, /MemoryLauncher|gateway-memory-launcher|conversation\.session\.header\.actions|name:'conversation\.view'/);
});

test('required sidebar registry, navigation and layout services are injected in both contexts', () => {
  const callback = template.match(/ctx\.inject\((\[[^\]]+\]), scope => \{\s*registerChatMemoryUI/);
  const root = template.match(/return \{ inject: (\[[^\]]+\]), apply \}/);
  assert.ok(callback && root);
  for (const declaration of [callback[1], root[1]]) for (const service of ['sidebarRightTabs', 'sidebarRight', 'layout']) {
    assert.ok(vm.runInNewContext(declaration).includes(service), service);
  }
});

test('facts default, Summary and official Mnemon panel navigation stay separate and session-scoped', async () => {
  const view = fixture();
  let tree = view.render('chat-a'); await tick(); tree = view.render('chat-a');
  assert.equal(button(tree, 'Chat facts').props['aria-selected'], true);
  assert.deepEqual(view.requests.map(request => request.sessionId), ['chat-a']);
  button(tree, 'Summary').props.onClick(); tree = view.render('chat-a');
  const summary = nodes(tree, node => node.type === view.MarkdownText);
  assert.equal(summary[0].props.text, '**Synthetic summary chat-a**');
  button(tree, 'Mnemon').props.onClick(); tree = view.render('chat-a');
  assert.match(text(tree), /Profile and project memory/);
  assert.match(text(tree), /separate from this chat/);
  assert.equal(button(tree, 'Open Mnemon').props.disabled, false);
  button(tree, 'Open Mnemon').props.onClick();
  assert.deepEqual(view.navigation, ['mnemon']);
  assert.equal(view.requests.length, 1, 'Opening Mnemon must not call chat CRUD or a private Mnemon API');
});

test('Mnemon availability follows the native main-slot ledger and is checked again on activation', () => {
  const view = fixture({ available: false });
  let tree = view.render(); button(tree, 'Mnemon').props.onClick(); tree = view.render();
  assert.equal(button(tree, 'Open Mnemon').props.disabled, true);
  assert.match(text(tree), /Mnemon is not available/);
  view.available(true); tree = view.render();
  const open = button(tree, 'Open Mnemon');
  assert.equal(open.props.disabled, false);
  view.available(false); open.props.onClick();
  assert.deepEqual(view.navigation, []);
});

test('unsaved drafts survive mode switches, native tab remounts and other sessions', async () => {
  const view = fixture();
  let tree = view.render(); await tick(); tree = view.render();
  button(tree, 'Add entry').props.onClick(); tree = view.render();
  nodes(tree, node => node.type === 'textarea')[0].props.onChange({ target: { value: '**Synthetic draft A**' } });
  tree = view.render();
  button(tree, 'Summary').props.onClick(); tree = view.render();
  button(tree, 'Mnemon').props.onClick(); tree = view.render();
  button(tree, 'Open Mnemon').props.onClick();
  assert.deepEqual(view.navigation, ['mnemon']);
  view.navigation.length = 0;
  button(tree, 'Chat facts').props.onClick(); tree = view.render();
  assert.equal(nodes(tree, node => node.type === 'textarea')[0].props.value, '**Synthetic draft A**');
  view.unmount('chat-a'); tree = view.render('chat-b'); await tick(); tree = view.render('chat-b');
  assert.equal(nodes(tree, node => node.type === 'textarea').length, 0);
  tree = view.render('chat-a'); await tick(); tree = view.render('chat-a');
  assert.equal(nodes(tree, node => node.type === 'textarea')[0].props.value, '**Synthetic draft A**');
  let prevented = false;
  const event = { preventDefault: () => { prevented = true; } };
  view.listeners.get('beforeunload')(event);
  assert.equal(prevented, true);
  button(tree, 'Back to chat').props.onClick();
  assert.deepEqual(view.navigation, [], 'Declining discard must stay in the owning chat');
  view.acceptDiscard(true); button(tree, 'Back to chat').props.onClick();
  assert.deepEqual(view.navigation, [null, 'collapse', 'focus:chat-a']);
  assert.equal(nodes(view.render(), node => node.type === 'textarea').length, 0);
});

test('Back to chat never toggles a collapsed sidebar open', () => {
  const view = fixture({ expanded: false });
  button(view.render('chat-b'), 'Back to chat').props.onClick();
  assert.deepEqual(view.navigation, [null, 'focus:chat-b']);
});

test('existing Markdown rendering and optimistic update payload preserve the owning session and original text', async () => {
  const original = '**Synthetic fact**  with spaces\n';
  const view = fixture({ records: [{ id: 'synthetic-record', text: original }] });
  let tree = view.render('chat-b'); await tick(); tree = view.render('chat-b');
  assert.equal(nodes(tree, node => node.type === view.MarkdownText)[0].props.text, original);
  button(tree, 'Edit entry').props.onClick(); tree = view.render('chat-b');
  nodes(tree, node => node.type === 'textarea')[0].props.onChange({ target: { value: '**Synthetic updated**\n' } });
  tree = view.render('chat-b');
  nodes(tree, node => node.type === 'form')[0].props.onSubmit({ preventDefault() {} });
  await tick();
  assert.deepEqual(view.requests.find(request => request.action === 'update'), {
    action: 'update', sessionId: 'chat-b', id: 'synthetic-record', text: '**Synthetic updated**\n', expectedText: original,
  });
});

test('new navigation/scope copy is translated without changing chat CRUD or Mnemon storage', () => {
  for (const key of ['guideDescription', 'chatScope', 'mnemonTab', 'mnemonScope', 'mnemonOpen', 'mnemonUnavailable']) {
    assert.equal(typeof russian[key], 'string', key);
    assert.ok(russian[key].length > 0, key);
  }
  assert.match(source, /requestId:draft\.requestId/);
  assert.match(source, /token !== generation\.current/);
  assert.match(source, /readToken===readGeneration\.current/);
  assert.doesNotMatch(source, /fetch\([^\n]*(?:mnemon|\/api\/sessions)/);
});
