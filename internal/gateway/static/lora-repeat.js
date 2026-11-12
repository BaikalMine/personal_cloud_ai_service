(() => {
  "use strict";
  const root = document.querySelector("[data-lora-library]");
  const dialog = document.getElementById("lora-repeat-confirm");
  if (!root || !dialog) return;
  const find = name => dialog.querySelector(`[data-repeat-${name}]`);
  const message = find("message"), facts = find("facts"), launch = find("enqueue");
  const prepare = find("prepare"), recover = find("recover"), task = find("task"), dataset = find("dataset");
  const storageKey = `gateway.lora-repeat.pending.${root.dataset.owner}`;
  const api = "/api/lora-training/repeats";
  const validID = id => typeof id === "string" && /^[A-Za-z0-9_-]{16,96}$/.test(id);
  let pending = null, current = null, confirmation = null, epoch = 0, busy = false, opener = null, expiryTimer;
  try {
    const value = JSON.parse(localStorage.getItem(storageKey) || "null");
    if (value && validID(value.source) && validID(value.request)) pending = value;
  } catch (_) {}
  function requestID() {
    const bytes = new Uint8Array(18); crypto.getRandomValues(bytes);
    return Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join("");
  }
  function savePending(value) {
    const encoded = JSON.stringify(value);
    localStorage.setItem(storageKey, encoded);
    if (localStorage.getItem(storageKey) !== encoded) throw new Error("storage_unavailable");
    pending = value;
  }
  function forgetPending() {
    pending = null;
    try { localStorage.removeItem(storageKey); } catch (_) {}
  }
  function state(text) { message.textContent = text; }
  function reset() {
    confirmation = null; clearTimeout(expiryTimer);
    [launch, prepare, recover, task, dataset].forEach(control => { control.hidden = true; control.disabled = false; });
    facts.replaceChildren(); task.removeAttribute("href"); dataset.removeAttribute("href");
  }
  function setBusy(value) {
    busy = value; dialog.setAttribute("aria-busy", String(value));
    [launch, prepare, recover].forEach(button => { button.disabled = value; });
  }
  async function request(url, body) {
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 30000);
    try {
      const response = await fetch(url, { method: body ? "POST" : "GET", credentials: "same-origin", cache: "no-store", signal: controller.signal,
        headers: body ? { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8", Accept: "application/json" } : { Accept: "application/json" }, body });
      const data = await response.json().catch(() => ({}));
      if (!response.ok || response.redirected) throw Object.assign(new Error(data.error || "Не удалось получить ответ сервера."), { status: response.redirected ? 401 : response.status });
      return data;
    } finally { clearTimeout(timer); }
  }
  function showReceipt(data) {
    if (!validID(data.job_id) || data.source_id !== current.source) throw new Error("Ответ не соответствует исходному обучению.");
    reset(); forgetPending();
    state(data.deleted ? "Повтор уже был создан. Задание удалено; новый запуск не отправлен." : "Повтор обучения создан. Его состояние доступно в задачах.");
    if (!data.deleted) { task.href = `/tasks?${new URLSearchParams({ kind: "lora_training", task: `lora_training:${data.job_id}` })}`; task.hidden = false; }
  }
  function showError(error) {
    state(error.status === 401 ? "Сессия завершена. Войдите снова." : error.status === 403 ? "Доступ к обучению закрыт." : error.message || "Не удалось получить ответ сервера.");
    if ([401, 403].includes(error.status)) { reset(); return; }
    prepare.hidden = false;
    recover.hidden = !pending;
  }
  async function quote() {
    if (busy || !current) return;
    const version = ++epoch; reset(); setBusy(true); state("Проверяем исходный набор и параметры обучения...");
    try {
      const data = await request(`${api}/quote`, new URLSearchParams({ csrf: root.dataset.csrf, source_id: current.source, client_request_id: current.request }));
      if (version !== epoch) return;
      if (data.job_id) { showReceipt(data); return; }
      if (data.source_id !== current.source || !validID(data.snapshot?.id) || !data.parameters || typeof data.confirmation !== "string" || data.expires_at * 1000 <= Date.now()) throw new Error("Подтверждение обучения недоступно.");
      confirmation = data;
      const dl = document.createElement("dl"); dl.className = "task-detail-facts";
      const p = data.parameters;
      for (const [label, value] of [["Исходное обучение", data.source_name], ["Набор", data.snapshot.name], ["Версия", data.snapshot.revision], ["Базовая модель", data.base_model], ["Триггер", data.trigger_word], ["Кадры", p.sample_count], ["Разрешение", p.resolution], ["Шаги", p.max_steps], ["Rank / alpha", `${p.network_dim} / ${p.network_alpha}`], ["Learning rate", p.learning_rate], ["Seed", p.seed]]) {
        const dt = document.createElement("dt"), dd = document.createElement("dd"); dt.textContent = label; dd.textContent = String(value ?? "Не записано"); dl.append(dt, dd);
      }
      facts.append(dl);
      state(data.recorded_components ? "Новое обучение с сохранёнными параметрами и проверкой компонентов. Исходная LoRA не изменится." : "Параметры и набор сохранены, но контрольные суммы компонентов исходного обучения не записаны. Повтор использует текущие файлы выбранного профиля.");
      dataset.href = `/train-lora?version=${encodeURIComponent(data.snapshot.id)}`; dataset.hidden = false; launch.hidden = false;
      expiryTimer = setTimeout(() => { confirmation = null; launch.hidden = true; prepare.hidden = false; state("Подтверждение истекло. Подготовьте повтор заново."); }, Math.max(1, data.expires_at * 1000 - Date.now()));
    } catch (error) { if (version === epoch) showError(error); }
    finally { if (version === epoch) setBusy(false); }
  }
  async function restore() {
    if (busy || !pending) return;
    current = { ...pending }; const version = ++epoch;
    reset(); setBusy(true); state("Проверяем, был ли создан повтор обучения...");
    try {
      const data = await request(`${api}?${new URLSearchParams({ request: current.request })}`);
      if (version === epoch) showReceipt(data);
    } catch (error) {
      if (version !== epoch) return;
      if (error.status === 404) { state("Задание пока не найдено. Проверьте запуск ещё раз или подготовьте подтверждение с тем же запросом."); prepare.hidden = false; recover.hidden = false; }
      else showError(error);
    } finally { if (version === epoch) setBusy(false); }
  }
  async function enqueue() {
    if (busy || !confirmation || confirmation.expires_at * 1000 <= Date.now()) return;
    try { savePending({ source: current.source, request: current.request }); }
    catch (_) { state("Браузер не сохранил запрос для восстановления. Обучение не отправлено. Разрешите локальное хранение и повторите подтверждение."); return; }
    const version = ++epoch, token = confirmation.confirmation;
    clearTimeout(expiryTimer); setBusy(true); state("Передаём повтор в очередь обучения...");
    try {
      const data = await request(`${api}/enqueue`, new URLSearchParams({ csrf: root.dataset.csrf, source_id: current.source, client_request_id: current.request, confirmation: token }));
      if (version === epoch) showReceipt(data);
    } catch (error) {
      if (version !== epoch) return;
      reset();
      state("Ответ о запуске не подтверждён. Проверьте состояние сохранённого запроса."); recover.hidden = false;
      if (error.status >= 400 && error.status < 500) showError(error);
    } finally { if (version === epoch) setBusy(false); }
  }
  function open(source, trigger) {
    if (busy) return;
    opener = trigger || document.activeElement;
    reset(); if (!dialog.open) dialog.showModal();
    dialog.querySelector("h2").focus({ preventScroll: true });
    if (pending) { current = { ...pending }; restore(); }
    else { current = { source, request: requestID() }; quote(); }
  }
  root.addEventListener("click", event => {
    const button = event.target.closest("[data-lora-repeat]");
    if (button && validID(button.dataset.loraRepeat)) open(button.dataset.loraRepeat, button);
  });
  launch.addEventListener("click", enqueue); prepare.addEventListener("click", quote); recover.addEventListener("click", restore);
  find("close").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => { ++epoch; setBusy(false); reset(); if (opener?.isConnected) opener.focus({ preventScroll: true }); });
  if (pending) open(pending.source);
})();
