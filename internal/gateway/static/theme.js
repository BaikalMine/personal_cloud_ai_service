(function () {
  "use strict";
  const key = "ai_gateway_theme";
  const valid = value => ["system", "light", "dark"].includes(value);
  const root = document.documentElement;
  const system = window.matchMedia("(prefers-color-scheme: dark)");
  let preference = root.dataset.themePreference || "system";
  let saved;
  try {
    const cookie = document.cookie.split(";").map(item => item.trim()).find(item => item.startsWith(key + "="));
    if (cookie) saved = decodeURIComponent(cookie.slice(key.length + 1));
  } catch (_) {}
  if (!valid(saved)) {
    try { saved = window.localStorage.getItem(key); } catch (_) {}
  }
  if (valid(saved)) preference = saved;
  if (!valid(preference)) preference = "system";

  function apply(value) {
    preference = valid(value) ? value : "system";
    root.dataset.themePreference = preference;
    root.dataset.theme = preference === "system" ? (system.matches ? "dark" : "light") : preference;
    document.querySelectorAll("[data-theme-preference-control]").forEach(control => { control.value = preference; });
  }

  function rememberCookie() {
    try { document.cookie = `${key}=${preference}; Path=/; Max-Age=31536000; SameSite=Lax${location.protocol === "https:" ? "; Secure" : ""}`; } catch (_) {}
  }

  // Blocking, same-origin head script: resolve the theme before CSS can paint.
  apply(preference);
  system.addEventListener("change", () => { if (preference === "system") apply("system"); });
  window.addEventListener("storage", event => {
    try { if (event.storageArea !== window.localStorage) return; } catch (_) { return; }
    if (event.key === key || event.key === null) {
      apply(event.newValue);
      rememberCookie();
    }
  });
  document.addEventListener("DOMContentLoaded", () => {
    document.querySelectorAll("[data-theme-picker]").forEach(picker => { picker.hidden = false; });
    document.querySelectorAll("[data-theme-preference-control]").forEach(control => {
      control.value = preference;
      control.addEventListener("change", () => {
        apply(control.value);
        try { localStorage.setItem(key, preference); } catch (_) {}
        rememberCookie();
      });
    });
    window.lucide?.createIcons({ attrs: { "aria-hidden": "true", width: 18, height: 18 } });
  });
})();
