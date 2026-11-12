// Synthetic only. Never point this test at a running Harness service.
// HARNESS_CONTAINER=harness-memory-navigation-test node --test integrations/harness/memory-navigation-ui.test.cjs
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { mkdirSync } = require('node:fs');
const { join } = require('node:path');
const { chromium, expect } = require('@playwright/test');

const assets = '/opt/harness/node_modules/@deepseek-ai/dsh-web-frontend/dist/assets';
const schemes = process.env.TEST_COLOR_SCHEME ? [process.env.TEST_COLOR_SCHEME] : ['light', 'dark'];
assert.ok(schemes.every(value => ['light', 'dark'].includes(value)));
let browser, files;

function expose(source, assignment) {
  const marker = '\t\treturn module.exports;';
  assert.equal(source.split(marker).length, 2, 'Unsupported installed module export boundary');
  return source.replace(marker, `\t\t${assignment}\n${marker}`);
}

before(async () => {
  const container = process.env.HARNESS_CONTAINER;
  assert.ok(container, 'HARNESS_CONTAINER must explicitly name a synthetic candidate-image hold container');
  const info = JSON.parse(execFileSync('docker', ['inspect', '--format',
    '{"network":{{json .HostConfig.NetworkMode}},"readonly":{{json .HostConfig.ReadonlyRootfs}},"mounts":{{json .Mounts}},"path":{{json .Path}},"args":{{json .Args}}}', container], { encoding: 'utf8' }));
  assert.equal(info.network, 'none');
  assert.equal(info.readonly, true, 'The fixture must read an immutable candidate-image hold');
  assert.deepEqual(info.mounts, [], 'The fixture must have no mounts, including user data');
  assert.match(info.path, /^(?:\/[^\s]+\/)?sleep$/);
  assert.equal(info.args.length, 1, 'Hold container must run only sleep');
  assert.match(info.args[0], /^(?:infinity|[1-9]\d*)$/);
  const read = file => execFileSync('docker', ['exec', container, 'cat', file], { maxBuffer: 32 * 1024 * 1024 });
  const names = execFileSync('docker', ['exec', container, 'ls', assets], { encoding: 'utf8' }).split(/\r?\n/);
  const find = pattern => {
    const matches = names.filter(name => pattern.test(name));
    assert.equal(matches.length, 1, `Expected one immutable asset matching ${pattern}`);
    return matches[0];
  };
  const index = find(/^index-.*\.js$/), vendor = find(/^vendor-.*\.js$/);
  const indexCss = find(/^index-.*\.css$/), vendorCss = find(/^vendor-.*\.css$/);
  let frontend = read(`${assets}/${index}`).toString('utf8');
  const boot = [...frontend.matchAll(/const [\w$]+=globalThis\.dshDesktopBoot/g)];
  const maps = [...frontend.matchAll(/function ([\w$]+)\(\)\{return\{react:[^}]+"@deepseek-ai\/dsh-client-ui-primitives":[^}]+\}\}/g)];
  assert.equal(boot.length, 1, 'Unsupported frontend bootstrap');
  assert.equal(maps.length, 1, 'Unsupported frontend builtin map');
  frontend = frontend.slice(0, boot[0].index) + `\nwindow.__fixtureModules = ${maps[0][1]}();\n`;
  const installed = name => read(`/opt/harness/node_modules/@deepseek-ai/${name}/lib/client.js`).toString('utf8');
  let gateway = read('/opt/harness/node_modules/@ai-gateway/harness-ui/client.js').toString('utf8');
  const gatewayBoundary = '    function apply(ctx) {';
  assert.equal(gateway.split(gatewayBoundary).length, 2, 'Unsupported installed gateway plugin boundary');
  assert.match(gateway, /guide:\[\{id:'memory',order:15,/,
    'The image must contain the built memory-navigation plugin; older word-diff-only images are unsupported');
  gateway = gateway.replace(gatewayBoundary,
    '    window.__gatewayFixture = { registerChatMemoryUI, registerWorkspaceFilesUI, chatMemorySummary, css, dictionaries };\n' + gatewayBoundary);
  files = {
    index, vendor, indexCss, vendorCss, frontend, read, extra: new Map(),
    vendorJS: read(`${assets}/${vendor}`), indexCSS: read(`${assets}/${indexCss}`), vendorCSS: read(`${assets}/${vendorCss}`),
    theme: expose(installed('dsh-client-ui-theme'), 'window.__themeFixture = { installThemeStyles, installIconSprite };'),
    sidebar: expose(installed('dsh-client-ui-sidebar-right'), 'window.__sidebarFixture = { GuideBody, SidebarRightTabRegistry };'),
    gateway,
  };
  browser = await chromium.launch({ headless: true, ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
    ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH } : {}) });
});

after(async () => browser?.close());

