function registerWorkspaceFilesUI(ctx, primitives) {
  const h = React.createElement;
  const { Menu, IconEllipsisOutlineRegular, IconDownloadOutlineRegular, IconEditOutlineRegular,
    IconTrashOutlineRegular, IconPlusOutlineRegular, IconProjectAddOutlineRegular, IconCloseOutlineRegular, IconPanelLeftOutlineRegular, IconCopyOutlineRegular } = primitives;
  const english = { actions:'File actions', files:'Back to files', toChat:'Back to chat', document:'Document', navigate:'Find and chapters', searchLabel:'Search', searchText:'Find in document', previousMatch:'Previous match', nextMatch:'Next match', matches:'{current} of {total}', noMatches:'No matches', chapters:'Contents', chooseChapter:'Choose a chapter or heading', copyChapter:'Copy section', copied:'Section copied', selectToCopy:'Text selected. Use the system menu to copy.', download:'Download', edit:'Edit text', rename:'Rename', delete:'Delete', mkdir:'New folder', upload:'Upload files',
    copyPath:'Copy folder path', copyItemPath:'Copy path', pathCopied:'Path copied', pathCopyFailed:'Could not copy. Select and copy the path below.', folderPath:'Folder path', itemPath:'Path', copy:'Copy', cancel:'Cancel', save:'Save', close:'Close', loading:'Loading...', working:'Working...', name:'Name', text:'File contents',
    copyToChat:'Copy to…', copyChatTarget:'Destination chat', copyChatChoose:'Choose a chat', copyChatCurrent:'Current chat', copyChatUntitled:'Untitled chat',
    copyChatFolder:'Destination folder', copyChatRoot:'Workspace root', copyChatListFailed:'Could not load other chats.', copyChatDone:'File copied.', copyChatFolderDone:'Folder copied.',
    copyChatSearch:'Find a chat', copyChatNoMatches:'No matching chats.', copySourceFile:'File', copySourceFolder:'Folder',
    copyChatExists:'A file or folder with this name already exists there. Choose a different destination.', copyChatUnavailable:'One of the chats is no longer available.', not_file:'Select a file.',
    invalid_copy_folder:'Enter a relative folder path without empty, current or parent segments.', copy_retention:'Generated output can only be copied where retention rules for the same job still apply.',
    copy_descendant:'Choose a destination outside the source folder.', copy_workspace_root:'Choose a folder inside the workspace to copy.',
    copy_limit:'The folder has too many items or too many levels of nesting.', copy_unsupported:'The folder contains an item that cannot be copied.',
    deleteTitle:'Delete this item?', deleteFile:'The file will be permanently deleted.', deleteFolder:'Only empty folders can be deleted.',
    discardTitle:'Discard unsaved changes?', discard:'Discard changes', keep:'Keep editing', fileActions:'Actions for {name}',
    uploaded:'Uploaded: {count}', uploading:'Uploading {percent}% · {loaded} / {total} MB', uploadLimit:'Each file must be no larger than 512 MB.', stream_upload_too_large:'The file exceeds 512 MB.', cancelUpload:'Cancel upload', uploadCancelled:'Upload cancelled',
    invalid_name:'Enter a name without slashes or control characters.', invalid_request:'Invalid file request.', session_unavailable:'The session is unavailable. Open it again.',
    outside_workspace:'This item is outside this session\'s workspace.', symlink:'Actions on symbolic links are not supported.', not_found:'This item no longer exists.',
    not_editable:'This file cannot be edited as text (up to 1 MB).', not_directory:'Select a folder.', changed:'The file changed in another task. Reopen it before changing it.',
    exists:'An item with this name already exists. Choose another name.', not_empty:'The folder is not empty. Its contents were not deleted.', permission:'Access to this item was denied.',
    upload_too_large:'The upload exceeds 20 MB or is invalid.', unavailable:'The operation was not confirmed. Refresh the files before trying again.' };
  ctx.effect(() => ctx.locale.register('gatewayFiles', { en: english }), 'gateway: file action copy');
  const t = ctx.locale.bind('gatewayFiles');
  const listeners = new Set();
  let current, nextEntry = 0;
  const publish = value => { current = value; for (const fn of listeners) fn(); };
  const subscribe = fn => { listeners.add(fn); return () => listeners.delete(fn); };
  const useCopy = () => { React.useSyncExternalStore(fn => ctx.locale.subscribe(fn), () => ctx.locale.getSnapshot().active); return t; };
  async function call(request, signal) {
    const response = await fetch(new URL('./api/gateway-files/manage', document.baseURI), {
      method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(request),
      signal, credentials:'same-origin', redirect:'error',
    });
    if (!response.ok) throw new Error(t('unavailable'));
    const result = await response.json();
    if (!result.ok) throw Object.assign(new Error(t(result.error.code in english ? result.error.code : 'unavailable')), { code: result.error.code });
    return result.value;
  }
  function open(action, owner) {
    const entry = { ...owner, action, key: ++nextEntry, opener: document.activeElement, loading: true, error: '', value: '', info: null };
    current?.controller?.abort();
    if (action === 'copyToChat') { entry.controller = new AbortController(); entry.folder = ''; }
    publish(entry);
    const signal = entry.controller && AbortSignal.any([entry.controller.signal, AbortSignal.timeout(15000)]);
    const chats = action === 'copyToChat' ? Promise.resolve().then(() => gatewayListOtherChats(owner.sessionId, signal)).catch(() => null) : null;
    Promise.all([call({ action: ['edit','navigate'].includes(action) ? 'read' : 'inspect', sessionId: owner.sessionId, path: owner.absolutePath }, signal), chats])
      .then(([info, chats]) => {
        if (current?.key !== entry.key) return;
        if (action === 'copyToChat' && info.root) throw new Error(t('copy_workspace_root'));
        publish({ ...current, loading:false, info, chats, value:['edit','navigate'].includes(action) ? info.text : action === 'rename' ? info.name : '' });
      })
      .catch(error => { if (current?.key === entry.key) publish({ ...current, loading: false, error: error.message }); });
  }
  function close() { const opener = current?.opener; current?.controller?.abort(); publish(undefined); if (opener?.isConnected) opener.focus({ preventScroll: true }); }
  function resourcePath(tab, root, sessionId) {
    try {
      const url = new URL(tab.contentId), parts = url.pathname.split('/').map(decodeURIComponent);
      if (url.protocol !== 'dsh-resource:' || url.host !== 'file' || parts[1] !== 'session' || parts[2] !== sessionId) return;
      const path = parts.slice(3).join('/');
      return path.startsWith('/') ? path : root.replace(/\/$/, '') + '/' + path;
    } catch {}
  }
  function reconcile(entry, result) {
    if (!['delete','rename'].includes(entry.action)) return;
    let wasOpen = false;
    for (const tab of ctx.sidebarRight.tabsIn(entry.sessionId)) {
      const path = resourcePath(tab, entry.info.workspace_root, entry.sessionId);
      if (path === entry.absolutePath || entry.info.type === 'directory' && path?.startsWith(entry.absolutePath + '/')) {
        wasOpen = true; ctx.sidebarRight.closeIn(entry.sessionId, tab.id);
      }
    }
    if (entry.action === 'rename' && wasOpen && entry.info.type === 'file') {
      const address = 'dsh-resource://file/session/' + encodeURIComponent(entry.sessionId) + '/' + result.path.split('/').map(encodeURIComponent).join('/');
      ctx.sidebarRight.openResourceIn(entry.sessionId, address);
    }
  }
  function download(owner) {
    const url = new URL('./api/gateway-files/download', document.baseURI);
    url.searchParams.set('sessionId', owner.sessionId); url.searchParams.set('path', owner.absolutePath);
    const anchor = document.createElement('a'); anchor.href = url.href; anchor.download = owner.absolutePath.split('/').at(-1); document.body.append(anchor); anchor.click(); anchor.remove();
  }
  function FileActions({ absolutePath, sessionId, entryType = 'file' }) {
    const t = useCopy(), [expanded, setExpanded] = React.useState(false);
    const name = absolutePath.split('/').at(-1), owner = { absolutePath, sessionId, entryType };
    const items = [
      ...(entryType === 'file' ? [{ id:'download', label:t('download'), icon:h(IconDownloadOutlineRegular) }, { id:'edit', label:t('edit'), icon:h(IconEditOutlineRegular) }] : []),
      { id:'copyItemPath', label:t('copyItemPath'), icon:h(IconCopyOutlineRegular) },
      ...(['file','directory'].includes(entryType) ? [{ id:'copyToChat', label:t('copyToChat'), icon:h(IconCopyOutlineRegular) }] : []),
      { id:'rename', label:t('rename'), icon:h(IconEditOutlineRegular) },
      ...(entryType === 'directory' ? [{ id:'mkdir', label:t('mkdir'), icon:h(IconProjectAddOutlineRegular) }] : []),
      { id:'delete', label:t('delete'), icon:h(IconTrashOutlineRegular), danger:true },
    ];
    const label = t('fileActions', { name });
    return h(Menu, { open: expanded, portal: true, align:'end', items, onClose:() => setExpanded(false),
      onSelect: action => { setExpanded(false); if (action === 'download') download(owner); else open(action, owner); },
      anchor: h('button', { type:'button', className:'gateway-file-tool', 'data-gateway-file-actions':absolutePath, title:label, 'aria-label':label,
        'aria-expanded':expanded, onClick: event => { event.stopPropagation(); setExpanded(value => !value); } }, h(IconEllipsisOutlineRegular, { size:20 })) });
  }
  function FilesToolbar({ absolutePath, sessionId }) {
    const t = useCopy(), input = React.useRef(null), [status, setStatus] = React.useState(''), [error, setError] = React.useState(''), [busy, setBusy] = React.useState(false), [progress, setProgress] = React.useState(null), [copyFallback, setCopyFallback] = React.useState(false);
    const lifetime = React.useRef(), activeUpload = React.useRef(null), copyInput = React.useRef(null), copySequence = React.useRef(0), owner = React.useRef(null);
    owner.current = { sessionId, absolutePath };
    React.useEffect(() => { const controller = new AbortController(); lifetime.current = controller; setBusy(false); setStatus(''); setError(''); setProgress(null); setCopyFallback(false); return () => { copySequence.current++; controller.abort(); }; }, [sessionId, absolutePath]);
    React.useEffect(() => { if (!status || busy) return; const timer=setTimeout(() => setStatus(''), 4500); return () => clearTimeout(timer); }, [status, busy]);
    React.useEffect(() => { if (copyFallback && copyInput.current) { copyInput.current.focus(); copyInput.current.select(); } }, [copyFallback]);
    async function upload(event) {
      const selected = [...event.target.files]; event.target.value = ''; if (!selected.length || busy) return;
      if (activeUpload.current) return;
      const owner = lifetime.current.signal, controller = new AbortController(), signal = controller.signal;
      activeUpload.current = controller;
      const abort = () => controller.abort(); owner.addEventListener('abort', abort, { once:true });
      setBusy(true); setError(''); setStatus(''); setProgress(null);
      let timer;
      try {
        for (const [index, file] of selected.entries()) {
          if (file.size > 512 * 1024 * 1024) throw new Error(t('uploadLimit'));
          const url = new URL('./api/gateway-files/upload', document.baseURI);
          for (const [key, value] of Object.entries({ sessionId, path:absolutePath, name:file.name, bytes:String(file.size) })) url.searchParams.set(key, value);
          const meter = createUploadMeter(); let loaded = 0;
          const publish = () => {
            if (signal.aborted || owner.aborted) return;
            const stats = meter.sample(loaded,file.size);
            setProgress({ ...stats, name:file.name });
            setStatus(t('uploading', { percent:stats.percent, loaded:(loaded/1048576).toFixed(1), total:(file.size/1048576).toFixed(1) }));
          };
          publish(); timer = setInterval(publish,500);
          await uploadWorkspaceFile({ url, file, signal, onProgress:value => { loaded = value; publish(); } });
          clearInterval(timer); timer = undefined;
          if (!signal.aborted) setStatus(t('uploaded', { count:index + 1 }));
          signal.throwIfAborted();
        }
      } catch (error) { if (!owner.aborted) { if (signal.aborted) setStatus(t('uploadCancelled')); else setError(error.code ? t(error.code in english ? error.code : 'unavailable') : error.message); } }
      finally { clearInterval(timer); owner.removeEventListener('abort',abort); if (activeUpload.current === controller) activeUpload.current = null; if (!owner.aborted) { setBusy(false); setProgress(null); } }
    }
    async function copyDirectory() {
      const expected = owner.current, sequence = ++copySequence.current;
      const isCurrent = () => copySequence.current === sequence && owner.current.sessionId === expected.sessionId && owner.current.absolutePath === expected.absolutePath && !lifetime.current?.signal.aborted;
      setError(''); setStatus(''); setCopyFallback(false);
      try { await navigator.clipboard.writeText(absolutePath); if (isCurrent()) setStatus(t('pathCopied')); }
      catch { if (isCurrent()) { setCopyFallback(true); setError(t('pathCopyFailed')); } }
    }
    return h('div', { className:'gateway-file-toolbar' },
      h(ChatButton),
      h('input', { ref:input, type:'file', multiple:true, hidden:true, onChange:upload, 'aria-label':t('upload') }),
      h('button', { type:'button', className:'gateway-file-tool', title:t('upload'), 'aria-label':t('upload'), disabled:busy, onClick:() => input.current.click() }, h(IconPlusOutlineRegular, { size:20 })),
      h('button', { type:'button', className:'gateway-file-tool', title:t('mkdir'), 'aria-label':t('mkdir'), disabled:busy, onClick:() => open('mkdir', { absolutePath, sessionId }) }, h(IconProjectAddOutlineRegular, { size:20 })),
      h('button', { type:'button', className:'gateway-file-tool', title:t('copyPath'), 'aria-label':t('copyPath'), disabled:busy, onClick:copyDirectory }, h(IconCopyOutlineRegular, { size:20 })),
      busy && h('button', { type:'button', className:'gateway-file-tool gateway-upload-cancel', title:t('cancelUpload'), 'aria-label':t('cancelUpload'), onClick:() => activeUpload.current?.abort() }, h(IconCloseOutlineRegular, { size:20 })),
      (busy || error || status) && h('span', { className:'gateway-file-upload-status', role:error ? 'alert' : 'status', title:error || status },
        busy && progress && h('span', { className:'gateway-upload-name' },progress.name),
        h('span',null,error || status || t('working')),
        busy && progress && h('progress', { max:100,value:progress.percent,'aria-label':t('upload') }),
        busy && progress && h('span', { className:'gateway-upload-metrics' },uploadMetricsText(progress,/[А-Яа-я]/.test(t('upload'))))),
      copyFallback && h('input', { ref:copyInput, className:'gateway-copy-path-fallback', readOnly:true, value:absolutePath, 'aria-label':t('folderPath'), onFocus:event => event.target.select(), onClick:event => event.currentTarget.select() }));
  }
  function DocumentActions(props) {
    const t = useCopy();
    return h('div', { className:'gateway-file-toolbar' },
      h(ChatButton),
      h('button', { type:'button', className:'gateway-file-tool', title:t('files'), 'aria-label':t('files'),
        onClick:() => ctx.sidebarRight.openTabIn(props.sessionId, 'files') }, h(IconPanelLeftOutlineRegular, { size:20 })),
      /\.(md|markdown|txt)$/i.test(props.absolutePath) && h('button', { type:'button', className:'gateway-file-navigate', onClick:() => open('navigate', props) }, t('navigate')),
      h(FileActions, props));
  }
  function ChatButton() {
    const t = useCopy();
    return h('button', { type:'button', className:'gateway-file-return-chat', onClick:() => ctx.sidebarRight.toggleExpanded() }, h(IconPanelLeftOutlineRegular, { size:18 }), t('toChat'));
  }
  const navigation = new Map();
  function DocumentNavigator({ entry, t }) {
    const key = entry.sessionId + '\0' + entry.absolutePath;
    const saved = navigation.get(key) || {};
    const [query, setQuery] = React.useState(saved.query || ''), [match, setMatch] = React.useState(saved.match ?? -1);
    const [chapter, setChapter] = React.useState(saved.chapter || ''), [notice, setNotice] = React.useState('');
    const [target, setTarget] = React.useState(null);
    const dialog = React.useRef(null), area = React.useRef(null), marker = React.useRef(null), restored = React.useRef(false);
    const text = entry.value || '';
    const headings = React.useMemo(() => {
      const found = [], pattern = /^(?:#{1,6}[ \t]+[^\r\n]+|[ \t]*(?:\*\*)?(?:Глава|Chapter)[ \t]+\d+[^\r\n]*)/gmi;
      for (const item of text.matchAll(pattern)) {
        const raw = item[0].trim(), markdown = raw.match(/^(#{1,6})\s/);
        found.push({ start:item.index, headingEnd:item.index + item[0].length, level:markdown ? markdown[1].length : 2, name:raw.replace(/^#+\s*|\*\*/g, '').slice(0, 100) });
      }
      const stack = [];
      for (const item of found) {
        while (stack.length && stack.at(-1).level >= item.level) stack.pop().end = item.start;
        item.end = text.length; stack.push(item);
      }
      return found;
    }, [text]);
    const matches = React.useMemo(() => {
      if (!query) return [];
      const positions = [], haystack = text.toLocaleLowerCase(), needle = query.toLocaleLowerCase();
      for (let pos = 0; (pos = haystack.indexOf(needle, pos)) !== -1; pos += Math.max(needle.length, 1)) positions.push(pos);
      return positions;
    }, [text, query]);
    React.useEffect(() => { dialog.current?.showModal(); }, []);
    React.useEffect(() => {
      if (!entry.loading && area.current && !restored.current) { area.current.scrollTop = saved.scrollTop || 0; restored.current = true; }
    }, [entry.loading]);
    React.useLayoutEffect(() => {
      const el = area.current, hit = marker.current;
      if (!el || !hit || !target) return;
      el.scrollTop += hit.getBoundingClientRect().top - el.getBoundingClientRect().top - el.clientHeight / 3;
      remember({ scrollTop:el.scrollTop });
    }, [target]);
    function remember(values) { navigation.set(key, { ...navigation.get(key), ...values }); }
    function showRange(start, end) { setTarget({ start, end }); }
    function selectRange(start, end) {
      const el = area.current; if (!el) return;
      const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT), range = document.createRange();
      let offset = 0, startSet = false, node;
      while ((node = walker.nextNode())) {
        const next = offset + node.length;
        if (!startSet && start <= next) { range.setStart(node, start - offset); startSet = true; }
        if (startSet && end <= next) { range.setEnd(node, end - offset); const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range); return; }
        offset = next;
      }
    }
    function goMatch(index) {
      if (!matches.length) return;
      const next = (index + matches.length) % matches.length; setMatch(next); remember({ match:next }); showRange(matches[next], matches[next] + query.length);
    }
    function goChapter(value) {
      setChapter(value); remember({ chapter:value }); setNotice('');
      const item = headings[Number(value)]; if (value !== '' && item) showRange(item.start, item.headingEnd);
    }
    async function copyChapter() {
      const item = headings[Number(chapter)]; if (chapter === '' || !item) return;
      try { await navigator.clipboard.writeText(text.slice(item.start, item.end)); setNotice(t('copied')); }
      catch { selectRange(item.start, item.end); setNotice(t('selectToCopy')); }
    }
    return h('dialog', { ref:dialog, className:'gateway-file-dialog gateway-document-navigator', 'aria-labelledby':'gateway-navigator-title', onCancel:event => { event.preventDefault(); close(); } },
      h('div', { className:'gateway-navigator-layout' },
        h('header', { className:'gateway-navigator-header' },
          h('div', { className:'gateway-navigator-identity' }, h('span', null, t('document')),
            h('h2', { id:'gateway-navigator-title', title:entry.info?.name || entry.absolutePath.split('/').at(-1) }, entry.info?.name || entry.absolutePath.split('/').at(-1))),
          h('button', { type:'button', className:'gateway-navigator-chat', onClick:() => { close(); ctx.sidebarRight.toggleExpanded(); } }, h(IconPanelLeftOutlineRegular, { size:18 }), t('toChat')),
          h('button', { type:'button', className:'gateway-file-tool', 'aria-label':t('close'), title:t('close'), onClick:close }, h(IconCloseOutlineRegular, { size:22 }))),
        h('div', { className:'gateway-navigator-controls' },
          h('div', { className:'gateway-navigator-group' }, h('div', { className:'gateway-navigator-label' }, h('label', { htmlFor:'gateway-document-search' }, t('searchLabel')),
            query && h('span', { role:'status', className:'gateway-navigator-count' }, matches.length ? t('matches', { current:Math.max(0, Math.min(match + 1, matches.length)), total:matches.length }) : t('noMatches'))),
            h('div', { className:'gateway-navigator-search' }, h('input', { id:'gateway-document-search', type:'search', value:query, placeholder:t('searchText'), 'aria-label':t('searchText'), onChange:event => { const value=event.target.value; setQuery(value); setMatch(-1); remember({ query:value, match:-1 }); }, onKeyDown:event => { if (event.key === 'Enter') { event.preventDefault(); goMatch(match + 1); } } }),
              h('button', { type:'button', 'aria-label':t('previousMatch'), title:t('previousMatch'), disabled:!matches.length, onClick:() => goMatch(match < 0 ? matches.length - 1 : match - 1) }, '↑'),
              h('button', { type:'button', 'aria-label':t('nextMatch'), title:t('nextMatch'), disabled:!matches.length, onClick:() => goMatch(match + 1) }, '↓'))),
          headings.length > 0 && h('div', { className:'gateway-navigator-group' }, h('label', { htmlFor:'gateway-document-chapter', className:'gateway-navigator-label' }, t('chapters')),
            h('div', { className:'gateway-navigator-chapters' }, h('select', { id:'gateway-document-chapter', value:chapter, 'aria-label':t('chapters'), onChange:event => goChapter(event.target.value) },
              h('option', { value:'' }, t('chooseChapter')), headings.map((item, index) => h('option', { key:item.start, value:String(index) }, item.name))),
              h('button', { type:'button', disabled:chapter === '', onClick:copyChapter }, t('copyChapter')))),
          notice && h('div', { role:'status', className:'gateway-navigator-count' }, notice)),
        entry.loading ? h('p', { className:'gateway-navigator-message', role:'status' }, t('loading')) : entry.error ? h('p', { className:'gateway-navigator-message gateway-file-error', role:'alert' }, entry.error) :
          h('div', { ref:area, className:'gateway-navigator-text', 'data-gateway-navigator-text':'', role:'region', tabIndex:0, 'aria-label':t('text'), onScroll:event => remember({ scrollTop:event.currentTarget.scrollTop }) },
            target ? [text.slice(0, target.start), h('mark', { ref:marker, key:'target' }, text.slice(target.start, target.end)), text.slice(target.end)] : text)));
  }
  const RowActions = props => h('span', { className:'gateway-file-row-actions' }, h(FileActions, props));
  function FileDialog() {
    const entry = React.useSyncExternalStore(subscribe, () => current), t = useCopy(), dialog = React.useRef(null), pathInput = React.useRef(null);
    const dirty = entry?.action === 'edit' && entry.info && entry.value !== entry.info.text;
    const cannotClose = entry?.busy && entry.action !== 'copyToChat';
    function requestClose() { if (cannotClose) return; if (dirty) publish({ ...entry, discard:true }); else close(); }
    React.useEffect(() => { if (entry && dialog.current && !dialog.current.open) dialog.current.showModal(); }, [entry?.key]);
    React.useEffect(() => ctx.uiSession.adapter.current.subscribe(() => { if (current && ctx.uiSession.adapter.current.getSnapshot().key !== current.sessionId) close(); }), []);
    if (!entry) return null;
    if (entry.action === 'navigate') return h(DocumentNavigator, { key:entry.key, entry, t });
    const copying = entry.action === 'copyToChat', copied = copying && entry.notice;
    const canCopy = copying && !copied && (entry.value === entry.sessionId || entry.chats?.some(chat => chat.id === entry.value));
    const title = entry.discard ? t('discardTitle') : entry.action === 'delete' ? t('deleteTitle') : t(entry.action);
    async function submit(event) {
      event.preventDefault(); if (entry.loading || current?.key !== entry.key || current.busy || !entry.info || copying && (!canCopy || current.notice)) return;
      if (entry.action === 'copyItemPath') {
        try { await navigator.clipboard.writeText(entry.absolutePath); if (current?.key === entry.key) publish({ ...current, notice:t('pathCopied'), error:'' }); }
        catch { if (current?.key === entry.key && current.sessionId === entry.sessionId) { pathInput.current?.focus(); pathInput.current?.select(); publish({ ...current, error:t('pathCopyFailed'), notice:'' }); } }
        return;
      }
      publish({ ...entry, busy:true, error:'' });
      try {
        const result = await call({ action:entry.action === 'edit' ? 'save' : entry.action, sessionId:entry.sessionId, path:entry.absolutePath,
          ...entry.info.version ? { version:entry.info.version } : {},
          ...copying ? { targetSessionId:entry.value, targetFolder:entry.folder } : entry.action === 'edit' ? { text:entry.value } : ['rename','mkdir'].includes(entry.action) ? { name:entry.value } : {} }, entry.controller?.signal);
        reconcile(entry, result);
        if (current?.key === entry.key) {
          if (copying) publish({ ...current, busy:false, notice:t(entry.info.type === 'directory' ? 'copyChatFolderDone' : 'copyChatDone') });
          else close();
        }
      } catch (error) {
        if (current?.key === entry.key) publish({ ...current, busy:false, error:copying && error.code === 'exists' ? t('copyChatExists') :
          copying && error.code === 'session_unavailable' ? t('copyChatUnavailable') : error.message });
      }
    }
    const itemName=entry.info?.name ?? entry.absolutePath.split('/').at(-1);
    const directory=entry.info?.type==='directory'||!entry.info&&entry.entryType==='directory';
    const ItemIcon=directory?primitives.IconFolderOutlineRegular:primitives.IconDocumentOutlineRegular;
    return h('dialog', { ref:dialog, className:copying?'gateway-file-dialog gateway-transfer-dialog':'gateway-file-dialog', 'data-gateway-file-dialog':entry.action, 'aria-labelledby':'gateway-file-dialog-title',
      onCancel:event => { event.preventDefault(); requestClose(); } },
      h('form', { onSubmit:submit, className:entry.action === 'edit' ? 'gateway-file-form gateway-file-editor' : 'gateway-file-form' },
        h('header', { className:'gateway-file-dialog-header' }, h('h2', { id:'gateway-file-dialog-title' }, title),
          h('button', { type:'button', className:'gateway-file-tool', 'aria-label':t('close'), title:t('close'), disabled:cannotClose, onClick:requestClose }, h(IconCloseOutlineRegular, { size:22 }))),
        h('div', { className:'gateway-file-dialog-content' },
          copying ? h('div',{className:'gateway-transfer-item',title:entry.absolutePath},
            ItemIcon&&h(ItemIcon,{size:20,'aria-hidden':true}),
            h('div',null,h('span',null,t(directory?'copySourceFolder':'copySourceFile')),h('strong',null,itemName))) :
            h('p', { className:'gateway-file-dialog-name', title:entry.absolutePath }, itemName),
          entry.loading ? h('p', { role:'status' }, t('loading')) : !entry.discard && entry.info && (
            entry.action === 'delete' ? h('p', null, t(entry.info.type === 'directory' ? 'deleteFolder' : 'deleteFile')) : entry.action === 'copyItemPath' ?
              h('label', { className:'gateway-file-field' }, h('span', null, t('itemPath')), h('input', { ref:pathInput, readOnly:true, value:entry.absolutePath, onFocus:event => event.target.select(), onClick:event => event.currentTarget.select() })) :
            copying ? (copied ? null : h(React.Fragment, null,
              h(GatewayTransferChatPicker,{key:entry.key,chats:[{id:entry.sessionId,title:t('copyChatCurrent')},...(entry.chats??[])],
                value:entry.value,disabled:entry.busy,label:t('copyChatTarget'),choose:t('copyChatChoose'),untitled:t('copyChatUntitled'),
                search:t('copyChatSearch'),noMatches:t('copyChatNoMatches'),onChange:value=>publish({...current,value,error:''})}),
              h('label', { className:'gateway-file-field' }, h('span', null, t('copyChatFolder')),
                h('input', { value:entry.folder, placeholder:t('copyChatRoot'), disabled:entry.busy, maxLength:4096, spellCheck:false,
                  onChange:event => publish({ ...current, folder:event.target.value, error:'' }) })),
              !entry.chats && h('p', { role:'status' }, t('copyChatListFailed')))) :
              h('label', { className:'gateway-file-field' }, h('span', null, t(entry.action === 'edit' ? 'text' : 'name')),
                h(entry.action === 'edit' ? 'textarea' : 'input', { value:entry.value, disabled:entry.busy, spellCheck:false, maxLength:entry.action === 'edit' ? 1048576 : 220,
                  autoFocus:entry.action !== 'edit', onChange:event => publish({ ...current, value:event.target.value }), 'data-gateway-file-input':true }))),
          entry.error && h('p', { role:'alert', className:'gateway-file-error' }, entry.error), entry.notice && h('p', { role:'status' }, entry.notice)),
        h('footer', { className:'gateway-file-dialog-footer' }, copied ? h('button', { type:'button', className:'gateway-file-primary', onClick:requestClose, autoFocus:true }, t('close')) : entry.discard ? h(React.Fragment, null,
          h('button', { type:'button', onClick:() => publish({ ...entry, discard:false }) }, t('keep')),
          h('button', { type:'button', className:'gateway-file-danger', onClick:close }, t('discard'))) : h(React.Fragment, null,
          h('button', { type:'button', onClick:requestClose, disabled:cannotClose, autoFocus:entry.action === 'delete' }, t('cancel')),
          h('button', { type:'submit', className:entry.action === 'delete' ? 'gateway-file-danger' : 'gateway-file-primary', disabled:entry.loading || entry.busy || !entry.info || copying && !canCopy || entry.action === 'edit' && !dirty || ['rename','mkdir'].includes(entry.action) && !entry.value.trim() }, entry.busy ? t('working') : t(entry.action === 'edit' ? 'save' : copying || entry.action === 'copyItemPath' ? 'copy' : entry.action))))));
  }
  for (const [name, id, component] of [
    ['gateway.files.row.actions','gateway.file-row-actions',RowActions],
    ['sidebar.right.tab.document.actions','gateway.document-actions',DocumentActions],
    ['sidebar.right.tab.files.actions','gateway.files-toolbar',FilesToolbar],
    ['shell.overlay','gateway.file-dialog',FileDialog],
  ]) ctx.slots.inject(name, () => ctx.slots.register({ name, id }, component));
}
