(() => {
  "use strict";
  const forms = Array.from(document.querySelectorAll("[data-auth-form]"));
  const busy = new WeakSet();
  const originalDisabled = new Map();
  const resetVisibility = [];
  for (const form of forms) {
    const button = form.querySelector('button[type="submit"]');
    if (button) originalDisabled.set(button, button.disabled);
    const toggle = form.querySelector("[data-password-toggle]");
    const password = form.querySelector('input[name="password"]');
    if (!toggle || !password) continue;
    toggle.hidden = false;
    const setVisible = visible => {
      password.type = visible ? "text" : "password";
      const label = visible ? "\u0421\u043a\u0440\u044b\u0442\u044c \u043f\u0430\u0440\u043e\u043b\u044c" : "\u041f\u043e\u043a\u0430\u0437\u0430\u0442\u044c \u043f\u0430\u0440\u043e\u043b\u044c";
      toggle.setAttribute("aria-label", label);
      toggle.setAttribute("title", label);
      toggle.setAttribute("aria-pressed", String(visible));
      const icon = document.createElement("i");
      icon.setAttribute("data-lucide", visible ? "eye-off" : "eye");
      icon.setAttribute("aria-hidden", "true");
      toggle.replaceChildren(icon);
      window.lucide?.createIcons();
    };
    toggle.addEventListener("click", () => setVisible(password.type === "password"));
    resetVisibility.push(() => setVisible(false));
  }
  document.addEventListener("submit", event => {
    const form = event.target;
    if (!forms.includes(form)) return;
    if (busy.has(form)) {
      event.preventDefault();
      event.stopImmediatePropagation();
    }
  }, true);
  document.addEventListener("submit", event => {
    const form = event.target;
    if (event.defaultPrevented || !forms.includes(form)) return;
    busy.add(form);
    form.setAttribute("aria-busy", "true");
    const progress = form.querySelector("[data-auth-progress]");
    if (progress) progress.textContent = "\u041f\u0440\u043e\u0432\u0435\u0440\u044f\u0435\u043c \u0434\u0430\u043d\u043d\u044b\u0435...";
    setTimeout(() => {
      const button = form.querySelector('button[type="submit"]');
      if (button) button.disabled = true;
    }, 0);
  });
  window.addEventListener("pageshow", event => {
    if (!event.persisted) return;
    for (const reset of resetVisibility) reset();
    for (const form of forms) {
      busy.delete(form);
      form.removeAttribute("aria-busy");
      const progress = form.querySelector("[data-auth-progress]");
      if (progress) progress.textContent = "";
    }
    for (const [button, disabled] of originalDisabled) button.disabled = disabled;
  });
  const back = document.querySelector("[data-recovery-back]");
  if (back && window.history.length > 1 && document.referrer) {
    try {
      if (new URL(document.referrer).origin === window.location.origin) {
        back.hidden = false;
        back.addEventListener("click", () => window.history.back());
      }
    } catch { /* An unusable referrer leaves the explicit safe route available. */ }
  }
})();