async function openFixture(width, colorScheme, { recordCount = 1, clock = false, sources = false, fileTransfer = false, extraChats = false, nodeGraph = false, memoryReview = false, reviewAdvisory = false } = {}) {
  const page = await browser.newPage({ viewport: { width, height: 900 }, colorScheme });
  if (clock) {
    await page.clock.install({ time: new Date('2026-01-01T00:00:00Z') });
    await page.clock.pauseAt(new Date('2026-01-01T00:00:01Z'));
  }
  const errors = [], blocked = [], requests = [];
  const records = new Map([
    ['synthetic-a', [{ id: 'seed-a', text: '## Synthetic project\n\n- **Keep decisions**\n- Use `tests`\n\n[Reference](https://example.invalid/synthetic)', createdAt: '2026-01-01T00:00:00Z' }]],
    ['synthetic-b', [{ id: 'seed-b', text: 'Synthetic B only' }]],
  ]);
  for (let index = 1; index < recordCount; index++) records.get('synthetic-a').push({
    id: `synthetic-page-${index}`, text: `Synthetic paged fact ${String(index).padStart(3, '0')}`,
  });
  if(sources)records.get('synthetic-a')[0].sources=[{file:'drafts/chapter-04.md',chapter:'Chapter 4',line_from:112,line_to:118,anchor:'An entirely synthetic source excerpt.',sha256:'a'.repeat(64)}];
  const revisions = new Map([...records.keys()].map(id => [id, 9007199254740993n]));
  const edges = new Map([...records.keys()].map(id => [id, []]));
  if(nodeGraph){
    const rows=records.get('synthetic-a');
    rows[0].text='The expedition reaches the northern gate.';
    rows[1].text='The gate can only be opened at dawn.';rows[1].status='critical';
    rows[2].text='A map marks a path around the gate.';rows[2].status='important';
    rows[3].text='The bridge floods after heavy rain.';
    rows[4].text='Mira carries the spare lantern.';
    for(const [index,[from,to,relation]] of [[0,1,'depends_on'],[0,2,'related'],[2,3,'supports'],[0,4,'related']].entries())
      edges.get('synthetic-a').push({id:'demo-edge-'+index,fromId:rows[from].id,toId:rows[to].id,relation});
  }
  const bump = id => revisions.set(id, revisions.get(id) + 1n);
  let reviewState;
  let nextId = 0, conflict = false, failList = false;
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => {
    window.__copiedText = [];
    Object.defineProperty(navigator, 'clipboard', { configurable: true,
      value: { writeText: async text => { window.__copiedText.push(text); } } });
    const nativeFetch = globalThis.fetch.bind(globalThis);
    globalThis.fetch = async (...args) => {
      const response = await nativeFetch(...args), gate = window.__memoryReadGate;
      const body = typeof args[1]?.body === 'string' ? JSON.parse(args[1].body) : {};
      if (gate && !gate.claimed && body.sessionId === gate.sessionId && body.action === gate.action) {
        gate.claimed = true;
        const json = response.json.bind(response);
        response.json = async () => {
          const value = await json();
          // Decoding completed before the session switch: abort alone cannot reject this stale result.
          await new Promise(resolve => { gate.release = resolve; gate.received = true; });
          return value;
        };
      }
      return response;
    };
  });
  await page.route('**/*', async route => {
    const url = new URL(route.request().url());
    if (url.origin !== 'http://fixture.invalid') { blocked.push(url.href); return route.abort(); }
    if (url.pathname === '/api/session/list') {
      const envelope=route.request().postDataJSON();
      assert.equal(envelope.method,'session/list');
      return route.fulfill({json:{result:{ok:true,value:{items:[
        {sessionId:'synthetic-a',projections:{values:{title:'Current synthetic chat'}}},
        {sessionId:'synthetic-b',projections:{values:{title:'Other synthetic chat'}}},
        ...(extraChats?Array.from({length:8},(_,index)=>({sessionId:'extra-'+index,projections:{values:{title:'Archived chat '+index}}})):[]),
      ]}}}});
    }
    if (fileTransfer && url.pathname === '/api/gateway-files/manage') {
      const type=fileTransfer==='directory'?'directory':'file', name=type==='directory'?'synthetic-folder':'synthetic.txt';
      const body=route.request().postDataJSON();
      requests.push(body);
      assert.equal(body.sessionId,'synthetic-a');
      assert.equal(body.path,'/workspace/'+name);
      if (body.action==='inspect') return route.fulfill({json:{ok:true,value:{type,name,version:'test-v1'}}});
      assert.equal(body.action,'copyToChat');
      assert.equal(body.version,'test-v1');
      if (body.targetFolder==='collision') return route.fulfill({json:{ok:false,error:{code:'exists'}}});
      return route.fulfill({json:{ok:true,value:{type,name,path:'/workspace/archive/'+name}}});
    }
    if (url.pathname === '/api/gateway-memory-review'&&memoryReview) {
      const body=route.request().postDataJSON();
      requests.push(body);
      assert.equal(body.sessionId,'synthetic-a');
      let value;
      if(body.action==='scan'||body.action==='scanPage'){
        const rows=records.get(body.sessionId);
        const starting=body.action==='scan';
        if(starting)assert.equal(body.semantic,true);
        else assert.equal(body.scanId,'synthetic-scan');
        value={id:'synthetic-scan',kind:'merge',revision:revisions.get(body.sessionId).toString(),
          scanned:rows.length,total:rows.length,excluded:0,groupCount:starting?0:1,nextPage:null,verified:0,
          status:starting?'running':'done',analyzed:starting?0:rows.length,
          groups:starting?[]:[{ids:[rows[0].id,rows.at(-1).id],groupIndex:0,hasProposal:true,reason:'semantic',label:'Синтетические записи для проверки',note:'Оба факта об одном событии.'}]};
      }else if(body.action==='scanPreview'){
        assert.equal(body.scanId,'synthetic-scan');
        assert.equal(body.groupIndex,0);
        const rows=records.get(body.sessionId);
        value={state:'unverified',reason:'Оба факта об одном событии.',suggestedText:'Сохранённый текст модели.',facts:[rows[0],rows.at(-1)],evidence:[]};
      }else if(body.action==='preview'){
        assert.equal(body.confirm,undefined);
        const selected=records.get(body.sessionId).filter(row=>body.ids.includes(row.id));
        assert.equal(selected.length,2);
        value=reviewAdvisory?{state:'unverified',reason:'Эти факты можно объединить после проверки.',suggestedText:'Предлагаемый факт.',facts:selected,evidence:[]}:
          {id:'synthetic-draft',state:'ready',before:{facts:selected,edges:[]},after:{facts:[{...selected[0],text:'Объединённая запись с деталями обоих фактов.'}],edges:[]},
          evidence:[{file:'drafts/chapter.md',excerpt:'Проверенный фрагмент.',lineFrom:6,lineTo:8,sha256:'a'.repeat(64)}],delta:-240,reserve:240};
      }else if(body.action==='apply'){
        assert.equal(body.confirm,true);assert.equal(body.draftId,'synthetic-draft');reviewState='applied';value={state:reviewState};
      }else if(body.action==='undo'){
        assert.equal(body.confirm,true);assert.equal(body.operationId,'synthetic-draft');reviewState='undone';value={state:reviewState};
      }else{
        assert.equal(body.action,'history');value=reviewState?[{id:'synthetic-draft',state:reviewState,created:Date.now(),expires:Date.now()+86400000}]:[];
      }
      return route.fulfill({json:{ok:true,value}});
    }
    if (url.pathname === '/api/gateway-chat-memory/manage') {
      const body = route.request().postDataJSON();
      requests.push(body);
      assert.equal(route.request().method(), 'POST');
      assert.ok(records.has(body.sessionId), 'Only synthetic session IDs are allowed');
      const rows = records.get(body.sessionId);
      if (body.action === 'revision') {
        assert.deepEqual(Object.keys(body).sort(), ['action', 'sessionId']);
      }
      if (body.action === 'list' && failList) return route.fulfill({ status: 503, json: { ok: false } });
      if (body.action === 'update' && conflict) return route.fulfill({ status: 409, json: { ok: false, error: { code: 'conflict' } } });
      let value;
      if (body.action === 'revision') value = { revision: revisions.get(body.sessionId).toString() };
      else if (body.action === 'usage') {
        const chat={facts:rows.length,links:edges.get(body.sessionId).length,bytes:rows.length*320};
        const facts=[...records.values()].reduce((n,items)=>n+items.length,0);
        const links=[...edges.values()].reduce((n,items)=>n+items.length,0);
        const limitBytes=50*1024*1024, profile={facts,links,bytes:facts*320};
        value={chat,profile,limitBytes,remainingBytes:limitBytes-profile.bytes};
      }
      else if(body.action==='graphOverview'){
        const revision=revisions.get(body.sessionId).toString(),list=edges.get(body.sessionId);
        if(body.expectedRevision!==undefined&&body.expectedRevision!==revision)return route.fulfill({json:{ok:false,error:{code:'conflict'}}});
        const n=body.page*60,e=body.page*120;
        value={revision,totals:{nodes:rows.length,edges:list.length},nextPage:n+60<rows.length||e+120<list.length?body.page+1:null,
          nodes:rows.slice(n,n+60).map(row=>({id:row.id,preview:row.text,status:row.status??'normal'})),edges:list.slice(e,e+120)};
      }
      else if (body.action === 'transferPreview' || body.action === 'transfer') {
        const [source,target]=body.direction==='import'?[body.otherSessionId,body.sessionId]:[body.sessionId,body.otherSessionId];
        assert.ok(source!==target&&records.has(source)&&records.has(target));
        const sourceRevision=revisions.get(source).toString(), targetRevision=revisions.get(target).toString();
        const missing=records.get(source).filter(row=>!records.get(target).some(existing=>existing.id===row.id));
        value={sourceRevision,targetRevision,facts:{added:missing.length,skipped:records.get(source).length-missing.length},links:{added:0,skipped:0},fits:true};
        if(body.action==='transfer'){
          if(body.sourceRevision!==sourceRevision||body.targetRevision!==targetRevision)return route.fulfill({json:{ok:false,error:{code:'conflict'}}});
          if(missing.length){records.get(target).push(...structuredClone(missing));bump(target);}
          value.targetRevision=revisions.get(target).toString();
        }
      }
      else if(body.action==='get'||body.action==='graph') {
        const revision=revisions.get(body.sessionId).toString();
        if(body.expectedRevision!==undefined&&body.expectedRevision!==revision)return route.fulfill({json:{ok:false,error:{code:'conflict'}}});
        const record=body.id?rows.find(row=>row.id===body.id):rows.at(-1);
        if(body.action==='get')value={record:record??null};
        else {
          const incident=edges.get(body.sessionId).filter(edge=>edge.fromId===record?.id||edge.toId===record?.id),cursor=body.cursor??0,limit=body.limit??20;
          const page=incident.slice(cursor,cursor+limit),ids=new Set(record?[record.id]:[]);
          for(const edge of page){ids.add(edge.fromId);ids.add(edge.toId);}
          value={nodeId:record?.id??null,nodes:rows.filter(row=>ids.has(row.id)).map(row=>({id:row.id,preview:[...row.text].slice(0,240).join(''),truncated:[...row.text].length>240})),edges:page,total:incident.length,nextCursor:cursor+limit<incident.length?cursor+limit:null,revision};
        }
      }
      else if(body.action==='link') {
        assert.ok(rows.some(row=>row.id===body.fromId)&&rows.some(row=>row.id===body.toId));
        const edge={id:`synthetic-edge-${++nextId}`,fromId:body.fromId,toId:body.toId,relation:body.relation,note:body.note??'',createdAt:'2026-01-01T00:00:00Z'};
        edges.get(body.sessionId).push(edge);bump(body.sessionId);value={edge};
      }
      else if(body.action==='unlink') {
        const list=edges.get(body.sessionId),index=list.findIndex(edge=>edge.id===body.id);
        if(index>=0){list.splice(index,1);bump(body.sessionId);}value={unlinked:index>=0};
      }
      else if (body.action === 'list') {
        const cursor = body.cursor ?? 0, limit = body.limit ?? 50;
        assert.ok(Number.isSafeInteger(cursor) && cursor >= 0);
        assert.ok(Number.isSafeInteger(limit) && limit > 0 && limit <= 50);
        if (body.includeRevision !== undefined) assert.equal(typeof body.includeRevision, 'boolean');
        const matches = rows.filter(row => row.text.toLowerCase().includes((body.query || '').toLowerCase()));
        value = { records: matches.slice(cursor, cursor + limit), nextCursor: cursor + limit < matches.length ? cursor + limit : null,
          ...(body.includeRevision === true ? { revision: revisions.get(body.sessionId).toString() } : {}) };
      }
      else if (body.action === 'add') {
        const record = { id: `synthetic-added-${++nextId}`, text: body.text };
        rows.push(record); bump(body.sessionId); value = { record };
      } else if (body.action === 'setStatus') {
        const record=rows.find(row=>row.id===body.id);
        assert.ok(record);
        if((record.status??'normal')!==body.expectedStatus)return route.fulfill({json:{ok:false,error:{code:'conflict'}}});
        if(body.status==='normal')delete record.status;else record.status=body.status;
        bump(body.sessionId);value={record};
      } else if (body.action === 'moveFact') {
        if(revisions.get(body.sessionId).toString()!==body.expectedRevision)return route.fulfill({json:{ok:false,error:{code:'conflict'}}});
        const index=rows.findIndex(row=>row.id===body.id), target=index+(body.direction==='up'?-1:1);
        assert.ok(index>=0&&target>=0&&target<rows.length);
        [rows[index],rows[target]]=[rows[target],rows[index]];
        bump(body.sessionId);value={moved:true};
      } else if (body.action === 'update') {
        const record = rows.find(row => row.id === body.id);
        assert.ok(record);
        if (body.expectedText !== record.text) return route.fulfill({ status: 409, json: { ok: false, error: { code: 'conflict' } } });
        record.text = body.text; bump(body.sessionId); value = { record };
      } else if (body.action === 'delete') {
        const index = rows.findIndex(row => row.id === body.id);
        assert.ok(index >= 0); rows.splice(index, 1); bump(body.sessionId); value = { deleted: true };
      } else throw new Error(`Unexpected synthetic action: ${body.action}`);
      const response = JSON.stringify({ ok: true, value });
      try { return await route.fulfill({ contentType: 'application/json', body: response }); }
      catch (error) { if (!page.isClosed() && !/closed|handled|cancel|abort/i.test(error.message)) throw error; }
    }
    if (url.pathname === '/') return route.fulfill({ contentType: 'text/html', body:
      `<html><head><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/assets/${files.vendorCss}"><link rel="stylesheet" href="/assets/${files.indexCss}"></head><body><div id="root"></div><script type="module" src="/assets/${files.index}"></script></body></html>` });
    if (/^\/assets\/(?:langs|fonts)\/[\w.-]+\.(?:js|woff2?)$/.test(url.pathname)) {
      if (!files.extra.has(url.pathname)) files.extra.set(url.pathname, files.read(assets + url.pathname.slice('/assets'.length)));
      return route.fulfill({ body: files.extra.get(url.pathname), contentType: url.pathname.endsWith('.js') ? 'text/javascript' : 'font/woff2' });
    }
    const name = url.pathname.split('/').pop();
    const body = name === files.index ? files.frontend : name === files.vendor ? files.vendorJS
      : name === files.indexCss ? files.indexCSS : name === files.vendorCss ? files.vendorCSS : null;
    if (body === null) { blocked.push(url.href); return route.abort(); }
    return route.fulfill({ body, contentType: name.endsWith('.css') ? 'text/css' : 'text/javascript' });
  });
  try {
    await page.goto('http://fixture.invalid/');
    await page.waitForFunction(() => window.__fixtureModules);
    await page.evaluate(({ theme, sidebar, gateway, colorScheme, fileTransfer }) => {
      const modules = window.__fixtureModules, React = modules.react, h = React.createElement;
      const { createRoot } = modules['react-dom/client'];
      document.body.toggleAttribute('data-ds-dark-theme', colorScheme === 'dark');
      window.__ModuleLoader__ = { load: ({ factory }) => factory(name => {
        if (!modules[name]) throw new Error(`Missing installed builtin: ${name}`);
        return modules[name];
      }) };
      (0, eval)(theme); (0, eval)(sidebar); (0, eval)(gateway);
      const disposers = [], effect = setup => { const dispose = setup(); if (typeof dispose === 'function') disposers.push(dispose); return dispose || (() => {}); };
      window.__themeFixture.installThemeStyles({ effect, on() {} });
      window.__themeFixture.installIconSprite({ effect, on() {} });
      const style = document.createElement('style');
      style.textContent = window.__gatewayFixture.css + '\nhtml,body,#root{margin:0;width:100%;height:100%;min-width:0}body{font-size:14px;font-family:var(--dsw-font-family,system-ui,sans-serif);background:var(--dsw-alias-bg-base);color:var(--dsw-alias-label-primary)}button,input,textarea{font:inherit}[data-fixture-shell]{height:100dvh;display:flex;flex-direction:column;min-width:0}[data-fixture-header]{height:44px;flex:none;display:flex;justify-content:flex-end}[data-fixture-pane]{flex:1;min-height:0;min-width:0}[data-fixture-chat]{padding:16px}';
      document.head.appendChild(style);
      const listeners = new Set(), dictionaries = {}, slots = [], calls = [];
      let main = [{ options: { key: 'mnemon' } }], view = 'guide', memoryOpened = false, sessionId = 'synthetic-a', activeLocale = 'en', redraw;
      let graphBinding;
      const emit = () => { for (const listener of listeners) listener(); };
      const eventWindow = { hasMore: false, entries: [
        { type: 'event', event: { type: 'compaction/summary', seq: 1, data: { compactionId: 'synthetic-compaction', summary: [{ type: 'text', text: '## Synthetic summary\n\n**Summary Markdown** and `code`.' }] } } },
        { type: 'event', event: { type: 'user/message', seq: 2, time: 1767225600000, surfaceOp: 'replace', data: { source: { kind: 'compact-checkpoint', compactionId: 'synthetic-compaction' } } } },
      ] };
      const eventWindows = new Map(['synthetic-a', 'synthetic-b'].map(id => [id, { ...eventWindow, entries: [...eventWindow.entries] }]));
      const eventListeners = new Map(['synthetic-a', 'synthetic-b'].map(id => [id, new Set()]));
      const bindings = new Map([...eventWindows.keys()].map(id => [id, {
        eventSource: { subscribe: listener => { eventListeners.get(id).add(listener); return () => eventListeners.get(id).delete(listener); },
          getSnapshot: () => eventWindows.get(id) },
        session: { loadOlder: async () => {}, getSnapshot: () => ({}) },
      }]));
      const ctx = {
        effect,
        locale: { register: (name, values) => { dictionaries[name] = values; },
          bind: name => (key, values = {}) => Object.entries(values).reduce((text, [key, value]) => text.replace(`{${key}}`, value),
            (activeLocale === 'ru' ? window.__gatewayFixture.dictionaries[name]?.[key] : undefined) || dictionaries[name]?.en?.[key] || key),
          subscribe: listener => { listeners.add(listener); return () => listeners.delete(listener); }, getSnapshot: () => ({ active: activeLocale }) },
        slots: { inject: (name, setup) => setup(), register: (options, component) => { slots.push({ options, component }); return () => {}; },
          entriesOfSlot: name => name === 'main' ? main : slots.filter(slot => slot.options.name === name),
          subscribe: (name, listener) => { listeners.add(listener); return () => listeners.delete(listener); } },
        sessions: { binding: id => { if (!bindings.has(id)) throw Error('Non-synthetic binding'); return bindings.get(id);
        }, scope: () => ({ get: () => ({ input: { for: () => ({ focus: () => document.querySelector('[data-fixture-composer]').focus() }) } }) }) },
        layout: { selectPanel: id => { calls.push(['selectPanel', id]); if (id === 'mnemon' || id === 'gateway.memory-nodes'||id==='gateway.memory-review') { graphBinding=undefined;view = id; redraw(); }
          else if(id===null&&['gateway.memory-nodes','gateway.memory-review'].includes(view)){view='chat';redraw();} } },
        uiSession:{current:{subscribe:fn=>{listeners.add(fn);return()=>listeners.delete(fn);},getSnapshot:()=>({key:view==='gateway.memory-nodes'?graphBinding:sessionId})}},
        sidebarRight: { toggleExpanded: () => { calls.push(['toggleExpanded']); view = 'chat'; memoryOpened = false; modules['react-dom'].flushSync(redraw); } },
      };
      const registry = new window.__sidebarFixture.SidebarRightTabRegistry(ctx);
      ctx.sidebarRightTabs = registry;
      // Use only the installed build: memory, summary, CSS and RU copy share its closure.
      window.__gatewayFixture.registerChatMemoryUI(ctx, modules['@deepseek-ai/dsh-client-ui-primitives']);
      if (fileTransfer) {
        ctx.uiSession={adapter:{current:{subscribe:()=>()=>{},getSnapshot:()=>({key:sessionId})}}};
        window.__gatewayFixture.registerWorkspaceFilesUI(ctx, modules['@deepseek-ai/dsh-client-ui-primitives']);
      }
      const fileRow=slots.find(slot=>slot.options.id==='gateway.file-row-actions');
      const fileDialog=slots.find(slot=>slot.options.id==='gateway.file-dialog');
      const type = registry.get('gateway-memory');
      if (type?.id !== 'gateway.chat-memory') throw Error('Memory tab type was not registered');
      const pane = slots.find(slot => slot.options.name === 'sidebar.right.pane.tab' && slot.options.key === type.id);
      const nodePanel = slots.find(slot => slot.options.name === 'main' && slot.options.key === 'gateway.memory-nodes');
      const reviewPanel=slots.find(slot=>slot.options.name==='main'&&slot.options.key==='gateway.memory-review');
      if (!pane) throw Error('Registered memory pane was not found');
      if (slots.some(slot => /header|corner/.test(slot.options.name))) throw Error('Memory must not register a header launcher');
      const useTabInfo = () => ({ sidebar: { expanded: view !== 'chat' }, tab: { visible: view === 'memory', actions: {
        openTab: (kind, options) => { if (!registry.get(kind)) throw Error('Unknown tab type'); calls.push(['openTab', kind, options]); view = 'memory'; memoryOpened = true; redraw(); },
        close: () => calls.push(['closeGuide']),
      } } });
      function Fixture() {
        const [, setRevision] = React.useState(0); redraw = () => setRevision(value => value + 1);
        return h('div', { 'data-fixture-shell': true },
          h('header', { 'data-fixture-header': true }, h('button', { 'aria-label': 'Open right sidebar', onClick: () => { view = 'guide'; redraw(); } }, 'Sidebar')),
          h('main', { 'data-fixture-pane': true },
            view === 'guide' ? h(window.__sidebarFixture.GuideBody, {
              useTabInfo, useGuideEntries: select => select(React.useSyncExternalStore(fn => registry.subscribe(fn), () => registry.guide())),
              useShortcuts: select => select([]), useEntryPins: select => select([]), useEntryActions: select => select(false), setPinned() {},
              renderSlot: (name, props, options) => options.fallback, renderSlotChain: (name, props, options) => options.fallback, t: key => key,
            }) : view === 'mnemon' ? h('p', { 'data-fixture-mnemon': true }, 'Synthetic Mnemon panel') :
              view === 'gateway.memory-nodes' ? h(nodePanel.component) :
              view === 'gateway.memory-review' ? h(reviewPanel.component) : null,
            memoryOpened && h('div', { hidden: view !== 'memory', style: { height: '100%' } }, h(pane.component, { sessionId, useTabInfo }))),
          h('div', { 'data-fixture-chat': true, hidden: view !== 'chat' }, h('textarea', { 'aria-label': 'Synthetic chat composer', 'data-fixture-composer': true })),
          fileRow&&h(fileRow.component,{sessionId,absolutePath:fileTransfer==='directory'?'/workspace/synthetic-folder':'/workspace/synthetic.txt',
            entryType:fileTransfer==='directory'?'directory':'file'}),
          fileDialog&&h(fileDialog.component));
      }
      const previewDisposers = [];
      window.__memoryFixture = { calls, registry: () => ({ id: type.id, kind: type.kind, keepMounted: type.keepMounted, guide: registry.guide().map(entry => ({ id: entry.id, kind: entry.kind, providerId: entry.providerId, order: entry.order })) }),
        emitSessionEvent: (id, event) => {
          if (!eventWindows.has(id)) throw Error('Non-synthetic event');
          const previous = eventWindows.get(id);
          eventWindows.set(id, { ...previous, entries: [...previous.entries, { type: 'event', event: { seq: previous.entries.length + 1, ...event } }] });
          for (const listener of eventListeners.get(id)) listener();
        },
        eventListenerCount: id => eventListeners.get(id).size,
        setDocumentHidden: hidden => {
          Object.defineProperty(document, 'hidden', { configurable: true, value: hidden });
          Object.defineProperty(document, 'visibilityState', { configurable: true, value: hidden ? 'hidden' : 'visible' });
          document.dispatchEvent(new Event('visibilitychange'));
        },
        setRussianPreview: enabled => {
          activeLocale = enabled ? 'ru' : 'en'; document.documentElement.lang = activeLocale;
          if (enabled && !previewDisposers.length) {
            const primitives = modules['@deepseek-ai/dsh-client-ui-primitives'];
            for (const [kind, order, en, ru, icon] of [
              ['files', 10, 'Files', '\u0424\u0430\u0439\u043b\u044b', primitives.IconFolderOutlineRegular],
              ['context', 20, 'Context', '\u041a\u043e\u043d\u0442\u0435\u043a\u0441\u0442', primitives.IconDocumentOutlineRegular],
              ['terminal', 30, 'Terminal', '\u0422\u0435\u0440\u043c\u0438\u043d\u0430\u043b', primitives.IconTerminalOutlineRegular],
            ]) previewDisposers.push(registry.register({ id: `synthetic-preview-${kind}`, kind, title: () => activeLocale === 'ru' ? ru : en,
              guide: [{ id: kind, order, title: () => activeLocale === 'ru' ? ru : en, icon }] }));
          } else if (!enabled) for (const dispose of previewDisposers.splice(0)) dispose();
          emit(); redraw();
        },
        setMnemon: available => { main = available ? [{ options: { key: 'mnemon' } }] : []; emit(); },
        navigate: next => { view = next; if (next === 'memory') memoryOpened = true; redraw(); },
        closeMemory: () => { memoryOpened = false; view = 'chat'; redraw(); },
        switchSession: id => { if (!['synthetic-a', 'synthetic-b'].includes(id)) throw Error('Non-synthetic session'); sessionId = id; graphBinding=id; emit(); redraw(); },
      };
      createRoot(document.getElementById('root')).render(h(Fixture));
    }, { theme: files.theme, sidebar: files.sidebar, gateway: files.gateway, colorScheme, fileTransfer });
    await page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').waitFor();
    return { page, errors, blocked, requests, setConflict: value => { conflict = value; }, setFailList: value => { failList = value; },
      modelMutate: async (sessionId, action, { id, text } = {}, { emit = true } = {}) => {
        assert.ok(records.has(sessionId), 'Only synthetic model mutations are allowed');
        const rows = records.get(sessionId);
        if (action === 'remember') { id = `synthetic-model-${++nextId}`; rows.push({ id, text }); }
        else {
          const index = rows.findIndex(record => record.id === id); assert.ok(index >= 0);
          if (action === 'update') rows[index] = { ...rows[index], text };
          else { assert.equal(action, 'forget'); rows.splice(index, 1); }
        }
        bump(sessionId);
        if (emit) await page.evaluate(({ sessionId, action, id }) => window.__memoryFixture.emitSessionEvent(sessionId, {
          type: 'tool/result', data: { name: 'chat_memory', arguments: { action }, result: { id, [{ remember: 'saved', update: 'updated', forget: 'deleted' }[action]]: true } },
        }), { sessionId, action, id });
        return id;
      },
      holdNextRead: async (sessionId, action = 'list') => {
        assert.ok(records.has(sessionId)); assert.ok(['list', 'revision'].includes(action));
        await page.evaluate(({ sessionId, action }) => { window.__memoryReadGate = { sessionId, action }; }, { sessionId, action });
        return {
          received: () => expect.poll(() => page.evaluate(() => !!window.__memoryReadGate?.received)).toBe(true),
          release: () => page.evaluate(() => { window.__memoryReadGate.release(); delete window.__memoryReadGate; }),
        };
      },
      dispose: async () => { await page.close(); },
    };
  } catch (error) { await page.close(); throw new Error(`${errors.join('; ')}; ${error.message}`, { cause: error }); }
}

