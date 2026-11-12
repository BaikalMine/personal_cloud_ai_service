(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else { root.AIGatewayAdminUpdates = api; api.mount(root.document); }
})(typeof window !== "undefined" ? window : globalThis, function () {
  "use strict";
  function selection(rows) {
    const selected = rows.filter(row => row.selected && row.checkable);
    return { count: selected.length, names: selected.map(row => row.name), installable: selected.filter(row => row.installable).length };
  }
  function confirmation(names) {
    return "Установить доступные обновления: " + names.join(", ") + "? Агент повторно проверит выбранные компоненты и перезапустит обновляемые.";
  }
  function mount(doc) {
    const root = doc?.querySelector("[data-updates-page]");
    const form = root?.querySelector("[data-updates-form]");
    if (!form || root.dataset.updatesReady) return;
    root.dataset.updatesReady = "true";
    const boxes = Array.from(form.querySelectorAll('input[name="components"]'));
    const all = form.querySelector("[data-updates-select-all]");
    const allWrap = form.querySelector("[data-updates-select-all-wrap]");
    const check = form.querySelector('button[value="check"]');
    const install = form.querySelector('button[value="install"]');
    const title = form.querySelector("[data-updates-selection]");
    const names = form.querySelector("[data-updates-selection-names]");
    const progress = form.querySelector("[data-updates-progress]");
    const nativeConfirm = form.querySelector("[data-updates-native-confirm]");
    const approval = form.querySelector('input[name="confirm_install"]');
    nativeConfirm.hidden = true;
    const commands = Array.from(form.querySelectorAll('button[type="submit"]'));
    let busy = false;
    function update() {
      const state = selection(boxes.map(box => {
        const row = box.closest("[data-update-component]");
        return { selected: box.checked, checkable: !box.disabled, name: row.dataset.updateName, installable: row.dataset.installAllowed === "true" };
      }));
      const enabled = boxes.filter(box => !box.disabled);
      all.checked = enabled.length > 0 && state.count === enabled.length;
      all.indeterminate = state.count > 0 && state.count < enabled.length;
      all.disabled = busy || enabled.length === 0;
      allWrap.hidden = boxes.length === 0;
      check.disabled = busy || state.count === 0;
      install.disabled = busy || state.installable === 0;
      install.dataset.confirm = confirmation(state.names);
      title.textContent = "Выбрано: " + state.count;
      names.textContent = state.names.length ? state.names.join(", ") : "Компоненты не выбраны.";
    }
    all.addEventListener("change", () => { for (const box of boxes) if (!box.disabled) box.checked = all.checked; update(); });
    for (const box of boxes) box.addEventListener("change", update);
    form.addEventListener("submit", event => { if (busy) { event.preventDefault(); event.stopImmediatePropagation(); } }, true);
    doc.addEventListener("submit", event => {
      if (event.target !== form || event.defaultPrevented) return;
      if (busy) { event.preventDefault(); return; }
      busy = true;
      form.setAttribute("aria-busy", "true");
      const installing = event.submitter?.value.startsWith("install");
      approval.checked = Boolean(installing);
      progress.textContent = installing ? "Ожидаем ответ агента. Не повторяйте установку; выбранные сервисы могут быть недоступны." : "Проверяем выбранные компоненты...";
      doc.defaultView.setTimeout(() => { if (busy) for (const command of commands) command.disabled = true; }, 0);
    });
    doc.defaultView.addEventListener("pageshow", event => {
      if (!event.persisted) return;
      busy = false; form.removeAttribute("aria-busy"); progress.textContent = "";
      approval.checked = false;
      for (const command of commands) command.disabled = false;
      update();
    });
    update();
  }
  return { selection, confirmation, mount };
});
