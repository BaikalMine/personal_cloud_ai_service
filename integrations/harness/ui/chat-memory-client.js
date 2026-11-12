function chatMemoryValidRevision(value) { return typeof value === 'string' && /^(0|[1-9][0-9]*)$/.test(value); }
const CHAT_MEMORY_NODE_PANEL = 'gateway.memory-nodes';
const nodeWidth = 156, nodeHeight = 44, nodeStepX = 194, nodeStepY = 89;
function memoryNodeLayout(nodes, edges) {
  const neighbors=new Map(nodes.map(node=>[node.id,new Set()]));
  for(const edge of edges)if(neighbors.has(edge.fromId)&&neighbors.has(edge.toId)){
    neighbors.get(edge.fromId).add(edge.toId);neighbors.get(edge.toId).add(edge.fromId);
  }
  const positions=new Map(), seen=new Set(), ordered=[];
  const columns=Math.min(8,Math.max(1,Math.ceil(Math.sqrt(nodes.length))));
  for(const start of nodes){
    if(seen.has(start.id))continue;
    const queue=[start.id];seen.add(start.id);
    for(let i=0;i<queue.length;i++){
      const id=queue[i];ordered.push(id);
      for(const next of neighbors.get(id))if(!seen.has(next)){seen.add(next);queue.push(next);}
    }
  }
  if(nodes.length>2&&nodes.length<=8&&edges.length){
    const [hub,...outer]=[...ordered].sort((a,b)=>neighbors.get(b).size-neighbors.get(a).size);
    const width=600,height=320;
    positions.set(hub,{x:width/2-26,y:height/2-nodeHeight/2});
    outer.forEach((id,i)=>{
      const angle=2*Math.PI*i/outer.length-Math.PI/2;
      positions.set(id,{x:width/2-26+Math.cos(angle)*197,y:height/2-nodeHeight/2+Math.sin(angle)*111});
    });
    return {positions,width:width+25,height};
  }
  ordered.forEach((id,i)=>positions.set(id,{x:24+(i%columns)*nodeStepX+(Math.floor(i/columns)%2)*18,y:24+Math.floor(i/columns)*nodeStepY+(i%2)*12}));
  return {positions,width:columns*nodeStepX+18,height:Math.max(1,Math.ceil(nodes.length/columns))*nodeStepY+15};
}
function chatMemoryValidatePage(value, cursor) {
  if (!chatMemoryValidRevision(value?.revision) || !Array.isArray(value.records) || !(value.nextCursor === null || (Number.isSafeInteger(value.nextCursor) && value.nextCursor > cursor)) || value.records.length > 50 || value.records.some(record => !record || !['string','number'].includes(typeof record.id) || typeof record.text !== 'string')) throw new Error('Invalid response');
  return value;
}
async function chatMemoryReadPages(read, pages, revision, all = false) {
  const records = [], ids = new Set();
  let cursor = 0, loaded = 0;
  do {
    const value = chatMemoryValidatePage(await read(cursor), cursor);
    if (revision === undefined) revision = value.revision;
    if (value.revision !== revision) throw new Error('Memory revision changed');
    for (const record of value.records) if (!ids.has(record.id)) { ids.add(record.id); records.push(record); }
    cursor = value.nextCursor; loaded++;
    if (cursor !== null && loaded >= Math.max(200, pages + 20)) throw new Error('Memory page budget exceeded');
  } while (cursor !== null && (all || loaded < pages));
  return { records, cursor, pages:loaded, revision };
}
function chatMemoryToolSignal(snapshot) {
  const entries = snapshot?.entries || [];
  for (let index = entries.length - 1; index >= 0; index--) {
    const event = entries[index]?.event;
    if (event?.type === 'tool/call' || event?.type === 'tool/result') return `${event.type}:${event.seq}`;
  }
  return '';
}
function chatMemoryHasSelection(pane) {
  const selection = globalThis.getSelection?.();
  if (!pane || !selection || selection.isCollapsed || !selection.rangeCount) return false;
  return pane.contains(selection.anchorNode) || pane.contains(selection.focusNode) || !!selection.getRangeAt?.(0)?.intersectsNode?.(pane);
}
function chatMemoryScrollAnchor(pane) {
  if (!pane) return null;
  const top = pane.getBoundingClientRect().top;
  return { pane, top:pane.scrollTop, left:pane.scrollLeft, rows:[...pane.querySelectorAll('[data-memory-record-id]')].filter(row => row.getBoundingClientRect().bottom > top).map(row => ({ id:row.getAttribute('data-memory-record-id'), offset:row.getBoundingClientRect().top - top })) };
}
function chatMemoryRestoreScroll(anchor) {
  if (!anchor) return;
  const { pane } = anchor, rows = [...pane.querySelectorAll('[data-memory-record-id]')];
  const kept = anchor.rows.find(row => rows.some(node => node.getAttribute('data-memory-record-id') === row.id));
  const node = kept && rows.find(row => row.getAttribute('data-memory-record-id') === kept.id);
  pane.scrollTop = node ? pane.scrollTop + node.getBoundingClientRect().top - pane.getBoundingClientRect().top - kept.offset : anchor.top;
  pane.scrollLeft = anchor.left;
}
function createChatMemoryLive({ refresh, blocked, schedule = setTimeout, unschedule = clearTimeout }) {
  let active = false, disposed = false, timer = null, running = null, pending = false, failures = 0, denied = false, epoch = 0;
  const clear = () => { if (timer !== null) unschedule(timer); timer = null; };
  function later(delay) { clear(); if (active && !disposed && !denied) timer = schedule(run, delay); }
  async function run() {
    timer = null;
    if (!active || disposed || denied || running) return;
    if (blocked()) { later(3000); return; }
    const controller = new AbortController(), token = epoch;
    running = controller; pending = false;
    try { await refresh(controller.signal); if (token === epoch) failures = 0; }
    catch (error) { if (token === epoch) { failures = Math.min(failures + 1, 4); denied = error?.status === 401 || error?.status === 403; } }
    finally {
      running = null;
      if (active && !disposed && !denied) later(pending ? 0 : Math.min(30000, 3000 * 2 ** failures));
    }
  }
  function invalidate() { epoch++; running?.abort(); pending = false; clear(); }
  function wake(explicit = false) {
    if (disposed || !active) return;
    if (explicit) { denied = false; failures = 0; }
    if (denied || (failures && !explicit)) return;
    if (running) pending = true; else later(0);
  }
  function settled(error) {
    if (error) { failures = Math.min(failures + 1, 4); denied = error.status === 401 || error.status === 403; }
    else { failures = 0; denied = false; }
    later(error ? Math.min(30000,3000 * 2 ** failures) : 0);
  }
  return { wake, invalidate, settled,
    setActive(value) { if (active === value || disposed) return; active = value; invalidate(); if (active) { denied = false; failures = 0; later(0); } },
    dispose() { active = false; disposed = true; invalidate(); },
  };
}
async function gatewayListOtherChats(sessionId, signal) {
  const response = await fetch(new URL('./api/session/list', document.baseURI), {
    method:'POST', credentials:'same-origin', mode:'same-origin', redirect:'error',
    headers:{'Content-Type':'application/json'}, signal,
    body:JSON.stringify({type:'client-request',rpcId:crypto.randomUUID?.() ?? String(Date.now()),method:'session/list',payload:{args:{_request:{}}}}),
  });
  if (!response.ok) throw new Error('session_list_unavailable');
  const {result} = await response.json();
  if (!result?.ok || !Array.isArray(result.value?.items)) throw new Error('session_list_unavailable');
  return result.value.items.flatMap(item => {
    const id = item?.sessionId;
    if (typeof id !== 'string' || !/^[a-zA-Z0-9_-]{1,128}$/.test(id) || id === sessionId) return [];
    const name = item.projections?.values?.title;
    return [{id,title:typeof name === 'string' ? name.trim() : '',updatedAt:item.updatedAt}];
  });
}

function GatewayTransferChatPicker({chats, value, onChange, disabled, label, choose, untitled, search, noMatches}) {
  const h=React.createElement, [query,setQuery]=React.useState('');
  const needle=query.trim().toLocaleLowerCase();
  const matches=new Set(chats.filter(chat=>(chat.title||untitled).toLocaleLowerCase().includes(needle)).map(chat=>chat.id));
  const shown=chats.filter(chat=>chat.id===value||matches.has(chat.id));
  return h('div',{className:'gateway-transfer-picker'},
    chats.length>6&&h('input',{type:'search',className:'gateway-transfer-search',value:query,
      disabled,placeholder:search,'aria-label':search,onChange:event=>setQuery(event.target.value),
      onKeyDown:event=>{if(event.key==='Enter')event.preventDefault();}}),
    h('label',{className:'gateway-file-field'},h('span',null,label),
      h('select',{value,disabled,onChange:event=>onChange(event.target.value)},
        h('option',{value:''},choose),shown.map(chat=>{
          const date=new Date(chat.updatedAt), dateLabel=chat.updatedAt&&Number.isFinite(date.getTime())?date.toLocaleDateString():null;
          return h('option',{key:chat.id,value:chat.id},[chat.title||untitled,dateLabel||(!chat.title?chat.id.slice(-8):null)].filter(Boolean).join(' · '));
        }))),
    needle&&!matches.size&&h('p',{role:'status',className:'gateway-transfer-muted'},noMatches));
}

