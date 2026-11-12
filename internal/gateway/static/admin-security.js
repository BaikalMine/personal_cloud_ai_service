(() => {
  'use strict';
  const page = document.querySelector('[data-session-list]');
  if (!page) return;
  const forms = Array.from(page.querySelectorAll('[data-revoke-session]'));
  const progress = page.querySelector('[data-security-progress]');
  let busy = false;
  for (const form of forms) {
    const checkbox = form.querySelector('[name="confirm_revoke"]');
    if (!checkbox) continue;
    checkbox.required = false;
    checkbox.checked = false;
    checkbox.closest('label').hidden = true;
  }
  // The shared confirmation owner re-submits the original form on acceptance.
  document.addEventListener('submit', (event) => {
    if (busy && forms.includes(event.target)) {
      event.preventDefault();
      event.stopImmediatePropagation();
    }
  }, true);
  document.addEventListener('submit', (event) => {
    const form = event.target;
    if (event.defaultPrevented || !forms.includes(form)) return;
    const checkbox = form.querySelector('[name="confirm_revoke"]');
    if (!checkbox || !event.submitter?.hasAttribute('data-confirm')) {
      event.preventDefault();
      return;
    }
    checkbox.checked = true;
    busy = true;
    page.setAttribute('aria-busy', 'true');
    if (progress) progress.textContent = 'Завершаем сессию #' + form.dataset.revokeSession + '...';
    setTimeout(() => {
      for (const target of forms) {
        for (const button of target.querySelectorAll('button[type="submit"]')) button.disabled = true;
      }
    }, 0);
  });
  window.addEventListener('pageshow', (event) => {
    if (event.persisted) window.location.replace(page.dataset.currentUrl);
  });
})();
