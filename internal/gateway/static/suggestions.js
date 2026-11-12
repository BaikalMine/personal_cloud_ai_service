(() => {
  "use strict";
  const length = value => Array.from(String(value || "").trim()).length;
  function validationError(fields, action) {
    if (action === "submit" && (length(fields.description) < 10 || length(fields.description) > 4000)) return "\u041e\u043f\u0438\u0448\u0438\u0442\u0435 \u043f\u0440\u0435\u0434\u043b\u043e\u0436\u0435\u043d\u0438\u0435: \u043e\u0442 10 \u0434\u043e 4000 \u0441\u0438\u043c\u0432\u043e\u043b\u043e\u0432.";
    if (action === "rejected" && length(fields.comment) < 3) return "\u0423\u043a\u0430\u0436\u0438\u0442\u0435 \u043f\u0440\u0438\u0447\u0438\u043d\u0443 \u043e\u0442\u043a\u043b\u043e\u043d\u0435\u043d\u0438\u044f: \u043e\u0442 3 \u0434\u043e 1000 \u0441\u0438\u043c\u0432\u043e\u043b\u043e\u0432.";
    if (length(fields.comment) > 1000) return "\u041a\u043e\u043c\u043c\u0435\u043d\u0442\u0430\u0440\u0438\u0439 \u043d\u0435 \u0434\u043e\u043b\u0436\u0435\u043d \u043f\u0440\u0435\u0432\u044b\u0448\u0430\u0442\u044c 1000 \u0441\u0438\u043c\u0432\u043e\u043b\u043e\u0432.";
    if (fields.fileSize > 5 * 1024 * 1024) return "JSON \u0434\u043e\u043b\u0436\u0435\u043d \u0431\u044b\u0442\u044c \u043d\u0435 \u0431\u043e\u043b\u044c\u0448\u0435 5 \u041c\u0411.";
    return "";
  }
  if (typeof module !== "undefined") module.exports = { validationError };
  if (typeof document === "undefined") return;
  const page = document.querySelector("[data-suggestions-page]");
  if (!page) return;
  const forms = Array.from(page.querySelectorAll("[data-suggestion-write]"));
  const originalDisabled = new Map();
  let busy = false;
  for (const form of forms) {
    for (const button of form.querySelectorAll('button[type="submit"]')) originalDisabled.set(button, button.disabled);
    const confirmation = form.querySelector("[data-suggestion-confirm]");
    if (confirmation) {
      confirmation.hidden = true;
      confirmation.querySelector("input").required = false;
    }
    const file = form.querySelector('input[type="file"]');
    const name = form.querySelector("[data-suggestion-file-name]");
    if (file && name) file.addEventListener("change", () => {
      name.textContent = file.files?.[0]?.name || "";
    });
  }
  document.addEventListener("submit", event => {
    const form = event.target;
    if (!forms.includes(form)) return;
    if (busy) {
      event.preventDefault();
      event.stopImmediatePropagation();
      return;
    }
    const fields = Object.fromEntries(new FormData(form));
    fields.fileSize = form.querySelector('input[type="file"]')?.files?.[0]?.size || 0;
    const error = validationError(fields, event.submitter?.value);
    const output = form.querySelector("[data-suggestion-form-error]");
    if (output) output.textContent = error;
    if (error) {
      event.preventDefault();
      event.stopImmediatePropagation();
      const field = form.querySelector(event.submitter?.value === "rejected" ? '[name="comment"]' : fields.fileSize > 5 * 1024 * 1024 ? '[type="file"]' : '[name="description"]');
      field?.closest("details")?.setAttribute("open", "");
      field?.focus();
    }
  }, true);
  // Shared app.js owns confirmation and re-submits the same native form.
  document.addEventListener("submit", event => {
    if (event.defaultPrevented || !forms.includes(event.target)) return;
    const confirmation = event.target.querySelector("[data-suggestion-confirm] input");
    if (confirmation) confirmation.checked = true;
    busy = true;
    page.setAttribute("aria-busy", "true");
    const progress = page.querySelector("[data-suggestion-progress]");
    if (progress) progress.textContent = "\u0421\u043e\u0445\u0440\u0430\u043d\u044f\u0435\u043c \u0438\u0437\u043c\u0435\u043d\u0435\u043d\u0438\u044f...";
    setTimeout(() => {
      for (const button of originalDisabled.keys()) button.disabled = true;
    }, 0);
  });
  window.addEventListener("pageshow", event => {
    if (!event.persisted) return;
    busy = false;
    page.removeAttribute("aria-busy");
    for (const [button, disabled] of originalDisabled) button.disabled = disabled;
    const progress = page.querySelector("[data-suggestion-progress]");
    if (progress) progress.textContent = "";
  });
})();
