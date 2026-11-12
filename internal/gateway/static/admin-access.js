(function (root) {
  "use strict";
  const flags = {
    harness: "grant_harness",
    quick_generation: "grant_quick_generation",
    text_to_image: "grant_text_to_image", image_to_image: "grant_image_to_image", video: "grant_video",
    advanced_settings: "grant_advanced_generation_settings", training: "grant_train_image_lora",
    pause_mining: "pause_mining_for_quick_generation", queue_priority: "queue_priority",
  };
  const limits = { image_daily_limit: "image_generation_daily", image_total_limit: "image_generation_total", video_daily_limit: "video_generation_daily", video_total_limit: "video_generation_total" };
  const userFlags = {
    harness: "can_use_harness",
    quick_generation: "can_use_quick_generation",
    text_to_image: "can_generate_text_to_image", image_to_image: "can_generate_image_to_image", video: "can_generate_video",
    advanced_settings: "can_use_advanced_generation_settings", training: "can_train_image_lora",
    pause_mining: "pause_mining_for_quick_generation", queue_priority: "queue_priority",
  };
  const integer = (raw) => /^-?\d+$/.test(raw) && Number.isSafeInteger(Number(raw)) ? Number(raw) : NaN;
  function readAccess(get, names = flags) {
    const a = {};
    for (const [key, name] of Object.entries(names)) a[key] = get(name) === "on";
    if (!a.quick_generation) for (const key of ["text_to_image", "image_to_image", "video", "advanced_settings", "pause_mining"]) a[key] = false;
    a.queue_priority = a.queue_priority && (a.quick_generation || a.training);
    for (const [key, name] of Object.entries(limits)) {
      const value = integer(get(name + "_limit"));
      a[key] = get(name + "_unlimited") === "on" ? 0 : value > 0 && value <= (key.includes("daily") ? 100000 : 10000000) ? value : NaN;
    }
    a.max_video_quality = integer(get("max_video_generation_quality"));
    return a;
  }
  function readSettings(get) {
    return { version: 1, access: readAccess(get), link_lifetime_hours: integer(get("invite_ttl_hours")), account_lifetime_seconds: get("account_type") === "temporary" ? integer(get("temporary_account_hours")) * 3600 : 0, max_uses: integer(get("max_uses")) };
  }
  function validSettings(s) {
    if (!s || s.version !== 1 || !s.access || typeof s.access !== "object") return false;
    const a = s.access;
    if (Object.keys(flags).some((k) => typeof a[k] !== "boolean")) return false;
    if (!a.quick_generation && ["text_to_image", "image_to_image", "video", "advanced_settings", "pause_mining"].some((k) => a[k])) return false;
    if (a.quick_generation && !a.text_to_image && !a.image_to_image && !a.video) return false;
    if (!a.quick_generation && !a.training && a.queue_priority) return false;
    if (!a.quick_generation && !a.training && !a.harness) return false;
    if (Object.keys(limits).some((k) => !Number.isSafeInteger(a[k]) || a[k] < 0 || a[k] > (k.includes("daily") ? 100000 : 10000000))) return false;
    return [480, 720, 1080, 1440].includes(a.max_video_quality) && Number.isSafeInteger(s.max_uses) && s.max_uses >= 1 && s.max_uses <= 10000 && Number.isSafeInteger(s.link_lifetime_hours) && s.link_lifetime_hours >= 1 && s.link_lifetime_hours <= 2160 && Number.isSafeInteger(s.account_lifetime_seconds) && (s.account_lifetime_seconds === 0 || (s.account_lifetime_seconds >= 3600 && s.account_lifetime_seconds <= 31536000 && s.account_lifetime_seconds % 3600 === 0));
  }
  function summary(a) {
    const yes = (value) => value ? "Разрешено" : "Нет доступа";
    const quota = (allowed, daily, total) => !allowed ? "Нет доступа" : (!Number.isSafeInteger(daily) || !Number.isSafeInteger(total) || daily < 0 || total < 0) ? "Укажите лимит" : `${daily > 0 ? daily + " в сутки" : "без ограничения в сутки"}; ${total > 0 ? total + " всего" : "без общего ограничения"}`;
    return [
      ["photo", "Фото", quota(a.text_to_image || a.image_to_image, a.image_daily_limit, a.image_total_limit)],
      ["video", "Видео", quota(a.video, a.video_daily_limit, a.video_total_limit)],
      ["quality", "Базовое качество видео", a.video ? a.max_video_quality + "p" : "Нет доступа"],
      ["text", "По тексту", yes(a.text_to_image)], ["edit", "Редактирование фото", yes(a.image_to_image)],
      ["harness", "Harness", yes(a.harness)], ["training", "Обучение LoRA", yes(a.training)], ["advanced", "Точные настройки", yes(a.advanced_settings)],
      ["priority", "Приоритетная очередь", yes(a.queue_priority)], ["mining", "Пауза майнинга", yes(a.pause_mining)],
    ];
  }
  const api = { readSettings, readAccess, validSettings, summary, renderFacts, userFlags, limits };
  if (typeof module === "object" && module.exports) module.exports = api;
  root.AdminAccess = Object.freeze(api);
  if (!root.document) return;
  const form = document.querySelector("[data-admin-invite-form]");
  if (!(form instanceof HTMLFormElement)) return;
  const find = (selector) => document.querySelector(selector);
  const field = (name) => form.elements.namedItem(name);
  const get = (name) => { const input = field(name); return input?.type === "checkbox" ? (input.checked ? "on" : "") : input?.value || ""; };
  const set = (name, value) => { const input = field(name); if (input?.type === "checkbox") input.checked = Boolean(value); else if (input) input.value = String(value); };
  const icon = (name) => { const i = document.createElement("i"); i.dataset.lucide = name; i.setAttribute("aria-hidden", "true"); return i; };
  const icons = () => root.lucide?.createIcons();
  const status = find("[data-template-status]");
  const choice = find("[data-template-choice]");
  const dialog = find("[data-template-dialog]");
  const submit = find("[data-invite-submit]");
  const listStatus = find("[data-template-list-status]");
  let defaults = [];
  try { defaults = JSON.parse(find("[data-invite-defaults]").content.textContent); } catch (_) { /* Keep the manual form usable. */ }
  let selected = /^[1-9]\d*$/.test(get("selected_template_id")) && /^[1-9]\d*$/.test(get("selected_template_revision")) ? { id: get("selected_template_id"), revision: get("selected_template_revision"), name: get("selected_template_name") } : null;
  let candidate = null;
  let editRevision = 0;
  let busy = false;
  let listController = null;
  let selectionController = null;
  let listOffset = 0;
  let listTotal = 0;
  let requestSequence = 0;
  const say = (message, error = false) => { status.textContent = message; status.className = message ? "ui-status-banner " + (error ? "is-error" : "is-success") : ""; };
  function renderFacts(target, rows) {
    const dl = document.createElement("dl"); dl.className = "access-facts";
    rows.forEach(([key, label, value]) => { const row = document.createElement("div"); const dt = document.createElement("dt"); const dd = document.createElement("dd"); dt.textContent = label; dd.textContent = value; dd.dataset.accessFact = key; row.append(dt, dd); dl.append(row); });
    if (target.tagName === "DL") target.replaceChildren(...dl.children);
    else target.replaceChildren(dl);
  }
  function sync() {
    const s = readSettings(get);
    find("[data-generation-capabilities]").hidden = !s.access.quick_generation;
    for (const name of ["grant_text_to_image", "grant_image_to_image", "grant_video", "grant_advanced_generation_settings", "pause_mining_for_quick_generation"]) field(name).disabled = !s.access.quick_generation;
    field("queue_priority").disabled = !s.access.quick_generation && !s.access.training;
    for (const name of ["grant_advanced_generation_settings", "pause_mining_for_quick_generation", "queue_priority"]) field(name).closest("label").hidden = field(name).disabled;
    find(".access-quality").hidden = !s.access.video;
    const temporary = get("account_type") === "temporary";
    find("[data-account-lifetime]").hidden = !temporary;
    field("temporary_account_hours").disabled = !temporary;
    field("temporary_account_hours").required = temporary;
    for (const [key, name] of Object.entries(limits)) {
      const unlimited = get(name + "_unlimited") === "on";
      field(name + "_limit").disabled = unlimited;
      field(name + "_limit").required = !unlimited;
      const allowed = key.startsWith("video") ? s.access.video : s.access.text_to_image || s.access.image_to_image;
      field(name + "_limit").closest("[data-access-quota]").hidden = !allowed && Number.isSafeInteger(s.access[key]);
    }
    field("grant_quick_generation").setCustomValidity(s.access.quick_generation && !s.access.text_to_image && !s.access.image_to_image && !s.access.video ? "Выберите хотя бы один сценарий генерации." : "");
    renderFacts(find("[data-access-summary]"), summary(s.access));
    renderFacts(find("[data-lifetime-summary]"), [["link", "Ссылка", Number.isFinite(s.link_lifetime_hours) ? s.link_lifetime_hours + " ч" : "Укажите срок"], ["account", "Аккаунт", !temporary ? "Постоянный" : Number.isFinite(s.account_lifetime_seconds) ? s.account_lifetime_seconds / 3600 + " ч после регистрации" : "Укажите срок"], ["uses", "Активации", Number.isFinite(s.max_uses) ? String(s.max_uses) : "Укажите число"]]);
    find("[data-template-current]").textContent = selected ? selected.name : "Свои настройки";
    find("[data-template-update]").disabled = !selected;
  }
  function invalidateReview() {
    set("invite_review", ""); submit.value = "review";
    submit.replaceChildren(icon("check"), Object.assign(document.createElement("span"), { textContent: "Проверить приглашение" }));
    find("[data-review-state]").textContent = "Настройки изменены. Проверьте новый итог.";
    icons();
  }
  function edited(event) {
    if (busy || event.target.matches("[data-template-name]")) return;
    editRevision++; selectionController?.abort(); invalidateReview(); sync();
  }
  form.addEventListener("input", edited);
  form.addEventListener("change", edited);
  function remember(item) {
    selected = item;
    set("selected_template_id", item?.id || ""); set("selected_template_revision", item?.revision || ""); set("selected_template_name", item?.name || "");
    sync();
  }
  function apply(settings, item) {
    if (!validSettings(settings)) { say("Шаблон имеет неподдерживаемые настройки. Поля не изменены.", true); return; }
    for (const [key, name] of Object.entries(flags)) set(name, settings.access[key]);
    for (const [key, name] of Object.entries(limits)) { set(name + "_unlimited", settings.access[key] === 0); set(name + "_limit", settings.access[key] || 1); }
    set("max_video_generation_quality", settings.access.max_video_quality);
    set("invite_ttl_hours", settings.link_lifetime_hours); set("max_uses", settings.max_uses);
    set("account_type", settings.account_lifetime_seconds ? "temporary" : "permanent"); set("temporary_account_hours", settings.account_lifetime_seconds ? settings.account_lifetime_seconds / 3600 : 168);
    editRevision++; invalidateReview(); remember(item); say("Настройки применены.");
    if (item) find("[data-template-name]").value = item.name;
  }
  async function request(url, options = {}) {
    const controller = new AbortController();
    const abort = () => controller.abort();
    let timedOut = false;
    if (options.signal?.aborted) controller.abort();
    options.signal?.addEventListener("abort", abort, { once: true });
    const timer = setTimeout(() => { timedOut = true; controller.abort(); }, 15000);
    try {
      const response = await fetch(url, { credentials: "same-origin", cache: "no-store", ...options, signal: controller.signal });
      if (response.redirected) throw new Error("Сессия завершена. Войдите снова; настройки не сохранены.");
      let data; try { data = await response.json(); } catch (_) { throw new Error("Не удалось прочитать ответ сервера. Поля сохранены в форме."); }
      if (!response.ok) throw new Error(data.error || "Не удалось выполнить действие.");
      return data;
    } catch (error) {
      if (timedOut) throw new Error("Ответ не получен. Проверьте сохранённые шаблоны перед повтором; поля не изменены.");
      throw error;
    } finally { clearTimeout(timer); options.signal?.removeEventListener("abort", abort); }
  }
  function body(action) { const values = new URLSearchParams(new FormData(form)); values.set("action", action); return values; }
  function freeze() {
    busy = true;
    const controls = Array.from(document.querySelectorAll(".access-workspace input, .access-workspace select, .access-workspace button, .access-template-dialog button, .access-template-dialog input"));
    const states = controls.map((control) => control.disabled);
    controls.forEach((control) => { control.disabled = true; });
    return () => { controls.forEach((control, index) => { control.disabled = states[index]; }); busy = false; sync(); };
  }
  async function save(update) {
    if (busy || !form.reportValidity()) return;
    const name = find("[data-template-name]").value.trim();
    if (!name) { say("Укажите имя шаблона.", true); find("[data-template-name]").focus(); return; }
    if (update && !selected) return;
    const values = body("save"); values.set("name", name); values.set("template_id", update ? selected.id : "0"); values.set("revision", update ? selected.revision : "0");
    const unfreeze = freeze(); say("Сохранение шаблона…");
    const controller = new AbortController(); const timer = setTimeout(() => controller.abort(), 15000);
    try {
      const result = await request("/admin/invite-templates", { method: "POST", body: values, signal: controller.signal });
      if (!/^[1-9]\d*$/.test(result.id) || !/^[1-9]\d*$/.test(result.revision) || !validSettings(result.settings)) throw new Error("Не удалось проверить сохранённый шаблон. Откройте список перед повтором.");
      selected = result; set("selected_template_id", result.id); set("selected_template_revision", result.revision); set("selected_template_name", result.name);
      say("Шаблон сохранён.");
    } catch (error) { say(error.name === "AbortError" || error instanceof TypeError ? "Ответ не получен. Проверьте сохранённые шаблоны перед повтором; поля не изменены." : error.message, true); }
    finally { clearTimeout(timer); unfreeze(); }
  }
  async function selectSaved(item) {
    selectionController?.abort(); selectionController = new AbortController();
    const controller = selectionController; const revision = editRevision;
    listStatus.textContent = "Загрузка шаблона…";
    try {
      const data = await request("/admin/invite-templates?id=" + encodeURIComponent(item.id), { signal: controller.signal });
      if (controller.signal.aborted || revision !== editRevision || !dialog.open) return;
      if (data.id !== item.id || !validSettings(data.settings)) throw new Error("Не удалось проверить настройки шаблона.");
      candidate = data;
      choice.querySelector('[data-saved-choice]')?.remove();
      const option = document.createElement("option"); option.value = "saved"; option.textContent = data.name; option.dataset.savedChoice = ""; choice.append(option); choice.value = "saved";
      dialog.close(); find("[data-template-apply]").focus(); say("Выбран шаблон: " + data.name + ". Нажмите «Применить», чтобы изменить поля.");
    } catch (error) { if (error.name !== "AbortError") listStatus.textContent = error.message; }
  }
  async function deleteSaved(item) {
    if (busy || !root.confirm("Удалить шаблон «" + item.name + "»? Уже созданные приглашения не изменятся.")) return;
    const values = new URLSearchParams({ csrf: get("csrf"), action: "delete", template_id: item.id, revision: item.revision });
    const unfreeze = freeze(); listStatus.textContent = "Удаление…";
    try {
      await request("/admin/invite-templates", { method: "POST", body: values });
      if (selected?.id === item.id) { selected = null; set("selected_template_id", ""); set("selected_template_revision", ""); set("selected_template_name", ""); }
      if (candidate?.id === item.id) { candidate = null; choice.value = "custom"; }
      unfreeze(); await loadTemplates(listOffset); return;
    } catch (error) { listStatus.textContent = error.message; }
    unfreeze();
  }
  async function loadTemplates(offset) {
    listController?.abort(); listController = new AbortController();
    const controller = listController; const sequence = ++requestSequence;
    const query = new URLSearchParams({ q: find("[data-template-search]").value.trim(), offset: String(offset) });
    listStatus.textContent = "Загрузка…"; find("[data-template-list]").replaceChildren();
    find("[data-template-prev]").disabled = true; find("[data-template-next]").disabled = true;
    try {
      const data = await request("/admin/invite-templates?" + query, { signal: controller.signal });
      if (controller.signal.aborted || sequence !== requestSequence || !dialog.open) return;
      if (!Array.isArray(data.templates) || !Number.isSafeInteger(data.total) || !Number.isSafeInteger(data.offset)) throw new Error("Не удалось прочитать список шаблонов.");
      listOffset = data.offset; listTotal = data.total;
      const list = find("[data-template-list]");
      for (const item of data.templates) {
        if (typeof item.id !== "string" || !/^[1-9]\d*$/.test(item.id) || typeof item.revision !== "string" || !/^[1-9]\d*$/.test(item.revision)) throw new Error("Некорректный номер шаблона.");
        const row = document.createElement("div"); row.className = "access-template-row";
        const title = document.createElement("strong"); title.textContent = item.name;
        const select = document.createElement("button"); select.type = "button"; select.className = "ghost"; select.textContent = "Выбрать"; select.addEventListener("click", () => selectSaved(item));
        const remove = document.createElement("button"); remove.type = "button"; remove.className = "icon-button ghost"; remove.title = "Удалить шаблон"; remove.setAttribute("aria-label", "Удалить шаблон " + item.name); remove.append(icon("trash-2")); remove.addEventListener("click", () => deleteSaved(item));
        row.append(title, select, remove); list.append(row);
      }
      listStatus.textContent = data.templates.length ? "" : "Шаблоны не найдены.";
      find("[data-template-count]").textContent = listTotal ? `${listOffset + 1}–${listOffset + data.templates.length} из ${listTotal}` : "0 шаблонов";
      find("[data-template-prev]").disabled = listOffset <= 0; find("[data-template-next]").disabled = listOffset + 50 >= listTotal; icons();
    } catch (error) { if (error.name !== "AbortError" && sequence === requestSequence) listStatus.textContent = error.message; }
  }
  find("[data-template-apply]").addEventListener("click", () => {
    if (busy) return;
    if (choice.value === "custom") { remember(null); say(""); return; }
    if (choice.value === "saved") { if (candidate) apply(candidate.settings, candidate); return; }
    const preset = defaults.find((item) => item.key === choice.value); if (preset) apply(preset.settings, null);
  });
  find("[data-template-open]").addEventListener("click", () => { if (busy) return; dialog.showModal(); find("[data-template-search]").focus(); loadTemplates(0); });
  find("[data-template-close]").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => { listController?.abort(); selectionController?.abort(); });
  find("[data-template-search-form]").addEventListener("submit", (event) => { event.preventDefault(); loadTemplates(0); });
  find("[data-template-prev]").addEventListener("click", () => loadTemplates(Math.max(0, listOffset - 50)));
  find("[data-template-next]").addEventListener("click", () => loadTemplates(listOffset + 50));
  find("[data-template-save]").addEventListener("click", () => save(false));
  find("[data-template-update]").addEventListener("click", () => save(true));
  form.addEventListener("submit", (event) => { if (busy) event.preventDefault(); });
  find("[data-access-templates]").hidden = false; find("[data-template-save-area]").hidden = false;
  sync(); icons();
  if (root.location.hash === "#access-results") {
    root.requestAnimationFrame(() => find("#access-results")?.scrollIntoView({ block: "start", behavior: "instant" }));
  }
})(globalThis);