async function screenshot(page, name, width, scheme) {
  if (!process.env.MEMORY_SCREENSHOT_DIRECTORY) return;
  mkdirSync(process.env.MEMORY_SCREENSHOT_DIRECTORY, { recursive: true });
  await page.screenshot({ path: join(process.env.MEMORY_SCREENSHOT_DIRECTORY, `memory-${name}-${width}-${scheme}.png`), fullPage: true });
}

async function assertGeometry(page) {
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'Document overflows viewport');
  const pane = page.locator('[data-gateway-chat-memory]');
  const geometry = await pane.evaluate(element => ({ width: element.clientWidth, scroll: element.scrollWidth, height: element.getBoundingClientRect().height, viewport: innerHeight }));
  assert.ok(geometry.scroll <= geometry.width + 1, 'Memory pane overflows horizontally');
  assert.ok(geometry.height <= geometry.viewport + 1, 'Memory pane exceeds viewport height');
}

for(const width of [390,1440])test(`memory review preview and explicit undo (${width}px)`,{timeout:45000},async()=>{
  const fixture=await openFixture(width,'dark',{recordCount:3,memoryReview:true}),{page,requests,errors,blocked}=fixture;
  try{
    await page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').click();
    await page.getByRole('button',{name:'Упорядочить память',exact:true}).click();
    const panel=page.locator('[data-gateway-memory-review]');
    await expect(panel.getByRole('heading',{name:'Упорядочить память'})).toBeVisible();
    const checks=panel.getByRole('checkbox');
    await checks.nth(0).check();await checks.nth(1).check();
    await panel.getByRole('button',{name:'Проверить выбранное'}).click();
    await expect(panel.getByRole('heading',{name:'До',exact:true})).toBeVisible();
    await expect(panel.getByRole('heading',{name:'После',exact:true})).toBeVisible();
    await expect(panel.getByText('Объединённая запись с деталями обоих фактов.')).toBeVisible();
    await panel.getByText('drafts/chapter.md · Фрагмент найден').click();
    await expect(panel.getByText('Проверенный фрагмент.')).toBeVisible();
    assert.ok(!requests.some(request=>request.action==='apply'));
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
    await screenshot(page,'review-preview',width,'dark');
    await panel.getByRole('button',{name:'Подтвердить и применить'}).click();
    await expect(panel.getByText('Применено',{exact:true})).toBeVisible();
    await panel.getByRole('button',{name:'Откатить',exact:true}).click();
    await expect(panel.getByText('Восстановить исходные факты и связи?')).toBeVisible();
    assert.ok(!requests.some(request=>request.action==='undo'));
    await panel.locator('footer').getByRole('button',{name:'Откатить',exact:true}).click();
    await expect(panel.getByText('Отменено',{exact:true})).toBeVisible();
    assert.deepEqual(errors,[]);assert.deepEqual(blocked,[]);
  }finally{await fixture.dispose();}
});

