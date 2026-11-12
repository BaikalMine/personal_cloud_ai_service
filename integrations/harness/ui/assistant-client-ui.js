function registerAssistantPresentationUI(ctx, primitives, images) {
  const h=React.createElement, {MarkdownText,IconFullscreenOutlineRegular,IconDownloadOutlineRegular}=primitives;
  ctx.effect(()=>ctx.locale.register('gatewayAssistant',{en:{ready:'Image ready',open:'Open image',download:'Download original',invalid:'The model printed a tool invocation as text. That invocation was not executed.'}}),'gateway: assistant result copy');
  const t=ctx.locale.bind('gatewayAssistant');
  function AssistantText({text,streaming,labels,fileMentions,pathImages,textInteractionSource,presentedFiles,sessionId}) {
    React.useSyncExternalStore(fn=>ctx.locale.subscribe(fn),()=>ctx.locale.getSnapshot().active);
    const result=reconcilePresentedText(text,presentedFiles),[viewer,setViewer]=React.useState(null);
    return h(React.Fragment,null,
      result.text.trim()&&h(MarkdownText,{text:result.text,streaming,labels,fileMentions,pathImages,textInteractionSource}),
      result.files.map(file=>h('div',{key:file.path,className:'gateway-assistant-file-notice','data-gateway-presentation-recovered':true},
        h('span',null,t('ready')),
        h('button',{type:'button',className:'gateway-file-tool',title:t('open'),'aria-label':t('open'),onClick:event=>setViewer({file,opener:event.currentTarget})},h(IconFullscreenOutlineRegular,{size:22})),
        h('a',{className:'gateway-file-tool',href:images.imageURL(sessionId,file.path,false),download:file.path.split('/').at(-1),title:t('download'),'aria-label':t('download')},h(IconDownloadOutlineRegular,{size:22})))),
      result.malformed&&h('p',{className:'gateway-assistant-format-warning',role:'status'},t('invalid')),
      viewer&&h(images.ImageViewer,{file:viewer.file,sessionId,opener:viewer.opener,onClose:()=>setViewer(null)}));
  }
  ctx.slots.inject('gateway.chat.assistant-text',()=>ctx.slots.register({name:'gateway.chat.assistant-text',id:'gateway.assistant-text',inject:sessionId=>({sessionId})},AssistantText));
}
