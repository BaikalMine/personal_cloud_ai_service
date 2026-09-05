(() => {
  const sidebar = document.querySelector('[data-workspace-sidebar]');
  if (!sidebar) return;
  const desktop = window.matchMedia('(min-width: 1100px)');
  const mobile = window.matchMedia('(max-width: 759px)');
  const toggles = [...document.querySelectorAll('[data-navigation-toggle]')];
  const backdrop = document.querySelector('[data-navigation-backdrop]');
  const account = document.querySelector('#workspace-account');
  const accountToggle = document.querySelector('[data-account-toggle]');
  const navigationTrap = window.AIGatewayDialogFocus.createFocusTrap({ root: sidebar });
  const accountTrap = window.AIGatewayDialogFocus.createFocusTrap({ root: account });
  let navigationOpen = false;
  let inertElements = [];

  const overlayEvent = (name) => document.dispatchEvent(new CustomEvent('workspace-overlay', { detail: name }));
  const refreshScrollLock = () => document.body.classList.toggle('workspace-overlay-open', navigationOpen || !account.hidden);
  const closeAccount = ({ restore = true } = {}) => {
    if (account.hidden) return;
    account.hidden = true;
    accountToggle.setAttribute('aria-expanded', 'false');
    accountTrap.deactivate({ restore });
    refreshScrollLock();
  };
  const closeNavigation = ({ restore = true } = {}) => {
    if (!navigationOpen) return;
    navigationOpen = false;
    sidebar.classList.remove('is-open');
    sidebar.removeAttribute('role');
    sidebar.removeAttribute('aria-modal');
    backdrop.hidden = true;
    toggles.forEach(button => button.setAttribute('aria-expanded', 'false'));
    inertElements.forEach(([element, wasInert]) => { element.inert = wasInert; });
    inertElements = [];
    navigationTrap.deactivate({ restore });
    refreshScrollLock();
  };
  const openNavigation = (trigger) => {
    if (desktop.matches) return;
    overlayEvent('navigation');
    navigationOpen = true;
    sidebar.classList.add('is-open');
    sidebar.setAttribute('role', 'dialog');
    sidebar.setAttribute('aria-modal', 'true');
    backdrop.hidden = false;
    toggles.forEach(button => button.setAttribute('aria-expanded', 'true'));
    inertElements = [...document.querySelectorAll('main, .workspace-topbar, .workspace-mobile-nav')].map(element => [element, element.inert]);
    inertElements.forEach(([element]) => { element.inert = true; });
    refreshScrollLock();
    navigationTrap.activate({ trigger, initialFocus: sidebar.querySelector('[data-navigation-close]'), onEscape: closeNavigation });
  };
  toggles.forEach(button => button.addEventListener('click', () => navigationOpen ? closeNavigation() : openNavigation(button)));
  sidebar.querySelector('[data-navigation-close]').addEventListener('click', closeNavigation);
  backdrop.addEventListener('click', () => closeNavigation());
  sidebar.addEventListener('click', event => {
    if (event.target.closest('a[href]')) closeNavigation({ restore: false });
  });
  accountToggle.addEventListener('click', () => {
    if (!account.hidden) return closeAccount();
    overlayEvent('account');
    account.hidden = false;
    accountToggle.setAttribute('aria-expanded', 'true');
    refreshScrollLock();
    accountTrap.activate({ trigger: accountToggle, initialFocus: account.querySelector('[data-account-close]'), onEscape: closeAccount });
  });
  account.querySelector('[data-account-close]').addEventListener('click', closeAccount);
  document.addEventListener('click', event => {
    if (!account.hidden && !account.contains(event.target) && !accountToggle.contains(event.target)) closeAccount({ restore: false });
  });
  document.addEventListener('workspace-overlay', event => {
    if (event.detail !== 'navigation') closeNavigation({ restore: false });
    if (event.detail !== 'account') closeAccount({ restore: false });
  });
  const resize = () => {
    const focusWasInSidebar = sidebar.contains(document.activeElement);
    const focusWasInAccount = account.contains(document.activeElement);
    closeNavigation({ restore: false });
    closeAccount({ restore: false });
    if (focusWasInSidebar) {
      const target = mobile.matches ? toggles.find(button => button.getClientRects().length) : sidebar.querySelector('[aria-current="page"], .workspace-link');
      target?.focus({ preventScroll: true });
    } else if (focusWasInAccount) accountToggle.focus({ preventScroll: true });
  };
  desktop.addEventListener('change', resize);
  mobile.addEventListener('change', resize);

  const currentPath = (sidebar.dataset.navigationPath || location.pathname).replace(/\/$/, '') || '/';
  for (const nav of document.querySelectorAll('.workspace-sidebar, .workspace-mobile-nav')) {
    const matching = [...nav.querySelectorAll('a[href]')].filter(link => {
      const path = new URL(link.href, location.origin).pathname.replace(/\/$/, '') || '/';
      return currentPath === path || (path !== '/admin' && currentPath.startsWith(`${path}/`));
    }).sort((a, b) => b.pathname.length - a.pathname.length)[0];
    matching?.setAttribute('aria-current', 'page');
  }
  document.documentElement.classList.add('shell-ready');
})();