for(const [width,scheme] of [[390,'dark'],[1440,'light']])test(`whole chat review includes the last fact (${width}px)`,{timeout:45000},async()=>{
  const fixture=await openFixture(width,scheme,{recordCount:65,memoryReview:true}),{page,requests,errors,blocked}=fixture;
  try{
    await page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').click();
    await page.getByRole('button',{name:'Упорядочить память',exact:true}).click();
    const panel=page.locator('[data-gateway-memory-review]');
    await panel.getByRole('button',{name:'Выбрать все',exact:true}).click();
    await panel.getByRole('button',{name:'Проверить весь чат',exact:true}).click();
    await expect(panel.getByText('Просмотрено: 65 из 65')).toBeVisible();
    await expect(panel.getByText('Модель анализирует память…')).toBeVisible();
    await expect(panel.getByText('Модель проверила: 65 из 65')).toBeVisible();
    await expect(panel.getByText('Оба факта об одном событии.')).toBeVisible();
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
    await screenshot(page,'review-whole',width,scheme);
    await panel.locator('.gateway-review-candidates').getByRole('button',{name:'Предпросмотр',exact:true}).click();
    await expect(panel.getByRole('heading',{name:'Предложение модели'})).toBeVisible();
    await expect(panel.getByText('Сохранённый текст модели.')).toBeVisible();
    await expect(panel.getByText('Synthetic paged fact 064')).toBeVisible();
    assert.ok(requests.some(request=>request.action==='scanPreview'&&request.groupIndex===0));
    assert.ok(!requests.some(request=>request.action==='preview'));
    assert.ok(!requests.some(request=>request.action==='apply'));
    assert.deepEqual(errors,[]);assert.deepEqual(blocked,[]);
  }finally{await fixture.dispose();}
});