function registerChatMemoryUI(ctx, primitives) {
  const h = React.createElement;
  const { IconCopyOutlineRegular, IconEditOutlineRegular, IconTrashOutlineRegular, IconPlusOutlineMedium, IconPanelLeftOutlineRegular, IconDatabaseOutlineRegular, IconChevronRightOutlineRegular, MarkdownText } = primitives;
  const english = { tab:'Chat memory', title:'Chat summary', note:'The latest saved summary after compaction. Later messages are not included; the summary is not the full chat history.',
    shared:'Mnemon shared memory is separate from this chat’s saved facts and compaction summary.', factsTitle:'Saved facts in this chat', factsEmpty:'No saved facts in this chat.', factsFailed:'Could not load saved facts. Try again.', retry:'Try again', more:'Load more', delete:'Delete', confirmDelete:'Delete this saved fact?', confirm:'Delete fact', cancel:'Cancel', deleteFailed:'Could not delete this fact. Try again.', deleting:'Deleting…',
    empty:'This chat has no saved compaction summary.', older:'No summary was found in the loaded history.', unavailable:'The summary for the latest compaction is outside the loaded history or unavailable.', loadOlder:'Load older history', loading:'Loading…', failed:'Could not load older history. Try again.', savedAt:'Saved {time}', copy:'Copy summary', copied:'Copied', copyFailed:'Text selected. Use your device menu to copy it.' };
  Object.assign(english,{tab:'Memory',heading:'Chat memory',factsTitle:'Saved entries',search:'Search saved entries',add:'Add entry',edit:'Edit entry',text:'Entry text',save:'Save',saving:'Saving...',close:'Back to chat',refresh:'Refresh',discard:'Discard unsaved changes?',saveFailed:'Could not confirm the save. Your text is still here; refresh before trying again.',conflict:'This entry has changed. Copy your text and refresh before saving.',limit:'The memory storage limit has been reached.',saved:'Entry saved',noMatches:'No matching entries.',factsTab:'Saved entries',summaryTab:'Summary',copyFact:'Copy entry'});
  english.copyEntryFailed='Could not copy. Select the entry text and copy it using your device menu.';
  english.tooLong='An entry must be no larger than 16 KB. Split it into several entries.';
  Object.assign(english,{heading:'Memory',factsTab:'Chat facts',factsTitle:'Permanent facts in this chat',guideDescription:'This chat and Mnemon',chatScope:'This chat',mnemonTab:'Mnemon',mnemonScope:'Profile and project memory',mnemonOpen:'Open Mnemon',mnemonUnavailable:'Mnemon is not available right now.'});
  Object.assign(english,{listView:'Entries',graphView:'Links',showAll:'Show all entries',nodeLinks:'Related facts',linkAdd:'Add link',linkTarget:'Other fact',linkSearch:'Find another fact',linkType:'Relationship',linkNote:'Link note',linkSave:'Link facts',linkRemove:'Remove link',linkConfirm:'Remove this link? Both facts will remain.',linkFailed:'Could not confirm the change. Refresh links before trying again.',graphFailed:'Could not load links. Refresh and try again.',graphEmpty:'This fact has no links.',incoming:'Incoming',outgoing:'Outgoing',rel_related:'Related',rel_supports:'Supports',rel_contradicts:'Contradicts',rel_depends_on:'Depends on',rel_updates:'Updates',rel_part_of:'Part of',noTarget:'Choose a fact',openNode:'Open this fact',linkNoteLong:'A link note must be no larger than 256 bytes.'});
  Object.assign(english,{sources:'Sources',sourceLines:'Lines',sourceVersion:'Observed file version',selectedFact:'Selected fact',incoming:'To this fact',outgoing:'From this fact',graphMissing:'This fact was deleted.'});
  Object.assign(english,{transfer:'Transfer memory',transferImport:'Import from a chat',transferExport:'Export to a chat',transferFrom:'Source chat',transferTo:'Destination chat',transferChoose:'Choose a chat',transferUntitled:'Untitled chat',transferNoChats:'No other chats.',transferChatsFailed:'Could not load chats.',transferCheck:'Preview',transferCopy:'Copy memory',transferFacts:'Facts',transferLinks:'Links',transferNew:'To copy',transferExisting:'Already present',transferEmpty:'No new facts or links to copy.',transferChanged:'Memory changed in one of the chats. Preview again.',transferMissing:'One of the chats is no longer available.',transferFailed:'Could not confirm the transfer. Preview again to see what remains.',transferCheckFailed:'Could not preview the transfer. Try again.',transferDone:'Copied (facts: {facts}, links: {links}).'});
  Object.assign(english,{transferCurrent:'Current chat',transferSearch:'Find a chat',transferNoMatches:'No matching chats.',transferPreviewTitle:'Transfer preview',transferClose:'Close'});
  Object.assign(english,{factStatus:'Fact priority',status_normal:'Normal',status_important:'Important',status_critical:'Critical',
    statusSaved:'Fact priority updated.',statusConflict:'The priority changed elsewhere. Check the current value and try again.',statusFailed:'Could not update the priority.'});
  Object.assign(english,{moveUp:'Move fact up',moveDown:'Move fact down',moveSaved:'Fact order updated.',
    moveConflict:'The fact list changed. Check the new order and try again.',moveFailed:'Could not change the fact order.'});
  Object.assign(english,{usageTitle:'Memory usage',usageChat:'This chat',usageCounts:'Facts: {facts} · Links: {links}',
    usageProfile:'All chats in this profile',usageUsed:'{used} / {limit} used',usageRemaining:'{size} free',usageReserved:'{size} reserved for undo',usageUnavailable:'Memory usage unavailable',usageBytes:'{value} B',usageKiB:'{value} KiB',usageMiB:'{value} MiB'});
  Object.assign(english,{nodeOpen:'Open graph',nodeTitle:'Fact graph',nodeCount:'Facts: {shown} of {total} · Links: {links} of {allLinks}',
    nodeEmpty:'No facts to show yet.',nodeFailed:'Could not load the graph.',nodeStale:'Memory has changed. Refresh the graph.',nodeDetailFailed:'Could not load this fact.',
    nodeCanvas:'Fact graph canvas',nodeSearch:'Find a fact',nodeNoMatch:'No matching loaded facts.',nodeZoomIn:'Zoom in',nodeZoomOut:'Zoom out',nodeReset:'Reset zoom',nodeCloseDetail:'Close fact',nodeLoadedLinks:'Loaded links',nodeNoLoadedLinks:'No links in the loaded part of the graph.'});
  ctx.effect(() => ctx.locale.register('gatewayChatMemory', { en:english }), 'gateway: chat memory language');
  const t = ctx.locale.bind('gatewayChatMemory');
  const emptyWindow = { entries:[], hasMore:false }, emptySubscribe = () => () => {};
  const review=typeof registerMemoryReviewUI==='function'?registerMemoryReviewUI(ctx,primitives):null;
  const drafts=new Map(),draftListeners=new Set();
  const subscribeDraft=listener=>{draftListeners.add(listener);return()=>draftListeners.delete(listener);};
  function requestId(){if(crypto.randomUUID)return crypto.randomUUID();const bytes=crypto.getRandomValues(new Uint8Array(16));bytes[6]=(bytes[6]&15)|64;bytes[8]=(bytes[8]&63)|128;const hex=Array.from(bytes,b=>b.toString(16).padStart(2,'0')).join('');return`${hex.slice(0,8)}-${hex.slice(8,12)}-${hex.slice(12,16)}-${hex.slice(16,20)}-${hex.slice(20)}`;}
  function rememberDraft(sessionId,draft){if(draft)drafts.set(sessionId,draft);else drafts.delete(sessionId);for(const listener of draftListeners)listener();}
  ctx.effect(()=>{const guard=event=>{if([...drafts.values()].some(draft=>draft.text!==draft.original)){event.preventDefault();event.returnValue='';}};globalThis.addEventListener('beforeunload',guard);return()=>globalThis.removeEventListener('beforeunload',guard);},'gateway: unsaved memory');
  function toolButton(Icon,label,onClick,disabled=false) { return h('button',{type:'button',className:'gateway-memory-icon',title:label,'aria-label':label,onClick,disabled},Icon?h(Icon,{size:19}):label); }
  const mnemonAvailable=()=>ctx.slots.entriesOfSlot('main').some(entry=>entry.options.key==='mnemon');
  const factStatuses=['normal','important','critical'];
  function factStatus(record) { return factStatuses.includes(record.status)?record.status:'normal'; }
  async function nodeRequest(sessionId, body, signal) {
    const response=await fetch(new URL('./api/gateway-chat-memory/manage',document.baseURI),{
      method:'POST',credentials:'same-origin',mode:'same-origin',redirect:'error',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({sessionId,...body}),signal:AbortSignal.any([signal,AbortSignal.timeout(15000)])
    });
    const result=await response.json();
    if(!response.ok||result?.ok!==true||!result.value)throw new Error(result?.error?.code||'unavailable');
    return result.value;
  }
  function FactNodeCanvas({sessionId,onBack}) {
    const [data,setData]=React.useState(null),[loading,setLoading]=React.useState(true),[error,setError]=React.useState('');
    const [zoom,setZoom]=React.useState(1),[query,setQuery]=React.useState(''),[selected,setSelected]=React.useState(null);
    const [detail,setDetail]=React.useState(null),[detailError,setDetailError]=React.useState(false);
    const requestRef=React.useRef(null),viewport=React.useRef(null),buttons=React.useRef(new Map()),drag=React.useRef(null);
    const arrowId=React.useId(), heading=React.useId();
    async function load(page) {
      requestRef.current?.abort();
      const controller=new AbortController();requestRef.current=controller;
      setLoading(true);setError('');
      try {
        const previous=page?data:null;
        const value=await nodeRequest(sessionId,{action:'graphOverview',page,...(previous?{expectedRevision:previous.revision}:{})},controller.signal);
        if(controller.signal.aborted)return;
        if(!chatMemoryValidRevision(value.revision)||!Array.isArray(value.nodes)||!Array.isArray(value.edges)||
          !Number.isSafeInteger(value.totals?.nodes)||!Number.isSafeInteger(value.totals?.edges)||
          !(value.nextPage===null||value.nextPage===page+1)||(previous&&value.revision!==previous.revision))throw Error('Invalid graph');
        setData({...value,nodes:[...(previous?.nodes??[]),...value.nodes],edges:[...(previous?.edges??[]),...value.edges]});
        if(!page){setSelected(null);viewport.current?.scrollTo(0,0);}
      }catch(reason){if(!controller.signal.aborted)setError(reason.message==='conflict'?'nodeStale':'nodeFailed');}
      finally{if(!controller.signal.aborted)setLoading(false);}
    }
    React.useEffect(()=>{load(0);return()=>requestRef.current?.abort();},[sessionId]);
    const revision=data?.revision;
    React.useEffect(()=>{
      setDetail(null);setDetailError(false);
      if(!selected||!revision)return;
      const controller=new AbortController();
      nodeRequest(sessionId,{action:'get',id:selected,expectedRevision:revision},controller.signal).then(value=>{
        if(!controller.signal.aborted&&value.record?.id===selected)setDetail(value.record);
        else if(!controller.signal.aborted)setDetailError(true);
      }).catch(()=>{if(!controller.signal.aborted)setDetailError(true);});
      return()=>controller.abort();
    },[sessionId,selected,revision]);
    const layout=React.useMemo(()=>memoryNodeLayout(data?.nodes??[],data?.edges??[]),[data]);
    const nodesById=React.useMemo(()=>new Map(data?.nodes.map(node=>[node.id,node])??[]),[data]);
    const visibleEdges=React.useMemo(()=>data?.edges.filter(edge=>layout.positions.has(edge.fromId)&&layout.positions.has(edge.toId))??[],[data,layout]);
    const near=new Set(selected?[selected]:[]),incident=[];
    for(const edge of data?.edges??[])if(edge.fromId===selected||edge.toId===selected){
      near.add(edge.fromId);near.add(edge.toId);incident.push(edge);
    }
    const needle=query.trim().toLocaleLowerCase();
    const matches=needle?data?.nodes.filter(node=>node.preview.toLocaleLowerCase().includes(needle))??[]:[];
    const nodeLabel=node=>node.preview.replace(/\s+/g,' ').trim();
    function focusNode(id) {
      setSelected(id);
      const button=buttons.current.get(id);
      button?.scrollIntoView({block:'nearest',inline:'nearest'});button?.focus({preventScroll:true});
    }
    function changeZoom(next) {
      const area=viewport.current;
      if(area){const x=area.scrollLeft/zoom,y=area.scrollTop/zoom;requestAnimationFrame(()=>area.scrollTo(x*next,y*next));}
      setZoom(next);
    }
    function pointerDown(event){
      if(event.pointerType!=='mouse'||event.button!==0||event.target.closest('button'))return;
      const area=event.currentTarget;drag.current={id:event.pointerId,x:event.clientX,y:event.clientY,left:area.scrollLeft,top:area.scrollTop};
      area.setPointerCapture(event.pointerId);
    }
    function pointerMove(event){
      const start=drag.current;
      if(start?.id===event.pointerId)event.currentTarget.scrollTo(start.left+start.x-event.clientX,start.top+start.y-event.clientY);
    }
    function edgePath(edge) {
      const a=layout.positions.get(edge.fromId), b=layout.positions.get(edge.toId);
      const dx=b.x-a.x,dy=b.y-a.y;
      const length=Math.hypot(dx,dy)||1;
      const ux=dx/length,uy=dy/length,lead=13,center=nodeHeight/2;
      const x1=a.x+center+ux*lead,y1=a.y+center+uy*lead;
      const x2=b.x+center-ux*lead,y2=b.y+center-uy*lead;
      const bend=Math.min(24,length/9);
      return `M ${x1},${y1} Q ${(x1+x2)/2-uy*bend},${(y1+y2)/2+ux*bend} ${x2},${y2}`;
    }
    const button=(Icon,label,action,disabled)=>toolButton(Icon,label,action,disabled);
    return h('section',{className:'gateway-chat-memory gateway-node-panel','aria-labelledby':heading,'data-gateway-memory-node-panel':true},
      h('header',{className:'gateway-node-header'},button(IconPanelLeftOutlineRegular,t('close'),onBack),h('h2',{id:heading},t('nodeTitle')),h('span',{className:'gateway-memory-context'},t('chatScope')),
        button(primitives.IconRefreshOutlineRegular,t('refresh'),()=>load(0),loading)),
      h('div',{className:'gateway-node-toolbar'},
        h('input',{type:'search',maxLength:256,value:query,placeholder:t('nodeSearch'),'aria-label':t('nodeSearch'),onChange:e=>setQuery(e.target.value),
          onKeyDown:e=>{if(e.key==='Enter'&&matches.length)focusNode(matches[(matches.findIndex(node=>node.id===selected)+1)%matches.length].id);}}),
        h('div',{className:'gateway-node-zoom'},button(primitives.IconSubtractOutlineRegular??(()=>h('span',null,'\u2212')),t('nodeZoomOut'),()=>changeZoom(Math.max(.5,zoom-.25)),zoom<=.5),
          h('button',{type:'button',title:t('nodeReset'),'aria-label':t('nodeReset'),onClick:()=>changeZoom(1)},Math.round(zoom*100)+'%'),
          button(IconPlusOutlineMedium,t('nodeZoomIn'),()=>changeZoom(Math.min(1.75,zoom+.25)),zoom>=1.75))),
      h('div',{className:'gateway-node-status'},
        data&&h('span',null,t('nodeCount',{shown:data.nodes.length,total:data.totals.nodes,links:visibleEdges.length,allLinks:data.totals.edges})),
        data?.nextPage!=null&&h('button',{type:'button',disabled:loading||error==='nodeStale',onClick:()=>load(data.nextPage)},t('more'))),
      error&&h('p',{role:'alert',className:'gateway-node-message'},t(error)),
      loading&&!data&&h('p',{role:'status',className:'gateway-node-message'},t('loading')),
      data?.nodes.length===0&&h('p',{className:'gateway-node-message'},t('nodeEmpty')),
      needle&&data&&!matches.length&&h('p',{role:'status',className:'gateway-node-message'},t('nodeNoMatch')),
      h('div',{className:'gateway-node-body'},
        h('div',{className:'gateway-node-viewport',ref:viewport,tabIndex:0,role:'region','aria-label':t('nodeCanvas'),
          onPointerDown:pointerDown,onPointerMove:pointerMove,onLostPointerCapture:()=>{drag.current=null;}},
          !!data?.nodes.length&&h('div',{className:'gateway-node-extent',style:{width:layout.width*zoom,height:layout.height*zoom}},
            h('div',{className:'gateway-node-world',style:{width:layout.width,height:layout.height,transform:`scale(${zoom})`}},
              h('svg',{className:'gateway-node-wires',width:layout.width,height:layout.height,'aria-hidden':true,'data-focused':!!selected},
                h('defs',null,h('marker',{id:arrowId,markerWidth:7,markerHeight:7,refX:6,refY:3.5,orient:'auto'},
                  h('path',{d:'M0,0 L7,3.5 L0,7 z',fill:'context-stroke'}))),
                visibleEdges.map(edge=>h('path',{key:edge.id,d:edgePath(edge),className:'gateway-node-wire','data-relation':edge.relation,
                  'data-highlight':selected&&(edge.fromId===selected||edge.toId===selected)?'yes':undefined,
                  markerEnd:edge.relation==='related'?undefined:`url(#${arrowId})`})) ),
              data.nodes.map(node=>{const pos=layout.positions.get(node.id), label=nodeLabel(node);return h('button',{
                key:node.id,type:'button',className:'gateway-node-card',style:{left:pos.x,top:pos.y,width:nodeWidth,height:nodeHeight},
                ref:element=>{if(element)buttons.current.set(node.id,element);else buttons.current.delete(node.id);},
                'aria-pressed':selected===node.id,'data-priority':factStatus(node),'data-muted':(selected&&!near.has(node.id))||(needle&&!node.preview.toLocaleLowerCase().includes(needle))?'yes':undefined,
                title:label,onClick:()=>setSelected(node.id)},h('i',{className:'gateway-node-dot','aria-hidden':true}),h('span',null,label),h('small',{className:'gateway-node-sr-only'},t('status_'+factStatus(node))));})))),
        selected&&h('aside',{className:'gateway-node-detail','aria-label':t('selectedFact')},
          h('header',null,h('h3',null,t('selectedFact')),button(primitives.IconCloseOutlineRegular,t('nodeCloseDetail'),()=>setSelected(null))),
          detail?.id===selected?h(React.Fragment,null,h('div',{className:'gateway-node-detail-text'},detail.text),h(MemorySources,{record:detail})):
            h('p',{role:detailError?'alert':'status'},t(detailError?'nodeDetailFailed':'loading')),
          h('h4',null,t(data.nextPage===null?'nodeLinks':'nodeLoadedLinks')),
          !incident.length?h('p',null,t(data.nextPage===null?'graphEmpty':'nodeNoLoadedLinks')):h('ul',null,incident.map(edge=>{const target=edge.fromId===selected?edge.toId:edge.fromId;const node=nodesById.get(target);
            return h('li',{key:edge.id},h('small',null,t('rel_'+edge.relation),edge.relation!=='related'?' · '+t(edge.fromId===selected?'outgoing':'incoming'):''),
              node&&h('button',{type:'button',onClick:()=>focusNode(target)},nodeLabel(node)));})))));
  }
  let nodePanelSession;
  function NodePanel() {
    const source=ctx.uiSession?.current;
    // The native main binding uses key and may go absent when the conversation seat unmounts.
    const id=React.useSyncExternalStore(source?fn=>source.subscribe(fn):emptySubscribe,()=>source?.getSnapshot()?.key??nodePanelSession??null);
    const back=()=>{nodePanelSession=undefined;ctx.layout.selectPanel(null);};
    return id?h(FactNodeCanvas,{key:id,sessionId:id,onBack:back}):h('section',{className:'gateway-chat-memory gateway-node-panel'},
      h('header',{className:'gateway-node-header'},toolButton(IconPanelLeftOutlineRegular,t('close'),back),h('h2',null,t('nodeTitle'))),
      h('p',{className:'gateway-node-message'},t('nodeEmpty')));
  }
  function MemoryUsage({sessionId,read,revision}) {
    const [usage,setUsage]=React.useState(null),[failed,setFailed]=React.useState(false);
    const current=React.useRef(read);current.current=read;
    React.useEffect(()=>{
      let pending;
      function refresh(){
        if(document.visibilityState==='hidden'||pending)return;
        const controller=new AbortController();pending=controller;
        current.current({action:'usage',sessionId},controller.signal).then(value=>{
          if(controller.signal.aborted)return;
          const numbers=[value?.chat?.facts,value?.chat?.links,value?.chat?.bytes,value?.profile?.facts,value?.profile?.links,value?.profile?.bytes,value?.remainingBytes,value?.limitBytes,value?.reservedBytes??0];
          if(!numbers.every(n=>Number.isSafeInteger(n)&&n>=0)||value.limitBytes===0)throw new Error('Invalid memory usage');
          setUsage(value);setFailed(false);
        }).catch(()=>{if(!controller.signal.aborted)setFailed(true);}).finally(()=>{if(pending===controller)pending=null;});
      }
      function visibility(){if(document.visibilityState==='hidden'){pending?.abort();pending=null;}else refresh();}
      refresh();
      document.addEventListener('visibilitychange',visibility);
      const interval=setInterval(refresh,15000);
      return()=>{clearInterval(interval);document.removeEventListener('visibilitychange',visibility);pending?.abort();};
    },[sessionId,revision]);
    const locale=ctx.locale.getSnapshot().active;
    const number=new Intl.NumberFormat(locale,{maximumFractionDigits:2});
    function size(bytes){return bytes>=1048576?t('usageMiB',{value:number.format(bytes/1048576)}):
      bytes>=1024?t('usageKiB',{value:number.format(bytes/1024)}):t('usageBytes',{value:number.format(bytes)});}
    const counts=scope=>t('usageCounts',{facts:number.format(scope.facts),links:number.format(scope.links)});
    if(!usage||failed)return h('p',{className:'gateway-memory-usage-placeholder'},t(failed?'usageUnavailable':'loading'));
    return h('section',{className:'gateway-memory-usage','aria-label':t('usageTitle')},
      h('dl',null,
        h('div',null,h('dt',null,t('usageChat')),h('dd',null,counts(usage.chat),' · ',size(usage.chat.bytes))),
        h('div',null,h('dt',null,t('usageProfile')),h('dd',null,counts(usage.profile)))),
      h('meter',{'aria-label':t('usageProfile'),min:0,max:usage.limitBytes,value:Math.min(usage.profile.bytes,usage.limitBytes)}),
      h('small',null,t('usageUsed',{used:size(usage.profile.bytes),limit:size(usage.limitBytes)}),' · ',t('usageRemaining',{size:size(usage.remainingBytes)}),
        usage.reservedBytes>0&&(' · '+t('usageReserved',{size:size(usage.reservedBytes)}))));
  }
  function MemorySources({record}) {
    if(!record.sources?.length)return null;
    return h('details',{className:'gateway-memory-sources'},h('summary',null,`${t('sources')} (${record.sources.length})`),h('ul',null,record.sources.map((source,index)=>h('li',{key:index},
      h('code',null,source.file),source.chapter&&h('span',null,source.chapter),source.line_from&&h('span',null,`${t('sourceLines')}: ${source.line_from}${source.line_to!==undefined?`-${source.line_to}`:''}`),source.anchor&&h('blockquote',null,source.anchor),source.sha256&&h('small',{title:source.sha256},`${t('sourceVersion')}: ${source.sha256.slice(0,12)}`)))));
  }
  function MemoryTransfer({sessionId,read,onClose,onCopied}) {
    const dialog=React.useRef(null), title=React.useId(), operation=React.useRef(null);
    const [attempt,setAttempt]=React.useState(0), [chats,setChats]=React.useState(null), [listFailed,setListFailed]=React.useState(false);
    const [direction,setDirection]=React.useState('import'), [other,setOther]=React.useState(''), [preview,setPreview]=React.useState(null);
    const [pending,setPending]=React.useState(''), [error,setError]=React.useState('');
    React.useEffect(()=>{
      const node=dialog.current;node.showModal();
      return()=>{operation.current?.abort();node.close();};
    },[]);
    React.useEffect(()=>{
      const controller=new AbortController(), timeout=setTimeout(()=>controller.abort('timeout'),15000);
      gatewayListOtherChats(sessionId,controller.signal).then(value=>{
        if(!controller.signal.aborted)setChats(value);
      }).catch(()=>{if(!controller.signal.aborted||controller.signal.reason==='timeout')setListFailed(true);})
        .finally(()=>clearTimeout(timeout));
      return()=>{clearTimeout(timeout);controller.abort();};
    },[sessionId,attempt]);
    function close(){if(pending!=='transfer')onClose();}
    function reset(){setPreview(null);setError('');}
    async function run(action) {
      if(operation.current||!other||(action==='transfer'&&!preview))return;
      const controller=new AbortController();operation.current=controller;setPending(action);setError('');
      try {
        const body={action,sessionId,otherSessionId:other,direction};
        if(action==='transfer'){body.sourceRevision=preview.sourceRevision;body.targetRevision=preview.targetRevision;}
        const value=await read(body,controller.signal);
        if(controller.signal.aborted)return;
        if(action==='transfer')onCopied(direction,value);
        else setPreview(value);
      } catch(error) {
        if(controller.signal.aborted)return;
        setPreview(null);
        setError(({conflict:'transferChanged',limit:'limit',session_unavailable:'transferMissing'})[error.message] ||
          (action==='transfer'?'transferFailed':'transferCheckFailed'));
      } finally {
        operation.current=null;
        if(!controller.signal.aborted)setPending('');
      }
    }
    const hasNew=preview&&(preview.facts.added>0||preview.links.added>0);
    const number=new Intl.NumberFormat();
    const selected=chats?.find(chat=>chat.id===other);
    const names=[t('transferCurrent'),selected?.title||t('transferUntitled')];
    if(direction==='import')names.reverse();
    return h('dialog',{ref:dialog,className:'gateway-file-dialog gateway-transfer-dialog gateway-memory-transfer','aria-labelledby':title,
      onCancel:event=>{event.preventDefault();close();}},
      h('header',{className:'gateway-file-dialog-header'},h('h2',{id:title},t('transfer')),
        toolButton(primitives.IconCloseOutlineRegular,t('transferClose'),close,pending==='transfer')),
      h('div',{className:'gateway-file-dialog-content'},
        h('div',{className:'gateway-memory-views',role:'group','aria-label':t('transfer')},['import','export'].map(value=>h('button',{type:'button',key:value,
          'aria-pressed':value===direction,disabled:!!pending,onClick:()=>{if(value!==direction){setDirection(value);reset();}}},t(value==='import'?'transferImport':'transferExport')))),
        listFailed?h('div',{role:'alert'},h('p',null,t('transferChatsFailed')),h('button',{type:'button',onClick:()=>{setListFailed(false);setAttempt(attempt+1);}},t('retry'))):
          !chats?h('p',{role:'status'},t('loading')):chats.length===0?h('p',{role:'status'},t('transferNoChats')):
          h(GatewayTransferChatPicker,{chats,value:other,disabled:!!pending,onChange:value=>{setOther(value);reset();},
            label:t(direction==='import'?'transferFrom':'transferTo'),choose:t('transferChoose'),untitled:t('transferUntitled'),
            search:t('transferSearch'),noMatches:t('transferNoMatches')}),
        selected&&h('div',{className:'gateway-transfer-route'},
          h('div',null,h('span',null,t('transferFrom')),h('strong',null,names[0])),
          h(IconChevronRightOutlineRegular,{size:18,'aria-hidden':true}),
          h('div',null,h('span',null,t('transferTo')),h('strong',null,names[1]))),
        preview&&h('div',{role:'status',className:'gateway-transfer-preview'},
          h('h3',null,t('transferPreviewTitle')),h('table',null,
          h('thead',null,h('tr',null,h('td',null),h('th',{scope:'col'},t('transferNew')),h('th',{scope:'col'},t('transferExisting')))),
          h('tbody',null,['facts','links'].map(key=>h('tr',{key},h('th',{scope:'row'},t(key==='facts'?'transferFacts':'transferLinks')),
            h('td',null,number.format(preview[key].added)),h('td',null,number.format(preview[key].skipped)))))),
          !preview.fits?h('p',{className:'gateway-chat-memory-error'},t('limit')):!hasNew?h('p',null,t('transferEmpty')):null),
        error&&h('p',{role:'alert',className:'gateway-chat-memory-error'},t(error))),
      h('footer',{className:'gateway-file-dialog-footer'},
        h('button',{type:'button',disabled:pending==='transfer',onClick:close},t('cancel')),
        h('button',{type:'button',className:'gateway-file-primary',disabled:!!pending||!other||!!(preview&&(!preview.fits||!hasNew)),
          onClick:()=>run(preview?'transfer':'transferPreview')},t(pending?'loading':preview?'transferCopy':'transferCheck'))));
  }
  function MemoryGraph({sessionId,initialId,read,source,onEdit,onList,onFocus}) {
    const [focus,setFocus]=React.useState(initialId),[value,setValue]=React.useState(null),[error,setError]=React.useState(''),[pending,setPending]=React.useState(false);
    const [adding,setAdding]=React.useState(false),[query,setQuery]=React.useState(''),[targets,setTargets]=React.useState({records:[],nextCursor:null}),[target,setTarget]=React.useState(''),[relation,setRelation]=React.useState('related'),[note,setNote]=React.useState(''),[confirm,setConfirm]=React.useState(null);
    const current=React.useRef({}),epoch=React.useRef(0),controllers=React.useRef(new Set()),live=React.useRef(null),ops=React.useRef(null),root=React.useRef(null),searchEpoch=React.useRef(0),actionBusy=React.useRef(false),targetsRef=React.useRef(targets);
    targetsRef.current=targets;
    current.current={value,focus,read,pending,adding};
    async function call(body,signal) {
      const controller=new AbortController();controllers.current.add(controller);
      const abort=()=>controller.abort();signal?.addEventListener('abort',abort,{once:true});if(signal?.aborted)controller.abort();
      try{return await current.current.read({sessionId,...body},controller.signal);}finally{signal?.removeEventListener('abort',abort);controllers.current.delete(controller);}
    }
    async function load(cursor=0,signal) {
      const token=++epoch.current,previous=current.current.value;
      const focused=current.current.focus??previous?.nodeId;
      const graph=await call({action:'graph',...(focused?{id:focused}:{}),cursor,limit:12,...(cursor&&previous?{expectedRevision:previous.revision}:{})},signal);
      if(token!==epoch.current||signal?.aborted)return;
      if(!chatMemoryValidRevision(graph?.revision)||!Array.isArray(graph.nodes)||!Array.isArray(graph.edges)||!(graph.nextCursor===null||Number.isSafeInteger(graph.nextCursor)&&graph.nextCursor>cursor))throw Error('Invalid graph');
      const detail=graph.nodeId?await call({action:'get',id:graph.nodeId,expectedRevision:graph.revision},signal):{record:null};
      if(token!==epoch.current||signal?.aborted)return;
      if(graph.nodeId&&detail.record?.id!==graph.nodeId)throw Error('Invalid graph detail');
      setValue({...graph,record:detail.record,nodes:cursor?[...new Map([...previous.nodes,...graph.nodes].map(node=>[node.id,node])).values()]:graph.nodes,edges:cursor?[...previous.edges,...graph.edges.filter(edge=>!previous.edges.some(old=>old.id===edge.id))]:graph.edges});if(graph.nodeId)onFocus(graph.nodeId);setError('');
    }
    async function refresh(cursor=0) {if(actionBusy.current)return;actionBusy.current=true;setPending(true);try{await load(cursor);}catch{setError('graphFailed');}finally{actionBusy.current=false;setPending(false);}}
    ops.current={async refresh(signal){
      const previous=current.current.value;
      if(previous){const result=await call({action:'revision'},signal);if(!chatMemoryValidRevision(result.revision))throw Error('Invalid revision');if(result.revision===previous.revision)return;}
      if(chatMemoryHasSelection(root.current))return;
      try{await load(0,signal);}catch(error){if(!signal.aborted)setError('graphFailed');throw error;}
    }};
    React.useEffect(()=>{
      current.current.value=null;setValue(null);setError('');setConfirm(null);
      const controller=createChatMemoryLive({refresh:signal=>ops.current.refresh(signal),blocked:()=>current.current.pending||current.current.adding});live.current=controller;
      let departed=false;
      const sync=()=>controller.setActive(!departed&&document.visibilityState!=='hidden');
      const wake=()=>{departed=false;sync();controller.wake(true);};
      const hide=()=>{departed=true;sync();};
      const selection=()=>{if(!chatMemoryHasSelection(root.current))controller.wake();};
      const events=[['focus',wake],['pageshow',wake],['visibilitychange',wake],['pagehide',hide],['selectionchange',selection]];
      for(const [name,handler]of events)globalThis.addEventListener(name,handler);sync();
      let signal=source?chatMemoryToolSignal(source.getSnapshot()):'';
      const unsubscribe=source?.subscribe(()=>{const next=chatMemoryToolSignal(source.getSnapshot());if(next!==signal){signal=next;controller.wake();}});
      return()=>{epoch.current++;searchEpoch.current++;controller.dispose();unsubscribe?.();for(const [name,handler]of events)globalThis.removeEventListener(name,handler);for(const request of controllers.current)request.abort();controllers.current.clear();};
    },[sessionId,focus,source]);
    async function findTargets(cursor=0,signal,token=++searchEpoch.current){
      const result=await call({action:'list',query:query.trim(),cursor,limit:50,includeRevision:true},signal);
      if(signal?.aborted||token!==searchEpoch.current)return;
      const page=chatMemoryValidatePage(result,cursor);
      if(cursor&&targetsRef.current.revision!==page.revision)throw Error('Memory revision changed');
      setTargets(previous=>({...page,records:cursor&&previous.revision===page.revision?[...previous.records,...page.records]:page.records}));
    }
    React.useEffect(()=>{if(!adding)return;const controller=new AbortController(),token=++searchEpoch.current;setTarget('');setTargets({records:[],nextCursor:null});const timer=setTimeout(()=>findTargets(0,controller.signal,token).catch(()=>{if(!controller.signal.aborted)setError('graphFailed');}),250);return()=>{clearTimeout(timer);controller.abort();searchEpoch.current++;};},[adding,query,sessionId]);
    async function change(body){
      if(actionBusy.current)return;actionBusy.current=true;setPending(true);setError('');
      try{await call(body);setAdding(false);setConfirm(null);setNote('');setTarget('');await load();}
      catch{setError('linkFailed');}finally{actionBusy.current=false;setPending(false);}
    }
    const record=value?.record,noteLong=new TextEncoder().encode(note).length>256;
    return h('section',{className:'gateway-memory-graph',ref:root,'aria-label':t('nodeLinks')},
      h('div',{className:'gateway-memory-graph-tools'},h('h3',null,t('graphView'),value&&h('span',{className:'gateway-memory-count'},value.total)),toolButton(primitives.IconRefreshOutlineRegular,t('refresh'),()=>refresh(),pending),h('button',{type:'button',disabled:pending||!record,onClick:()=>{setAdding(!adding);setError('');}},IconPlusOutlineMedium&&h(IconPlusOutlineMedium,{size:18}),t('linkAdd'))),
      error&&h('p',{role:'alert',className:'gateway-chat-memory-error'},t(error)),
      !value&&!error&&h('p',{role:'status'},t('loading')),
      value&&!record&&h('div',{className:'gateway-chat-memory-empty'},h('p',null,t(focus||initialId?'graphMissing':'factsEmpty')),h('button',{type:'button',onClick:onList},t('showAll'))),
      record&&h(React.Fragment,null,
        h('article',{className:'gateway-memory-focus','data-memory-node':record.id},h('header',{className:'gateway-memory-focus-head'},h('span',null,t('selectedFact')),toolButton(IconEditOutlineRegular,t('edit'),()=>onEdit(record),pending)),h('div',{className:'gateway-memory-node-text'},h(MarkdownText,{text:record.text,streaming:false})),h(MemorySources,{record})),
        adding&&h('form',{className:'gateway-memory-link-form',onSubmit:event=>{event.preventDefault();change({action:'link',fromId:record.id,toId:target,relation,note});}},
          h('label',null,t('linkSearch'),h('input',{type:'search',value:query,maxLength:512,disabled:pending,onChange:event=>setQuery(event.target.value)})),
          h('label',null,t('linkTarget'),h('select',{'aria-label':t('linkTarget'),value:target,disabled:pending,onChange:event=>setTarget(event.target.value)},h('option',{value:''},t('noTarget')),targets.records.filter(node=>node.id!==record.id).map(node=>h('option',{key:node.id,value:node.id},[...node.text].slice(0,120).join(''))))),
          targets.nextCursor!==null&&h('button',{type:'button',disabled:pending,onClick:()=>findTargets(targets.nextCursor).catch(()=>setError('graphFailed'))},t('more')),
          h('label',null,t('linkType'),h('select',{'aria-label':t('linkType'),value:relation,disabled:pending,onChange:event=>setRelation(event.target.value)},['related','supports','contradicts','depends_on','updates','part_of'].map(type=>h('option',{key:type,value:type},t('rel_'+type))))),
          h('label',null,t('linkNote'),h('input',{value:note,disabled:pending,onChange:event=>setNote(event.target.value)})),noteLong&&h('p',{role:'alert',className:'gateway-chat-memory-error'},t('linkNoteLong')),
          h('div',{className:'gateway-memory-editor-actions'},h('button',{type:'button',disabled:pending,onClick:()=>setAdding(false)},t('cancel')),h('button',{type:'submit',disabled:pending||!target||noteLong,className:'gateway-memory-primary'},t('linkSave')))),
        !value.edges.length&&h('p',{className:'gateway-chat-memory-empty'},t('graphEmpty')),
        h('ul',{className:'gateway-memory-edge-list'},value.edges.map(edge=>{
          const outgoing=edge.fromId===record.id,node=value.nodes.find(node=>node.id===(outgoing?edge.toId:edge.fromId));
          return h('li',{key:edge.id,className:'gateway-memory-edge','data-relation':edge.relation},
            h('div',{className:'gateway-memory-edge-label'},h('span',{className:'gateway-memory-relation'},t('rel_'+edge.relation)),edge.relation!=='related'&&h('span',{className:'gateway-memory-direction'},IconChevronRightOutlineRegular&&h(IconChevronRightOutlineRegular,{size:14,style:{transform:`rotate(${outgoing?90:-90}deg)`}}),t(outgoing?'outgoing':'incoming')),toolButton(IconTrashOutlineRegular,t('linkRemove'),()=>setConfirm(edge.id),pending)),
            h('button',{type:'button',className:'gateway-memory-neighbor',title:t('openNode'),disabled:pending,onClick:()=>{setAdding(false);setFocus(node.id);onFocus(node.id);}},node?.preview,IconChevronRightOutlineRegular&&h(IconChevronRightOutlineRegular,{size:18})),
            edge.note&&h('p',{className:'gateway-memory-edge-note'},edge.note),confirm===edge.id&&h('div',{className:'gateway-chat-memory-confirm'},h('span',null,t('linkConfirm')),h('button',{type:'button',disabled:pending,onClick:()=>change({action:'unlink',id:edge.id})},t('linkRemove')),h('button',{type:'button',disabled:pending,onClick:()=>setConfirm(null)},t('cancel'))));
        })),value.nextCursor!==null&&h('button',{type:'button',disabled:pending,onClick:()=>refresh(value.nextCursor)},t('more'))));
  }
  function ChatMemoryView({ sessionId, onBack, onOpenGraph, onOpenReview, visible = true }) {
    React.useSyncExternalStore(fn => ctx.locale.subscribe(fn), () => ctx.locale.getSnapshot().active);
    const canOpenMnemon=React.useSyncExternalStore(fn=>ctx.slots.subscribe('main',fn),mnemonAvailable);
    const modeId=React.useId();
    const binding = sessionId ? ctx.sessions.binding(sessionId) : undefined, source = binding?.eventSource;
    const subscribe = React.useCallback(fn => source ? source.subscribe(fn) : emptySubscribe(), [source]);
    const snapshot = React.useCallback(() => source ? source.getSnapshot() : emptyWindow, [source]);
    const window = React.useSyncExternalStore(subscribe, snapshot);
    const [loading, setLoading] = React.useState(false), [notice, setNotice] = React.useState('');
    const [transferSession,setTransferSession]=React.useState(null), [graphVersion,setGraphVersion]=React.useState(0);
    React.useEffect(()=>{if(!visible||transferSession!==sessionId)setTransferSession(null);},[sessionId,visible,transferSession]);
    const [mode,setMode]=React.useState('facts'),[query,setQuery]=React.useState(''),[search,setSearch]=React.useState(''),[saving,setSaving]=React.useState(false),[factView,setFactView]=React.useState('list'),[graphNode,setGraphNode]=React.useState(undefined);
    const draft=React.useSyncExternalStore(subscribeDraft,()=>drafts.get(sessionId)||null),setDraft=value=>rememberDraft(sessionId,value);
    const dirty=!!draft&&draft.text!==draft.original;
    const tooLong=!!draft&&new TextEncoder().encode(draft.text).length>16384;
    React.useEffect(()=>{setSaving(false);setQuery('');setSearch('');setMode('facts');setFactView('list');setGraphNode(undefined);},[sessionId]);
    const content = React.useRef(null), pane = React.useRef(null), scrollAnchor = React.useRef(null), deferredSelection = React.useRef(false), moveFocus=React.useRef(null);
    const [facts, setFacts] = React.useState({ sessionId:null, records:[], cursor:null, pages:0, revision:null, loading:false, error:'', deleting:null, confirm:null });
    const factsRef=React.useRef(facts), live=React.useRef(null), liveOps=React.useRef(null), readBusy=React.useRef(false), shown=React.useRef(false), departed=React.useRef(false);
    factsRef.current=facts;
    shown.current=visible&&mode==='facts'&&factView==='list'&&document.visibilityState!=='hidden'&&!departed.current;
    const generation = React.useRef(0), controllers = React.useRef(new Set()), readControllers=React.useRef(new Set()), readGeneration=React.useRef(0), busy = React.useRef(false),searchRef=React.useRef(search);
    searchRef.current=search;
    function cancelReads(){live.current?.invalidate();readGeneration.current++;readBusy.current=false;for(const controller of readControllers.current)controller.abort();readControllers.current.clear();}
    React.useEffect(()=>{if(saving||facts.deleting)return;const timer=setTimeout(()=>setSearch(query.trim()),250);return()=>clearTimeout(timer);},[query,saving,facts.deleting]);
    const currentSession = React.useRef(sessionId);
    if (currentSession.current !== sessionId) { currentSession.current = sessionId; generation.current++; }
    React.useEffect(() => {
      generation.current++;
      busy.current = false;
      return () => { generation.current++; controllers.current.forEach(controller => controller.abort()); controllers.current.clear(); };
    }, [sessionId]);
    React.useEffect(()=>{cancelReads();const empty={sessionId,records:[],cursor:null,pages:0,revision:null,loading:false,error:'',deleting:null,confirm:null};factsRef.current=empty;setFacts(empty);return cancelReads;},[sessionId,search]);
    async function request(body, token, signal) {
      if(signal?.aborted||token!==generation.current)throw new Error('Stale response');
      const controller = new AbortController(); controllers.current.add(controller);
      if(['list','revision','graph','get'].includes(body.action))readControllers.current.add(controller);
      const abort=()=>controller.abort();signal?.addEventListener('abort',abort,{once:true});if(signal?.aborted)controller.abort();
      const timeout=setTimeout(()=>controller.abort(),15000);
      try {
        const response = await fetch(new URL('./api/gateway-chat-memory/manage', document.baseURI), { method:'POST', credentials:'same-origin', mode:'same-origin', redirect:'error', headers:{ 'Content-Type':'application/json' }, body:JSON.stringify(body), signal:controller.signal });
        if(response.status===401||response.status===403)throw Object.assign(new Error('Authentication required'),{status:response.status});
        const result = await response.json();
        if (!response.ok || controller.signal.aborted || token !== generation.current || !result || result.ok !== true || !result.value) throw new Error(result?.error?.code || 'Invalid or stale response');
        return result.value;
      } finally { clearTimeout(timeout);signal?.removeEventListener('abort',abort);controllers.current.delete(controller);readControllers.current.delete(controller); }
    }
    async function loadFacts(cursor, token = generation.current) {
      if (!sessionId || busy.current || !shown.current) return;
      cancelReads();const readToken=readGeneration.current;
      readBusy.current=true;
      let failure;
      setFacts(previous => ({ ...previous, loading:true, error:'' }));
      try {
        const previous=factsRef.current, read=offset=>{if(readToken!==readGeneration.current||!shown.current)throw new Error('Stale read');return request({action:'list',sessionId,limit:50,cursor:offset,query:searchRef.current,includeRevision:true},token);};
        let value;
        if(cursor===0)value=await chatMemoryReadPages(read,Math.max(1,previous.pages),undefined,previous.pages>0&&previous.cursor===null);
        else {
          const page=chatMemoryValidatePage(await read(cursor),cursor);
          value=page.revision===previous.revision?{records:[...previous.records,...page.records.filter(record=>!previous.records.some(existing=>existing.id===record.id))],cursor:page.nextCursor,pages:previous.pages+1,revision:page.revision}:await chatMemoryReadPages(read,previous.pages+1,page.revision);
        }
        if (token === generation.current&&readToken===readGeneration.current&&shown.current) {scrollAnchor.current=chatMemoryScrollAnchor(pane.current);setFacts(previous => ({ ...previous, ...value, loading:false, error:'' }));}
      } catch(error) { failure=error;if (token === generation.current&&readToken===readGeneration.current) setFacts(previous => ({ ...previous, loading:false, error:'factsFailed' })); }
      finally {if(readToken===readGeneration.current){readBusy.current=false;live.current?.settled(failure);}}
    }
    liveOps.current={async refresh(signal){
      if(!sessionId||!shown.current||readBusy.current||busy.current)return;
      const previous=factsRef.current,token=generation.current,readToken=readGeneration.current;
      const value=previous.pages?await request({action:'revision',sessionId},token,signal):null;
      if(value&&!chatMemoryValidRevision(value.revision))throw new Error('Invalid revision');
      if(value?.revision===previous.revision)return;
      if(chatMemoryHasSelection(pane.current)){deferredSelection.current=true;return;}
      const next=await chatMemoryReadPages(cursor=>request({action:'list',sessionId,limit:50,cursor,query:searchRef.current,includeRevision:true},token,signal),Math.max(1,previous.pages),value?.revision,previous.pages>0&&previous.cursor===null);
      if(signal.aborted||token!==generation.current||readToken!==readGeneration.current||!shown.current)return;
      if(chatMemoryHasSelection(pane.current)){deferredSelection.current=true;return;}
      deferredSelection.current=false;
      scrollAnchor.current=chatMemoryScrollAnchor(pane.current);
      setFacts(current=>({...current,...next,loading:false,error:''}));
    },blocked:()=>readBusy.current||busy.current};
    React.useEffect(()=>{
      const controller=createChatMemoryLive({refresh:signal=>liveOps.current.refresh(signal),blocked:()=>liveOps.current.blocked()});
      live.current=controller;
      const sync=()=>controller.setActive(!!sessionId&&shown.current&&document.visibilityState!=='hidden');
      const focus=()=>{sync();controller.wake(true);};
      const visibility=()=>{departed.current=false;shown.current=visibleRef.current&&document.visibilityState!=='hidden';sync();if(shown.current)controller.wake(true);else {cancelReads();setFacts(current=>({...current,loading:false}));}};
      const hide=()=>{departed.current=true;shown.current=false;controller.setActive(false);cancelReads();setFacts(current=>({...current,loading:false}));};
      const selection=()=>{if(deferredSelection.current&&!chatMemoryHasSelection(pane.current)){deferredSelection.current=false;controller.wake();}};
      const events=[['focus',focus,false],['pageshow',visibility,false],['pagehide',hide,false],['visibilitychange',visibility,true],['selectionchange',selection,true]];
      for(const [name,listener,capture] of events)globalThis.addEventListener(name,listener,capture);
      return()=>{controller.dispose();for(const [name,listener,capture] of events)globalThis.removeEventListener(name,listener,capture);if(live.current===controller)live.current=null;};
    },[sessionId,search]);
    const visibleRef=React.useRef(visible&&mode==='facts'&&factView==='list');visibleRef.current=visible&&mode==='facts'&&factView==='list';
    React.useEffect(()=>{live.current?.setActive(!!sessionId&&shown.current);if(!shown.current){cancelReads();setFacts(current=>({...current,loading:false}));}else if(!factsRef.current.pages&&!readBusy.current)loadFacts(0);},[sessionId,search,visible,mode,factView]);
    React.useEffect(()=>{if(!source)return;let signal=chatMemoryToolSignal(source.getSnapshot());return source.subscribe(()=>{const next=chatMemoryToolSignal(source.getSnapshot());if(next!==signal){signal=next;live.current?.wake();}});},[source]);
    (React.useLayoutEffect||React.useEffect)(()=>{
      chatMemoryRestoreScroll(scrollAnchor.current);scrollAnchor.current=null;
      const focus=moveFocus.current;
      if(!focus||facts.loading||saving)return;
      moveFocus.current=null;
      if(focus.sessionId!==sessionId)return;
      const row=Array.from(pane.current?.querySelectorAll('[data-memory-record-id]')??[]).find(node=>node.dataset.memoryRecordId===focus.id);
      const button=row?.querySelector(`[data-move-fact="${focus.direction}"]:not(:disabled)`)??row?.querySelector('[data-move-fact]:not(:disabled)');
      button?.focus({preventScroll:true});
    },[facts.records,facts.loading,saving,sessionId]);
    async function deleteFact(id) {
      if (busy.current || !sessionId) return;
      const token = generation.current; busy.current = true;cancelReads();
      setFacts(previous => ({ ...previous, deleting:id, loading:false,error:'' }));
      try {
        const value = await request({ action:'delete', sessionId, id }, token);
        if (value.deleted !== true) throw new Error('Not deleted');
        if (token === generation.current) setFacts(previous => ({ ...previous, records:previous.records.filter(record=>record.id!==id), deleting:null, confirm:null }));
      } catch { if (token === generation.current) {setNotice(t('deleteFailed'));setFacts(previous => ({ ...previous, deleting:null }));} }
      finally { if (token === generation.current) busy.current = false; }
      if(token===generation.current)loadFacts(0,token);
    }
    async function changeStatus(record,status) {
      const expectedStatus=factStatus(record);
      if(busy.current||!factStatuses.includes(status)||status===expectedStatus)return;
      const token=generation.current;busy.current=true;cancelReads();setSaving(true);setNotice('');
      try {
        const value=await request({action:'setStatus',sessionId,id:record.id,status,expectedStatus},token);
        if(value.record?.id!==record.id||factStatus(value.record)!==status)throw Error('statusFailed');
        if(token===generation.current)setNotice(t('statusSaved'));
      } catch(error) {
        if(token===generation.current)setNotice(t(error.message==='conflict'?'statusConflict':error.message==='limit'?'limit':'statusFailed'));
      } finally {
        if(token===generation.current){busy.current=false;setSaving(false);loadFacts(0,token);}
      }
    }
    async function moveFact(id,direction) {
      if(busy.current||query.trim()||search||!chatMemoryValidRevision(facts.revision))return;
      const token=generation.current;busy.current=true;cancelReads();setSaving(true);setNotice('');
      try {
        const value=await request({action:'moveFact',sessionId,id,direction,expectedRevision:facts.revision},token);
        if(typeof value.moved!=='boolean')throw Error('moveFailed');
        if(token===generation.current&&value.moved)setNotice(t('moveSaved'));
      } catch(error) {
        if(token===generation.current)setNotice(t(error.message==='conflict'?'moveConflict':'moveFailed'));
      } finally {
        if(token===generation.current){
          moveFocus.current={sessionId,id:String(id),direction};
          busy.current=false;setSaving(false);loadFacts(0,token);
        }
      }
    }
    function edit(record) { if(dirty&&!globalThis.confirm(t('discard')))return;setFactView('list');setNotice('');setDraft(record?{id:record.id,text:record.text,original:record.text}:{requestId:requestId(),text:'',original:''}); }
    function cancelDraft(){if(!dirty||globalThis.confirm(t('discard')))setDraft(null);}
    function backToChat(){if(saving||dirty&&!globalThis.confirm(t('discard')))return;setDraft(null);onBack();}
    function openMnemon(){if(mnemonAvailable())ctx.layout.selectPanel('mnemon');else setNotice(t('mnemonUnavailable'));}
    async function save() {
      if(busy.current||tooLong||!draft?.text.trim())return;
      const token=generation.current;busy.current=true;cancelReads();setFacts(previous=>({...previous,loading:false}));setSaving(true);setNotice('');
      try{
        const value=await request(draft.id?{action:'update',sessionId,id:draft.id,text:draft.text,expectedText:draft.original}:{action:'add',sessionId,text:draft.text,requestId:draft.requestId},token);
        if(!value.record?.id)throw Error('saveFailed');
        if(token===generation.current){setDraft(null);setNotice(t('saved'));}
      }catch(error){if(token===generation.current)setNotice(t(['conflict','limit'].includes(error.message)?error.message:'saveFailed'));}
      finally{if(token===generation.current){busy.current=false;setSaving(false);}}
      if(token===generation.current)loadFacts(0,token);
    }
    async function copyFact(text){const token=generation.current;try{await navigator.clipboard.writeText(text);if(token===generation.current)setNotice(t('copied'));}catch{if(token===generation.current)setNotice(t('copyEntryFailed'));}}
    React.useEffect(() => { setLoading(false); setNotice(''); }, [sessionId]);
    const summary = source ? chatMemorySummary(window) : { state:'loading', hasMore:false };
    async function loadOlder() {
      if (!binding || loading) return;
      const token = generation.current;
      setLoading(true); setNotice('');
      try { await binding.session.loadOlder(); if (token === generation.current && binding.session.getSnapshot().openError) setNotice(t('failed')); } catch { if (token === generation.current) setNotice(t('failed')); }
      finally { if (token === generation.current) setLoading(false); }
    }
    async function copy() {
      const token = generation.current;
      try { await navigator.clipboard.writeText(summary.text); if (token === generation.current) setNotice(t('copied')); }
      catch {
        if (token !== generation.current) return;
        const selection = globalThis.getSelection();
        if (selection && content.current) { const range = document.createRange(); range.selectNodeContents(content.current); selection.removeAllRanges(); selection.addRange(range); }
        setNotice(t('copyFailed'));
      }
    }
    return h('section', { className:'gateway-chat-memory', 'data-gateway-chat-memory':true },
      h('header',{className:'gateway-memory-hub-head'},h('h2',null,t('heading')),toolButton(IconPanelLeftOutlineRegular,t('close'),backToChat,saving)),
      h('div',{className:'gateway-memory-modes',role:'tablist','aria-label':t('heading')},[['facts','factsTab'],['summary','summaryTab'],['mnemon','mnemonTab']].map(([id,label],index,modes)=>h('button',{key:id,id:`${modeId}-${id}`,type:'button',role:'tab','aria-selected':mode===id,'aria-controls':`${modeId}-panel`,tabIndex:mode===id?0:-1,onClick:()=>setMode(id),onKeyDown:event=>{let next;if(event.key==='ArrowRight')next=(index+1)%modes.length;else if(event.key==='ArrowLeft')next=(index+modes.length-1)%modes.length;else if(event.key==='Home')next=0;else if(event.key==='End')next=modes.length-1;else return;event.preventDefault();setMode(modes[next][0]);event.currentTarget.parentElement.children[next].focus();}},t(label)))),
      h('div', { ref:pane,className:'gateway-chat-memory-inner',id:`${modeId}-panel`,role:'tabpanel','aria-labelledby':`${modeId}-${mode}`,tabIndex:0 },
        mode==='facts'&&h('section', { className:'gateway-chat-memory-facts', 'aria-label':t('factsTitle') },
          visible&&sessionId&&h(MemoryUsage,{key:sessionId,sessionId,revision:facts.revision+':'+graphVersion,
            read:(body,signal)=>request(body,generation.current,signal)}),
          h('div',{className:'gateway-memory-facts-head'},h('button',{type:'button',disabled:!sessionId||dirty||saving||!!facts.deleting,onClick:()=>setTransferSession(sessionId)},t('transfer')),
          h('button',{type:'button',className:'gateway-memory-open-nodes',disabled:!sessionId||dirty||saving,onClick:onOpenGraph},h(IconChevronRightOutlineRegular,{size:18}),t('nodeOpen')),
          review&&h('button',{type:'button',disabled:!sessionId||dirty||saving,onClick:onOpenReview},review.label()),
          h('div',{className:'gateway-memory-views','aria-label':t('factsTitle')},[['list','listView'],['graph','graphView']].map(([id,label])=>h('button',{type:'button',key:id,'aria-pressed':factView===id,disabled:saving,onClick:()=>setFactView(id)},t(label))))),
          visible&&transferSession===sessionId&&sessionId&&h(MemoryTransfer,{key:sessionId,sessionId,
            read:(body,signal)=>request(body,generation.current,signal),onClose:()=>setTransferSession(null),
            onCopied:(direction,value)=>{setTransferSession(null);setNotice(t('transferDone',{facts:value.facts.added,links:value.links.added}));
              if(direction==='import'){if(factView==='graph')setGraphVersion(v=>v+1);else live.current?.wake(true);}}}),
          notice&&h('p',{role:'status',className:'gateway-chat-memory-notice'},notice),
          factView==='graph'&&visible&&h(MemoryGraph,{key:sessionId+':'+graphVersion,sessionId,initialId:graphNode,read:(body,signal)=>request(body,generation.current,signal),source,onEdit:edit,onList:()=>setFactView('list'),onFocus:setGraphNode}),
          factView==='list'&&h(React.Fragment,null,
          h('div',{className:'gateway-memory-toolbar'},h('input',{type:'search',value:query,maxLength:512,placeholder:t('search'),'aria-label':t('search'),disabled:saving||!!facts.deleting,onChange:e=>setQuery(e.target.value)}),
            toolButton(primitives.IconRefreshOutlineRegular,t('refresh'),()=>loadFacts(0),facts.loading||saving),h('button',{type:'button',className:'gateway-memory-add',disabled:saving,onClick:()=>edit(null)},IconPlusOutlineMedium&&h(IconPlusOutlineMedium,{size:18}),t('add'))),
          draft&&h('form',{className:'gateway-memory-editor',onSubmit:e=>{e.preventDefault();save();}},h('label',null,t(draft.id?'edit':'add'),h('textarea',{'aria-label':t('text'),value:draft.text,autoFocus:true,disabled:saving,onChange:e=>setDraft({...draft,text:e.target.value})})),tooLong&&h('p',{role:'alert',className:'gateway-chat-memory-error'},t('tooLong')),h('div',{className:'gateway-memory-editor-actions'},h('button',{type:'button',disabled:saving,onClick:cancelDraft},t('cancel')),h('button',{type:'submit',className:'gateway-memory-primary',disabled:saving||facts.loading||!!facts.deleting||tooLong||!draft.text.trim()||!dirty},t(saving?'saving':'save')))),
          facts.sessionId !== sessionId || (facts.loading && facts.records.length === 0) ? h('p', { role:'status' }, t('loading')) : h(React.Fragment, null,
            facts.records.length > 0 ? h('ul', { className:'gateway-chat-memory-fact-list' }, facts.records.map((record,index) => h('li', { key:record.id,'data-memory-record-id':String(record.id) },
              h('div',{className:'gateway-memory-record-actions'},
                h('label',{className:'gateway-memory-status','data-priority':factStatus(record)},
                  h('select',{'aria-label':t('factStatus'),value:factStatus(record),disabled:saving||facts.loading||!!facts.deleting,
                    onChange:event=>changeStatus(record,event.target.value)},factStatuses.map(status=>h('option',{key:status,value:status},t('status_'+status))))),
                toolButton(IconDatabaseOutlineRegular,t('nodeLinks'),()=>{setGraphNode(record.id);setFactView('graph');},saving),toolButton(IconCopyOutlineRegular,t('copyFact'),()=>copyFact(record.text)),toolButton(IconEditOutlineRegular,t('edit'),()=>edit(record),saving),toolButton(IconTrashOutlineRegular,t('delete'),()=>setFacts(previous=>({...previous,confirm:record.id,error:''})),facts.loading||saving)),
              h('div', { className:'gateway-chat-memory-fact-text' }, h(MarkdownText,{text:record.text,streaming:false})),
              h(MemorySources,{record}),
              h('footer',{className:'gateway-memory-record-footer'},
                record.createdAt && !Number.isNaN(new Date(record.createdAt).getTime()) && h('p', { className:'gateway-chat-memory-time' }, t('savedAt', { time:new Date(record.createdAt).toLocaleString(ctx.locale.getSnapshot().active === 'ru' ? 'ru-RU' : 'en-US') })),
                !query.trim()&&!search&&facts.records.length>1&&h('div',{className:'gateway-memory-order'},
                  ['up','down'].map(direction=>{
                    const label=t(direction==='up'?'moveUp':'moveDown');
                    return h('button',{key:direction,type:'button',className:'gateway-memory-icon','data-move-fact':direction,
                      title:label,'aria-label':label,disabled:saving||facts.loading||!!facts.deleting||!facts.revision||
                        (direction==='up'?index===0:index===facts.records.length-1),onClick:()=>moveFact(record.id,direction)},
                    IconChevronRightOutlineRegular?h('span',{className:'gateway-memory-order-icon','aria-hidden':true},h(IconChevronRightOutlineRegular,{size:18})):label);
                  }))),
              facts.confirm === record.id ? h('div', { className:'gateway-chat-memory-confirm' }, h('span', null, t('confirmDelete')), h('button', { type:'button', disabled:!!facts.deleting, onClick:() => deleteFact(record.id) }, facts.deleting === record.id ? t('deleting') : t('confirm')), h('button', { type:'button', disabled:!!facts.deleting, onClick:() => setFacts(previous => ({ ...previous, confirm:null, error:'' })) }, t('cancel'))) :
                null))) : !facts.error && h('div', {className:'gateway-chat-memory-empty'},h('p',null,t(search?'noMatches':'factsEmpty')),search&&h('button',{type:'button',onClick:()=>{setQuery('');setSearch('');}},t('showAll'))),
            facts.loading && h('p', { role:'status' }, t('loading')),
            facts.error && h('p', { className:'gateway-chat-memory-error', role:'alert' }, t(facts.error)),
            facts.error === 'factsFailed' && h('button', { type:'button', onClick:() => loadFacts(facts.cursor ?? 0) }, t('retry')),
            facts.cursor !== null && !facts.error && h('button', { type:'button', disabled:facts.loading || !!facts.deleting, onClick:() => loadFacts(facts.cursor) }, t('more'))))),
        mode==='summary'&&h('section', { className:'gateway-chat-memory-compaction' }, h('header', { className:'gateway-chat-memory-head' }, h('h2', null, t('title')),
        summary.state === 'ready' && h('button', { type:'button', className:'gateway-chat-memory-copy', onClick:copy, title:t('copy'), 'aria-label':t('copy') }, h(IconCopyOutlineRegular, { size:20 }))),
        h('p', { className:'gateway-chat-memory-description' }, t('note')),
        summary.state === 'ready' ? h(React.Fragment, null,
          Number.isFinite(summary.time) && h('p', { className:'gateway-chat-memory-time' }, t('savedAt', { time:new Date(summary.time).toLocaleString(ctx.locale.getSnapshot().active === 'ru' ? 'ru-RU' : 'en-US') })),
          h('div', { ref:content, className:'gateway-chat-memory-summary', tabIndex:0 }, h(MarkdownText, { text:summary.text, streaming:false }))) :
          h('div', { className:'gateway-chat-memory-empty' }, h('p', null, t(summary.state)),
            summary.hasMore && h('button', { type:'button', disabled:loading, onClick:loadOlder }, loading ? t('loading') : t('loadOlder'))),
        notice && h('p', { className:'gateway-chat-memory-notice', role:'status' }, notice)),
        mode==='mnemon'&&h('section',{className:'gateway-memory-mnemon','data-gateway-memory-mnemon':true,'aria-label':t('mnemonTab')},
          h('h2',null,t('mnemonTab')),h('p',{className:'gateway-chat-memory-scope'},t('mnemonScope')),h('p',{className:'gateway-chat-memory-shared'},t('shared')),
          h('button',{type:'button',className:'gateway-memory-open-mnemon',disabled:!canOpenMnemon,onClick:openMnemon},h(IconChevronRightOutlineRegular,{size:18}),t('mnemonOpen')),
          !canOpenMnemon&&h('p',{role:'status',className:'gateway-chat-memory-notice'},t('mnemonUnavailable')))));
  }
  function MemoryTab({sessionId,useTabInfo}) {
    const {sidebar,tab}=useTabInfo();
    function back(){ctx.layout.selectPanel(null);if(sidebar.expanded)ctx.sidebarRight.toggleExpanded();const session=ctx.sessions.scope(sessionId);session?.get('conversation')?.input.for(session).focus();}
    function openGraph(){nodePanelSession=sessionId;if(sidebar.expanded)ctx.sidebarRight.toggleExpanded();ctx.layout.selectPanel(CHAT_MEMORY_NODE_PANEL);}
    function openReview(){if(sidebar.expanded)ctx.sidebarRight.toggleExpanded();review?.open(sessionId);}
    return h(ChatMemoryView,{key:sessionId,sessionId,onBack:back,onOpenGraph:openGraph,onOpenReview:openReview,visible:tab?.visible??true});
  }
  const tabId='gateway.chat-memory',kind='gateway-memory';
  ctx.effect(()=>ctx.sidebarRightTabs.register({id:tabId,kind,title:()=>t('tab'),keepMounted:true,
    guide:[{id:'memory',order:15,title:()=>t('tab'),description:()=>t('guideDescription'),icon:IconDatabaseOutlineRegular}]}),'gateway: memory sidebar type');
  ctx.effect(()=>ctx.slots.inject('sidebar.right.pane.tab',()=>ctx.slots.register({name:'sidebar.right.pane.tab',key:tabId,locale:'gatewayChatMemory'},MemoryTab)),'gateway: memory sidebar body');
  ctx.effect(()=>ctx.slots.inject('main',()=>ctx.slots.register({name:'main',key:CHAT_MEMORY_NODE_PANEL,locale:'gatewayChatMemory'},NodePanel)),'gateway: fact graph main panel');
}
