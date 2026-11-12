export function composerViewport({height, top=0, scale=1, layoutHeight, focused=false, mobile=false}) {
  const visible = Number.isFinite(height) && height > 0 ? height : layoutHeight;
  const offset = Number.isFinite(top) && top > 0 ? top : 0;
  return {height:visible, top:offset, keyboard:mobile && focused && Math.abs(scale - 1) < 0.05 &&
    layoutHeight - visible > Math.max(120, layoutHeight * 0.18)};
}

export function installComposerViewport(win=window, doc=document) {
  const viewport=win.visualViewport, root=doc.documentElement, media=win.matchMedia('(max-width: 720px)');
  const previousHeight=root.style.getPropertyValue('--gateway-viewport-height');
  const previousTop=root.style.getPropertyValue('--gateway-viewport-top');
  const previousKeyboard=doc.body.hasAttribute('data-gateway-keyboard-open');
  let width=win.innerWidth, baseline=Math.max(win.innerHeight,root.clientHeight), pending=null, disposed=false;
  function update() {
    if(disposed)return;
    pending=null;
    if(Math.abs(win.innerWidth-width)>80){width=win.innerWidth;baseline=Math.max(win.innerHeight,root.clientHeight);}
    const focused=Boolean(doc.activeElement?.closest('textarea,input,[contenteditable="true"]'));
    const layout=Math.max(win.innerHeight,root.clientHeight), visible=viewport?.height??win.innerHeight;
    if(!focused&&Math.abs(visible-layout)<2&&Math.abs((viewport?.scale??1)-1)<0.05)baseline=layout;
    else baseline=Math.max(baseline,layout);
    const state=composerViewport({height:viewport?.height??win.innerHeight,top:viewport?.offsetTop,
      scale:viewport?.scale??1,layoutHeight:baseline,focused,mobile:media.matches});
    const height=`${state.height}px`,top=`${state.top}px`;
    if(root.style.getPropertyValue('--gateway-viewport-height')!==height)root.style.setProperty('--gateway-viewport-height',height);
    if(root.style.getPropertyValue('--gateway-viewport-top')!==top)root.style.setProperty('--gateway-viewport-top',top);
    doc.body.toggleAttribute('data-gateway-keyboard-open',state.keyboard);
  }
  function schedule(){if(!disposed&&pending===null)pending=win.requestAnimationFrame(update);}
  update();
  win.addEventListener('resize',schedule);
  viewport?.addEventListener('resize',schedule);
  viewport?.addEventListener('scroll',schedule);
  media.addEventListener('change',schedule);
  doc.addEventListener('focusin',schedule);
  doc.addEventListener('focusout',schedule);
  return () => {
    disposed=true;
    if(pending!==null)win.cancelAnimationFrame(pending);
    win.removeEventListener('resize',schedule);
    viewport?.removeEventListener('resize',schedule);
    viewport?.removeEventListener('scroll',schedule);
    media.removeEventListener('change',schedule);
    doc.removeEventListener('focusin',schedule);
    doc.removeEventListener('focusout',schedule);
    for(const [name,value] of [['--gateway-viewport-height',previousHeight],['--gateway-viewport-top',previousTop]]){
      if(value)root.style.setProperty(name,value);else root.style.removeProperty(name);
    }
    doc.body.toggleAttribute('data-gateway-keyboard-open',previousKeyboard);
  };
}

