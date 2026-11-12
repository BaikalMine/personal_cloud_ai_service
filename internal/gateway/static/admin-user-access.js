(function (root) {
  "use strict";
  const form = document.querySelector("[data-admin-user-access-form]");
  const access = root.AdminAccess;
  if (!(form instanceof HTMLFormElement) || !access) return;
  const field = (name) => form.elements.namedItem(name);
  const get = (name) => { const input = field(name); return input?.type === "checkbox" ? (input.checked ? "on" : "") : input?.value || ""; };
  let dirty = false;
  function sync() {
    const a = access.readAccess(get, access.userFlags);
    form.querySelector("[data-generation-capabilities]").hidden = !a.quick_generation;
    for (const key of ["text_to_image", "image_to_image", "video", "advanced_settings", "pause_mining"]) field(access.userFlags[key]).disabled = !a.quick_generation;
    field("queue_priority").disabled = !a.quick_generation && !a.training;
    for (const name of ["can_use_advanced_generation_settings", "pause_mining_for_quick_generation", "queue_priority"]) field(name).closest("label").hidden = field(name).disabled;
    form.querySelector(".access-quality").hidden = !a.video;
    for (const [key, name] of Object.entries(access.limits)) {
      const input = field(name + "_limit");
      const unlimited = get(name + "_unlimited") === "on";
      input.disabled = unlimited;
      input.required = !unlimited;
      const allowed = key.startsWith("video") ? a.video : a.text_to_image || a.image_to_image;
      input.closest("[data-access-quota]").hidden = !allowed && Number.isSafeInteger(a[key]);
    }
    field("can_use_quick_generation").setCustomValidity(a.quick_generation && !a.text_to_image && !a.image_to_image && !a.video ? "Выберите хотя бы один сценарий генерации." : "");
    access.renderFacts(form.querySelector("[data-access-summary]"), access.summary(a));
    form.querySelector("[data-user-mining-summary]").textContent = get("can_manage_mining") === "on" ? "Разрешено" : "Нет доступа";
  }
  function changed() {
    dirty = true;
    form.querySelector("[data-user-access-status]").textContent = "Изменения ещё не сохранены.";
    sync();
  }
  form.addEventListener("input", changed);
  form.addEventListener("change", changed);
  form.addEventListener("submit", () => { dirty = false; });
  root.addEventListener("beforeunload", (event) => {
    if (!dirty) return;
    event.preventDefault(); event.returnValue = "";
  });
  form.querySelector("[data-user-access-review]").hidden = true;
  sync();
})(window);