test('manual memory review displays the Russian advisory without enabling writes',{timeout:45000},async()=>{
  const fixture=await openFixture(390,'dark',{recordCount:3,memoryReview:true,reviewAdvisory:true}),{page,requests,errors}=fixture;
  try{
    await page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').click();
    await page.getByRole('button',{name:'Упорядочить память',exact:true}).click();
    const panel=page.locator('[data-gateway-memory-review]');
    await panel.getByRole('checkbox').nth(0).check();
    await panel.getByRole('checkbox').nth(1).check();
    await panel.getByRole('button',{name:'Проверить выбранное'}).click();
    await expect(panel.getByRole('heading',{name:'Предложение модели'})).toBeVisible();
    await expect(panel.getByText('Эти факты можно объединить после проверки.')).toBeVisible();
    await expect(panel.getByText('Предлагаемый факт.')).toBeVisible();
    await expect(panel.getByRole('button',{name:'Подтвердить и применить'})).toHaveCount(0);
    assert.ok(!requests.some(request=>request.action==='apply'));
    assert.deepEqual(errors,[]);
    await screenshot(page,'review-advisory',390,'dark');
  }finally{await fixture.dispose();}
});

for(const width of [390,1440])test(`fact graph opens the native main panel, draws links and stays scoped (${width}px)`,{timeout:45000},async()=>{
  const fixture=await openFixture(width,'dark',{recordCount:5,sources:true,nodeGraph:true}),{page,errors,blocked}=fixture;
  try {
    await page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').click();
    await page.getByRole('button',{name:'Open graph',exact:true}).click();
    const panel=page.locator('[data-gateway-memory-node-panel]');
    await expect(panel.getByRole('heading',{name:'Fact graph'})).toBeVisible();
    await expect(panel.locator('.gateway-node-card')).toHaveCount(5);
    await expect(panel.locator('path.gateway-node-wire')).toHaveCount(4);
    await expect(panel.locator('path.gateway-node-wire[marker-end]')).toHaveCount(2);
    await expect(panel.getByText('Facts: 5 of 5 · Links: 4 of 4')).toBeVisible();
    await expect(panel.locator('[data-priority="critical"]')).toContainText('Critical');
    await panel.getByRole('button',{name:/The expedition reaches/}).click();
    const details=panel.getByRole('complementary',{name:'Selected fact'});
    await expect(details).toContainText('The expedition reaches the northern gate.');
    await expect(details).toContainText('Depends on');
    await expect(panel.locator('[data-highlight="yes"]')).toHaveCount(3);
    await panel.getByRole('button',{name:'Zoom out',exact:true}).click();
    await expect(panel.getByRole('button',{name:'Reset zoom'})).toHaveText('75%');
    await expect(panel).toBeVisible();
    assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
    await page.evaluate(()=>window.__memoryFixture.setRussianPreview(true));
    await expect(panel.getByRole('heading',{name:'Граф фактов'})).toBeVisible();
    await screenshot(page,'nodes',width,'dark');
    await page.evaluate(()=>window.__memoryFixture.setRussianPreview(false));
    const search=panel.getByRole('searchbox',{name:'Find a fact'});
    await search.fill('map marks');
    await search.press('Enter');
    await expect(details).toContainText('A map marks a path around the gate.');
    await page.evaluate(()=>window.__memoryFixture.switchSession('synthetic-b'));
    await expect(panel.locator('.gateway-node-card')).toHaveCount(1);
    await expect(panel.getByRole('button',{name:/Synthetic B only/})).toBeVisible();
    await expect(panel.locator('path.gateway-node-wire')).toHaveCount(0);
    await panel.getByRole('button',{name:'Back to chat'}).click();
    await expect(panel).toHaveCount(0);
    await expect(page.getByRole('textbox',{name:'Synthetic chat composer'})).toBeVisible();
    assert.deepEqual(errors,[]);assert.deepEqual(blocked,[]);
  }finally{await fixture.dispose();}
});

for(const scheme of schemes)for(const width of [1440,390]) {
  test(`synthetic fact-node links, direction, editing and isolation at ${width}px (${scheme})`,{timeout:60000},async()=>{
    const fixture=await openFixture(width,scheme,{recordCount:3,sources:true}),{page,errors,blocked,requests}=fixture;
    try{
      await page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').click();
      const pane=page.locator('[data-gateway-chat-memory]');
      await pane.getByRole('button',{name:'Related facts',exact:true}).first().click();
      const graph=pane.locator('.gateway-memory-graph');
      await expect(graph.locator('[data-memory-node="seed-a"]')).toBeVisible();
      await expect(graph.getByText('This fact has no links.',{exact:true})).toBeVisible();
      await graph.getByRole('button',{name:'Add link',exact:true}).click();
      await graph.getByLabel('Other fact',{exact:true}).selectOption('synthetic-page-1');
      await graph.getByLabel('Relationship',{exact:true}).selectOption('depends_on');
      await graph.getByLabel('Link note',{exact:true}).fill('Synthetic dependency evidence');
      await graph.getByRole('button',{name:'Link facts',exact:true}).click();
      await expect(graph.locator('.gateway-memory-edge')).toHaveCount(1);
      await expect(graph.getByText('From this fact',{exact:true})).toBeVisible();
      await graph.locator('.gateway-memory-sources summary').click();
      await expect(graph.getByText('drafts/chapter-04.md',{exact:true})).toBeVisible();
      await expect(graph.getByText('Lines: 112-118',{exact:true})).toBeVisible();
      await assertGeometry(page);await screenshot(page,'graph',width,scheme);
      await page.evaluate(()=>window.__memoryFixture.setRussianPreview(true));
      await expect(graph.getByText('Строки: 112-118',{exact:true})).toBeVisible();
      await screenshot(page,'graph-ru',width,scheme);
      await page.evaluate(()=>window.__memoryFixture.setRussianPreview(false));
      await graph.getByTitle('Open this fact',{exact:true}).click();
      await expect(graph.locator('[data-memory-node="synthetic-page-1"]')).toBeVisible();
      await expect(graph.getByText('To this fact',{exact:true})).toBeVisible();
      await graph.getByRole('button',{name:'Remove link',exact:true}).click();
      await expect(graph.getByText('Remove this link? Both facts will remain.',{exact:true})).toBeVisible();
      await graph.getByRole('button',{name:'Cancel',exact:true}).click();
      await expect(graph.locator('.gateway-memory-edge')).toHaveCount(1);
      await graph.getByRole('button',{name:'Edit entry',exact:true}).click();
      await expect(pane.getByRole('textbox',{name:'Entry text',exact:true})).toHaveValue('Synthetic paged fact 001');
      await pane.getByRole('button',{name:'Cancel',exact:true}).click();
      await pane.getByRole('button',{name:'Links',exact:true}).click();
      await expect(graph.locator('[data-memory-node]')).toBeVisible();
      await page.evaluate(()=>window.__memoryFixture.switchSession('synthetic-b'));
      await expect(pane.locator('.gateway-chat-memory-fact-text')).toHaveText('Synthetic B only');
      await pane.getByRole('button',{name:'Links',exact:true}).click();
      await expect(graph.locator('[data-memory-node="seed-b"]')).toBeVisible();
      await expect(graph.locator('.gateway-memory-edge')).toHaveCount(0);
      assert.ok(requests.some(request=>request.action==='link'&&request.sessionId==='synthetic-a'));
      assert.equal(requests.some(request=>request.action==='link'&&request.sessionId==='synthetic-b'),false);
      assert.deepEqual(errors,[]);assert.deepEqual(blocked,[]);
    }catch(error){await screenshot(page,'graph-failure',width,scheme);throw new Error(`${error.message}; browser=${JSON.stringify(errors)}; requests=${JSON.stringify(requests.slice(-5))}`,{cause:error});}finally{await fixture.dispose();}
  });
}