export function registerComposerLayoutUI(ctx, {IconChevronDownOutlineRegular,IconChevronUpOutlineRegular}) {
  function ComposerFold({sessionId}) {
    const button=React.useRef(null), parts=React.useRef(null), savedScroll=React.useRef(0);
    const [fold,setFold]=React.useState({sessionId,value:false});
    const [length,setLength]=React.useState({sessionId,value:false});
    const collapsed=fold.sessionId===sessionId&&fold.value;
    const long=length.sessionId===sessionId&&length.value;
    const locale=React.useSyncExternalStore(listener=>ctx.locale.subscribe(listener),()=>ctx.locale.getSnapshot().active);
    const label=locale==='ru'?(collapsed?'Развернуть сообщение':'Свернуть сообщение'):(collapsed?'Expand message':'Collapse message');
    React.useLayoutEffect(()=>{
      const card=button.current?.closest('[data-composer-card]');
      const scroll=card?.querySelector('[data-input-scroll]'), editor=scroll?.querySelector('[data-composer-input]');
      if(!card||!scroll||!editor)return;
      const previousInert=scroll.inert;
      parts.current={card,scroll,editor,previousInert};
      const media=matchMedia('(max-width: 720px)');
      let frame=null,disposed=false;
      function measure(){
        if(disposed)return;
        frame=null;
        // Keep the native editor and selection: only presentation is folded.
        const minimum=parseFloat(getComputedStyle(editor).minHeight)||0;
        const tall=editor.scrollHeight>Math.max(80,minimum)+1;
        const next=media.matches&&tall&&!card.querySelector('[data-trigger-menu]');
        card.toggleAttribute('data-gateway-composer-long',next);
        setLength(previous=>previous.sessionId===sessionId&&previous.value===next?previous:{sessionId,value:next});
        if(!next)setFold(previous=>previous.sessionId===sessionId&&!previous.value?previous:{sessionId,value:false});
      }
      function schedule(){if(!disposed&&frame===null)frame=requestAnimationFrame(measure);}
      function breakpoint(){if(!media.matches)setFold({sessionId,value:false});schedule();}
      const resize=new ResizeObserver(schedule), mutation=new MutationObserver(schedule);
      resize.observe(editor);resize.observe(scroll);resize.observe(card);
      mutation.observe(card,{childList:true,subtree:true,characterData:true});
      editor.addEventListener('input',schedule);
      media.addEventListener('change',breakpoint);
      measure();
      return ()=>{
        disposed=true;if(frame!==null)cancelAnimationFrame(frame);
        resize.disconnect();mutation.disconnect();editor.removeEventListener('input',schedule);
        media.removeEventListener('change',breakpoint);
        card.removeAttribute('data-gateway-composer-long');card.removeAttribute('data-gateway-composer-collapsed');
        scroll.inert=previousInert;parts.current=null;
      };
    },[sessionId]);
    React.useLayoutEffect(()=>{
      const current=parts.current;if(!current)return;
      current.card.toggleAttribute('data-gateway-composer-collapsed',collapsed);
      current.scroll.inert=current.previousInert||collapsed;
    },[sessionId,collapsed]);
    function toggle(){
      const current=parts.current;if(!current)return;
      if(current.card.querySelector('[data-trigger-menu]'))return;
      if(!collapsed){
        savedScroll.current=current.scroll.scrollTop;
        button.current?.focus({preventScroll:true});
        setFold({sessionId,value:true});
      }else{
        // Keep focus inside the tap gesture so mobile Safari can reopen its keyboard.
        current.card.removeAttribute('data-gateway-composer-collapsed');
        current.scroll.inert=current.previousInert;
        setFold({sessionId,value:false});
        try{
          const session=ctx.sessions.scope(sessionId),input=session?.get('conversation')?.input.for(session);
          if(input)input.focus();else current.editor.focus({preventScroll:true});
        }catch{current.editor.focus({preventScroll:true});}
        requestAnimationFrame(()=>{if(parts.current===current)current.scroll.scrollTop=savedScroll.current;});
      }
    }
    return React.createElement('button',{ref:button,type:'button',className:'gateway-composer-fold',
      'data-gateway-composer-toggle':true,hidden:!long,title:label,'aria-label':label,'aria-expanded':!collapsed,
      onClick:toggle},React.createElement(collapsed?IconChevronUpOutlineRegular:IconChevronDownOutlineRegular,{size:18}));
  }
  ctx.slots.inject('conversation.input.overlay',()=>ctx.slots.register({name:'conversation.input.overlay',
    id:'gateway.composer-fold',order:90,inject:sessionId=>({sessionId})},ComposerFold));
}
