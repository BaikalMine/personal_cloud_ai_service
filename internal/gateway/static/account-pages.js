(() => {
  "use strict";
  const page = document.querySelector("[data-account-page]");
  if (!page) return;
  const forms = Array.from(page.querySelectorAll("[data-account-form]"));
  const originalDisabled = new Map();
  const resetPasswords = [];
  let busy = false;
  page.querySelectorAll("[data-account-script-only]").forEach(node => { node.hidden = false; });
  for (const form of forms) {
    for (const button of form.querySelectorAll('button[type="submit"]')) originalDisabled.set(button, button.disabled);
    const checkbox = form.querySelector('[name="confirm_revoke"]');
    if (checkbox) { checkbox.required = false; checkbox.checked = false; checkbox.closest("label").hidden = true; }
  }
  for (const button of page.querySelectorAll("[data-account-password-toggle]")) {
    const input = document.getElementById(button.dataset.accountPasswordToggle);
    if (!input) continue;
    const show = button.getAttribute("aria-label");
    const hide = show.replace("\u041f\u043e\u043a\u0430\u0437\u0430\u0442\u044c", "\u0421\u043a\u0440\u044b\u0442\u044c");
    const visible = state => {
      input.type = state ? "text" : "password";
      button.setAttribute("aria-label", state ? hide : show); button.title = state ? hide : show;
      button.setAttribute("aria-pressed", String(state));
      const icon = document.createElement("i"); icon.setAttribute("data-lucide", state ? "eye-off" : "eye"); icon.setAttribute("aria-hidden", "true");
      button.replaceChildren(icon); window.lucide?.createIcons();
    };
    button.hidden = false; button.addEventListener("click", () => visible(input.type === "password"));
    resetPasswords.push(() => visible(false));
  }
  document.addEventListener("submit", event => {
    if (busy && forms.includes(event.target)) { event.preventDefault(); event.stopImmediatePropagation(); }
  }, true);
  // Shared app.js confirms before its second native submission reaches this owner.
  document.addEventListener("submit", event => {
    const form = event.target;
    if (event.defaultPrevented || !forms.includes(form)) return;
    if (form.hasAttribute("data-account-confirm")) {
      const checkbox = form.querySelector('[name="confirm_revoke"]');
      if (!checkbox || !event.submitter?.hasAttribute("data-confirm")) { event.preventDefault(); return; }
      checkbox.checked = true;
    }
    busy = true; form.setAttribute("aria-busy", "true");
    const progress = form.querySelector("[data-account-progress]");
    if (progress) progress.textContent = "\u0421\u043e\u0445\u0440\u0430\u043d\u044f\u0435\u043c...";
    setTimeout(() => { for (const button of originalDisabled.keys()) button.disabled = true; }, 0);
  });
  window.addEventListener("pageshow", event => {
    if (!event.persisted) return;
    for (const reset of resetPasswords) reset();
    if (page.dataset.accountPage === "sessions") { window.location.replace(page.dataset.accountRefresh || "/account/sessions"); return; }
    busy = false;
    for (const form of forms) { form.removeAttribute("aria-busy"); const progress = form.querySelector("[data-account-progress]"); if (progress) progress.textContent = ""; }
    for (const [button, disabled] of originalDisabled) button.disabled = disabled;
  });
})();
