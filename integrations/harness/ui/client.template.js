window.__ModuleLoader__.load({
  id: '@ai-gateway/harness-ui',
  factory: (require) => {
    const React = require('react');
    const { IconPanelLeftOutlineRegular, IconCloseOutlineRegular } = require('@deepseek-ai/dsh-client-ui-primitives');
    const dictionaries = /* DICTIONARIES */;
    const nativeRuMerged = /* NATIVE_RU_MERGED */;
    const css = /* STYLES */;
    const frameSelector = '.pI_x6G_frame';
    const preferenceKey = 'ai-gateway.harness.language';
    /* WORKSPACE_FILES */
    /* ULTRA_IMAGES */
    /* ASSISTANT_FORMAT */
    /* ASSISTANT_UI */
    /* COMPACT_FEEDBACK */
    /* CHAT_MEMORY */
    /* COMPOSER_LAYOUT */
    /* TEXT_SELECTION_START */
    function installTextSelectionBoundary(doc = document) {
      const selector = '[data-textpreview-body] [data-code-block-content] pre, [data-textpreview-plain], [data-document-markdown], [data-gateway-navigator-text], [data-conversation-content]';
      const editable = 'input, textarea, [contenteditable]:not([contenteditable="false"])';
      const element = node => node?.nodeType === 1 ? node : node?.parentElement;
      const scope = node => element(node)?.closest(selector);
      const usable = node => {
        const modal = doc.querySelector('dialog:modal');
        return node?.isConnected && !node.closest('[inert], [hidden]') && (!modal || modal.contains(node)) && node.getClientRects().length > 0;
      };
      const selection = () => doc.defaultView.getSelection();
      // Native selection handles, scrolling and copying must not trigger range rewrites.
      function keydown(event) {
        if ((!event.ctrlKey && !event.metaKey) || event.altKey || event.key.toLowerCase() !== 'a' ||
            element(event.target)?.closest(editable + ', button, a, [role="tab"]')) return;
        const selected = selection();
        const target = scope(event.target) || scope(selected?.anchorNode);
        if (!selected || !usable(target)) return;
        event.preventDefault();
        const range = doc.createRange();
        range.selectNodeContents(target);
        selected.removeAllRanges();
        selected.addRange(range);
      }
      doc.addEventListener('keydown', keydown);
      return () => {
        doc.removeEventListener('keydown', keydown);
      };
    }
    /* TEXT_SELECTION_END */

    function apply(ctx) {
      registerWorkspaceFilesUI(ctx, require('@deepseek-ai/dsh-client-ui-primitives'));
      const images=registerUltraImagesUI(ctx, require('@deepseek-ai/dsh-client-ui-primitives'));
      registerAssistantPresentationUI(ctx, require('@deepseek-ai/dsh-client-ui-primitives'), images);
      ctx.inject(['locale', 'sessions', 'slots', 'sidebarRightTabs', 'sidebarRight', 'layout', 'uiSession'], scope => {
        registerChatMemoryUI(scope, require('@deepseek-ai/dsh-client-ui-primitives'));
        registerComposerLayoutUI(scope, require('@deepseek-ai/dsh-client-ui-primitives'));
        if (document.getElementById('gateway-upload-retention')) {
          function RetentionNotice() { return React.createElement('p',{style:{margin:'4px 12px',fontSize:12,lineHeight:'18px',textAlign:'center',color:'var(--text-secondary,#808080)'}},'Загруженные файлы хранятся 24 часа. История чатов сохраняется.'); }
          scope.slots.inject('shell.bottom',()=>scope.slots.register({name:'shell.bottom',id:'gateway.upload-retention',order:4},RetentionNotice));
        }
        const feedback = registerCompactFeedback(scope), t = scope.locale.bind('gatewayCompact');
        function CompactFeedback({sessionId}) {
          const notice = React.useSyncExternalStore(feedback.subscribe, () => feedback.getSnapshot(sessionId));
          React.useSyncExternalStore(listener => scope.locale.subscribe(listener), () => scope.locale.getSnapshot().active);
          if (!notice) return null;
          return React.createElement('div', {className:'gateway-compact-feedback',role:'alert','data-gateway-compact-feedback':true},
            React.createElement('span', null, t(notice.key)),
            React.createElement('button', {type:'button',className:'gateway-compact-dismiss',title:t('dismiss'),'aria-label':t('dismiss'),onClick:() => {
              feedback.dismiss(sessionId);
              const session = scope.sessions.scope(sessionId);
              session?.get('conversation')?.input.for(session).focus();
            }}, React.createElement(IconCloseOutlineRegular, {size:18})));
        }
        scope.slots.inject('conversation.input.dock', () => scope.slots.register({name:'conversation.input.dock',id:'gateway.compact-feedback',order:5,inject:sessionId => ({sessionId})}, CompactFeedback));
      });
      const nativeRussian = ctx.locale.getSnapshot().locales.some(language => language.id === 'ru');
      if (nativeRussian && !nativeRuMerged) throw new Error('Native RU overrides were not verified at build time');
      if (!nativeRussian) ctx.effect(() => ctx.locale.addLanguage({ id: 'ru', label: 'Русский', fallback: 'en' }), 'gateway: Russian language');
      for (const [namespace, dictionary] of Object.entries(dictionaries)) {
        if (nativeRussian && !namespace.startsWith('gateway') && !['mnemon', 'dsh-context', 'skills-manager'].includes(namespace)) continue;
        ctx.effect(() => ctx.locale.register(namespace, 'ru', dictionary), `gateway: ${namespace} translation`);
      }
      let saved;
      try { saved = localStorage.getItem(preferenceKey); } catch {}
      const languages = ctx.locale.getSnapshot().locales;
      ctx.locale.setLocale(languages.some(language => language.id === saved) ? saved : 'ru');
      ctx.effect(() => ctx.locale.subscribe(() => {
        try { localStorage.setItem(preferenceKey, ctx.locale.getSnapshot().active); } catch {}
      }), 'gateway: language preference');
      ctx.effect(() => {
        document.body.setAttribute('data-gateway-harness', '');
        const style = document.createElement('style');
        style.id = 'gateway-harness-ui';
        style.textContent = css;
        document.head.append(style);
        const disposeViewport = installComposerViewport();
        const disposeSelection = installTextSelectionBoundary();
        return () => {
          style.remove();
          document.body.removeAttribute('data-gateway-harness');
          disposeViewport();
          disposeSelection();
        };
      }, 'gateway: responsive presentation');

      function MobileNavigation() {
        const [expanded, setExpanded] = React.useState(false);
        const [locale, setLocale] = React.useState(ctx.locale.getSnapshot().active);
        const button = React.useRef(null);
        const close = () => {
          const frame = document.querySelector(frameSelector);
          if (frame && !frame.hasAttribute('data-sidebar-collapsed')) ctx.layout.toggleSidebar();
          button.current?.focus({ preventScroll: true });
        };
        React.useEffect(() => {
          const sync = () => setExpanded(Boolean(document.querySelector(`${frameSelector}:not([data-sidebar-collapsed])`)));
          const observer = new MutationObserver(sync);
          observer.observe(document.body, { subtree: true, attributes: true, attributeFilter: ['data-sidebar-collapsed'], childList: true });
          sync();
          const escape = event => {
            if (event.key === 'Escape' && matchMedia('(max-width: 720px)').matches &&
                !document.querySelector('[aria-modal="true"]')) close();
          };
          document.addEventListener('keydown', escape);
          const disposeLocale = ctx.locale.subscribe(() => setLocale(ctx.locale.getSnapshot().active));
          let activePanel = ctx.layout.panelInfo.getSnapshot().activePanelId;
          const disposeNavigation = ctx.layout.panelInfo.subscribe(() => {
            const next = ctx.layout.panelInfo.getSnapshot().activePanelId;
            const changed = next !== activePanel;
            activePanel = next;
            if (changed && matchMedia('(max-width: 720px)').matches) close();
          });
          let activeSession = ctx.uiSession.current.getSnapshot().sessionId;
          const disposeSession = ctx.uiSession.current.subscribe(() => {
            const next = ctx.uiSession.current.getSnapshot().sessionId;
            const changed = next !== activeSession;
            activeSession = next;
            if (changed && matchMedia('(max-width: 720px)').matches) close();
          });
          return () => { observer.disconnect(); disposeLocale(); disposeNavigation(); disposeSession(); document.removeEventListener('keydown', escape); };
        }, []);
        React.useEffect(() => {
          const frame = document.querySelector(frameSelector);
          const center = frame?.querySelector('.pI_x6G_centerCol');
          const media = matchMedia('(max-width: 720px)');
          const previous = center?.inert;
          const sync = () => { if (center) center.inert = Boolean(previous || (expanded && media.matches)); };
          sync();
          media.addEventListener('change', sync);
          return () => { media.removeEventListener('change', sync); if (center) center.inert = Boolean(previous); };
        }, [expanded]);
        const label = locale === 'ru' ? (expanded ? 'Закрыть меню' : 'Открыть меню') : (expanded ? 'Close menu' : 'Open menu');
        return React.createElement(React.Fragment, null,
          React.createElement('div', { className: 'gateway-harness-mobile-title' }, 'DeepSeek Harness'),
          expanded && React.createElement('button', { type: 'button', className: 'gateway-harness-backdrop', 'aria-label': locale === 'ru' ? 'Закрыть боковое меню' : 'Close sidebar menu', onClick: close, tabIndex: -1 }),
          React.createElement('button', {
            ref: button, type: 'button', className: 'gateway-harness-menu', title: label,
            'aria-label': label, 'aria-expanded': expanded, onClick: () => ctx.layout.toggleSidebar(),
          }, React.createElement(expanded ? IconCloseOutlineRegular : IconPanelLeftOutlineRegular, { size: 22 })));
      }
      ctx.slots.inject('shell.overlay', () => ctx.slots.register({ name: 'shell.overlay', id: 'gateway.mobile-navigation' }, MobileNavigation));
    }
    return { inject: ['locale', 'layout', 'slots', 'uiSession', 'connection', 'sidebarRightTabs', 'sidebarRight'], apply };
  }
});
