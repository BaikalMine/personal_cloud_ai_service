(() => {
  "use strict";
  const page = document.querySelector("[data-storage-page]");
  const model = window.AIGatewayStorage;
  if (!page || !model || !window.createStorageOperations) return;
  const find = selector => page.querySelector(selector);
  const form = find("[data-storage-filters]"), list = find("[data-storage-list]");
  const detail = find("[data-storage-detail]"), detailBody = find("[data-storage-detail-body]");
  const detailTrap = window.AIGatewayDialogFocus.createFocusTrap({ root: detail });
  let view = "comfy", mode = "records", items = [], data = null, selected = new Map(), offset = 0;
  let cursor = "", snapshot = "", pages = [], next = "", loading = false, epoch = 0, detailEpoch = 0, controller, denied = false;
  let operations;
  const element = (tag, className, text) => { const node = document.createElement(tag); if (className) node.className = className; if (text !== undefined) node.textContent = String(text); return node; };
  const icon = name => {
    const key = name.split("-").map(part => part[0].toUpperCase() + part.slice(1)).join("");
    const definition = window.lucide?.icons?.[key];
    const node = definition ? window.lucide.createElement(definition) : element("span");
    node.setAttribute("aria-hidden", "true"); return node;
  };
  const button = (label, name, action, className = "ghost") => {
    const node = element("button", className); node.type = "button";
    if (name) node.append(icon(name)); node.append(element("span", "", label));
    if (action) node.addEventListener("click", action); return node;
  };
  const number = value => Number.isFinite(Number(value)) ? new Intl.NumberFormat("ru-RU").format(Number(value)) : "Нет данных";
  function bytes(value) {
    if (value === null || value === undefined || !Number.isFinite(Number(value)) || Number(value) < 0) return "Нет данных";
    let n = Number(value), unit = 0; const units = ["Б", "КиБ", "МиБ", "ГиБ", "ТиБ"];
    while (n >= 1024 && unit < units.length - 1) { n /= 1024; unit++; }
    return `${new Intl.NumberFormat("ru-RU", { maximumFractionDigits: unit ? 1 : 0 }).format(n)} ${units[unit]}`;
  }
  const date = value => value && Number.isFinite(Date.parse(value)) ? new Intl.DateTimeFormat("ru-RU", { dateStyle: "short", timeStyle: "short" }).format(new Date(value)) : "Нет данных";
  const age = (start, checked) => {
    const hours = Math.floor((Date.parse(checked) - Date.parse(start)) / 3600000);
    if (!Number.isFinite(hours) || hours < 0) return "Нет данных";
    return hours < 1 ? "Меньше часа" : hours < 24 ? `${number(hours)} ч` : `${number(Math.floor(hours / 24))} д ${hours % 24} ч`;
  };
  const status = code => { const node = element("span", "storage-state", model.label(code)); node.dataset.tone = model.tone(code); return node; };
  const facts = rows => {
    const dl = element("dl", "storage-facts");
    for (const [label, value, note] of rows) {
      const cell = element("div"); cell.append(element("dt", "", label), element("dd", "", value ?? "Нет данных"));
      if (note) cell.querySelector("dd").append(element("small", "", note)); dl.append(cell);
    }
    return dl;
  };
  function accessLost() {
    denied = true; controller?.abort(); epoch++; items = []; data = null; selected.clear();
    list.replaceChildren(); find("[data-storage-metrics]").replaceChildren();
    find("[data-storage-database-panel]").replaceChildren();
    for (const tab of page.querySelectorAll("[data-storage-view]")) tab.disabled = true;
    find("[data-storage-freshness]").textContent = ""; find("[data-storage-status]").textContent = "Доступ закрыт. Обновите страницу после входа.";
    detailBody.replaceChildren(); closeDetail(); operations?.lock(); syncSelection();
    find("[data-storage-status]").tabIndex = -1; find("[data-storage-status]").focus({ preventScroll: true });
  }
  async function request(url, options = {}) {
    const abort = new AbortController();
    const onAbort = () => abort.abort();
    options.signal?.addEventListener("abort", onAbort, { once: true });
    if (options.signal?.aborted) abort.abort();
    const timer = setTimeout(onAbort, options.method === "POST" ? 150000 : 35000);
    try {
      const response = await fetch(url, { method: options.method || "GET", credentials: "same-origin", cache: "no-store", redirect: "error", signal: abort.signal,
        headers: { Accept: "application/json", ...(options.method === "POST" ? { "Content-Type": "application/json", "X-CSRF-Token": page.dataset.csrf } : {}) }, body: options.body ? JSON.stringify(options.body) : undefined });
      let result; try { result = await response.json(); } catch (_) { throw new Error("invalid_response"); }
      if (!response.ok) {
        if (response.status === 401 || response.status === 403 && result.error === "access_denied") accessLost();
        const error = new Error(result.error || "host_unavailable"); error.code = result.error; error.status = response.status; throw error;
      }
      if (!result || typeof result !== "object") throw new Error("invalid_response");
      return result;
    } catch (error) {
      if (error.name === "TypeError" || error.name === "AbortError") error.code = "host_unavailable";
      throw error;
    } finally { clearTimeout(timer); options.signal?.removeEventListener("abort", onAbort); }
  }
  const metricRows = rows => { const nodes = facts(rows); find("[data-storage-metrics]").replaceChildren(...nodes.children); };
  function notice(text, tone = "warning") { const node = find("[data-storage-notice]"); node.hidden = !text; node.dataset.tone = tone; node.querySelector("p").textContent = text || ""; }
  function closeDetail() { detailEpoch++; if (detail.open) detail.close(); detailTrap.deactivate(); }
  for (const node of [find("[data-storage-detail-close]"), find("[data-storage-detail-done]")]) node.addEventListener("click", closeDetail);
  detail.addEventListener("cancel", event => { event.preventDefault(); closeDetail(); });
  function showDetail(title, content, trigger) {
    find("#storage-detail-title").textContent = title; detailBody.replaceChildren(content);
    if (!detail.open) { detail.showModal(); detailTrap.activate({ trigger, initialFocus: find("[data-storage-detail-close]"), onEscape: closeDetail }); }
  }
  function itemDetails(item, trigger) {
    const body = element("div");
    if (view === "training") {
      body.append(status(item.state), facts([["Объект", item.label], ["Категория", model.label(item.kind)], ["Размер", item.state === "unavailable" ? "Нет данных" : bytes(item.size_bytes)], ["Файлов", item.state === "unavailable" ? "Нет данных" : number(item.files)], ["Профиль", item.profile_id || "Не указан"], ["Задача обучения", item.gateway_job_id || item.job_id || "Не привязан"], ["Очистка", item.can_cleanup ? "Доступна после проверки" : model.label(item.protected_reason || item.state)]]));
    } else if (mode === "unresolved") {
      body.append(status("upload_unconfirmed"), facts([["Файл", item.filename], ["Каталог", item.subfolder], ["ID владельца", item.user_id || "Не указан"], ["ID задания", item.job_id || "Задание удалено"], ["Ожидаемый размер", bytes(item.size_bytes)], ["Ожидаемый SHA-256", item.sha256], ["Зарегистрировано", date(item.created_at)], ["Очистка", "Заблокирована до сверки загрузки"]]));
    } else if (mode === "files") {
      body.append(status(item.registration), facts([["Файл", item.filename], ["Каталог", [form.elements.kind.value, item.subfolder].filter(Boolean).join("/")], ["Тип", model.label(item.entry_type)], ["Размер", item.size_known ? bytes(item.size_bytes) : "Не измерен"], ["Изменён", date(item.modified_at)], ["Наблюдение", date(data.inventory.checked_at)], ["Сопоставление с реестром", date(data.registry_checked_at)]]));
      body.append(element("p", "muted", "Очистка этого наблюдения недоступна. Принадлежность текущего содержимого не подтверждена."));
    } else {
      body.append(status(item.state), facts([["Файл", item.filename], ["Каталог", [item.storage_type, item.subfolder].filter(Boolean).join("/")], ["Владелец", item.owner_name || (item.owner_count > 1 ? "Несколько владельцев" : "Не указан")], ["Ожидаемый размер", item.size_known ? bytes(item.size_bytes) : "Неизвестен"], ["Причина", model.label(item.reason)], ["Скрыт из результатов", item.profile_hidden ? "Да" : "Нет"], ["Хранить до", date(item.expires_at)], ["Удаление допустимо с", date(item.eligible_at)], ["Попыток", number(item.attempts)], ["Последняя попытка", date(item.last_attempt_at)], ["Следующая попытка", date(item.next_attempt_at)], ["Последний результат", model.label(item.last_outcome)]]));
      const reasons = (item.protection || []).filter(code => code !== "executor_not_fenced" || !data.manual_cleanup_available);
      if (reasons.length) body.append(element("h3", "", "Ограничения"), ...reasons.map(code => element("p", "muted", model.label(code))));
      if (item.can_inspect) body.append(button("Проверить файл на хосте", "scan-search", () => inspect([item], trigger)));
    }
    showDetail(item.label || item.filename || "Объект хранилища", body, trigger);
  }
  function table(headers) {
    const node = element("table", "compact-table storage-object-table"); const head = element("thead"), row = element("tr");
    for (const label of headers) row.append(element("th", "", label));
    head.append(row); node.append(head); const body = element("tbody"); node.append(body); return { node, head: row, body };
  }
  function cell(row, label, content, className = "") {
    const td = element("td", className); td.dataset.label = label;
    td.append(content instanceof Node ? content : document.createTextNode(String(content))); row.append(td); return td;
  }
  function objectName(name, sub, type = "file") {
    const node = element("span", "storage-object-name"); node.append(icon(type), element("strong", "", name));
    if (sub) node.append(element("small", "", sub)); return node;
  }
  function canSelect(item) { return view === "training" ? item.can_cleanup && model.trainingID(item.id) : item.can_inspect && model.registryKey(item.key); }
  function renderRows() {
    if (mode === "unresolved") {
      const t = table(["Файл", "ID владельца", "Ожидаемый размер", "Состояние", ""]);
      for (const item of items) {
        const row = element("tr"); row.dataset.storageId = item.id;
        cell(row, "Файл", objectName(item.filename, item.subfolder, "file"));
        cell(row, "ID владельца", item.user_id || "Не указан");
        cell(row, "Ожидаемый размер", bytes(item.size_bytes));
        cell(row, "Состояние", status("upload_unconfirmed"));
        const action = button("Сведения", "arrow-up-right", null, "ghost icon-button");
        action.title = "Сведения"; action.setAttribute("aria-label", `Сведения: ${item.filename}`);
        action.querySelector("span").className = "visually-hidden";
        action.addEventListener("click", () => itemDetails(item, action));
        cell(row, "", action, "storage-row-action"); t.body.append(row);
      }
      if (!items.length) { const row = element("tr"); cell(row, "", "Неподтверждённых загрузок нет.").colSpan = 5; t.body.append(row); }
      list.replaceChildren(t.node); return;
    }
    const historyMode = mode === "operations", filesMode = view === "comfy" && mode === "files";
    const headers = historyMode ? ["Операция", "Автор", "Ожидалось", "Подтверждено", "Состояние", ""] : filesMode ? ["Файл", "Размер", "Учёт", "Изменён", ""] : view === "training" ? ["", "Объект", "Категория", "Размер", "Состояние", ""] : ["", "Файл", "Владелец", "Размер", "Состояние", ""];
    const t = table(headers);
    if (!historyMode && !filesMode) {
      const label = element("label", "storage-checkbox"), check = element("input"); check.type = "checkbox"; check.setAttribute("aria-label", "Выбрать доступные на странице");
      check.disabled = !items.some(canSelect); check.addEventListener("change", () => { selected.clear(); if (check.checked) for (const item of items.filter(canSelect).slice(0, 50)) selected.set(item.key || item.id, item); syncSelection(); });
      check.checked = items.some(canSelect) && items.filter(canSelect).every(item => selected.has(item.key || item.id));
      check.indeterminate = selected.size > 0 && !check.checked; label.append(check); t.head.firstChild.append(label);
    }
    for (const item of items) {
      const row = element("tr"); row.dataset.storageId = item.key || item.id;
      if (historyMode) {
        cell(row, "Операция", objectName(date(item.created_at), `${number(item.item_count)} объектов`, "list-checks"));
        cell(row, "Автор", item.origin === "retention" ? "Система" : item.actor_name || "Удалённый администратор");
        cell(row, "Ожидалось", bytes(item.expected_bytes)); cell(row, "Подтверждено", bytes(item.freed_bytes)); cell(row, "Состояние", status(item.state));
      } else {
        if (!filesMode) {
          const label = element("label", "storage-checkbox"), check = element("input"); check.type = "checkbox"; check.checked = selected.has(item.key || item.id); check.disabled = !canSelect(item);
          check.setAttribute("aria-label", `Выбрать ${item.label || item.filename}`); label.title = check.disabled ? model.label(item.protected_reason || item.protection?.[0] || "fingerprint_unavailable") : "";
          check.addEventListener("change", () => { const key = item.key || item.id; if (check.checked && selected.size < 50) selected.set(key, item); else { selected.delete(key); check.checked = false; } syncSelection(); });
          label.append(check); cell(row, "Выбор", label, "storage-choice-cell");
        }
        cell(row, view === "training" ? "Объект" : "Файл", objectName(item.label || item.filename, view === "training" ? item.profile_id || item.gateway_job_id : [filesMode ? form.elements.kind.value : item.storage_type, item.subfolder].filter(Boolean).join("/"), view === "training" ? item.kind.includes("model") ? "box" : "folder" : "file"));
        if (view === "training") cell(row, "Категория", model.label(item.kind));
        else if (!filesMode) cell(row, "Владелец", item.owner_name || (item.owner_count > 1 ? "Несколько" : "Не указан"));
        cell(row, "Размер", view === "training" ? item.state === "unavailable" ? "Нет данных" : bytes(item.size_bytes) : item.size_known ? bytes(item.size_bytes) : "Неизвестен");
        if (filesMode) { const entry = cell(row, "Учёт", status(item.registration)); if (item.entry_type !== "file") entry.append(element("small", "", model.label(item.entry_type))); cell(row, "Изменён", date(item.modified_at)); }
        else {
          const code = view === "training" ? item.state : item.pending ? item.last_outcome && !["not_queued", "not_attempted"].includes(item.last_outcome) ? item.last_outcome : "pending" : item.state;
          const entry = cell(row, "Состояние", status(code));
          const reason = view === "training" ? item.protected_reason : item.reference_protected ? item.protection?.find(v => v !== "executor_not_fenced") : item.profile_hidden ? "profile_hidden" : item.pending ? item.reason : "retained";
          if (reason) entry.append(element("small", "", model.label(reason)));
        }
      }
      const action = button(historyMode ? "Открыть операцию" : "Сведения", "arrow-up-right", null, "ghost icon-button");
      const actionLabel = `${historyMode ? "Открыть операцию" : "Сведения"}: ${item.label || item.filename || date(item.created_at)}`;
      action.setAttribute("aria-label", actionLabel); action.title = historyMode ? "Открыть операцию" : "Сведения"; action.querySelector("span").className = "visually-hidden";
      action.addEventListener("click", () => historyMode ? operations.open(view, item.id, action) : itemDetails(item, action));
      cell(row, "", action, "storage-row-action"); t.body.append(row);
    }
    if (!items.length) { const row = element("tr"), td = cell(row, "", mode === "operations" ? "Операций за этот период нет." : "Подходящих объектов нет."); td.colSpan = headers.length; t.body.append(row); }
    list.replaceChildren(t.node);
  }
  function syncSelection() {
    const node = find("[data-storage-selection]"); node.hidden = selected.size === 0 || denied;
    find("[data-storage-selected-count]").textContent = `Выбрано: ${selected.size}`;
    const inspect = find("[data-storage-inspect]"); inspect.hidden = view !== "comfy" || mode !== "records"; inspect.disabled = loading;
    const prepare = find("[data-storage-prepare]");
    const allowed = data?.manual_cleanup_available && [...selected.values()].every(item => view === "training" ? item.can_cleanup : model.canPrepare(item, Date.parse(data.inventory.checked_at)));
    prepare.disabled = loading || !selected.size || !allowed || !!operations?.blocked();
    prepare.title = operations?.blocked() ? "Сначала завершите сохранённую операцию" : !allowed ? "Выбранные объекты пока нельзя очистить" : "Проверить точный перечень перед удалением";
    for (const row of list.querySelectorAll("tr[data-storage-id]")) { const check = row.querySelector('input[type="checkbox"]'); if (check) check.checked = selected.has(row.dataset.storageId); }
    const all = list.querySelector('thead input[type="checkbox"]');
    if (all) { const eligible = items.filter(canSelect); all.checked = eligible.length > 0 && eligible.every(item => selected.has(item.key || item.id)); all.indeterminate = selected.size > 0 && !all.checked; }
  }
  function resetPages() { offset = 0; cursor = ""; snapshot = ""; pages = []; next = ""; selected.clear(); }
  function urlQuery() {
    const query = new URLSearchParams({ view, mode });
    if (view === "comfy" && ["records", "files"].includes(mode)) for (const name of ["kind", "state", "q"]) if (!form.elements[name].disabled && form.elements[name].value) query.set(name, form.elements[name].value);
    return query;
  }
  function setURL() { history.replaceState(null, "", `${location.pathname}?${urlQuery()}`); }
  function configure() {
    find("[data-storage-host]").hidden = view === "database"; find("[data-storage-database-panel]").hidden = view !== "database";
    find("[data-storage-host]").setAttribute("aria-labelledby", `storage-tab-${view}`);
    for (const tab of page.querySelectorAll("[data-storage-view]")) { const active = tab.dataset.storageView === view; tab.setAttribute("aria-selected", String(active)); tab.tabIndex = active ? 0 : -1; }
    for (const tab of page.querySelectorAll("[data-storage-mode]")) { tab.hidden = tab.dataset.storageMode === "files" && view !== "comfy"; tab.setAttribute("aria-pressed", String(tab.dataset.storageMode === mode)); if (tab.dataset.storageMode === "records") tab.textContent = view === "training" ? "Объекты" : "Реестр"; }
    for (const tab of page.querySelectorAll('[data-storage-mode="unresolved"]')) tab.hidden = view !== "comfy";
    form.hidden = view !== "comfy" || ["operations", "unresolved"].includes(mode);
    form.elements.state.disabled = mode !== "records"; form.elements.state.closest("label").hidden = mode !== "records";
    const all = form.elements.kind.querySelector('option[value=""]'); all.disabled = mode === "files";
    if (mode === "files" && !form.elements.kind.value) form.elements.kind.value = "output";
    form.elements.q.placeholder = mode === "files" ? "Имя файла или папка" : "Имя файла, папка или владелец";
  }
  async function load(reset = false) {
    controller?.abort(); const token = ++epoch; controller = new AbortController();
    if (reset) resetPages(); selected.clear(); data = null; items = []; loading = true; syncSelection();
    find("[data-storage-prev]").disabled = true; find("[data-storage-next]").disabled = true;
    if (view === "database" || denied) { loading = false; return; }
    notice(""); find("[data-storage-status]").textContent = "Загружаем данные…"; find("[data-storage-count]").textContent = "";
    list.setAttribute("aria-busy", "true"); list.replaceChildren(element("div", "storage-loading", "Загрузка списка")); metricRows([]); find("[data-storage-freshness]").textContent = "";
    let path = `/admin/storage/${view}`, query = new URLSearchParams();
    if (mode === "operations") { path += "/operations"; query.set("offset", offset); }
    else if (mode === "unresolved") { path += "/unresolved-inputs"; query.set("limit", "50"); if (cursor) query.set("after", cursor); }
    else if (view === "training") { query.set("limit", "50"); if (cursor) query.set("after", cursor); }
    else if (mode === "files") { path += "/files"; query.set("kind", form.elements.kind.value); query.set("limit", "50"); if (form.elements.q.value) query.set("q", form.elements.q.value); if (cursor) { query.set("cursor", cursor); query.set("snapshot", snapshot); } }
    else { query.set("limit", "50"); query.set("offset", offset); for (const name of ["q", "kind", "state"]) if (form.elements[name].value) query.set(name, form.elements[name].value); }
    try {
      const value = await request(`${path}?${query}`, { signal: controller.signal }); if (token !== epoch) return;
      data = value; let total = 0, checked = "";
      if (mode === "unresolved") {
        const parsed = model.unresolvedInputs(value); items = parsed.items; next = parsed.next;
        metricRows([["Загрузок на странице", number(items.length)], ["Ожидаемый объём", bytes(items.reduce((sum, item) => sum + item.size_bytes, 0)), "По реестру"], ["Без задания", number(items.filter(item => item.job_id === null).length)], ["Очистка", "Заблокирована"]]);
      } else if (mode === "operations") {
        const h = model.history(value); items = h.items; total = h.total; checked = h.checked; next = offset + items.length < total ? "offset" : "";
        metricRows([["Операций", number(total), "За 90 дней и незавершённые"], ["Подтверждено удаление", bytes(h.freed), "По этим операциям"], ["Начало периода", date(h.since)]]);
      } else {
        const inv = value.inventory; if (!inv || !Array.isArray(inv.items)) throw new Error("invalid_response"); items = inv.items; checked = inv.checked_at;
        if (view === "training") {
          total = inv.total_items; next = inv.next_cursor || "";
          const size = kind => { const chosen = items.filter(item => item.kind === kind); return chosen.some(item => item.state === "unavailable") ? "Не полностью измерен" : bytes(chosen.reduce((sum, item) => sum + Number(item.size_bytes || 0), 0)); };
          metricRows([["Кэши обучения", size("training_cache"), "На этой странице"], ["Копии моделей", size("normalized_model"), "На этой странице"], ["Исходные модели", size("original_model"), "Только чтение"], ["Обученные LoRA", size("trained_artifact"), "Удаление через владельца"]]);
          if (!inv.complete) notice("Часть объектов на странице не удалось проверить. Их объём неизвестен.");
          if (!value.manual_cleanup_available) notice(model.label(value.manual_cleanup_reason || "restore_drill"));
        } else if (mode === "files") {
          total = inv.matched_items; next = inv.next_cursor; snapshot = inv.snapshot;
          const unreadable = ["root_missing", "root_unavailable", "root_rejected"].includes(inv.issue);
          metricRows([["Наблюдаемый объём", unreadable ? "Нет данных" : bytes(inv.logical_bytes), "Без целей ссылок"], ["Файлов в обходе", unreadable ? "Нет данных" : number(inv.total_files)], ["Неучтённых на странице", unreadable ? "Нет данных" : number(items.filter(item => item.registration === "unregistered_path").length)], ["Обход каталога", inv.complete ? "Полный" : "Неполный"]]);
          if (!inv.complete) notice(`${model.label(inv.issue)}. Числа относятся только к прочитанной части.`);
        } else {
          total = inv.total; next = offset + items.length < total ? "offset" : ""; const s = inv.summary;
          metricRows([["Ждёт удаления", bytes(s.pending_expected_bytes), `${number(s.pending_records)} записей`], ["Подтверждено за 90 дней", bytes(s.confirmed_freed_bytes), `${number(s.missing_receipts)} квитанций «файл отсутствует»`], ["Самая старая очередь", s.oldest_eligible_at ? age(s.oldest_eligible_at, checked) : "Очередь пуста", s.oldest_eligible_at ? date(s.oldest_eligible_at) : ""], ["Используются задачами", number(s.protected_references), `${number(s.unconfirmed_uploads)} неподтверждённых загрузок`]]);
          if (!value.manual_cleanup_available) notice(model.label(value.manual_cleanup_reason || "executor_not_fenced"));
          else if (s.generation_active || s.executor_occupied || s.execution_unconfirmed) notice("Есть активная или неподтверждённая работа. Зависимые файлы защищены от очистки.");
        }
      }
      if (!Array.isArray(items) || items.length > 100) throw new Error("invalid_response");
      renderRows(); find("[data-storage-freshness]").textContent = `Проверено: ${date(checked)}${view === "training" && mode !== "operations" ? " · Размеры относятся к текущей странице" : ""}`;
      find("[data-storage-count]").textContent = items.length ? `${number(mode === "operations" || view === "comfy" && mode === "records" ? offset + 1 : pages.length * 50 + 1)}–${number((mode === "operations" || view === "comfy" && mode === "records" ? offset : pages.length * 50) + items.length)} из ${number(total)}` : `Найдено: ${number(total)}`;
      find("[data-storage-prev]").disabled = !(offset > 0 || pages.length > 0); find("[data-storage-next]").disabled = !next;
      if (mode === "unresolved") {
        find("[data-storage-freshness]").textContent = "";
        find("[data-storage-count]").textContent = `На странице: ${number(items.length)}`;
      }
      find("[data-storage-status]").textContent = "";
    } catch (error) {
      if (token !== epoch) return;
      items = []; data = null; list.replaceChildren(element("div", "ui-empty-state", "Данные не загружены")); metricRows([]);
      find("[data-storage-status]").textContent = `${model.label(error.code || error.message)} Обновите данные для повторной проверки.`;
    } finally { if (token === epoch) { loading = false; list.removeAttribute("aria-busy"); syncSelection(); } }
  }
  async function inspect(selection, trigger) {
    if (!selection.length || denied) return;
    const token = ++detailEpoch; showDetail("Проверка файлов на хосте", element("p", "", "Проверяем содержимое…"), trigger);
    try {
      const result = await request("/admin/storage/comfy/inspect", { method: "POST", body: { items: selection.map(({ key, version }) => ({ key, version })) } });
      if (token !== detailEpoch || !detail.open) return;
      const inspection = result.inspection; if (!inspection || !Array.isArray(inspection.items)) throw new Error("invalid_response");
      const body = element("div"); body.append(facts([["Проверено на хосте", date(inspection.host_checked_at)], ["Полнота", inspection.complete ? "Все выбранные файлы" : "Не все файлы проверены"]]));
      const outcomes = new Map(inspection.items.map(item => [item.key, item])); const ul = element("ul", "storage-operation-files");
      for (const item of selection) { const li = element("li"), outcome = outcomes.get(item.key); li.append(element("strong", "", [item.subfolder, item.filename].filter(Boolean).join("/")), status(outcome?.version === item.version ? outcome.status : "unavailable")); ul.append(li); }
      body.append(ul); detailBody.replaceChildren(body);
    } catch (error) { if (token === detailEpoch && detail.open) detailBody.replaceChildren(element("p", "", model.label(error.code || error.message))); }
  }
  operations = window.createStorageOperations({ page, request, element, button, facts, status, bytes, date, refresh: () => load(true), onChange: () => { if (operations) syncSelection(); } });
  find("[data-storage-clear]").addEventListener("click", () => { selected.clear(); renderRows(); syncSelection(); find("[data-storage-refresh]").focus({ preventScroll: true }); });
  find("[data-storage-inspect]").addEventListener("click", event => inspect([...selected.values()], event.currentTarget));
  find("[data-storage-prepare]").addEventListener("click", event => { if (event.currentTarget.disabled) return; const selection = view === "training" ? [...selected.keys()] : [...selected.values()].map(({ key, version }) => ({ key, version })); operations.prepare(view, selection, event.currentTarget); });
  find("[data-storage-refresh]").addEventListener("click", () => load(true));
  form.addEventListener("submit", event => { event.preventDefault(); setURL(); load(true); });
  form.addEventListener("change", event => { if (event.target instanceof HTMLSelectElement) { setURL(); load(true); } });
  for (const tab of page.querySelectorAll("[data-storage-view]")) tab.addEventListener("click", () => { view = tab.dataset.storageView; mode = "records"; configure(); setURL(); load(true); });
  for (const tab of page.querySelectorAll("[data-storage-mode]")) tab.addEventListener("click", () => { mode = tab.dataset.storageMode; configure(); setURL(); load(true); });
  find("[data-storage-tabs]").addEventListener("keydown", event => {
    const tabs = [...page.querySelectorAll("[data-storage-view]")], index = tabs.indexOf(document.activeElement);
    if (index < 0 || !["ArrowRight", "ArrowLeft", "Home", "End"].includes(event.key)) return;
    event.preventDefault(); const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (index + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length; tabs[next].focus(); tabs[next].click();
  });
  find("[data-storage-next]").addEventListener("click", () => { if (!next || loading) return; if (next === "offset") offset += 50; else { pages.push({ cursor, snapshot }); cursor = next; } load(); });
  find("[data-storage-prev]").addEventListener("click", () => { if (loading) return; if (offset) offset = Math.max(0, offset - 50); else { const previous = pages.pop(); if (!previous) return; cursor = previous.cursor; snapshot = previous.snapshot; } load(); });
  function readURL() {
    const q = new URLSearchParams(location.search); view = ["comfy", "training", "database"].includes(q.get("view")) ? q.get("view") : "comfy";
    mode = ["records", "operations"].includes(q.get("mode")) || view === "comfy" && ["files", "unresolved"].includes(q.get("mode")) ? q.get("mode") : "records";
    for (const name of ["kind", "state", "q"]) { const control = form.elements[name]; control.value = (q.get(name) || "").slice(0, 200); if (control instanceof HTMLSelectElement && control.selectedIndex < 0) control.selectedIndex = 0; }
    configure(); load(true);
  }
  window.addEventListener("popstate", readURL);
  window.addEventListener("pagehide", () => { controller?.abort(); epoch++; closeDetail(); });
  find("[data-storage-tabs]").hidden = false;
  readURL();
})();
