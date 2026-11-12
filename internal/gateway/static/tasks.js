(() => {
  const root = document.querySelector("[data-task-page]");
  const model = window.AIGatewayTasks;
  const gpuModel = window.AIGatewayTaskGPU;
  if (!root || !model || !gpuModel) return;
  const find = selector => root.querySelector(selector);
  const form = find("[data-task-filters]"), list = find("[data-task-list]");
  const inspector = find("[data-task-inspector]"), detailBody = find("[data-task-detail-body]");
  const actions = find("[data-task-actions]"), confirm = find("[data-task-confirm]");
  const closeButton = find("[data-task-close]"), moreButton = find("[data-task-more]");
  const mobile = window.matchMedia("(max-width: 999px)");
  const storageKey = `ai-gateway.tasks.${root.dataset.owner}`;
  const state = { items: [], total: 0, revision: 0, cursor: "", pages: 1, selected: null, seriesOrigin: null, detail: null, loaded: false };
  const rowNodes = new Map(), batchNodes = new Map(), batchCache = new Map(), batchRequests = new Map();
  const expanded = new Set(), revealed = new Set();
  let filters = {}, listController, detailController, listEpoch = 0, detailEpoch = 0;
  let loading = false, refreshPending = false, actionRunning = false, connected = false, pageActive = !document.hidden, authRequired = false;
  let events, reconnectTimer, countdownTimer, fallbackTimer, searchTimer, retryAt = 0, failures = 0, detailSignature = "", scrollRestored = false;
  let saved = {};
  let gpuMonitor, listGPUMonitor, listGPUTimer;
  let listGPUViews = new Map();
  const observedRows = new Set();
  let previewTimer, previewExpiryTimer;
  let selectionOrigin = null, rowIntent = null;
  try { saved = JSON.parse(window.sessionStorage.getItem(storageKey) || "{}") || {}; } catch (_) {}
  const initialQuery = new URLSearchParams(window.location.search);
  if (Number.isInteger(saved.pages) && saved.pages > 0 && saved.pages <= 100) state.pages = saved.pages;
  const initialFilters = initialQuery.size ? Object.fromEntries(initialQuery) : saved.filters || {};
  ["kind", "state", "search", "from", "to"].forEach(name => {
    const value = String(initialFilters[name] || "");
    if (name === "kind") {
      const radio = [...form.querySelectorAll('[name="kind"]')].find(input => input.value === (value === "lora_caption_series" ? "lora_caption" : value));
      if (radio) radio.checked = true;
      if (value === "lora_caption_series" && form.elements.caption_view) form.elements.caption_view.value = "series";
    } else if (form.elements[name]) form.elements[name].value = value;
  });
  state.selected = model.selection(initialQuery.get("task") || (!initialQuery.size ? saved.selected : ""));
  const initialSeries = model.selection(initialQuery.get("series") || (!initialQuery.size ? saved.series : ""));
  if (state.selected?.kind === "lora_caption" && initialSeries?.kind === "lora_caption_series") state.seriesOrigin = initialSeries;
  (Array.isArray(saved.expanded) ? saved.expanded : []).slice(0, 100).forEach(id => expanded.add(String(id)));

  const element = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = String(text);
    return node;
  };
  const icon = name => {
    const key = name.split("-").map(part => part[0].toUpperCase() + part.slice(1)).join("");
    const definition = window.lucide?.icons?.[key];
    const node = definition ? window.lucide.createElement(definition) : element("span");
    node.setAttribute("aria-hidden", "true");
    return node;
  };
  const button = (label, name, handler, className = "ghost") => {
    const node = element("button", className);
    node.type = "button";
    if (name) node.append(icon(name));
    node.append(element("span", "", label));
    if (handler) node.addEventListener("click", handler);
    return node;
  };
  const safeURL = value => {
    try {
      const url = new URL(value, window.location.origin);
      return String(value).startsWith("/") && url.origin === window.location.origin ? url.pathname + url.search + url.hash : "";
    } catch (_) { return ""; }
  };
  const link = (label, href, name, className = "button ghost") => {
    const node = element("a", className);
    const safe = safeURL(href);
    if (safe) node.href = safe;
    if (name) node.append(icon(name));
    node.append(element("span", "", label));
    return node;
  };
  const dateText = (value, full = false) => {
    const date = new Date(value);
    if (!value || !Number.isFinite(date.getTime())) return "Время не указано";
    return new Intl.DateTimeFormat("ru-RU", { day: "2-digit", month: "short", ...(full ? { year: "numeric" } : {}), hour: "2-digit", minute: "2-digit" }).format(date);
  };
  const gpuNode = element("section", "task-gpu-status"), gpuHeading = element("div", "task-gpu-heading");
  const gpuTitle = element("h3", "", "GPU"), gpuMessage = element("p"), gpuPosition = element("p"), gpuTime = element("time");
  gpuNode.dataset.taskGpu = ""; gpuNode.hidden = true;
  gpuTitle.id = "task-gpu-title"; gpuNode.setAttribute("aria-labelledby", gpuTitle.id);
  gpuMessage.dataset.taskGpuMessage = ""; gpuMessage.setAttribute("role", "status");
  gpuPosition.dataset.taskGpuPosition = ""; gpuTime.dataset.taskGpuTime = "";
  const gpuRefresh = button("", "refresh-cw", () => connected ? gpuMonitor.refresh() : checkNow(), "ghost task-icon-button");
  gpuRefresh.dataset.taskGpuRefresh = ""; gpuRefresh.title = "Обновить состояние GPU"; gpuRefresh.setAttribute("aria-label", gpuRefresh.title);
  gpuHeading.append(gpuTitle, gpuRefresh); gpuNode.append(gpuHeading, gpuMessage, gpuPosition, gpuTime);
  const renderGPU = view => {
    gpuNode.hidden = view.hidden; gpuNode.dataset.tone = view.tone; gpuNode.dataset.fresh = String(view.fresh);
    if (gpuMessage.textContent !== view.message) gpuMessage.textContent = view.message;
    gpuPosition.hidden = !view.position; gpuPosition.textContent = view.position ? `Место в очереди: ${view.position}` : "";
    gpuTime.hidden = !view.timestamp; gpuTime.dateTime = view.timestamp;
    gpuTime.textContent = view.timestamp ? `Последняя проверка: ${new Intl.DateTimeFormat("ru-RU", { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(view.timestamp))}` : "";
    // Background polling must not blur a focused control in Firefox.
    gpuRefresh.disabled = view.paused;
    gpuRefresh.setAttribute("aria-busy", String(view.pending));
    gpuNode.setAttribute("aria-busy", String(view.pending));
    fitInspector();
  };
  const renderRowGPU = (key, node) => {
    // Preserve existing text nodes during background updates.
    const updateText = (target, value) => {
      if (target.textContent === value) return;
      if (target.firstChild?.nodeType === Node.TEXT_NODE) target.firstChild.data = value;
      else target.textContent = value;
    };
    const view = listGPUViews.get(key), gpu = node.querySelector("[data-task-row-gpu]");
    const visible = view && !view.hidden;
    gpu.hidden = !visible;
    gpu.dataset.fresh = String(Boolean(visible && view.fresh));
    gpu.dataset.tone = view?.tone || "muted";
    const message = visible ? view.message : "", position = visible && view.position ? `Очередь GPU: ${view.position}` : "";
    const label = [message, position].filter(Boolean).join(". ");
    updateText(gpu.children[0], message);
    updateText(gpu.children[1], position); gpu.children[1].hidden = !position;
    const time = gpu.children[2];
    time.hidden = !visible || !view.timestamp; time.dateTime = visible ? view.timestamp : "";
    updateText(time, visible && view.timestamp ? `Проверено ${new Intl.DateTimeFormat("ru-RU", { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(view.timestamp))}` : "");
    if (visible && view.timestamp) time.title = dateText(view.timestamp, true); else time.removeAttribute("title");
    node.setAttribute("aria-label", node.dataset.sourceLabel + (label ? `. ${label}` : ""));
  };
  const renderListGPU = views => {
    const oldKeys = new Set(listGPUViews.keys());
    listGPUViews = new Map(views.map(view => [view.key, view]));
    listGPUViews.forEach((_, key) => oldKeys.add(key));
    oldKeys.forEach(key => { const node = rowNodes.get(key); if (node) renderRowGPU(key, node); });
  };
  const syncListGPU = () => {
    window.clearTimeout(listGPUTimer); listGPUTimer = null;
    if (!listGPUMonitor) return;
    if (!pageActive || authRequired || (mobile.matches && inspector.open)) { listGPUMonitor.setActive(false); return; }
    const visible = [];
    for (const task of state.items) {
      if (!["generation", "lora_training", "lora_caption"].includes(task.kind)) continue;
      const node = rowNodes.get(model.key(task));
      if (!node?.isConnected || node.closest("details:not([open])")) continue;
      const box = node.getBoundingClientRect();
      if (box.width <= 0 || box.height <= 0 || box.bottom <= 0 || box.top >= window.innerHeight || box.right <= 0 || box.left >= window.innerWidth) continue;
      visible.push({ kind: task.kind, id: task.id, terminal: model.terminal(task.state) });
      if (visible.length === gpuModel.listLimit) break;
    }
    listGPUMonitor.select(visible); listGPUMonitor.setActive(true);
  };
  const scheduleListGPU = () => {
    if (!listGPUTimer) listGPUTimer = window.setTimeout(syncListGPU, 100);
  };
  const rowVisibility = new IntersectionObserver(scheduleListGPU);
  const observeListRows = () => {
    const current = new Set(list.querySelectorAll("[data-task-key]"));
    observedRows.forEach(node => { if (!current.has(node)) { rowVisibility.unobserve(node); observedRows.delete(node); } });
    current.forEach(node => { if (!observedRows.has(node)) { observedRows.add(node); rowVisibility.observe(node); } });
    scheduleListGPU();
  };
  const syncCaptionView = () => {
    const caption = form.querySelector('[name="kind"]:checked')?.value === "lora_caption";
    const selector = form.elements.caption_view;
    if (selector) { selector.disabled = !caption; find("[data-task-caption-view]").hidden = !caption; }
    const series = caption && selector?.value === "series";
    [...form.elements.state.options].forEach(option => {
      option.disabled = series ? !["", "running", "completed", "failed", "cancelled", "partial"].includes(option.value) : option.value === "partial";
      option.hidden = option.disabled;
      if (option.disabled && option.selected) form.elements.state.value = "";
    });
    return series;
  };
  const readFilters = () => ({ kind: syncCaptionView() ? "lora_caption_series" : form.querySelector('[name="kind"]:checked')?.value || "", state: form.elements.state.value, search: form.elements.search.value.trim(), from: form.elements.from.value, to: form.elements.to.value });
  const persist = () => {
    try { window.sessionStorage.setItem(storageKey, JSON.stringify({ filters, pages: state.pages, selected: state.selected ? model.key(state.selected) : "", series: state.seriesOrigin ? model.key(state.seriesOrigin) : "", expanded: [...expanded].slice(0, 100), scroll: window.scrollY, detailScroll: detailBody.scrollTop })); } catch (_) {}
  };
  const updateURL = () => {
    const query = new URLSearchParams();
    Object.entries(filters).forEach(([name, value]) => { if (value) query.set(name, value); });
    if (state.selected) query.set("task", model.key(state.selected));
    if (state.seriesOrigin) query.set("series", model.key(state.seriesOrigin));
    window.history.replaceState(null, "", window.location.pathname + (query.size ? "?" + query : ""));
    persist();
  };
  const showError = message => {
    const node = find("[data-task-error]");
    node.textContent = message || "";
    node.hidden = !message;
  };
  const actionNotice = (message, error = false) => {
    const node = find("[data-task-action-notice]");
    node.textContent = message || "";
    node.hidden = !message;
    node.classList.toggle("is-error", error);
  };
  const request = async (url, options = {}) => {
    const controller = new AbortController(), upstream = options.signal;
    const abort = () => controller.abort();
    if (upstream?.aborted) controller.abort();
    upstream?.addEventListener("abort", abort, { once: true });
    const timer = window.setTimeout(abort, 20000);
    try {
      const response = await fetch(url, { credentials: "same-origin", cache: "no-store", ...options, signal: controller.signal });
      if (response.redirected || response.status === 401) {
        const error = new Error("Сеанс завершён. Войдите в аккаунт снова."); error.status = 401; throw error;
      }
      const payload = await response.json().catch(() => null);
      if (!response.ok || !payload) {
        const error = new Error(payload?.error && !/^[a-z_]+$/.test(payload.error) ? payload.error : response.status === 403 ? "Доступ к задачам изменился." : "Не удалось получить данные. Попробуйте ещё раз.");
        error.status = response.status; throw error;
      }
      return payload;
    } catch (error) {
      if (!upstream?.aborted && (error.name === "AbortError" || error instanceof TypeError)) throw new Error("Связь с Gateway прервалась. Попробуйте ещё раз.");
      throw error;
    } finally {
      window.clearTimeout(timer);
      upstream?.removeEventListener("abort", abort);
    }
  };
  const reconcile = (parent, nodes) => {
    const keep = new Set(nodes);
    let next = parent.firstElementChild;
    nodes.forEach(node => {
      if (node === next) next = next.nextElementSibling;
      else parent.insertBefore(node, next);
    });
    [...parent.children].forEach(node => { if (!keep.has(node)) node.remove(); });
  };
  const mediaAllowed = media => !media.sensitive || document.body.classList.contains("show-sensitive-content") || revealed.has(media.url);
  const renderRow = (task, variant = false) => {
    const key = model.key(task);
    let node = rowNodes.get(key);
    if (!node) {
      node = element("button", "task-row"); node.type = "button"; node.dataset.taskKey = key;
      node.append(element("span", "task-row-mark"), element("span", "task-row-main"), element("span", "task-row-meta"));
      node.children[1].append(element("strong", "task-row-name"), element("span", "task-row-secondary"));
      node.children[2].append(element("span", "task-state"), element("time"));
      const gpu = element("span", "task-row-gpu"); gpu.dataset.taskRowGpu = ""; gpu.hidden = true;
      const message = element("span"), position = element("span"), time = element("time");
      message.dataset.taskRowGpuMessage = ""; position.dataset.taskRowGpuPosition = ""; time.dataset.taskRowGpuTime = "";
      gpu.append(message, position, time); node.append(gpu);
      node.addEventListener("click", () => selectTask(model.selection(key)));
      const entry = element("div", "task-row-entry"); entry.dataset.taskRowKey = key;
      const next = button("", null, () => {
        const current = state.items.find(item => model.key(item) === key);
        if (!current) return;
        const action = model.rowAction(current);
        if ((rowIntent || actionRunning) && action.code !== "inspect") return;
        if (!connected && action.code !== "inspect") return;
        selectTask(model.selection(key), { action: action.code, origin: next });
      }, "ghost task-icon-button task-row-action");
      next.dataset.taskRowAction = "";
      entry.append(node, next);
      rowNodes.set(key, node);
    }
    const status = model.status(task);
    node.setAttribute("aria-pressed", String(Boolean(state.selected && model.key(state.selected) === key)));
    node.dataset.sourceLabel = `${task.name || model.kinds[task.kind]}. ${status.label}${model.seriesSummary(task) ? `. ${model.seriesSummary(task)}` : ""}${task.preview?.label ? `. ${task.preview.label}` : ""}`;
    renderRowGPU(key, node);
    node.children[1].children[0].textContent = variant ? `Вариант ${task.batch_position || ""} · ${task.name || "Генерация"}` : task.name || model.kinds[task.kind];
    node.children[1].children[1].textContent = model.seriesSummary(task) || (task.kind === "lora_training" ? model.kinds[task.kind] : task.stage || model.kinds[task.kind]);
    node.children[2].children[0].textContent = status.label;
    node.children[2].children[0].dataset.tone = status.tone;
    node.children[2].children[1].textContent = dateText(task.updated_at);
    node.children[2].children[1].dateTime = task.updated_at || "";
    const mark = node.children[0], preview = task.preview;
    const media = preview && mediaAllowed(preview) && safeURL(preview.url) && Number(preview.expires_unix) > Date.now() ? preview : null;
    const origin = media?.source_type === "dataset" ? "кадр исходного набора" : media?.source_type === "video" ? "первый кадр видео" : "";
    if (origin) node.children[1].children[1].textContent += ` · ${origin}`;
    mark.title = media?.label || "";
    const markKey = media?.url || model.icons[task.kind];
    if (mark.dataset.key !== markKey) {
      delete mark.dataset.failed;
      if (media) {
        const image = element("img"); image.alt = media.label || ""; image.loading = "lazy"; image.decoding = "async";
        image.addEventListener("error", () => {
          if (mark.firstElementChild === image) {
            mark.dataset.failed = "true";
            mark.replaceChildren(icon(model.icons[task.kind] || "file"));
          }
        }, { once: true });
        image.src = safeURL(media.url); mark.replaceChildren(image);
      }
      else mark.replaceChildren(icon(model.icons[task.kind] || "file"));
      mark.dataset.key = markKey;
    }
    if (mark.firstElementChild?.tagName === "IMG") mark.firstElementChild.alt = media?.label || "";
    const next = node.parentElement.querySelector("[data-task-row-action]"), action = model.rowAction(task);
    next.dataset.action = action.code;
    next.title = action.label;
    next.setAttribute("aria-label", `${action.label}: ${task.name || model.kinds[task.kind]}`);
    next.disabled = action.code !== "inspect" && (!connected || actionRunning);
    next.setAttribute("aria-busy", String(rowIntent?.key === key));
    if (next.dataset.icon !== action.icon) { next.replaceChildren(icon(action.icon)); next.dataset.icon = action.icon; }
    node.parentElement.dataset.selected = node.getAttribute("aria-pressed");
    return node.parentElement;
  };
  const renderBatch = group => {
    let node = batchNodes.get(group.id);
    if (!node) {
      node = element("details", "task-batch"); node.dataset.taskBatch = group.id;
      const summary = element("summary");
      const title = element("span", "task-batch-title"); title.append(element("strong"), element("small"));
      summary.append(icon("chevron-right"), title);
      node.append(summary, element("div", "task-batch-body"));
      node.addEventListener("toggle", () => {
        if (node.open) expanded.add(group.id); else expanded.delete(group.id);
        persist();
        syncListGPU();
      });
      batchNodes.set(group.id, node);
    }
    const cached = batchCache.get(group.id), batch = cached?.detail?.batch;
    node.querySelector("strong").textContent = `Пакет · ${batch?.model_name || group.name || "Генерации"}`;
    node.querySelector("small").textContent = batch ? `${batch.completed_count} готово из ${batch.total_count} · ${batch.parameter_label || "Разные seed"}` : "Уточняем результаты пакета...";
    const body = node.children[1];
    let open = body.querySelector(".task-batch-open");
    if (!open) open = button("Детали пакета", "layers", () => selectTask({ kind: "generation_batch", id: group.id }), "ghost task-batch-open");
    reconcile(body, [open, ...group.children.map(task => renderRow(task, true))]);
    node.open = expanded.has(group.id);
    if ((!cached || cached.revision !== state.revision) && !batchRequests.has(group.id) && pageActive) loadBatch(group.id);
    return node;
  };
  const renderList = () => {
    window.clearTimeout(previewExpiryTimer);
    const deadlines = state.items.map(task => Number(task.preview?.expires_unix)).filter(time => time > Date.now());
    if (pageActive && deadlines.length) previewExpiryTimer = window.setTimeout(renderList, Math.min(2147483647, Math.max(1, Math.min(...deadlines) - Date.now() + 1)));
    find("[data-task-count]").textContent = state.loaded ? `${filters.kind === "lora_caption_series" ? "Найдено серий" : "Найдено задач"}: ${state.total}` : "Загрузка задач...";
    const groups = model.grouped(state.items);
    if (!groups.length) {
      const empty = element("div", "ui-empty-state");
      empty.append(element("p", "", state.loaded ? Object.values(filters).some(Boolean) ? "По этим фильтрам задач нет." : "Задач пока нет." : "Загрузка задач..."));
      if (state.loaded && Object.values(filters).some(Boolean)) empty.append(button("Сбросить фильтры", "filter-x", resetFilters));
      list.replaceChildren(empty);
    } else reconcile(list, groups.map(row => row.task ? renderRow(row.task) : renderBatch(row.batch)));
    observeListRows();
    list.setAttribute("aria-busy", String(loading));
    moreButton.hidden = !state.cursor;
    moreButton.disabled = loading;
    find("[data-task-loaded]").textContent = state.loaded && state.total ? `Загружено: ${state.items.length} из ${state.total}` : "";
    if (!scrollRestored && state.loaded) {
      scrollRestored = true;
      if (Number.isFinite(saved.scroll) && saved.scroll > 0) window.requestAnimationFrame(() => window.scrollTo({ top: saved.scroll, behavior: "instant" }));
    }
  };
  const loadBatch = async id => {
    const controller = new AbortController(), epoch = listEpoch, revision = state.revision;
    batchRequests.set(id, controller);
    try {
      const detail = await request(`/api/tasks/detail?kind=generation_batch&id=${encodeURIComponent(id)}`, { signal: controller.signal });
      if (controller.signal.aborted || epoch !== listEpoch || detail.id !== id || detail.kind !== "generation_batch") return;
      batchCache.set(id, { detail, revision });
      renderList();
    } catch (_) {
      if (epoch === listEpoch && !controller.signal.aborted) {
        const node = batchNodes.get(id);
        if (node) node.querySelector("small").textContent = "Результаты пакета временно недоступны";
      }
    } finally { if (batchRequests.get(id) === controller) batchRequests.delete(id); }
  };

  const setConnection = ready => {
    connected = ready;
    // Media retention, classification and hiding need not advance task revision.
    if (!ready) { window.clearTimeout(previewTimer); previewTimer = null; }
    else if (!previewTimer && pageActive && !authRequired) previewTimer = window.setTimeout(() => {
      previewTimer = null;
      refresh();
    }, 30000);
    gpuMonitor?.setConnected(ready);
    listGPUMonitor?.setConnected(ready);
    const banner = find("[data-task-connection]");
    banner.hidden = ready;
    if (!ready) {
      const seconds = Math.max(1, Math.ceil((retryAt - Date.now()) / 1000));
      find("[data-task-connection-text]").textContent = authRequired ? "Сеанс завершён. Войдите в аккаунт снова." : retryAt ? `Нет связи. ${state.loaded ? "Показано последнее подтверждённое состояние. " : ""}Повтор через ${seconds} с.` : "Подключаем обновления...";
    }
    actions.querySelectorAll("button[data-task-mutation]").forEach(node => { node.disabled = !connected || actionRunning; });
    list.querySelectorAll("[data-task-row-action]").forEach(node => { node.disabled = node.dataset.action !== "inspect" && (!connected || actionRunning); });
    fitInspector();
  };
  const revokeTaskAccess = message => {
    listGPUMonitor.clear();
    listController?.abort(); listEpoch++; loading = false; refreshPending = false;
    batchRequests.forEach(controller => controller.abort()); batchRequests.clear();
    authRequired = true; state.items = []; state.total = 0; state.loaded = true; state.cursor = "";
    find("[data-task-refresh]").disabled = false;
    closeInspector({ restore: false }); renderList(); stopEvents(); setConnection(false); showError(message);
  };
  const updateAccess = access => {
    if (!access) return;
    root.querySelectorAll("[data-task-access]").forEach(label => {
      label.hidden = !access[label.dataset.taskAccess];
      const input = label.querySelector("input"); input.disabled = label.hidden;
      if (input.disabled && input.checked) {
        form.querySelector('[name="kind"][value=""]').checked = true;
        filters = readFilters(); updateURL(); refreshPending = true;
      }
    });
  };
  const refresh = async ({ reset = false, more = false } = {}) => {
    if (!pageActive || authRequired) return;
    try { model.apiQuery(filters); } catch (error) { showError(error.message); return; }
    if (loading && !reset) { refreshPending = true; return; }
    if (reset) {
      listController?.abort(); detailController?.abort();
      batchRequests.forEach(controller => controller.abort()); batchRequests.clear();
      state.pages = 1; state.cursor = "";
    }
    const epoch = ++listEpoch;
    listController = new AbortController();
    const controller = listController;
    loading = true; refreshPending = false;
    list.setAttribute("aria-busy", "true"); moreButton.disabled = true;
    find("[data-task-refresh]").disabled = true;
    try {
      const pages = [], desired = more ? 1 : state.pages;
      let cursor = more ? state.cursor : "", total = state.total, revision = state.revision, access;
      for (let index = 0; index < desired; index++) {
        const page = await request(`/api/tasks?${model.apiQuery(filters, cursor)}`, { signal: controller.signal });
        if (epoch !== listEpoch) return;
        if (page.schema_version !== 1 || !Array.isArray(page.tasks)) throw new Error("Не удалось прочитать список задач.");
        if (more && page.revision !== state.revision) { state.pages += 1; refreshPending = true; return; }
        if (index === 0) { revision = page.revision; total = page.total; }
        else if (page.revision !== revision) refreshPending = true;
        access = page.access;
        pages.push(page.tasks); cursor = page.next_cursor || "";
        if (!cursor) break;
      }
      if (epoch !== listEpoch) return;
      state.items = model.merge(more ? [state.items, ...pages] : pages);
      state.total = total; state.revision = revision; state.cursor = cursor; state.loaded = true;
      if (more) state.pages += 1;
      rowNodes.forEach(node => { if (node.children[0].dataset.failed) delete node.children[0].dataset.key; });
      updateAccess(access); showError(""); setConnection(true); renderList();
      if (state.selected) loadDetail({ quiet: Boolean(state.detail) });
    } catch (error) {
      if (controller.signal.aborted || epoch !== listEpoch) return;
      if (error.status === 400 && (more || state.pages > 1)) { state.pages = 1; state.cursor = ""; refreshPending = true; }
      else {
        showError(error.message || "Не удалось обновить задачи.");
        if (error.status === 401 || error.status === 403) {
          revokeTaskAccess(error.message);
        }
        setConnection(false);
        if (!authRequired) scheduleReconnect();
      }
    } finally {
      if (epoch === listEpoch) {
        loading = false; list.setAttribute("aria-busy", "false"); moreButton.disabled = false;
        find("[data-task-refresh]").disabled = false;
        if (refreshPending && pageActive) { refreshPending = false; queueMicrotask(() => refresh()); }
      }
    }
  };

  const fitInspector = () => {
    if (!inspector.open || mobile.matches) return;
    const top = Math.max(84, inspector.getBoundingClientRect().top);
    inspector.style.setProperty("--task-inspector-height", `${Math.max(260, window.innerHeight - top - 24)}px`);
  };
  const showInspector = () => {
    find("[data-task-placeholder]").hidden = true;
    if (inspector.open) return;
    if (mobile.matches) inspector.showModal(); else inspector.show();
    syncListGPU();
    fitInspector();
  };
  const closeInspector = ({ restore = true } = {}) => {
    const previous = state.selected && model.key(state.selected);
    const origin = selectionOrigin || (state.seriesOrigin && rowNodes.get(model.key(state.seriesOrigin)));
    selectionOrigin = null; rowIntent = null;
    detailController?.abort(); detailEpoch++;
    gpuMonitor.clear();
    state.selected = null; state.seriesOrigin = null; state.detail = null; detailSignature = "";
    if (confirm.open) confirm.close();
    if (inspector.open) inspector.close();
    syncListGPU();
    find("[data-task-placeholder]").hidden = false;
    actions.replaceChildren(); actionNotice(""); updateURL(); renderList();
    detailBody.replaceChildren();
    if (restore && previous) (origin?.isConnected && !origin.disabled ? origin : rowNodes.get(previous))?.focus({ preventScroll: true });
  };
  const selectTask = (selected, { action = "inspect", origin = null, parentSeries = null } = {}) => {
    if (!selected) return;
    rowIntent = action === "inspect" ? null : { key: model.key(selected), action };
    selectionOrigin = origin || rowNodes.get(model.key(selected)) || null;
    const changed = !state.selected || model.key(selected) !== model.key(state.selected);
    if (changed) gpuMonitor.clear();
    if (confirm.open) confirm.close();
    state.selected = selected;
    state.seriesOrigin = selected.kind === "lora_caption" && parentSeries?.kind === "lora_caption_series" ? parentSeries : null;
    if (changed) { state.detail = null; detailSignature = ""; saved.detailScroll = 0; actionNotice(""); detailBody.scrollTop = 0; }
    showInspector(); updateURL(); renderList();
    closeButton.focus({ preventScroll: true });
    loadDetail({ quiet: !changed, intent: rowIntent });
  };
  const addFacts = (parent, pairs) => {
    const facts = element("dl", "task-detail-facts");
    pairs.filter(([, value]) => value !== undefined && value !== null && value !== "").forEach(([label, value]) => facts.append(element("dt", "", label), element("dd", "", value)));
    if (facts.children.length) parent.append(facts);
  };
  const appendMedia = (parent, media, label = "Результат") => {
    const url = safeURL(media.url);
    if (!url) return;
    const figure = element("figure", "task-detail-media");
    if (!mediaAllowed(media)) {
      figure.append(button("Показать контент 18+", "eye", () => { revealed.add(media.url); renderDetail(state.detail, true); renderList(); }));
    } else {
      const node = element(media.media_type === "video" ? "video" : "img");
      node.src = url; node.dataset.taskMedia = url;
      if (node.tagName === "VIDEO") { node.controls = true; node.playsInline = true; node.preload = "metadata"; }
      else { node.alt = label; node.loading = "lazy"; }
      node.addEventListener("error", () => { figure.replaceChildren(element("p", "ui-status-banner", "Файл сейчас недоступен. Его срок хранения мог истечь.")); });
      figure.append(node);
      const download = link("Скачать файл", url); download.download = media.filename || "";
      figure.append(download);
    }
    figure.append(element("figcaption", "", media.filename || label));
    parent.append(figure);
  };
  const copyCaption = async text => {
    try {
      if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(text);
      else {
        const field = element("textarea"); field.value = text; inspector.append(field); field.select();
        const copied = document.execCommand("copy"); field.remove();
        if (!copied) throw new Error("copy_failed");
      }
      actionNotice("Подпись скопирована.");
    } catch (_) { actionNotice("Не удалось скопировать подпись. Выделите текст в задаче.", true); }
  };
  const renderDetail = (detail, force = false) => {
    if (!detail || !state.selected || model.key(detail) !== model.key(state.selected)) return;
    const signature = JSON.stringify({ ...detail, gpu: undefined });
    if (!force && detailSignature === signature) return;
    detailSignature = signature;
    const task = model.detailTask(detail), status = model.status(task), scroll = detailBody.scrollTop;
    const playing = [...detailBody.querySelectorAll("video")].map(node => ({ src: node.dataset.taskMedia, time: node.currentTime, paused: node.paused }));
    const body = document.createDocumentFragment();
    find("[data-task-detail-title]").textContent = task.name;
    find("[data-task-detail-kind]").textContent = model.kinds[task.kind];
    const stateRow = element("div", "task-detail-state"), badge = element("span", "task-state", status.label);
    badge.dataset.tone = status.tone; stateRow.append(badge);
    const training = detail.training;
    const hasTrainingSteps = training && task.state === "running" && Number.isInteger(training.training_step) && Number.isInteger(training.training_total_steps) && training.training_total_steps > 0 && training.training_step >= 0 && training.training_step <= training.training_total_steps;
    const trainingPercent = hasTrainingSteps ? Math.floor(training.training_step * 100 / training.training_total_steps) : null;
    const shownProgress = training && task.state === "running" ? trainingPercent : task.progress;
    if (Number.isFinite(shownProgress)) stateRow.append(element("span", "muted", hasTrainingSteps ? `${trainingPercent}% обучения` : `${shownProgress}% · оценка`));
    body.append(stateRow);
    if (Number.isFinite(shownProgress) && !model.terminal(task.state)) {
      const progress = element("progress", "task-detail-progress"); progress.max = 100; progress.value = shownProgress; progress.setAttribute("aria-label", hasTrainingSteps ? "Прогресс шагов обучения" : "Приблизительный общий прогресс"); body.append(progress);
    }
    if (training && task.state === "running") body.append(element("p", "muted", hasTrainingSteps ? `Шаг ${training.training_step} из ${training.training_total_steps}` : "Точный прогресс обучения пока недоступен."));
    body.append(element("p", "task-detail-message", task.message || task.stage || status.label));
    body.append(gpuNode);
    if (detail.open_reason) body.append(element("p", "ui-status-banner", detail.open_reason));
    if (["queued", "waiting_for_resources", "draft"].includes(task.state)) body.append(element("p", "muted", "Оценка времени пока недоступна."));
    if (detail.generation) {
      const job = detail.generation;
      (job.media || []).forEach(media => appendMedia(body, media));
      if (job.prompt) { body.append(element("h3", "task-detail-label", "Промпт"), element("p", "task-detail-text", job.prompt)); }
      if (window.AIGatewayPostprocess?.isJob(job)) {
        addFacts(body, [...window.AIGatewayPostprocess.facts(job.postprocess), ["Попытка", job.attempt]]);
        if (!job.postprocess) body.append(element("p", "muted", "План обработки недоступен"));
        for (const source of window.AIGatewayPostprocess.sources(job.postprocess)) {
          if (source.href) { const link = element("a", "button ghost", source.label); link.href = source.href; body.append(link); }
          else body.append(element("p", "muted", source.message));
        }
      } else addFacts(body, [["Модель", job.model_name], ["Seed", job.seed], ["Попытка", job.attempt]]);
    }
    if (detail.training) {
      const job = detail.training;
      if (job.error) body.append(element("p", "ui-status-banner is-error", job.error));
      addFacts(body, [["Последний этап", job.stage], ["Базовая модель", job.base_model], ["Материал", `${job.sample_count} фото`], ["Размер", `${job.resolution} px`], ["Шаги", job.max_train_steps], ["Файл LoRA", job.artifact_name]]);
    }
    if (detail.caption) {
      const job = detail.caption;
      appendMedia(body, { url: detail.preview_url, media_type: "image", filename: job.source?.image?.name || "Исходный кадр" }, "Исходный кадр");
      if (job.caption) body.append(element("h3", "task-detail-label", "Подпись"), element("p", "task-detail-text", job.caption));
      if (job.warning) body.append(element("p", "ui-status-banner", job.warning));
      if (job.error) body.append(element("p", "ui-status-banner is-error", job.error));
      if (detail.retry_reason) body.append(element("p", "ui-status-banner", detail.retry_reason));
      addFacts(body, [["Trigger", job.source?.trigger_word], ["Модель", job.model]]);
    }
    if (detail.batch) {
      const batch = detail.batch;
      addFacts(body, [["Готово", `${batch.completed_count} из ${batch.total_count}`], ["Ошибки", batch.failed_count], ["Отменено или истекло", batch.cancelled_count], ["Эксперимент", batch.parameter_label || "Разные seed"]]);
      if (batch.mode === "lora_verification") {
        const compare = element("a", "button ghost", "Сравнить результаты LoRA");
        compare.href = `/generate?lora_comparison=${encodeURIComponent(batch.batch_id)}`;
        body.append(compare);
        if (batch.missing_count) addFacts(body, [["Удалённые результаты", batch.missing_count]]);
      }
      body.append(element("h3", "task-detail-label", "Все варианты пакета"));
      (batch.jobs || []).forEach(job => {
        const task = model.generation(job);
        const row = button(`Вариант ${job.batch_position} · ${model.status(task).label}`, "image", () => selectTask({ kind: "generation", id: job.job_id }), "ghost task-detail-retry");
        body.append(row);
        const media = job.media?.[0]; if (media) appendMedia(body, media, `Вариант ${job.batch_position}`);
      });
    }
    if (detail.caption_series) {
      const series = detail.caption_series;
      addFacts(body, [["Готово", series.completed_count], ["Ошибки", series.failed_count], ["Отменено", series.cancelled_count], ["Недоступно", series.unavailable_count]]);
      body.append(element("h3", "task-detail-label", "Кадры серии"));
      (series.members || []).forEach(member => {
        const label = `Кадр ${member.position} · ${member.state === "unavailable" ? "Недоступно" : model.status(member).label}`;
        if (member.available) body.append(button(label, "captions", () => selectTask({ kind: "lora_caption", id: member.job_id }, { parentSeries: { kind: "lora_caption_series", id: series.id }, origin: rowNodes.get(`lora_caption_series:${series.id}`) }), "ghost task-detail-retry"));
        else body.append(element("p", "task-detail-message", `${label}. Эта попытка больше не доступна.`));
      });
    }
    addFacts(body, [["Создана", dateText(task.created_at, true)], ["Обновлена", dateText(task.updated_at, true)], ["Хранится до", task.expires_at ? dateText(task.expires_at, true) : ""]]);
    const technical = element("details", "ui-disclosure"); technical.append(element("summary", "", "Технические сведения"));
    addFacts(technical, [["ID", task.id], ["Состояние", task.state], ["Набор", task.dataset_id], ["Кадр", task.image_id]]);
    body.append(technical);
    const openTechnical = detailBody.querySelector("details")?.open;
    detailBody.replaceChildren(body); technical.open = Boolean(openTechnical);
    detailBody.scrollTop = scroll;
    playing.forEach(prior => {
      const video = [...detailBody.querySelectorAll("video")].find(node => node.dataset.taskMedia === prior.src);
      if (video) video.addEventListener("loadedmetadata", () => { video.currentTime = Math.min(prior.time, video.duration || prior.time); if (!prior.paused) video.play().catch(() => {}); }, { once: true });
    });
    const nodes = [];
    if (detail.open_url) nodes.push(link(detail.open_label || "Открыть в студии", detail.open_url, "arrow-up-right"));
    if (detail.kind === "lora_caption" && state.seriesOrigin) {
      const parent = { ...state.seriesOrigin };
      nodes.push(button("К серии", "arrow-left", () => selectTask(parent)));
    } else if (detail.series_url) nodes.push(link("Открыть серию", detail.series_url, "captions"));
    if (detail.training?.can_download && detail.training.download_url) nodes.push(link("Скачать LoRA", detail.training.download_url, "download"));
    if (detail.caption?.caption) nodes.push(button("Скопировать подпись", "copy", () => copyCaption(detail.caption.caption)));
    if (detail.retry_url) {
      const retry = button("Повторить описание", "rotate-cw", () => runAction("retry", detail));
      retry.dataset.taskMutation = "retry"; retry.disabled = !connected || actionRunning; nodes.push(retry);
    }
    if (detail.cancel_url) {
      const cancel = button(detail.kind === "generation_batch" ? "Отменить остаток" : "Отменить", "square", () => askCancellation(detail), "ghost danger");
      cancel.dataset.taskMutation = "cancel"; cancel.disabled = !connected || actionRunning; nodes.push(cancel);
    }
    actions.replaceChildren(...nodes);
    fitInspector();
  };
  const loadDetail = async ({ quiet = false, intent = null } = {}) => {
    if (!state.selected || !pageActive) return;
    detailController?.abort(); const epoch = ++detailEpoch, selected = { ...state.selected };
    const controller = new AbortController(); detailController = controller;
    const requestStartedAt = performance.now();
    showInspector();
    if (!quiet) {
      detailSignature = "";
      find("[data-task-detail-title]").textContent = "Загрузка задачи...";
      find("[data-task-detail-kind]").textContent = model.kinds[selected.kind];
      detailBody.replaceChildren(element("p", "muted", "Загрузка...")); actions.replaceChildren();
    }
    inspector.setAttribute("aria-busy", "true");
    try {
      const detail = await request(`/api/tasks/detail?kind=${encodeURIComponent(selected.kind)}&id=${encodeURIComponent(selected.id)}`, { signal: controller.signal });
      if (controller.signal.aborted || epoch !== detailEpoch || !state.selected || model.key(detail) !== model.key(state.selected)) return;
      state.detail = detail;
      gpuMonitor.select({ ...state.selected, terminal: model.terminal(model.detailTask(detail).state) }, detail.gpu ?? null, requestStartedAt);
      renderDetail(detail);
      if (Number.isFinite(saved.detailScroll) && saved.detailScroll > 0) {
        detailBody.scrollTop = saved.detailScroll; saved.detailScroll = 0;
      }
      if (detail.generation) {
        const index = state.items.findIndex(item => model.key(item) === model.key(selected));
        if (index >= 0) renderList();
      }
      if (intent && rowIntent === intent && connected) {
        rowIntent = null; renderList();
        performRowAction(intent.action, detail);
      }
    } catch (error) {
      if (controller.signal.aborted || epoch !== detailEpoch) return;
      if (error.status === 401 || error.status === 403) {
        revokeTaskAccess(error.message);
        return;
      }
      if (error.status === 404 || !state.detail) {
        gpuMonitor.clear();
        state.detail = null; detailSignature = "";
        find("[data-task-detail-title]").textContent = error.status === 404 ? "Задача недоступна" : "Не удалось загрузить задачу";
        detailBody.replaceChildren(element("p", "task-detail-message", error.message), button("Проверить ещё раз", "refresh-cw", () => loadDetail()));
        actions.replaceChildren();
      } else actionNotice("Не удалось обновить задачу. Показано последнее подтверждённое состояние.", true);
    } finally {
      if (rowIntent === intent && intent) {
        rowIntent = null; renderList();
        if (state.selected && model.key(state.selected) === intent.key && controller.signal.aborted) actionNotice("Состояние обновляется. Проверьте задачу перед действием.");
      }
      if (epoch === detailEpoch) inspector.removeAttribute("aria-busy");
    }
  };
  const performRowAction = (action, detail) => {
    if (!connected || !state.selected || model.key(detail) !== model.key(state.selected)) return;
    if (action === "cancel") {
      if (detail.cancel_url) askCancellation(detail);
      else actionNotice("Отмена больше недоступна. Показано текущее состояние задачи.");
      return;
    }
    let path = "", reason = "";
    if (action === "edit_generation") {
      if (detail.generation?.retryable) path = safeURL(detail.edit_url);
      reason = "Параметры этой задачи больше недоступны.";
    } else if (action === "open_caption" || action === "open_training") {
      path = safeURL(detail.open_url);
      reason = detail.open_reason || "Исходные материалы больше недоступны.";
    } else if (action === "download_training") {
      if (detail.training?.can_download) path = safeURL(detail.training.download_url);
      reason = "Файл LoRA сейчас недоступен.";
    }
    if (path) {
      if (action === "edit_generation") {
        const target = new URL(path, window.location.origin);
        target.searchParams.set("return_to", window.location.pathname + window.location.search);
        path = target.pathname + target.search;
      }
      persist(); window.location.assign(path);
    }
    else actionNotice(reason || "Действие больше недоступно. Проверьте состояние задачи.", true);
  };
  let pendingCancellation = null;
  const askCancellation = detail => {
    if (actionRunning || !connected) return;
    pendingCancellation = detail;
    find("[data-task-confirm-name]").textContent = model.detailTask(detail).name;
    find("[data-task-confirm-copy]").textContent = detail.kind === "generation_batch" ? "Готовые варианты останутся. Ожидающие варианты не запустятся." : "Если задача уже выполняется, потребуется подтверждение её остановки.";
    confirm.showModal(); find("[data-task-confirm-back]").focus();
  };
  const runAction = async (action, detail) => {
    if (actionRunning || !connected || !state.selected || model.key(detail) !== model.key(state.selected)) return;
    const path = safeURL(action === "retry" ? detail.retry_url : detail.cancel_url);
    if (!path) return;
    actionRunning = true; setConnection(connected); actionNotice("Отправляем запрос...");
    const key = model.key(detail);
    try {
      const caption = detail.kind === "lora_caption";
      const body = new URLSearchParams({ csrf: root.dataset.csrf || "" });
      if (detail.kind === "generation") body.set("job_id", detail.id);
      if (detail.kind === "generation_batch") body.set("batch_id", detail.id);
      await request(path, { method: "POST", headers: { Accept: "application/json", ...(caption ? { "X-CSRF-Token": root.dataset.csrf || "" } : { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }) }, body: caption ? undefined : body });
      if (state.selected && model.key(state.selected) === key) {
        actionNotice(action === "retry" ? "Повтор описания принят." : "Запрос отмены принят. Проверяем состояние задачи.");
        await loadDetail({ quiet: true });
      }
      await refresh();
    } catch (error) {
      if (state.selected && model.key(state.selected) === key) {
        actionNotice(error.status && error.status < 500 ? error.message : "Не удалось подтвердить ответ. Запрос мог быть принят; проверьте состояние задачи перед повтором.", true);
        await loadDetail({ quiet: true });
      }
    } finally { actionRunning = false; setConnection(connected); }
  };
  const stopEvents = () => {
    events?.close(); events = null;
    window.clearTimeout(reconnectTimer); window.clearInterval(countdownTimer); window.clearTimeout(fallbackTimer);
    reconnectTimer = countdownTimer = fallbackTimer = null;
  };
  const scheduleReconnect = () => {
    if (!pageActive || authRequired || reconnectTimer) return;
    events?.close(); events = null;
    failures += 1; retryAt = Date.now() + Math.min(15000, 2000 * (2 ** Math.min(failures - 1, 3)));
    setConnection(false);
    window.clearInterval(countdownTimer);
    countdownTimer = window.setInterval(() => setConnection(false), 1000);
    reconnectTimer = window.setTimeout(async () => {
      reconnectTimer = null; window.clearInterval(countdownTimer); countdownTimer = null;
      retryAt = 0; await refresh(); if (connected) connectEvents(); else scheduleReconnect();
    }, Math.max(1, retryAt - Date.now()));
  };
  const connectEvents = () => {
    if (!pageActive || authRequired) return;
    stopEvents(); retryAt = 0;
    if (!("EventSource" in window)) {
      fallbackTimer = window.setTimeout(async () => { await refresh(); connectEvents(); }, 10000); return;
    }
    const stream = new EventSource(`/api/tasks/events?since=${encodeURIComponent(state.revision)}`); events = stream;
    const receive = event => {
      if (events !== stream || !pageActive) return;
      failures = 0; setConnection(true);
      if (Number(event.data) !== Number(state.revision)) refresh();
    };
    stream.addEventListener("ready", receive); stream.addEventListener("tasks", receive);
    stream.addEventListener("error", () => { if (events === stream) scheduleReconnect(); });
  };
  const checkNow = async () => {
    if (authRequired) { window.location.assign("/login?next=" + encodeURIComponent(window.location.pathname + window.location.search)); return; }
    stopEvents(); retryAt = 0; await refresh(); syncListGPU(); listGPUMonitor.refresh({ force: true }); if (connected) connectEvents();
  };
  const changeFilters = () => {
    filters = readFilters(); updateURL(); saved.scroll = 0; scrollRestored = true;
    refresh({ reset: true });
  };
  const resetFilters = () => { form.reset(); changeFilters(); };
  form.addEventListener("submit", event => { event.preventDefault(); window.clearTimeout(searchTimer); changeFilters(); });
  form.addEventListener("change", event => { if (event.target.name !== "search") changeFilters(); });
  form.elements.search.addEventListener("input", () => { window.clearTimeout(searchTimer); searchTimer = window.setTimeout(changeFilters, 300); });
  find("[data-task-reset]").addEventListener("click", resetFilters);
  find("[data-task-refresh]").addEventListener("click", checkNow); find("[data-task-check]").addEventListener("click", checkNow);
  moreButton.addEventListener("click", () => refresh({ more: true }));
  closeButton.addEventListener("click", () => closeInspector());
  inspector.addEventListener("cancel", event => { event.preventDefault(); closeInspector(); });
  inspector.addEventListener("keydown", event => { if (event.key === "Escape" && !mobile.matches) { event.preventDefault(); closeInspector(); } });
  find("[data-task-confirm-back]").addEventListener("click", () => { pendingCancellation = null; confirm.close(); });
  find("[data-task-confirm-submit]").addEventListener("click", () => { const detail = pendingCancellation; pendingCancellation = null; confirm.close(); if (detail) runAction("cancel", detail); });
  confirm.addEventListener("cancel", () => { pendingCancellation = null; });
  mobile.addEventListener("change", () => {
    scheduleListGPU();
    if (!state.selected || !inspector.open) return;
    const focused = document.activeElement;
    inspector.close(); showInspector();
    if (inspector.contains(focused)) focused.focus({ preventScroll: true });
  });
  window.addEventListener("scroll", fitInspector, { passive: true });
  window.addEventListener("resize", fitInspector, { passive: true });
  window.addEventListener("scroll", scheduleListGPU, { passive: true });
  window.addEventListener("resize", scheduleListGPU, { passive: true });
  document.querySelectorAll("[data-sensitive-toggle]").forEach(toggle => toggle.addEventListener("change", () => { if (!toggle.checked) revealed.clear(); renderDetail(state.detail, true); renderList(); }));
  const pause = () => {
    persist(); pageActive = false; stopEvents(); listController?.abort(); detailController?.abort();
    rowIntent = null;
    gpuMonitor.setActive(false); setConnection(false);
    listGPUMonitor.setActive(false); window.clearTimeout(listGPUTimer); listGPUTimer = null;
    batchRequests.forEach(controller => controller.abort()); batchRequests.clear();
    window.clearTimeout(searchTimer);
    window.clearTimeout(previewExpiryTimer);
  };
  const resume = async () => {
    if (pageActive || document.hidden) return;
    pageActive = true; loading = false; await refresh(); gpuMonitor.setActive(true); syncListGPU(); if (connected) connectEvents();
  };
  window.addEventListener("pagehide", pause); window.addEventListener("pageshow", resume);
  document.addEventListener("visibilitychange", () => document.hidden ? pause() : resume());
  gpuMonitor = gpuModel.createMonitor({
    fetchSnapshot: (selected, signal) => request(`/api/tasks/gpu?kind=${encodeURIComponent(selected.kind)}&id=${encodeURIComponent(selected.id)}`, { signal }),
    onChange: renderGPU,
    onAccessError: (error, selected) => {
      if (!state.selected || model.key(selected) !== model.key(state.selected)) return;
      if (error.status === 404) loadDetail({ quiet: true }); else revokeTaskAccess(error.message);
    }
  });
  gpuMonitor.setActive(pageActive);
  listGPUMonitor = gpuModel.createListMonitor({
    fetchSnapshots: (sources, signal) => {
      const query = new URLSearchParams(); sources.forEach(source => query.append("task", model.key(source)));
      return request(`/api/tasks/gpu-snapshots?${query}`, { signal });
    },
    onChange: renderListGPU,
    onUnavailable: () => refresh(),
    onAccessError: error => revokeTaskAccess(error.message)
  });
  listGPUMonitor.setActive(pageActive);
  filters = readFilters();
  if (state.selected) showInspector();
  refresh().then(() => { if (connected) connectEvents(); });
})();
