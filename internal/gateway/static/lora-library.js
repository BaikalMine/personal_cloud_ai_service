(() => {
  "use strict";
  const root = document.querySelector("[data-lora-library]");
  if (!root) return;
  const find = name => root.querySelector(`[data-library-${name}]`);
  const form = find("filters"), list = find("list"), inspector = find("inspector");
  const body = find("body"), actions = find("actions"), title = root.querySelector("#lora-library-title");
  const mobile = matchMedia("(max-width: 999px)");
  const key = `gateway.lora-library.v1.${root.dataset.owner}`;
  const idOK = value => typeof value === "string" && /^[A-Za-z0-9_-]{1,96}$/.test(value);
  let saved = {};
  try { saved = JSON.parse(sessionStorage.getItem(key) || "{}") || {}; } catch (_) {}
  const query = new URLSearchParams(location.search);
  if (query.has("created") || query.has("cancelled") || query.has("deleted")) saved = {};
  form.elements.view.value = (query.get("view") || saved.view) === "datasets" ? "datasets" : "loras";
  form.elements.search.value = String(saved.search || "").slice(0, 200);
  form.elements.state.value = saved.state || "";
  if (form.elements.state.selectedIndex < 0) form.elements.state.value = "";
  let selected = query.has("job") ? query.get("job") : saved.selected || "";
  if (!idOK(selected) || form.elements.view.value === "datasets" || query.get("deleted") === "1") selected = "";
  let rows = [], datasets = [], cursor = "", revision = "", total = 0;
  let listRequest, detailRequest, listEpoch = 0, detailEpoch = 0, inputTimer, expiryTimer;
  let opener = null;
  let usageRequest, usageTimer, verificationHistoryRequest;
  const stopUsage = (includeHistory = false) => { usageRequest?.abort(); usageRequest = null; if (includeHistory) { verificationHistoryRequest?.abort(); verificationHistoryRequest = null; } clearTimeout(usageTimer); };
  const node = (tag, text, cls) => {
    const value = document.createElement(tag);
    if (text !== undefined) value.textContent = text;
    if (cls) value.className = cls;
    return value;
  };
  const icon = name => {
    const definition = window.lucide?.icons?.[name];
    const value = definition ? window.lucide.createElement(definition) : node("span");
    value.setAttribute("aria-hidden", "true");
    return value;
  };
  const date = value => {
    const parsed = new Date(value);
    return Number.isFinite(parsed.getTime()) ? parsed.toLocaleDateString("ru-RU") : "Не записано";
  };
  const bytes = value => Number.isFinite(value) && value >= 0 ? `${(value / 1048576).toLocaleString("ru-RU", { maximumFractionDigits: 1 })} МБ` : "Не записано";
  const textValue = value => value === undefined || value === null || value === "" ? "Не записано" : String(value);
  function persist() {
    try { sessionStorage.setItem(key, JSON.stringify({ view: form.elements.view.value, search: form.elements.search.value, state: form.elements.state.value, selected })); } catch (_) {}
    const url = new URL(location.href);
    url.searchParams.delete("job"); url.searchParams.delete("view");
    if (selected) url.searchParams.set("job", selected);
    if (form.elements.view.value === "datasets") url.searchParams.set("view", "datasets");
    history.replaceState(history.state, "", url);
  }
  function errorMessage(error) {
    if (error.status === 401) return "Сессия завершена. Войдите снова.";
    if (error.status === 403) return "Доступ к обучению закрыт.";
    if (error.status === 404) return "LoRA или её паспорт больше недоступны.";
    return "Не удалось загрузить данные. Проверьте соединение и повторите запрос.";
  }
  async function get(url, signal) {
    const response = await fetch(url, { signal, credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" } });
    if (!response.ok || response.redirected) throw Object.assign(new Error("read_failed"), { status: response.redirected ? 401 : response.status });
    return response.json();
  }
  function facts(values) {
    const dl = node("dl", undefined, "task-detail-facts");
    values.forEach(([label, value, code]) => {
      dl.append(node("dt", label));
      const dd = node("dd");
      dd.append(node(code ? "code" : "span", textValue(value)));
      dl.append(dd);
    });
    return dl;
  }
  function link(label, href, symbol) {
    const a = node("a", undefined, "button ghost"); a.href = href;
    if (symbol) a.append(icon(symbol));
    a.append(node("span", label)); return a;
  }
  function clearAccess(error) {
    if (![401, 403].includes(error.status)) return false;
    ++listEpoch; listRequest?.abort();
    rows = []; datasets = []; cursor = ""; total = 0;
    closeDetail(false); renderList();
    list.setAttribute("aria-busy", "false"); find("more").disabled = false;
    find("error").textContent = errorMessage(error); find("error").hidden = false;
    if (error.status === 401) find("error").append(" ", link("Войти", "/login"));
    return true;
  }
  function mediaMark(item) {
    const mark = node("span", undefined, "task-row-mark"), preview = item.preview;
    mark.append(icon("Layers"));
    if (!preview || !Number.isFinite(preview.expires_unix) || preview.expires_unix <= Date.now() || preview.sensitive && !document.body.classList.contains("show-sensitive-content")) return mark;
    let url;
    try { url = new URL(preview.url, location.origin); } catch (_) { return mark; }
    if (url.origin !== location.origin || url.pathname !== "/api/tasks/thumbnail") return mark;
    const image = node("img"); image.alt = "Кадр исходного набора обучения";
    image.loading = "lazy"; image.src = url.href;
    image.addEventListener("error", () => mark.replaceChildren(icon("Layers")), { once: true });
    mark.title = image.alt; mark.replaceChildren(image);
    return mark;
  }
  function renderList() {
    const focusedID = list.contains(document.activeElement) ? document.activeElement.dataset.libraryId : "";
    clearTimeout(expiryTimer);
    const isDatasets = form.elements.view.value === "datasets";
    root.dataset.view = isDatasets ? "datasets" : "loras";
    find("state-field").hidden = isDatasets;
    find("placeholder").hidden = !!selected || isDatasets;
    const term = form.elements.search.value.trim().toLocaleLowerCase("ru-RU");
    const visible = isDatasets ? datasets.filter(item => `${item.name} ${item.id}`.toLocaleLowerCase("ru-RU").includes(term)) : rows;
    list.replaceChildren();
    for (const item of visible) {
      if (!idOK(item.id)) continue;
      const row = node(isDatasets ? "a" : "button", undefined, "task-row");
      row.dataset.libraryId = item.id;
      if (isDatasets) row.href = `/train-lora?dataset=${encodeURIComponent(item.id)}`;
      else {
        row.type = "button"; row.setAttribute("aria-pressed", String(item.id === selected));
        row.addEventListener("click", () => { opener = row; openDetail(item.id, true); });
      }
      const main = node("span", undefined, "task-row-main"), meta = node("span", undefined, "task-row-meta");
      main.append(node("span", item.name || "Без названия", "task-row-name"));
      main.append(node("span", isDatasets ? `${item.image_count} кадров · ${bytes(item.size_bytes)}` : item.preview ? "Кадр исходного набора" : "Обучение LoRA", "task-row-secondary"));
      if (isDatasets) {
        const mark = node("span", undefined, "task-row-mark"); mark.append(icon("Images")); row.append(mark);
        meta.append(node("span", `Хранится до ${date(item.expires_at)}`, "task-row-secondary"));
      } else {
        row.append(mediaMark(item));
        const status = window.AIGatewayTasks.status(item);
        const state = node("span", status.label, "task-state");
        state.dataset.tone = status.tone;
        meta.append(state);
      }
      meta.append(node("span", date(isDatasets ? item.updated_at : item.created_at), "task-row-secondary"));
      row.append(main, meta); list.append(row);
    }
    if (!visible.length) list.append(node("p", term || form.elements.state.value && !isDatasets ? "Ничего не найдено. Измените фильтры." : isDatasets ? "Сохранённых наборов пока нет." : "Обучений пока нет.", "ui-empty-state"));
    find("count").textContent = `${isDatasets ? "Наборы" : "Обучения"}: ${isDatasets ? visible.length : total}`;
    find("loaded").textContent = visible.length ? isDatasets ? `${visible.length} из ${datasets.length}` : `${rows.length} из ${total}` : "";
    find("more").hidden = isDatasets || !cursor;
    const expiry = rows.map(item => item.preview?.expires_unix).filter(value => value > Date.now());
    if (expiry.length) expiryTimer = setTimeout(renderList, Math.min(...expiry) - Date.now() + 20);
    if (idOK(focusedID)) list.querySelector(`[data-library-id="${focusedID}"]`)?.focus({ preventScroll: true });
  }
  async function loadList(append = false) {
    listRequest?.abort(); const controller = new AbortController(); listRequest = controller;
    const epoch = ++listEpoch, isDatasets = form.elements.view.value === "datasets";
    const timeout = setTimeout(() => controller.abort(), 15000);
    list.setAttribute("aria-busy", "true"); find("more").disabled = true; find("error").hidden = true;
    if (!(isDatasets ? datasets : rows).length) {
      list.replaceChildren(node("p", "Загрузка...", "ui-empty-state"));
      find("count").textContent = "Загрузка...";
    }
    try {
      if (isDatasets) {
        const data = await get("/api/lora-datasets", controller.signal);
        if (epoch !== listEpoch) return;
        datasets = Array.isArray(data.datasets) ? data.datasets : [];
      } else {
        const params = new URLSearchParams({ kind: "lora_training", limit: "30", search: form.elements.search.value.trim(), state: form.elements.state.value });
        if (append && cursor) params.set("cursor", cursor);
        const data = await get(`/api/tasks?${params}`, controller.signal);
        if (epoch !== listEpoch) return;
        if (!data.access?.training) throw Object.assign(new Error("access"), { status: 403 });
        if (append && revision !== data.revision) { await loadList(); return; }
        rows = append ? [...rows, ...(data.tasks || []).filter(item => !rows.some(row => row.id === item.id))] : data.tasks || [];
        revision = data.revision; cursor = data.next_cursor || ""; total = data.total;
      }
      renderList(); persist();
    } catch (error) {
      if (epoch !== listEpoch) return;
      if (append && error.status === 400) { await loadList(); return; }
      if (!clearAccess(error)) { renderList(); find("error").textContent = errorMessage(error); find("error").hidden = false; }
    } finally {
      clearTimeout(timeout);
      if (epoch === listEpoch) { list.setAttribute("aria-busy", "false"); find("more").disabled = false; }
    }
  }
  function showInspector(focus = false) {
    const active = inspector.contains(document.activeElement) ? document.activeElement : null;
    if (inspector.open) inspector.close();
    if (selected) { if (mobile.matches) inspector.showModal(); else inspector.show(); }
    sizeInspector();
    if (focus) title.focus({ preventScroll: true }); else active?.focus({ preventScroll: true });
  }
  function sizeInspector() {
    if (!selected || mobile.matches) { inspector.style.removeProperty("--task-inspector-height"); return; }
    const top = Math.max(84, inspector.getBoundingClientRect().top);
    inspector.style.setProperty("--task-inspector-height", `${Math.max(220, innerHeight - top - 18)}px`);
  }
  function closeDetail(restore = true) {
    stopUsage(true);
    selected = ""; ++detailEpoch; detailRequest?.abort();
    if (inspector.open) inspector.close();
    body.replaceChildren(); actions.replaceChildren(); renderList(); persist();
    if (restore) {
      const target = opener && list.querySelector(`[data-library-id="${opener.dataset.libraryId}"]`);
      (target || find("refresh")).focus({ preventScroll: true });
    }
  }
  function renderDetail(data) {
    const { training: job, passport } = data;
    body.replaceChildren(); actions.replaceChildren(); title.textContent = job.name || "LoRA";
    const state = window.AIGatewayTasks.status(job);
    const status = node("p", state.label, "task-state");
    status.dataset.tone = state.tone;
    body.append(status);
    if (passport.repeat_source && idOK(passport.repeat_source.id)) {
      body.append(facts([["Повтор обучения", passport.repeat_source.name]]));
      if (passport.repeat_source.available) body.append(link("Исходное обучение", `/train-lora?job=${encodeURIComponent(passport.repeat_source.id)}`, "History"));
      else body.append(node("p", "Исходное обучение удалено.", "muted"));
    }
    if (job.message || job.error) body.append(node("p", job.error || job.message, "task-detail-message"));
    const trigger = node("dl", undefined, "lora-trigger"); trigger.append(node("dt", "Триггер"), node("dd", passport.trigger || "Не записан")); body.append(trigger);
    body.append(facts([["Семейство", job.family_label || passport.family], ["Базовая модель", passport.execution?.profile?.base_model || job.base_model], ["Кадры", job.sample_count], ["Размер файла", job.artifact_bytes > 0 ? bytes(job.artifact_bytes) : null], ["Создано", date(job.created_at)]]));
    const evidence = ({ artifact_recorded: "Файл LoRA и компоненты обучения записаны в паспорт.", inputs_recorded: "Компоненты обучения записаны. Итоговый файл ещё не подтверждён.", not_recorded: "Для этого обучения данные об исполнении не записаны. Параметры заявки сохранены ниже." })[passport.evidence_state];
    body.append(node("p", evidence || "Сведения об исполнении недоступны.", "lora-evidence muted"));
    if (!passport.dataset_snapshot_available) body.append(node("p", "Исходная версия набора больше недоступна.", "lora-evidence muted"));
    if (job.state === "completed") renderUsage(body, selected);
    const details = node("details", undefined, "ui-disclosure"); details.append(node("summary", "Паспорт обучения"));
    const p = passport.requested_parameters || {};
    details.append(node("h3", "Параметры заявки", "lora-file-title"), facts([["Разрешение", p.resolution], ["Шаги", p.max_steps], ["Rank / alpha", `${textValue(p.network_dim)} / ${textValue(p.network_alpha)}`], ["Learning rate", p.learning_rate], ["Seed", p.seed, true], ["Версия набора", passport.dataset_snapshot_id, true], ["SHA-256 набора", passport.dataset_snapshot_hash, true]]));
    const execution = passport.execution;
    if (execution) {
      const actual = execution.parameters || {};
      details.append(node("h3", "Фактическое исполнение", "lora-file-title"), facts([["Зафиксировано", date(execution.captured_at)], ["Проверено", execution.verified_at ? date(execution.verified_at) : null], ["Профиль", execution.profile?.id], ["Версия модели", execution.profile?.model_version], ["SHA-256 профиля", execution.profile_sha256, true], ["Разрешение", actual.resolution], ["Шаги", actual.max_steps], ["Rank / alpha", `${textValue(actual.network_dim)} / ${textValue(actual.network_alpha)}`], ["Learning rate", actual.learning_rate], ["Seed", actual.seed, true]]));
      const labels = { base_source: "Исходная база", base_execution: "База обучения", vae: "VAE", text_encoder: "Текстовый энкодер" };
      for (const file of [...(execution.models || []).map(item => ({ ...item, label: labels[item.role] || item.role })), { ...execution.dataset_archive, label: "Архив набора" }, ...(execution.artifact ? [{ ...execution.artifact, label: "Файл LoRA" }] : [])]) {
        details.append(node("h3", file.label, "lora-file-title"), facts([["Файл", file.name], ["Размер", bytes(file.bytes)], ["SHA-256", file.sha256, true]]));
      }
      details.append(node("h3", "Код обучения", "lora-file-title"), facts([["SHA-256", execution.tuner?.source_sha256, true], ["Ревизия", execution.tuner?.revision, true], ["Локальные изменения", execution.tuner?.dirty === undefined ? null : execution.tuner.dirty ? "Есть" : "Нет"]]));
    }
    body.append(details);
    if (job.can_download && job.download_url === `/train-lora/${encodeURIComponent(selected)}/download`) actions.append(link("Скачать LoRA", job.download_url, "Download"));
    if (passport.dataset_snapshot_available && idOK(passport.dataset_snapshot_id)) actions.append(link("Исходный набор", `/train-lora?version=${encodeURIComponent(passport.dataset_snapshot_id)}`, "Images"));
    if (["completed", "failed", "cancelled"].includes(job.state) && passport.dataset_snapshot_available) {
      const repeat = node("button", undefined, "ghost"); repeat.type = "button"; repeat.dataset.loraRepeat = selected;
      repeat.append(icon("RotateCcw"), node("span", "Обучить заново")); actions.append(repeat);
    }
    actions.append(link("Задача", `/tasks?kind=lora_training&task=${encodeURIComponent(`lora_training:${selected}`)}`, "ListChecks"));
  }
  function renderUsage(parent, id) {
    const section = node("section", undefined, "lora-use");
    const message = node("p", "Совместимость ещё не проверена.", "muted");
    message.setAttribute("role", "status");
    const controls = node("div", undefined, "lora-use-controls");
    const check = node("button", undefined, "ghost"); check.type = "button";
    check.append(icon("RefreshCw"), node("span", "Проверить совместимость"));
    const field = node("label", undefined, "ui-field");
    field.append(node("span", "Модель", "ui-field-label"));
    const select = node("select"); select.setAttribute("aria-label", "Модель для LoRA"); field.append(select); field.hidden = true;
    const use = link("Использовать", "#", "ArrowRight"); use.className = "button"; use.hidden = true;
    const verify = link("Проверить LoRA", "#", "Columns2"); verify.hidden = true;
    controls.append(check); actions.append(use, verify); section.append(node("h3", "Генерация", "lora-file-title"), message, field, controls); parent.append(section);
    const historyLink = link("История проверок", `/generate?${new URLSearchParams({ lora_history: id })}`, "History"); controls.append(historyLink);
    const history = node("div", undefined, "lora-check-history");
    history.append(node("h3", "Проверки LoRA", "lora-file-title"), node("p", "Загрузка проверок...", "muted")); parent.append(history);
    const historyController = new AbortController(); verificationHistoryRequest = historyController;
    const historyEpoch = detailEpoch;
    get(`/api/lora-training/verifications?${new URLSearchParams({ source: id })}`, historyController.signal).then(data => {
      if (historyController.signal.aborted || historyEpoch !== detailEpoch || selected !== id) return;
      if (data.source_id !== id || !Array.isArray(data.items)) throw new Error("identity_mismatch");
      history.replaceChildren(node("h3", "Проверки LoRA", "lora-file-title"));
      data.items.slice(0, 3).forEach(item => {
        if (idOK(item.batch_id)) history.append(link(`${date(item.created_at)} · ${item.completed_count} из ${item.total_count} готово`, `/generate?${new URLSearchParams({ lora_comparison: item.batch_id })}`, "Columns2"));
      });
      if (!data.items.length) history.append(node("p", "Проверок пока нет.", "muted"));
    }).catch(error => {
      if (!historyController.signal.aborted && historyEpoch === detailEpoch && selected === id) { history.replaceChildren(node("p", "История проверок временно недоступна.", "muted"), link("Открыть историю", historyLink.href, "History")); }
    });
    let expiresAt = 0, models = [];
    function sync() {
      const model = models.find(item => item.id === select.value && item.available);
      use.hidden = !model || Date.now() >= expiresAt;
      if (!use.hidden) use.href = `/generate?${new URLSearchParams({ lora_job: id, lora_model: model.id })}`;
      else use.removeAttribute("href");
      verify.hidden = use.hidden;
      if (!verify.hidden) verify.href = `/generate?${new URLSearchParams({ lora_job: id, lora_model: model.id, lora_check: "1" })}`;
      else verify.removeAttribute("href");
    }
    select.addEventListener("change", sync);
    use.addEventListener("click", event => {
      if (Date.now() >= expiresAt || selected !== id) { event.preventDefault(); sync(); check.focus(); }
    });
    verify.addEventListener("click", event => {
      if (Date.now() >= expiresAt || selected !== id) { event.preventDefault(); sync(); check.focus(); }
    });
    check.addEventListener("click", async () => {
      stopUsage();
      const controller = new AbortController(); usageRequest = controller;
      const epoch = detailEpoch, timeout = setTimeout(() => controller.abort(), 60000);
      check.disabled = true; use.hidden = true; use.removeAttribute("href"); verify.hidden = true; verify.removeAttribute("href"); field.hidden = true;
      section.setAttribute("aria-busy", "true"); message.textContent = "Проверяем файл LoRA и модели...";
      try {
        const data = await get(`/api/lora-training/usage/${encodeURIComponent(id)}`, controller.signal);
        if (controller.signal.aborted || epoch !== detailEpoch || selected !== id) return;
        if (data.job_id !== id || !Array.isArray(data.models)) throw new Error("identity_mismatch");
        expiresAt = Number.isFinite(data.expires_at) ? Math.min(data.expires_at, Date.now() + 60000) : 0;
        models = data.models.filter(item => typeof item.id === "string" && item.id.length > 0 && item.id.length <= 3000);
        select.replaceChildren();
        models.forEach(item => {
          const option = node("option", item.available ? item.name : `${item.name}: ${item.reason || "Недоступна"}`);
          option.value = item.id; option.disabled = !item.available; select.append(option);
        });
        const available = models.find(item => item.available);
        select.value = available?.id || "";
        field.hidden = !models.length;
        message.textContent = data.message || "Совместимость не подтверждена.";
        sync();
        if (available && expiresAt > Date.now()) usageTimer = setTimeout(() => {
          const hadFocus = document.activeElement === use;
          sync(); message.textContent = "Проверка устарела. Проверьте совместимость снова.";
          if (hadFocus) check.focus({ preventScroll: true });
        }, expiresAt - Date.now() + 20);
      } catch (error) {
        if (epoch !== detailEpoch || selected !== id || clearAccess(error)) return;
        message.textContent = errorMessage(error);
      } finally {
        clearTimeout(timeout);
        if (epoch === detailEpoch && selected === id) { check.disabled = false; section.setAttribute("aria-busy", "false"); }
      }
    });
  }
  async function openDetail(id, focus = false) {
    if (!idOK(id)) return;
    stopUsage(true);
    selected = id; detailRequest?.abort();
    const controller = new AbortController(); detailRequest = controller;
    const epoch = ++detailEpoch, timeout = setTimeout(() => controller.abort(), 15000);
    title.textContent = "LoRA"; actions.replaceChildren(); body.replaceChildren(node("p", "Загрузка паспорта...", "muted"));
    renderList(); showInspector(focus); persist();
    try {
      const [detail, passport] = await Promise.all([get(`/api/tasks/detail?kind=lora_training&id=${encodeURIComponent(id)}`, controller.signal), get(`/api/lora-training/passports/${encodeURIComponent(id)}`, controller.signal)]);
      if (epoch !== detailEpoch || id !== selected) return;
      if (detail.id !== id || detail.training?.id !== id || passport.job_id !== id) throw new Error("identity_mismatch");
      renderDetail({ training: { ...detail.training, cancellation_pending: detail.cancellation_pending }, passport });
    } catch (error) {
      if (epoch !== detailEpoch || clearAccess(error)) return;
      body.replaceChildren(node("p", errorMessage(error), "ui-status-banner is-error"));
      const retry = node("button", "Повторить", "ghost"); retry.type = "button"; retry.addEventListener("click", () => openDetail(id)); body.append(retry);
    } finally { clearTimeout(timeout); }
  }
  function filtersChanged() {
    clearTimeout(inputTimer); ++listEpoch; listRequest?.abort();
    rows = []; cursor = ""; total = 0;
    if (form.elements.view.value === "datasets") closeDetail(false);
    renderList(); persist(); loadList();
  }
  form.addEventListener("submit", event => { event.preventDefault(); filtersChanged(); });
  form.addEventListener("change", event => { if (event.target.type !== "search") filtersChanged(); });
  form.elements.search.addEventListener("input", () => { clearTimeout(inputTimer); inputTimer = setTimeout(filtersChanged, 250); });
  find("refresh").addEventListener("click", () => { loadList(); if (selected) openDetail(selected); });
  find("more").addEventListener("click", () => loadList(true));
  find("close").addEventListener("click", () => closeDetail());
  inspector.addEventListener("cancel", event => { event.preventDefault(); closeDetail(); });
  mobile.addEventListener("change", () => { if (selected) showInspector(); });
  window.addEventListener("resize", sizeInspector);
  window.addEventListener("scroll", sizeInspector, { passive: true });
  document.querySelectorAll("[data-sensitive-toggle]").forEach(toggle => toggle.addEventListener("change", renderList));
  function pause() {
    stopUsage(true);
    ++listEpoch; ++detailEpoch; listRequest?.abort(); detailRequest?.abort(); clearTimeout(expiryTimer); clearTimeout(inputTimer);
    rows = rows.map(item => ({ ...item, preview: undefined })); renderList();
    actions.replaceChildren();
  }
  function resume() { if (!document.hidden) { loadList(); if (selected) openDetail(selected); } }
  window.addEventListener("pagehide", pause);
  window.addEventListener("pageshow", event => { if (event.persisted) resume(); });
  document.addEventListener("visibilitychange", () => document.hidden ? pause() : resume());
  renderList(); loadList(); if (selected) openDetail(selected);
})();
