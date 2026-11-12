function registerMemoryReviewUI(ctx,icons) {
  const h=React.createElement,key='gateway.memory-review';
  const ru={
    title:'Упорядочить память',back:'К чату',select:'Выбор фактов',preview:'Предпросмотр',history:'История',
    merge:'Объединить',link:'Предложить связь',selectCount:'Выбрано: {count} из 12',next:'Проверить выбранное',cancel:'Отменить проверку',checking:'Проверяем источники и готовим предложение…',
    before:'До',after:'После',links:'Связи',sources:'Проверка источников',verified:'Фрагмент найден',unverified:'Не удалось проверить',apply:'Подтвердить и применить',
    undo:'Откатить',undoConfirm:'Восстановить исходные факты и связи?',applied:'Применено',undone:'Отменено',expired:'Срок отката истёк',
    noHistory:'Пока нет операций.',noFacts:'Нет фактов для проверки.',noProposal:'Подходящих изменений не найдено.',missingEvidence:'Недостаточно проверенных источников. Память не изменена.',
    modelUnavailable:'Модель анализа не подключена. Память не изменена.',fail:'Операция не выполнена.',stale:'Факты изменились. Подготовьте новый предпросмотр.',
    sourceChanged:'Файл изменился после проверки. Подготовьте новый предпросмотр.',undoConflict:'Затронутые факты или связи изменились. Откат ничего не перезаписал.',
    critical:'Критические факты нельзя объединять.',linkConflict:'Связи нельзя перенести без потерь.',retry:'Повторить',
    draftExpired:'Предпросмотр устарел. Запустите проверку ещё раз.',historyLimit:'Хранилище отката заполнено. Изменения не применены.',
    more:'Показать ещё',changes:'{before} → {after} фактов',reserve:'Резерв для отката: {bytes} Б',cost:'Дополнительный объём: {bytes} Б',
    until:'Откат до {time}',loading:'Загрузка…',normal:'Обычный',important:'Важный',criticalStatus:'Критический',
    related:'Связан с',supports:'Подтверждает',contradicts:'Противоречит',depends_on:'Зависит от',updates:'Уточняет',part_of:'Часть',
    scope:'Область проверки',scopeManual:'Выбрать факты',scopeAll:'Выбрать все',scan:'Проверить весь чат',scanning:'Просматриваем факты чата…',
    scanCount:'Просмотрено: {scanned} из {total}',groups:'Кандидаты: {count}',excluded:'Критические вне объединения: {count}',
    exactGroup:'Точные дубли',sourceGroup:'Общий источник',groupFacts:'Фактов: {count}',openGroup:'Предпросмотр',
    noGroups:'Подходящих предложений не найдено.',rescan:'Проверить снова',semanticGroup:'Предложение модели',
    analyzed:'Модель проверила: {analyzed} из {total}',verifiedFacts:'Сверено с источниками: {verified}',
    scanStart:'Готовим факты к анализу…',scanActive:'Модель анализирует память…',
    scanCancelled:'Анализ остановлен. Показаны результаты обработанных пакетов.',
    scanTimeout:'Время анализа истекло. Показаны частичные результаты.',
    scanFailed:'Модель не завершила анализ. Показаны частичные результаты.',
    scanUnavailable:'Модель анализа недоступна. Показаны только локальные совпадения.',
    advisoryTitle:'Предложение модели',selectedFacts:'Выбранные факты',
    previewBusy:'Другой анализ памяти уже идёт.',
    inputLimit:'Факты и источники не помещаются в один запрос. Выберите меньше фактов.',
    modelError:'Не удалось получить предложение от модели.',
    modelTimeout:'Локальная модель не ответила вовремя.',
    modelTruncated:'Локальная модель вернула обрезанный ответ.',
    modelResponse:'Локальная модель вернула ответ в неожиданном формате.',
    gpuBusy:'GPU занят другой задачей.',
    executorUnavailable:'Исполнитель локальной модели недоступен.',
    skippedFacts:'Не обработано из-за ошибки: {failed}',
    candidateOnly:'Для этой группы нет готового предложения модели.',
    checkGroup:'Проверить группу моделью',candidateOnlyBadge:'Без предложения',
    modelReason:'Обоснование модели',proposedText:'Предлагаемый текст',proposedLink:'Предлагаемая связь',
    metadataLimit:'Объединение превысит лимит источников или псевдонимов. Память не изменена.',
  };
  ctx.effect(()=>ctx.locale.register('gatewayMemoryReview',{en:ru,ru}),'gateway: memory review labels');
  const t=ctx.locale.bind('gatewayMemoryReview');
  let openedSession;
  const errorLabels={review_stale:'stale',review_source_changed:'sourceChanged',review_undo_conflict:'undoConflict',review_draft_expired:'draftExpired',
    review_critical:'critical',review_link_conflict:'linkConflict',review_history_limit:'historyLimit',review_model_unavailable:'modelUnavailable',
    review_busy:'previewBusy',review_input_limit:'inputLimit',review_model_response:'modelResponse',review_model_timeout:'modelTimeout',
    review_model_truncated:'modelTruncated',review_gpu_busy:'gpuBusy',review_executor_unavailable:'executorUnavailable',review_metadata_limit:'metadataLimit'};
  async function post(path,body,signal){
    const response=await fetch(new URL(path,document.baseURI),{method:'POST',credentials:'same-origin',mode:'same-origin',redirect:'error',
      headers:{'Content-Type':'application/json'},body:JSON.stringify(body),signal});
    const result=await response.json();
    if(!response.ok||!result?.ok)throw Error(result?.error?.code||'review_failed');
    return result.value;
  }
  function Panel({sessionId}){
    const [screen,setScreen]=React.useState('select'),[rows,setRows]=React.useState([]),[cursor,setCursor]=React.useState(null);
    const [selected,setSelected]=React.useState([]),[kind,setKind]=React.useState('merge'),[proposal,setProposal]=React.useState(null);
    const [history,setHistory]=React.useState(null),[busy,setBusy]=React.useState(''),[error,setError]=React.useState(''),[undo,setUndo]=React.useState(null);
    const [scope,setScope]=React.useState('manual'),[scan,setScan]=React.useState(null);
    const job=React.useRef(null), activeScan=React.useRef(null);
    const call=(action,fields,signal)=>post('./api/gateway-memory-review',{sessionId,action,...fields},signal);
    async function run(task,operation){
      if(job.current)return;
      const controller=new AbortController();job.current=controller;setBusy(task);setError('');
      try{await operation(controller.signal);}catch(reason){
        if(!controller.signal.aborted)setError(t(errorLabels[reason.message]||'fail'));
      }finally{if(job.current===controller){job.current=null;setBusy('');}}
    }
    function list(offset=0){return run('list',async signal=>{
      const value=await post('./api/gateway-chat-memory/manage',{action:'list',sessionId,cursor:offset,limit:50},signal);
      if(signal.aborted)return;
      setRows(previous=>offset?[...new Map([...previous,...value.records].map(row=>[row.id,row])).values()]:value.records);
      setCursor(value.nextCursor);
    });}
    function stopScan(){
      const scanId=activeScan.current;
      if(scanId)void call('scanCancel',{scanId}).catch(()=>{});
    }
    React.useEffect(()=>{list();return()=>{stopScan();job.current?.abort();job.current=null;};},[]);
    function goHistory(){setScreen('history');setUndo(null);return run('history',async signal=>setHistory(await call('history',{},signal)));}
    function preview(ids=selected,expectedRevision){return run('preview',async signal=>{
      const value=await call('preview',{ids,kind,...(expectedRevision?{expectedRevision}:{})},signal);
      if(!signal.aborted){setProposal(value);setScreen('preview');}
    });}
    function openCandidate(group){return run('saved',async signal=>{
      const value=await call('scanPreview',{scanId:scan.id,groupIndex:group.groupIndex},signal);
      if(!signal.aborted){setProposal(value);setScreen('preview');}
    });}
    function scanAll(page){return run('scan',async signal=>{
      if(page!==undefined){
        const value=await call('scanPage',{scanId:scan.id,page},signal);
        if(!signal.aborted)setScan(previous=>({...value,groups:[...previous.groups,...value.groups]}));
        return;
      }
      setScan(null);
      let value=await call('scan',{kind,semantic:true},signal);
      if(signal.aborted)return;
      setScan(value);
      if(value.status!=='running')return;
      activeScan.current=value.id;
      try{
        while(value.status==='running'&&!signal.aborted){
          await new Promise(resolve=>setTimeout(resolve,1000));
          if(signal.aborted)return;
          value=await call('scanPage',{scanId:value.id,page:0},signal);
          if(!signal.aborted)setScan(value);
        }
      }finally{if(value.status==='running')stopScan();activeScan.current=null;}
    });}
    function apply(){return run('apply',async signal=>{
      await call('apply',{draftId:proposal.id,confirm:true},signal);
      setProposal(null);setSelected([]);setScan(null);setScreen('history');setHistory(await call('history',{},signal));
    });}
    function restore(){return run('undo',async signal=>{
      await call('undo',{operationId:undo,confirm:true},signal);
      setUndo(null);setScan(null);setHistory(await call('history',{},signal));
    });}
    function navigate(next){
      if(next==='history')goHistory();
      else if(next==='select'){setScreen(next);setProposal(null);setUndo(null);if(scope==='manual')list();}
    }
    function priority(fact){return fact.status==='critical'?'criticalStatus':fact.status==='important'?'important':'normal';}
    function textOf(id){return [...(proposal?.before?.facts??proposal?.facts??[]),...rows].find(fact=>fact.id===id)?.text??id.slice(0,8);}
    function suggestedLink(link){return h('section',{className:'gateway-review-column'},h('h3',null,t('proposedLink')),
      h('p',null,textOf(link.fromId)),h('small',null,t(link.relation)),h('p',null,textOf(link.toId)));}
    function column(label,part){
      return h('section',{className:'gateway-review-column'},h('h3',null,t(label)),
        part.facts.map(fact=>h('article',{key:fact.id},h('small',null,t(priority(fact))),h('p',null,fact.text))),
        part.edges.length>0&&h('details',null,h('summary',null,t('links')+' ('+part.edges.length+')'),
          h('ul',null,part.edges.map(edge=>h('li',{key:edge.id},textOf(edge.fromId),' · ',t(edge.relation),' · ',textOf(edge.toId))))));
    }
    return h('section',{className:'gateway-chat-memory gateway-review','data-gateway-memory-review':true},
      h('header',{className:'gateway-node-header'},
        h('button',{type:'button',className:'gateway-memory-icon','aria-label':t('back'),title:t('back'),disabled:busy==='apply'||busy==='undo',onClick:()=>ctx.layout.selectPanel(null)},
          h(icons.IconPanelLeftOutlineRegular,{size:18})),h('h2',null,t('title'))),
      h('nav',{className:'gateway-review-steps','aria-label':t('title')},['select','preview','history'].map(step=>h('button',{
        type:'button',key:step,'aria-current':screen===step?'step':undefined,disabled:!!busy||step==='preview',onClick:()=>navigate(step)},t(step)))),
      h('div',{className:'gateway-review-body'},
        error&&h('p',{role:'alert',className:'gateway-review-error'},error),
        screen==='select'&&h(React.Fragment,null,
          h('div',{className:'gateway-review-filters'},
            h('div',{className:'gateway-review-modes',role:'group','aria-label':t('scope')},['manual','all'].map(value=>h('button',{key:value,type:'button','aria-pressed':scope===value,disabled:!!busy,
              onClick:()=>{setScope(value);setError('');}},t(value==='all'?'scopeAll':'scopeManual')))),
            h('div',{className:'gateway-review-modes'},['merge','link'].map(mode=>h('button',{key:mode,type:'button','aria-pressed':mode===kind,disabled:!!busy,
              onClick:()=>{if(kind!==mode){setKind(mode);setSelected([]);setScan(null);}}},t(mode))))),
          scope==='all'?scan&&h(React.Fragment,null,
            h('div',{className:'gateway-review-scan-summary',role:'status'},h('strong',null,t('scanCount',scan)),
              typeof scan.analyzed==='number'&&h('strong',null,t('analyzed',scan)),
              typeof scan.verified==='number'&&h('span',null,t('verifiedFacts',scan)),
              h('span',null,t('groups',{count:scan.groupCount})),scan.excluded>0&&h('span',null,t('excluded',{count:scan.excluded})),
              scan.failed>0&&h('span',null,t('skippedFacts',scan))),
            ['cancelled','timed_out','model_failed','model_unavailable','stale','partial'].includes(scan.status)&&h('p',{role:'status'},
              t({cancelled:'scanCancelled',timed_out:'scanTimeout',model_failed:'scanFailed',model_unavailable:'scanUnavailable',stale:'stale',partial:'scanFailed'}[scan.status]),
              scan.errorCode&&(' '+t(errorLabels[scan.errorCode]||'modelError'))),
            !scan.groups.length?(scan.status!=='running'&&h('p',{className:'gateway-review-empty'},t('noGroups'))):
              h('ul',{className:'gateway-review-candidates'},scan.groups.map(group=>h('li',{key:group.ids.join(',')},
                h('div',null,h('small',null,t({exact:'exactGroup',source:'sourceGroup',semantic:'semanticGroup'}[group.reason]),' · ',t('groupFacts',{count:group.ids.length})),
                  h('p',null,group.label),h('small',null,group.note||(!group.hasProposal?t('candidateOnlyBadge'):null))),
                h('button',{type:'button',disabled:!!busy||scan.status==='stale',onClick:()=>openCandidate(group)},t('openGroup'))))),
            scan.nextPage!==null&&h('button',{type:'button',disabled:!!busy,onClick:()=>scanAll(scan.nextPage)},t('more'))):h(React.Fragment,null,
          busy==='list'&&!rows.length?h('p',{role:'status'},t('loading')):!rows.length?h('p',null,t('noFacts')):
            h('ul',{className:'gateway-review-facts'},rows.map(fact=>{
              const checked=selected.includes(fact.id),locked=kind==='merge'&&fact.status==='critical';
              return h('li',{key:fact.id},h('label',null,h('input',{type:'checkbox',checked,disabled:!!busy||locked||(!checked&&selected.length===(kind==='link'?2:12)),
                onChange:()=>setSelected(current=>checked?current.filter(id=>id!==fact.id):[...current,fact.id])}),
                h('span',null,fact.text),h('small',null,t(priority(fact)))));
            })),
          cursor!==null&&h('button',{type:'button',disabled:!!busy,onClick:()=>list(cursor)},t('more')))),
        screen==='preview'&&proposal&&h(React.Fragment,null,
          proposal.state==='ready'?h(React.Fragment,null,
            h('div',{className:'gateway-review-metrics'},h('strong',null,t('changes',{before:proposal.before.facts.length,after:proposal.after.facts.length})),
              h('span',null,t(proposal.delta>0?'cost':'reserve',{bytes:proposal.delta>0?proposal.delta:proposal.reserve}))),
            proposal.reason&&h('section',{className:'gateway-review-column'},h('h3',null,t('modelReason')),
              h('p',{className:'gateway-review-reason'},proposal.reason)),
            proposal.suggestedLink&&suggestedLink(proposal.suggestedLink),
            h('div',{className:'gateway-review-diff'},column('before',proposal.before),column('after',proposal.after))):
            h(React.Fragment,null,
              h('p',{role:'status'},t({unverified:'missingEvidence',model_unavailable:'modelUnavailable',no_proposal:'noProposal',
                candidate_only:'candidateOnly',source_changed:'sourceChanged',link_conflict:'linkConflict',metadata_limit:'metadataLimit'}[proposal.state]??'fail')),
              proposal.state==='candidate_only'&&h('button',{type:'button',disabled:!!busy,onClick:()=>preview(proposal.ids,proposal.revision)},t(busy==='preview'?'checking':'checkGroup')),
              (proposal.reason||proposal.suggestedText)&&h('section',{className:'gateway-review-column'},h('h3',null,t('advisoryTitle')),
                proposal.reason&&h('p',null,proposal.reason),
                proposal.suggestedText&&h('article',null,h('small',null,t('proposedText')),h('p',null,proposal.suggestedText))),
              proposal.suggestedLink&&suggestedLink(proposal.suggestedLink),
              proposal.facts?.length>0&&column('selectedFacts',{facts:proposal.facts,edges:[]})),
          proposal.evidence?.length>0&&h('section',{className:'gateway-review-evidence'},h('h3',null,t('sources')),
            proposal.evidence.map((source,i)=>h('details',{key:i},h('summary',null,source.file,' · ',t(source.excerpt?'verified':'unverified')),
              source.excerpt&&h(React.Fragment,null,h('small',null,`${source.lineFrom}–${source.lineTo} · ${source.sha256.slice(0,12)}`),h('blockquote',null,source.excerpt)))))),
        screen==='history'&&(!history?h('p',{role:'status'},t('loading')):!history.length?h('p',null,t('noHistory')):
          h('ul',{className:'gateway-review-history'},history.map(operation=>{
            const status=operation.state==='applied'&&operation.expires<=Date.now()?'expired':operation.state;
            return h('li',{key:operation.id},h('div',null,h('strong',null,t(status)),h('time',null,new Date(operation.created).toLocaleString())),
              status==='applied'&&h(React.Fragment,null,h('small',null,t('until',{time:new Date(operation.expires).toLocaleDateString()})),
                h('button',{type:'button',disabled:!!busy,onClick:()=>setUndo(operation.id)},t('undo'))));
          })))),
      (screen==='select'||screen==='preview'&&proposal?.state==='ready'||undo)&&h('footer',{className:'gateway-review-actions'},
        screen==='select'&&(['preview','scan'].includes(busy)?h(React.Fragment,null,h('span',{role:'status'},t(busy==='scan'?(scan?.status==='running'?'scanActive':'scanStart'):'checking')),
          h('button',{type:'button',disabled:busy==='scan'&&scan?.status!=='running',onClick:busy==='scan'?stopScan:()=>job.current?.abort()},t('cancel'))):
          scope==='all'?h('button',{type:'button',className:'gateway-memory-primary',disabled:!!busy,onClick:()=>scanAll()},t(scan?'rescan':'scan')):
          h(React.Fragment,null,h('span',null,kind==='merge'?t('selectCount',{count:selected.length}):selected.length+' / 2'),
            h('button',{type:'button',className:'gateway-memory-primary',disabled:!!busy||selected.length<2,onClick:()=>preview()},t('next')))),
        screen==='preview'&&proposal?.state==='ready'&&h('button',{type:'button',className:'gateway-memory-primary',disabled:!!busy,onClick:apply},t('apply')),
        undo&&h(React.Fragment,null,h('span',null,t('undoConfirm')),h('button',{type:'button',disabled:!!busy,onClick:()=>setUndo(null)},t('back')),
          h('button',{type:'button',className:'gateway-memory-primary',disabled:!!busy,onClick:restore},t('undo')))));
  }
  function Main(){
    const id=openedSession;
    return id?h(Panel,{key:id,sessionId:id}):null;
  }
  ctx.effect(()=>ctx.slots.inject('main',()=>ctx.slots.register({name:'main',key,locale:'gatewayMemoryReview'},Main)),'gateway: memory review main');
  return {label:()=>t('title'),open(id){openedSession=id;ctx.layout.selectPanel(key);}};
}