for (const scheme of schemes) for (const width of [1440, 390]) {
  test(`synthetic native memory navigation, CRUD and isolation at ${width}px (${scheme})`, { timeout: 90000 }, async t => {
    const fixture = await openFixture(width, scheme);
    const { page, errors, blocked, requests } = fixture;
    const pane = page.locator('[data-gateway-chat-memory]');
    const facts = pane.locator('.gateway-chat-memory-facts');
    const tab = name => pane.getByRole('tab', { name, exact: true });
    const button = name => pane.getByRole('button', { name, exact: true });
    const editor = pane.getByRole('textbox', { name: 'Entry text', exact: true });
    try {
      assert.deepEqual(await page.evaluate(() => window.__memoryFixture.registry()), {
        id: 'gateway.chat-memory', kind: 'gateway-memory', keepMounted: true,
        guide: [{ id: 'memory', kind: 'gateway-memory', providerId: 'gateway.chat-memory', order: 15 }],
      });
      await expect(page.locator('[data-fixture-header]').getByRole('button', { name: /memory/i })).toHaveCount(0);
      await screenshot(page, 'guide', width, scheme);
      const guide = page.locator('[data-sidebar-right-guide-entry="gateway-memory"]');
      await expect(guide).toContainText('This chat and Mnemon');
      await guide.focus(); await page.keyboard.press('Enter');
      await expect(pane).toBeVisible();
      assert.match(await pane.evaluate(element => getComputedStyle(element).fontFamily), /Segoe UI|Helvetica|Arial|sans-serif/,
        'The fixture must inherit the installed native text-font token, not browser serif defaults');
      assert.deepEqual(await page.evaluate(() => window.__memoryFixture.calls.slice(0, 2)), [['openTab', 'gateway-memory', { replaceTab: true }], ['closeGuide']]);
      await expect(pane.getByRole('tab')).toHaveCount(3);
      await expect(tab('Chat facts')).toHaveAttribute('aria-selected', 'true');
      await expect(facts.getByRole('heading', { name: 'Synthetic project' })).toBeVisible();
      const usage=pane.getByRole('region',{name:'Memory usage'});
      await expect(usage).toContainText('Facts: 1 · Links: 0 · 320 B');
      await expect(usage).toContainText('All chats in this profile');
      await expect(usage).toContainText('Facts: 2 · Links: 0');
      const fonts = await page.evaluate(() => Object.fromEntries([
        ['body', document.body], ['heading', document.querySelector('.gateway-chat-memory-fact-text h2')],
        ['button', document.querySelector('.gateway-memory-add')], ['input', document.querySelector('input[type="search"]')],
      ].map(([name, element]) => [name, { family: getComputedStyle(element).fontFamily, size: getComputedStyle(element).fontSize }])));
      for (const [name, font] of Object.entries(fonts)) {
        assert.match(font.family, /system-ui|Segoe UI|Helvetica|Arial|sans-serif/, `${name} must use native or system sans typography`);
        assert.doesNotMatch(font.family, /Times|(?:^|,\s*)serif(?:,|$)/i, `${name} must not use browser-default serif typography`);
      }
      assert.equal(fonts.body.size, '14px');
      if (width === 390 && scheme === schemes[0]) t.diagnostic(`Computed fixture fonts: ${JSON.stringify(fonts)}`);
      await expect(facts.locator('strong')).toHaveText('Keep decisions');
      await expect(facts.locator('code')).toHaveText('tests');
      await assertGeometry(page); await screenshot(page, 'facts', width, scheme);

      await tab('Chat facts').focus(); await page.keyboard.press('ArrowRight');
      await expect(tab('Summary')).toBeFocused();
      await expect(tab('Summary')).toHaveAttribute('aria-selected', 'true');
      await expect(pane.getByRole('heading', { name: 'Synthetic summary' })).toBeVisible();
      await expect(pane.locator('.gateway-chat-memory-summary strong')).toHaveText('Summary Markdown');
      await button('Copy summary').click();
      assert.match((await page.evaluate(() => window.__copiedText)).at(-1), /## Synthetic summary/);
      await screenshot(page, 'summary', width, scheme);
      await tab('Summary').focus(); await page.keyboard.press('End');
      await expect(tab('Mnemon')).toBeFocused(); await expect(tab('Mnemon')).toHaveAttribute('aria-selected', 'true');
      await expect(button('Open Mnemon')).toBeEnabled(); await button('Open Mnemon').click();
      await expect(page.locator('[data-fixture-mnemon]')).toBeVisible();
      assert.deepEqual(await page.evaluate(() => window.__memoryFixture.calls.at(-1)), ['selectPanel', 'mnemon']);
      await page.evaluate(() => { window.__memoryFixture.setMnemon(false); window.__memoryFixture.navigate('memory'); });
      await tab('Mnemon').click(); await expect(button('Open Mnemon')).toBeDisabled();
      await expect(pane.getByRole('status')).toHaveText('Mnemon is not available right now.');
      await screenshot(page, 'mnemon-unavailable', width, scheme);
      await tab('Mnemon').focus(); await page.keyboard.press('Home');
      await expect(tab('Chat facts')).toBeFocused();

      await button('Add entry').click(); await editor.fill('## Added fixture\n\nA **synthetic** entry.');
      await button('Save').click(); await expect(editor).toHaveCount(0);
      const record = facts.locator('.gateway-chat-memory-fact-list > li').filter({ has: page.getByRole('heading', { name: 'Added fixture' }) });
      await expect(record).toBeVisible();
      await expect(usage).toContainText('Facts: 2 · Links: 0 · 640 B');
      await record.getByRole('button', { name: 'Edit entry', exact: true }).click();
      await editor.fill('## Added fixture\n\nUpdated **synthetic** entry.'); await button('Save').click();
      await expect(editor).toHaveCount(0); await expect(record).toContainText('Updated synthetic entry.');
      await record.getByRole('button', { name: 'Copy entry', exact: true }).click();
      assert.match((await page.evaluate(() => window.__copiedText)).at(-1), /Updated \*\*synthetic\*\*/);
      await facts.getByRole('searchbox').fill('ADDED FIXTURE'); await expect(facts.locator('.gateway-chat-memory-fact-list > li')).toHaveCount(1);
      await facts.getByRole('searchbox').fill('no synthetic matches'); await expect(facts.getByText('No matching entries.', { exact: true })).toBeVisible();
      await expect(usage).toContainText('Facts: 2 · Links: 0 · 640 B');
      await facts.getByRole('searchbox').fill(''); await expect(facts.locator('.gateway-chat-memory-fact-list > li')).toHaveCount(2);

      await record.getByRole('button', { name: 'Edit entry', exact: true }).click(); await editor.fill('Conflicting synthetic draft');
      fixture.setConflict(true); await button('Save').click();
      await expect(pane.getByRole('status')).toContainText('This entry has changed.');
      await expect(editor).toHaveValue('Conflicting synthetic draft'); fixture.setConflict(false);
      await page.evaluate(() => window.__memoryFixture.navigate('guide'));
      await page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').click();
      await expect(editor).toHaveValue('Conflicting synthetic draft');
      await page.evaluate(() => window.__memoryFixture.switchSession('synthetic-b'));
      await expect(editor).toHaveCount(0); await expect(facts).toContainText('Synthetic B only');
      await expect(facts).not.toContainText('Synthetic project');
      await page.evaluate(() => window.__memoryFixture.switchSession('synthetic-a'));
      await expect(editor).toHaveValue('Conflicting synthetic draft');
      await expect(facts.getByRole('heading', { name: 'Synthetic project' })).toBeVisible();
      page.once('dialog', dialog => dialog.dismiss()); await button('Back to chat').click(); await expect(pane).toBeVisible();
      page.once('dialog', dialog => dialog.accept()); await button('Cancel').click(); await expect(editor).toHaveCount(0);

      fixture.setFailList(true); await button('Refresh').click(); await expect(pane.getByRole('alert')).toContainText('Could not load saved facts');
      fixture.setFailList(false); await button('Try again').click(); await expect(pane.getByRole('alert')).toHaveCount(0);
      await record.getByRole('button', { name: 'Delete', exact: true }).click();
      await record.getByRole('button', { name: 'Delete fact', exact: true }).click(); await expect(record).toHaveCount(0);
      await button('Add entry').click(); await editor.fill('Synthetic keyboard draft\n'.repeat(40));
      await page.setViewportSize({ width, height: 440 });
      await button('Save').scrollIntoViewIfNeeded(); await expect(button('Save')).toBeInViewport();
      await assertGeometry(page); await screenshot(page, 'keyboard', width, scheme);
      page.once('dialog', dialog => dialog.accept()); await button('Cancel').click();
      await page.setViewportSize({ width, height: 900 });
      await button('Back to chat').focus(); await page.keyboard.press('Enter');
      await expect(pane).toHaveCount(0); await expect(page.getByRole('textbox', { name: 'Synthetic chat composer' })).toBeFocused();
      assert.deepEqual(await page.evaluate(() => window.__memoryFixture.calls.slice(-2)), [['selectPanel', null], ['toggleExpanded']]);
      await page.getByRole('button', { name: 'Open right sidebar' }).click();
      await page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').click();
      await expect(editor).toHaveCount(0); await expect(tab('Chat facts')).toHaveAttribute('aria-selected', 'true');
      await assertGeometry(page);
      if (process.env.MEMORY_SCREENSHOT_DIRECTORY) {
        await page.evaluate(() => { window.__memoryFixture.setRussianPreview(true); window.__memoryFixture.navigate('guide'); });
        await expect(page.locator('[data-sidebar-right-guide-entry]')).toHaveCount(4);
        assert.deepEqual(await page.locator('[data-sidebar-right-guide-entry]').evaluateAll(entries => entries.map(entry => entry.dataset.sidebarRightGuideEntry)),
          ['files', 'gateway-memory', 'context', 'terminal']);
        await expect(page.locator('[data-sidebar-right-guide-entry="gateway-memory"]')).not.toContainText('Memory');
        await screenshot(page, 'guide-ru', width, scheme);
        await page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').click();
        await expect(facts.getByRole('heading', { name: 'Synthetic project' })).toBeVisible();
        const russian = await page.evaluate(() => window.__gatewayFixture.dictionaries.gatewayChatMemory);
        assert.ok(russian?.factsTab && russian?.summaryTab, 'Installed build must include Russian memory translations');
        await expect(pane.getByRole('tab')).toHaveText([russian.factsTab, russian.summaryTab, russian.mnemonTab]);
        await assertGeometry(page); await screenshot(page, 'facts-ru', width, scheme);
        await page.evaluate(() => window.__memoryFixture.setRussianPreview(false));
        await expect(tab('Chat facts')).toHaveAttribute('aria-selected', 'true');
      }
      assert.ok(requests.some(request => request.action === 'list' && request.sessionId === 'synthetic-b'));
      const allowedActions = new Set(['list', 'revision', 'usage', 'add', 'update', 'delete']);
      assert.ok(requests.every(request => allowedActions.has(request.action)));
      for (const action of ['list', 'add', 'update', 'delete']) assert.ok(requests.some(request => request.action === action));
      assert.deepEqual(blocked, [], 'Unexpected network attempt (all unknown URLs are blocked)');
      assert.deepEqual(errors, [], 'Browser JavaScript errors');
    } finally { await page.close(); }
  });
}

const liveRows = page => page.locator('.gateway-chat-memory-fact-list > li');
const reads = fixture => fixture.requests.filter(request => ['list', 'revision'].includes(request.action));
async function openMemory(fixture) {
  await fixture.page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').click();
  await expect(liveRows(fixture.page).first()).toBeVisible();
  await expect(fixture.page.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled();
}

for (const width of [390, 1440]) test(`memory transfer previews both directions and requires recheck after conflicts (${width}px)`, { timeout:60000 }, async () => {
  const fixture=await openFixture(width,'dark'), {page,requests,blocked,errors}=fixture;
  try {
    await openMemory(fixture);
    await page.getByRole('button',{name:'Transfer memory'}).click();
    const dialog=page.getByRole('dialog',{name:'Transfer memory'});
    await expect(dialog).toBeVisible();
    const from=dialog.getByLabel('Source chat');
    await expect(from.locator('option')).toHaveCount(2);
    await expect(from.locator('option').last()).toContainText('Other synthetic chat');
    await from.selectOption('synthetic-b');
    await dialog.getByRole('button',{name:'Preview',exact:true}).click();
    await expect(dialog.getByRole('table')).toBeVisible();
    assert.ok(await dialog.evaluate(el=>el.scrollWidth===el.clientWidth));
    await fixture.modelMutate('synthetic-b','remember',{text:'Added after preview'},{emit:false});
    await dialog.getByRole('button',{name:'Copy memory'}).click();
    await expect(dialog.getByRole('alert')).toContainText('Preview again');
    await dialog.getByRole('button',{name:'Preview',exact:true}).click();
    await dialog.getByRole('button',{name:'Copy memory'}).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByText('Copied (facts: 2, links: 0).')).toBeVisible();
    await expect(page.getByText('Added after preview')).toBeVisible();
    await page.getByRole('button',{name:'Transfer memory'}).click();
    await dialog.getByRole('button',{name:'Export to a chat'}).click();
    await dialog.getByLabel('Destination chat').selectOption('synthetic-b');
    await dialog.getByRole('button',{name:'Preview',exact:true}).click();
    await expect(dialog.getByRole('table')).toBeVisible();
    await page.evaluate(()=>window.__memoryFixture.setRussianPreview(true));
    await expect(page.getByRole('dialog',{name:'Перенос памяти'})).toBeVisible();
    await screenshot(page,'transfer',width,'dark');
    await page.evaluate(()=>window.__memoryFixture.setRussianPreview(false));
    await dialog.getByRole('button',{name:'Copy memory'}).click();
    await expect(page.getByText('Copied (facts: 1, links: 0).')).toBeVisible();
    assert.deepEqual(requests.filter(body=>body.action==='transfer').map(body=>body.direction),['import','import','export']);
    assert.deepEqual(blocked,[]);
    assert.deepEqual(errors,[]);
  } finally { await page.close(); }
});

for (const width of [390, 1440]) for (const type of ['file','directory']) test(`${type} copy picks a chat and an existing folder (${width}px)`, { timeout:60000 }, async () => {
  const name=type==='directory'?'synthetic-folder':'synthetic.txt';
  const {page,requests,blocked,errors}=await openFixture(width,'dark',{fileTransfer:type});
  try {
    await page.getByRole('button',{name:'Actions for '+name}).click();
    await page.getByText('Copy to…',{exact:true}).click();
    const dialog=page.getByRole('dialog',{name:'Copy to…'});
    const target=dialog.getByLabel('Destination chat');
    await expect(target.locator('option')).toHaveCount(3);
    await expect(target.locator('option').nth(1)).toHaveText('Current chat');
    await target.selectOption('synthetic-b');
    await dialog.getByLabel('Destination folder').fill('collision');
    await page.evaluate(()=>window.__memoryFixture.setRussianPreview(true));
    const localized=page.getByRole('dialog',{name:'Копировать в…'});
    await expect(localized).toBeVisible();
    await localized.getByRole('button',{name:'Копировать',exact:true}).click();
    await expect(localized.getByRole('alert')).toContainText('уже есть');
    await localized.getByLabel('Папка назначения').fill('archive');
    assert.ok(await localized.evaluate(el=>el.scrollWidth===el.clientWidth));
    await screenshot(page,type+'-transfer',width,'dark');
    await localized.getByRole('button',{name:'Копировать',exact:true}).click();
    await expect(localized.getByRole('status')).toHaveText(/скопирован/);
    assert.deepEqual(requests.findLast(body=>body.action==='copyToChat'),{
      action:'copyToChat',sessionId:'synthetic-a',path:'/workspace/'+name,version:'test-v1',
      targetSessionId:'synthetic-b',targetFolder:'archive',
    });
    assert.deepEqual(blocked,[]);
    assert.deepEqual(errors,[]);
  } finally { await page.close(); }
});

test('transfer chat search keeps selection and the mobile sheet keeps its actions above the keyboard', { timeout:60000 }, async () => {
  const {page,requests,errors}=await openFixture(390,'light',{fileTransfer:'directory',extraChats:true});
  try {
    await page.getByRole('button',{name:'Actions for synthetic-folder'}).click();
    await page.getByText('Copy to…',{exact:true}).click();
    const dialog=page.getByRole('dialog',{name:'Copy to…'}), picker=dialog.getByLabel('Destination chat');
    const search=dialog.getByRole('searchbox',{name:'Find a chat'}), button=dialog.getByRole('button',{name:'Copy',exact:true});
    await expect(picker.locator('option')).toHaveCount(11);
    await search.fill('Other synthetic');
    await expect(picker.locator('option')).toHaveCount(2);
    await picker.selectOption('synthetic-b');
    await search.fill('no result');
    await expect(dialog.getByRole('status')).toHaveText('No matching chats.');
    await expect(picker).toHaveValue('synthetic-b');
    await search.press('Enter');
    assert.equal(requests.some(request=>request.action==='copyToChat'),false);
    await page.evaluate(()=>{
      document.documentElement.style.setProperty('--gateway-viewport-top','20px');
      document.documentElement.style.setProperty('--gateway-viewport-height','320px');
    });
    const box=await dialog.boundingBox(), action=await button.boundingBox();
    assert.ok(box.y>=19&&box.y+box.height<=341);
    assert.ok(action.y+action.height<=341);
    assert.deepEqual(errors,[]);
  } finally { await page.close(); }
});

for(const width of [390,1440])test(`fact priorities and order persist after reopening (${width}px)`,{timeout:60000},async()=>{
  const fixture=await openFixture(width,'dark',{recordCount:3}),{page,requests,errors}=fixture;
  try {
    await openMemory(fixture);
    const row=page.locator('[data-memory-record-id="seed-a"]'), status=row.getByLabel('Fact priority');
    await expect(status).toHaveValue('normal');
    await status.selectOption('critical');
    await expect(status).toHaveValue('critical');
    await expect(row.getByRole('button',{name:'Move fact up'})).toBeDisabled();
    await expect(row.getByRole('button',{name:'Move fact up'}).locator('.gateway-memory-order-icon')).toHaveCSS('transform','matrix(0, -1, 1, 0, 0, 0)');
    await expect(row.getByRole('button',{name:'Move fact down'}).locator('.gateway-memory-order-icon')).toHaveCSS('transform','matrix(0, 1, -1, 0, 0, 0)');
    await row.getByRole('button',{name:'Move fact down'}).click();
    await expect(liveRows(page).nth(1)).toHaveAttribute('data-memory-record-id','seed-a');
    await expect(row.getByRole('button',{name:'Move fact down'})).toBeFocused();
    await page.evaluate(()=>window.__memoryFixture.closeMemory());
    await page.getByRole('button',{name:'Open right sidebar'}).click();
    await openMemory(fixture);
    await expect(liveRows(page).nth(1)).toHaveAttribute('data-memory-record-id','seed-a');
    await expect(status).toHaveValue('critical');
    await page.evaluate(()=>window.__memoryFixture.setRussianPreview(true));
    await expect(row.getByLabel('Важность факта')).toBeVisible();
    await screenshot(page,'priority-order',width,'dark');
    assert.deepEqual(requests.find(request=>request.action==='setStatus'),{action:'setStatus',sessionId:'synthetic-a',id:'seed-a',status:'critical',expectedStatus:'normal'});
    assert.ok(requests.some(request=>request.action==='moveFact'&&request.id==='seed-a'&&request.direction==='down'&&typeof request.expectedRevision==='string'));
    assert.deepEqual(errors,[]);
  }finally{await page.close();}
});
async function advancePoll(page) {
  await page.clock.fastForward(6000);
  await page.evaluate(() => {});
}
async function assertNoLiveReads(fixture, trigger) {
  await trigger();
  await fixture.page.clock.fastForward(1000);
  const count = reads(fixture).length;
  await advancePoll(fixture.page);
  await advancePoll(fixture.page);
  assert.equal(reads(fixture).length, count, 'Inactive memory must not perform revision or list polling');
}

for (const scheme of schemes) for (const width of [1440, 390]) {
  test(`synthetic live model facts preserve loaded pages, selection and scroll at ${width}px (${scheme})`, { timeout: 60000 }, async () => {
    const fixture = await openFixture(width, scheme, { recordCount: 121, clock: true });
    const { page, requests, blocked, errors } = fixture;
    const rows = liveRows(page), inner = page.locator('.gateway-chat-memory-inner');
    try {
      await openMemory(fixture);
      await expect(rows).toHaveCount(50);
      await page.getByRole('button', { name: 'Load more', exact: true }).click();
      await expect(rows).toHaveCount(100);
      await page.getByRole('button', { name: 'Load more', exact: true }).click();
      await expect(rows).toHaveCount(121);
      assert.ok(requests.filter(request => request.action === 'list').every(request => request.includeRevision === true),
        'Every list page must request its atomic revision');

      await inner.evaluate(element => { element.scrollTop = 500; });
      const scrollTop = await inner.evaluate(element => element.scrollTop);
      assert.ok(scrollTop > 0, 'The fixture must actually be scrolled');
      await page.evaluate(() => {
        const element = [...document.querySelectorAll('.gateway-chat-memory-fact-text')].find(element => element.textContent === 'Synthetic paged fact 002');
        window.__selectedFactNode = element.firstChild;
        const range = document.createRange(); range.selectNodeContents(element);
        const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range);
      });
      const selection = await page.evaluate(() => getSelection().toString());
      assert.equal(selection, 'Synthetic paged fact 002');
      const listsBefore = requests.filter(request => request.action === 'list').length;
      const revisionsBefore = requests.filter(request => request.action === 'revision').length;
      await advancePoll(page);
      await expect.poll(() => requests.filter(request => request.action === 'revision').length).toBeGreaterThan(revisionsBefore);
      assert.equal(requests.filter(request => request.action === 'list').length, listsBefore, 'An unchanged revision must not reload facts');
      assert.equal(await page.evaluate(() => getSelection().toString()), selection, 'Polling must not clear text selection');
      assert.equal(await page.evaluate(() => window.__selectedFactNode.isConnected), true, 'Unchanged fact DOM must remain mounted');
      assert.equal(await inner.evaluate(element => element.scrollTop), scrollTop, 'Unchanged polling must not move scroll');

      const modelId = await fixture.modelMutate('synthetic-a', 'remember', { text: 'Synthetic model remembered live' });
      await page.clock.fastForward(500);
      await expect(rows).toHaveCount(121);
      assert.equal(await page.evaluate(() => getSelection().toString()), selection, 'A changed revision must not destroy selected content');
      assert.equal(await page.evaluate(() => window.__selectedFactNode.isConnected), true);
      await page.evaluate(() => { getSelection().removeAllRanges(); dispatchEvent(new Event('selectionchange')); });
      await page.clock.fastForward(500);
      await expect(rows).toHaveCount(122);
      await expect(rows.last()).toContainText('Synthetic model remembered live');
      assert.equal(await inner.evaluate(element => element.scrollTop), scrollTop, 'Background reconciliation must preserve scroll');
      await expect(rows.filter({ hasText: 'Synthetic paged fact 120' })).toHaveCount(1);
      await fixture.modelMutate('synthetic-a', 'update', { id: modelId, text: 'Synthetic model corrected live' });
      await page.clock.fastForward(500);
      await expect(rows.last()).toContainText('Synthetic model corrected live');
      await expect(rows).toHaveCount(122);
      await fixture.modelMutate('synthetic-a', 'forget', { id: modelId });
      await page.clock.fastForward(500);
      await expect(rows).toHaveCount(121);
      await expect(rows.filter({ hasText: 'Synthetic model corrected live' })).toHaveCount(0);

      // No event enters the loaded window: only the revision poll can discover this write.
      await fixture.modelMutate('synthetic-a', 'update', { id: 'synthetic-page-120', text: 'Synthetic out-of-window correction' }, { emit: false });
      await advancePoll(page);
      await expect(rows.last()).toContainText('Synthetic out-of-window correction');
      await expect(rows).toHaveCount(121);
      await assertGeometry(page);
      await screenshot(page, 'live-paginated', width, scheme);
      assert.deepEqual(blocked, []); assert.deepEqual(errors, []);
    } finally { await fixture.dispose(); }
  });

  test(`synthetic fully loaded 50-fact tail grows live to 51 at ${width}px (${scheme})`, { timeout: 60000 }, async () => {
    const fixture = await openFixture(width, scheme, { recordCount: 50, clock: true });
    const { page, requests, blocked, errors } = fixture;
    try {
      await openMemory(fixture);
      await expect(liveRows(page)).toHaveCount(50);
      await expect(page.getByRole('button', { name: 'Load more', exact: true })).toHaveCount(0);
      const count = requests.filter(request => request.action === 'list').length;
      await fixture.modelMutate('synthetic-a', 'remember', { text: 'Synthetic fifty-first live tail' });
      await page.clock.fastForward(500);
      await expect(liveRows(page)).toHaveCount(51);
      await expect(liveRows(page).last()).toContainText('Synthetic fifty-first live tail');
      assert.ok(requests.filter(request => request.action === 'list').length > count);
      assert.ok(requests.some(request => request.action === 'list' && request.cursor === 50),
        'A previously exhausted page must discover the new tail without a manual load or reopen');
      assert.deepEqual(blocked, []); assert.deepEqual(errors, []);
    } finally { await fixture.dispose(); }
  });

  test(`synthetic live reconciliation preserves editor draft and conflict baseline at ${width}px (${scheme})`, { timeout: 60000 }, async () => {
    const fixture = await openFixture(width, scheme, { clock: true });
    const { page, requests, blocked, errors } = fixture;
    const editor = page.getByRole('textbox', { name: 'Entry text', exact: true });
    const original = '## Synthetic project\n\n- **Keep decisions**\n- Use `tests`\n\n[Reference](https://example.invalid/synthetic)';
    try {
      await openMemory(fixture);
      await page.getByRole('button', { name: 'Edit entry', exact: true }).click();
      await editor.fill('Synthetic unsaved local draft');
      await editor.evaluate(element => { element.setSelectionRange(10, 17); window.__memoryEditorNode = element; });
      await fixture.modelMutate('synthetic-a', 'update', { id: 'seed-a', text: 'Synthetic concurrent model version' });
      await page.clock.fastForward(500);
      await expect(liveRows(page)).toContainText('Synthetic concurrent model version');
      await expect(editor).toHaveValue('Synthetic unsaved local draft');
      await expect(editor).toBeFocused();
      assert.deepEqual(await editor.evaluate(element => [element === window.__memoryEditorNode, element.selectionStart, element.selectionEnd]), [true, 10, 17]);
      await page.getByRole('button', { name: 'Save', exact: true }).click();
      await expect(page.getByRole('status')).toContainText('This entry has changed.');
      assert.equal(requests.findLast(request => request.action === 'update').expectedText, original,
        'Live reconciliation must not silently rebase an open editor onto concurrent text');
      await expect(editor).toHaveValue('Synthetic unsaved local draft');
      await fixture.modelMutate('synthetic-a', 'remember', { text: 'Synthetic unrelated model fact' });
      await page.clock.fastForward(500);
      await expect(liveRows(page)).toHaveCount(2);
      await expect(page.getByRole('status')).toContainText('This entry has changed.');
      await expect(editor).toHaveValue('Synthetic unsaved local draft');
      await screenshot(page, 'live-conflict', width, scheme);
      assert.deepEqual(blocked, []); assert.deepEqual(errors, []);
    } finally { await fixture.dispose(); }
  });

  test(`synthetic live polling lifecycle and stale session isolation at ${width}px (${scheme})`, { timeout: 60000 }, async () => {
    const fixture = await openFixture(width, scheme, { clock: true });
    const { page, requests, blocked, errors } = fixture;
    const mode = name => page.getByRole('tab', { name, exact: true });
    try {
      await openMemory(fixture);
      assert.ok(await page.evaluate(() => window.__memoryFixture.eventListenerCount('synthetic-a')) > 0);
      await assertNoLiveReads(fixture, () => mode('Summary').click());
      await assertNoLiveReads(fixture, () => mode('Mnemon').click());
      await mode('Chat facts').click();
      await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled();
      await assertNoLiveReads(fixture, () => page.evaluate(() => window.__memoryFixture.setDocumentHidden(true)));
      const hiddenId = await fixture.modelMutate('synthetic-a', 'remember', { text: 'Synthetic hidden write' }, { emit: false });
      const resumeCount = reads(fixture).length;
      await page.evaluate(() => { window.__memoryFixture.setDocumentHidden(false); dispatchEvent(new Event('focus')); });
      await page.clock.fastForward(500);
      await expect(liveRows(page).filter({ hasText: 'Synthetic hidden write' })).toHaveCount(1);
      assert.ok(reads(fixture).length > resumeCount, 'Focus/visibility resume must check immediately');
      await assertNoLiveReads(fixture, () => page.evaluate(() => dispatchEvent(new Event('pagehide'))));
      await page.evaluate(() => dispatchEvent(new Event('pageshow')));
      await page.clock.fastForward(500);
      const focusCount = reads(fixture).length;
      await fixture.modelMutate('synthetic-a', 'update', { id: hiddenId, text: 'Synthetic focus-only correction' }, { emit: false });
      await page.evaluate(() => dispatchEvent(new Event('focus')));
      await page.clock.fastForward(500);
      await expect(liveRows(page).filter({ hasText: 'Synthetic focus-only correction' })).toHaveCount(1);
      assert.ok(reads(fixture).length > focusCount);

      await assertNoLiveReads(fixture, () => page.evaluate(() => window.__memoryFixture.navigate('guide')));
      await expect(page.locator('[data-gateway-chat-memory]')).toHaveCount(1);
      await expect(page.locator('[data-gateway-chat-memory]')).toBeHidden();
      await page.locator('[data-sidebar-right-guide-entry="gateway-memory"]').click();
      await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeEnabled();

      const held = await fixture.holdNextRead('synthetic-a');
      await page.getByRole('button', { name: 'Refresh', exact: true }).click();
      await held.received();
      await page.evaluate(() => window.__memoryFixture.switchSession('synthetic-b'));
      await expect(liveRows(page)).toHaveText(/Synthetic B only/);
      await held.release();
      await page.clock.fastForward(500);
      await expect(liveRows(page)).toHaveCount(1);
      await expect(liveRows(page)).toHaveText(/Synthetic B only/);
      await expect(page.locator('[data-gateway-chat-memory]')).not.toContainText('Synthetic project');
      const oldSessionReads = requests.filter(request => request.sessionId === 'synthetic-a').length;
      await fixture.modelMutate('synthetic-a', 'remember', { text: 'Synthetic wrong-session signal' });
      await advancePoll(page);
      assert.equal(requests.filter(request => request.sessionId === 'synthetic-a').length, oldSessionReads, 'Old session events must not restart its reads');
      await expect(liveRows(page)).toHaveText(/Synthetic B only/);
      await assertNoLiveReads(fixture, () => page.evaluate(() => window.__memoryFixture.closeMemory()));
      assert.equal(await page.evaluate(() => window.__memoryFixture.eventListenerCount('synthetic-b')), 0);
      assert.deepEqual(blocked, []); assert.deepEqual(errors, []);
    } finally { await fixture.dispose(); }
  });
}
