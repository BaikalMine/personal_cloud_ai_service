function registerUltraImagesUI(ctx, primitives) {
  const h = React.createElement;
  const { IconDownloadOutlineRegular, IconFullscreenOutlineRegular, IconCloseOutlineRegular,
    IconPlusOutlineRegular, IconRefreshOutlineRegular } = primitives;
  const english = { title:'Krea2 Ultra', running:'Generating image...', ready:'Image ready', error:'Image generation failed',
    open:'Open image', download:'Download original', close:'Close', zoomIn:'Zoom in', zoomOut:'Zoom out', fit:'Fit image',
    retry:'Retry loading image', unavailable:'The image is unavailable. It may have been moved or deleted.', loading:'Loading image...',
    details:'Details', seconds:'{seconds} s', image:'Image {number}', imageAlt:'Generated image {number}',
    preparing:'Preparing request', awaiting_model:'Waiting for image model selection', uploading:'Preparing references',
    validating:'Checking generation settings', waiting_gpu:'Waiting for GPU', generating:'Generating image', finalizing:'Releasing GPU', finalizing_failed:'Generation failed; releasing GPU', delivering:'Saving image',
    chooseModel:'Choose image model', elapsed:'{time} elapsed' };
  ctx.effect(() => ctx.locale.register('gatewayUltra', { en:english }), 'gateway: image result copy');
  const t = ctx.locale.bind('gatewayUltra');
  const useCopy = () => { React.useSyncExternalStore(fn => ctx.locale.subscribe(fn), () => ctx.locale.getSnapshot().active); return t; };
  function imageURL(sessionId, path, preview) {
    const url = new URL(preview ? './api/gateway-files/preview' : './api/gateway-files/download', document.baseURI);
    url.searchParams.set('sessionId', sessionId); url.searchParams.set('path', path); return url.href;
  }
  function ImageViewer({ file, sessionId, onClose, opener }) {
    const t = useCopy(), dialog = React.useRef(null), stage = React.useRef(null), image = React.useRef(null);
    const pointers = React.useRef(new Map()), view = React.useRef({ zoom:1, x:0, y:0 });
    const [position, setPosition] = React.useState(view.current), [failed, setFailed] = React.useState(false);
    function update(next) {
      const box = stage.current?.getBoundingClientRect(), picture = image.current;
      next.zoom = Math.max(1, Math.min(8, next.zoom));
      if (box && picture?.naturalWidth) {
        const fit = Math.min(box.width / picture.naturalWidth, box.height / picture.naturalHeight);
        const maxX = Math.max(0, (picture.naturalWidth * fit * next.zoom - box.width) / 2);
        const maxY = Math.max(0, (picture.naturalHeight * fit * next.zoom - box.height) / 2);
        next.x = Math.max(-maxX, Math.min(maxX, next.x)); next.y = Math.max(-maxY, Math.min(maxY, next.y));
      }
      view.current = next; setPosition(next);
    }
    function zoom(factor, point) {
      const old = view.current, value = Math.max(1, Math.min(8, old.zoom * factor)), ratio = value / old.zoom;
      update({ zoom:value, x:point ? (old.x - point.x) * ratio + point.x : old.x * ratio, y:point ? (old.y - point.y) * ratio + point.y : old.y * ratio });
    }
    function point(event) { const box = stage.current.getBoundingClientRect(); return { x:event.clientX - box.left - box.width / 2, y:event.clientY - box.top - box.height / 2 }; }
    React.useEffect(() => {
      dialog.current.showModal();
      const overflow = document.body.style.overflow; document.body.style.overflow = 'hidden';
      const wheel = event => { event.preventDefault(); zoom(Math.exp(-event.deltaY * 0.002), point(event)); };
      const target = stage.current; target.addEventListener('wheel', wheel, { passive:false });
      const resize = () => update({ ...view.current }); window.addEventListener('resize', resize);
      return () => { target.removeEventListener('wheel', wheel); window.removeEventListener('resize', resize); document.body.style.overflow = overflow; if (opener?.isConnected) opener.focus({ preventScroll:true }); };
    }, []);
    function move(event) {
      const active = pointers.current; if (!active.has(event.pointerId)) return;
      const before = [...active.values()]; active.set(event.pointerId, point(event)); const after = [...active.values()];
      if (after.length === 1) update({ ...view.current, x:view.current.x + after[0].x - before[0].x, y:view.current.y + after[0].y - before[0].y });
      else if (after.length === 2) {
        const distance = values => Math.hypot(values[0].x-values[1].x, values[0].y-values[1].y);
        const center = values => ({ x:(values[0].x+values[1].x)/2, y:(values[0].y+values[1].y)/2 });
        const old = center(before), next = center(after), previous = view.current;
        const value = Math.max(1, Math.min(8, previous.zoom * distance(after) / Math.max(1, distance(before)))), ratio = value / previous.zoom;
        update({ zoom:value, x:(previous.x-old.x)*ratio+next.x, y:(previous.y-old.y)*ratio+next.y });
      }
    }
    const button = (label, icon, action, disabled) => h('button', { type:'button', className:'gateway-file-tool', title:label, 'aria-label':label, onClick:action, disabled }, icon);
    return h('dialog', { ref:dialog, className:'gateway-image-viewer', 'data-gateway-image-viewer':true, 'aria-label':file.description || t('ready'),
      onCancel:event => { event.preventDefault(); onClose(); }, onKeyDown:event => {
        if (['+','=','-','0'].includes(event.key)) { event.preventDefault(); if (event.key === '0') update({ zoom:1,x:0,y:0 }); else zoom(event.key === '-' ? 1/1.5 : 1.5); }
      } },
      h('header', { className:'gateway-image-viewer-toolbar' },
        h('span', { className:'gateway-image-viewer-name', title:file.path.split('/').at(-1) }, file.description || t('ready')),
        button(t('zoomOut'), h('span', { className:'gateway-image-minus', 'aria-hidden':true }, '-'), () => zoom(1/1.5), position.zoom <= 1),
        h('output', { className:'gateway-image-zoom', 'aria-label':t('fit'), 'data-gateway-image-zoom':true }, `${Math.round(position.zoom*100)}%`),
        button(t('zoomIn'), h(IconPlusOutlineRegular, { size:22 }), () => zoom(1.5), position.zoom >= 8),
        button(t('fit'), h(IconFullscreenOutlineRegular, { size:22 }), () => update({ zoom:1,x:0,y:0 })),
        h('a', { className:'gateway-file-tool', href:imageURL(sessionId,file.path,false), download:file.path.split('/').at(-1), title:t('download'), 'aria-label':t('download') }, h(IconDownloadOutlineRegular, { size:22 })),
        button(t('close'), h(IconCloseOutlineRegular, { size:24 }), onClose)),
      h('div', { ref:stage, className:'gateway-image-stage', 'data-gateway-image-stage':true, 'data-zoomed':position.zoom > 1,
        onDoubleClick:event => position.zoom > 1 ? update({ zoom:1,x:0,y:0 }) : zoom(2,point(event)),
        onPointerDown:event => { if (pointers.current.size >= 2 || event.button > 0) return; event.currentTarget.setPointerCapture(event.pointerId); pointers.current.set(event.pointerId,point(event)); },
        onPointerMove:move, onPointerUp:event => pointers.current.delete(event.pointerId), onPointerCancel:event => pointers.current.delete(event.pointerId),
        onLostPointerCapture:event => pointers.current.delete(event.pointerId) },
        h('img', { ref:image, src:imageURL(sessionId,file.path,true), alt:file.description || t('ready'), draggable:false, onLoad:() => update({ ...view.current }), onError:() => setFailed(true), style:{ transform:`translate(${position.x}px, ${position.y}px) scale(${position.zoom})` } }),
        failed && h('p', { className:'gateway-image-error', role:'alert' }, t('unavailable'))));
  }
  function GeneratedImage({ file, number, sessionId, elapsed }) {
    const t = useCopy(), [viewer, setViewer] = React.useState(null), [state, setState] = React.useState('loading'), [retry, setRetry] = React.useState(0);
    const url = imageURL(sessionId,file.path,true);
    return h('figure', { className:'gateway-generated-image', 'data-gateway-generated-image':file.path },
      h('button', { type:'button', className:'gateway-generated-image-open', 'aria-label':t('open'), title:t('open'), onClick:event => setViewer(event.currentTarget) },
        h('img', { key:retry, src:url, alt:file.description || t('imageAlt',{number}), width:file.width || 1024, height:file.height || 1024, loading:'lazy', decoding:'async', onLoad:() => setState('ready'), onError:() => setState('failed') }),
        state === 'loading' && h('span', { className:'gateway-image-loading', role:'status' }, t('loading'))),
      state === 'failed' && h('div', { className:'gateway-image-error', role:'alert' }, t('unavailable'), h('button', { type:'button', className:'gateway-file-tool', title:t('retry'), 'aria-label':t('retry'), onClick:() => { setState('loading'); setRetry(value => value+1); } }, h(IconRefreshOutlineRegular,{size:20}))),
      h('figcaption', { className:'gateway-generated-image-caption' },
        h('span', { className:'gateway-generated-image-meta' }, h('strong', null, file.description || t('image',{number})),
          h('span', null, [file.width && file.height ? `${file.width} × ${file.height}` : null, Number.isFinite(elapsed) ? t('seconds',{seconds:Math.round(elapsed)}) : null].filter(Boolean).join(' · '))),
        h('button', { type:'button', className:'gateway-file-tool', title:t('open'), 'aria-label':t('open'), onClick:event => setViewer(event.currentTarget) }, h(IconFullscreenOutlineRegular,{size:21})),
        h('a', { className:'gateway-file-tool', href:imageURL(sessionId,file.path,false), download:file.path.split('/').at(-1), title:t('download'), 'aria-label':t('download') }, h(IconDownloadOutlineRegular,{size:21}))),
      viewer && h(ImageViewer,{file,sessionId,opener:viewer,onClose:() => setViewer(null)}));
  }
  function UltraToolView({ phase, block, inspect, callId, useProjection, revealModel, title }) {
    const t = useCopy();
    const progress = useProjection('gatewayUltra', view => view?.calls.find(row => row.call_id === callId));
    const [clock, setClock] = React.useState(Date.now());
    React.useEffect(() => {
      if (phase === 'result') return;
      const timer = setInterval(() => setClock(Date.now()), 1000);
      return () => clearInterval(timer);
    }, [phase]);
    const seconds = progress ? Math.max(0, Math.floor((clock - progress.phase_started_at) / 1000)) : 0;
    const elapsed = `${Math.floor(seconds/60)}:${String(seconds%60).padStart(2,'0')}`;
    const currentPhase = progress && !['settled','failed','cancelled'].includes(progress.phase) ? progress.phase : 'preparing';
    let result, raw = '';
    if (phase === 'result') { raw = block.content?.filter(item => item.type === 'text').map(item => item.text).join('\n') || ''; try { result = JSON.parse(raw); } catch {} }
    const images = !block.isError && result?.state === 'completed' && result.session_id && result.files?.filter(file => typeof file.path === 'string' && /\.png$/i.test(file.path));
    return h('div', { className:'gateway-ultra-result', 'data-gateway-ultra-result':true },
      images?.length ? images.map((file,index) => h(GeneratedImage,{key:file.path,file,number:index+1,sessionId:result.session_id,elapsed:result.timings?.active_tool_seconds ?? result.timings?.total_tool_seconds ?? result.elapsed_seconds})) :
        phase !== 'result' ? h('div', { className:'gateway-ultra-progress', 'data-gateway-ultra-phase':currentPhase },
          h('span', { className:'gateway-ultra-status', role:'status' }, t(currentPhase)),
          progress && h('span', { className:'gateway-ultra-elapsed' }, t('elapsed',{time:elapsed})),
          currentPhase === 'awaiting_model' && h('button', { type:'button', className:'gateway-ultra-choose', onClick:() => revealModel(progress.question_call_id || callId) }, t('chooseModel'))) :
        h('span', { className:'gateway-ultra-status', role:block.isError ? 'alert' : undefined }, block.isError ? t('error') : title || t('title')),
      phase === 'result' && h('details', { className:'gateway-ultra-details' }, h('summary',null,t('details')), h('pre',null,raw), inspect && h('button',{type:'button',onClick:inspect},t('details'))));
  }
  ctx.slots.inject('tool.call.toolview', () => ctx.slots.register({ name:'tool.call.toolview', key:'krea_ultra', id:'gateway.ultra-images',
    inject:sessionId => ({ revealModel:callId => ctx.get('userQuestionPanels')?.reveal(sessionId,callId) ?? false }) }, UltraToolView));
  ctx.slots.inject('tool.call.toolview', () => ctx.slots.register({ name:'tool.call.toolview', key:'moss_image', id:'gateway.moss-images',
    inject:sessionId => ({ revealModel:callId => ctx.get('userQuestionPanels')?.reveal(sessionId,callId) ?? false }) },
    props => h(UltraToolView,{...props,title:'MOSS Image'})));
  ctx.slots.inject('gateway.deliverables.image', () => ctx.slots.register({ name:'gateway.deliverables.image', id:'gateway.ultra-delivered-image' },
    ({file,sessionId}) => h(GeneratedImage,{file,sessionId,number:1,elapsed:file.elapsed_seconds})));
  return {ImageViewer,imageURL};
}
