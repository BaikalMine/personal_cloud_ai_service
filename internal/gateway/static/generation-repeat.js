(() => {
  "use strict";
  const dialog = document.getElementById("generation-repeat");
  const api = window.AIGatewayGeneration;
  if (!dialog || !api?.createRepeatController || !dialog.dataset.owner) return;
  const find = name => dialog.querySelector(`[data-repeat-${name}]`);
  const message = find("message"), details = find("details"), media = find("media");
  const controls = Object.fromEntries(["submit", "recover", "check", "discard", "task"].map(name => [name, find(name)]));
  const key = `gateway.generation-repeat.pending.${dialog.dataset.owner}`;
  let opener = null, openerJobID = "", shownView = null, mediaIndex = 0, notifiedID = "", mediaTimer = null;
  const focusTrap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: dialog, documentRef: document });
  dialog.querySelectorAll("summary").forEach(summary => { summary.tabIndex = 0; });
  const revealed = new Set();
  const stateMessages = {
    checking: "Проверяем параметры, модели и исходные материалы...",
    submitting: "Передаём подтверждённый повтор в очередь...",
    recovering: "Уточняем, было ли создано задание...",
    discarding: "Проверяем, можно ли остановить запрос...",
    uncertain: "Ответ о запуске не подтверждён. Сохранённый запрос ещё может завершиться.",
    expired: "Подтверждение истекло. Проверьте условия повтора заново.",
    cancelled: "Запрос отклонён. Он больше не сможет создать задание.",
    paused: "Проверка приостановлена.",
    storage_error: "Браузер не сохранил запрос для восстановления. Повтор не отправлен. Разрешите хранение данных сайта и обновите страницу.",
  };
  async function request(endpoint, value, signal) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 50000);
    const abort = () => controller.abort();
    signal?.addEventListener("abort", abort, { once: true });
    if (signal?.aborted) controller.abort();
    const status = endpoint === "status";
    const body = status ? undefined : new URLSearchParams({ csrf: dialog.dataset.csrf, source_job_id: value.sourceID, client_request_id: value.requestID });
    if (value.confirmation) body.set("confirmation", value.confirmation);
    try {
      const response = await fetch(`/generate/jobs/repeat/${endpoint}${status ? `?${new URLSearchParams({ request_id: value.requestID })}` : ""}`, {
        method: status ? "GET" : "POST", body, credentials: "same-origin", cache: "no-store", signal: controller.signal,
        headers: { Accept: "application/json", ...(status ? {} : { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }) },
      });
      const data = await response.json().catch(() => ({}));
      if (!response.ok || response.redirected) throw Object.assign(new Error(data.error || "Не удалось получить ответ сервера."), { status: response.redirected ? 401 : response.status });
      return data;
    } finally { clearTimeout(timer); signal?.removeEventListener("abort", abort); }
  }
  function stopMedia() {
    clearTimeout(mediaTimer); mediaTimer = null;
    media.querySelectorAll("video").forEach(video => { video.pause(); video.removeAttribute("src"); video.load(); });
    media.querySelectorAll("img").forEach(img => img.removeAttribute("src"));
    media.replaceChildren();
  }
  function renderMedia() {
    stopMedia();
    if (!dialog.open || document.hidden || !shownView) return;
    const items = shownView.source?.media || [], item = items[mediaIndex];
    find("media-nav").hidden = items.length < 2;
    find("media-count").textContent = `${mediaIndex + 1} / ${items.length}`;
    find("previous").disabled = mediaIndex <= 0;
    find("next").disabled = mediaIndex >= items.length - 1;
    const unavailable = text => { const p = document.createElement("p"); p.textContent = text; media.append(p); };
    if (!item || !item.url) { unavailable("Исходный результат не сохранён"); return; }
    if (item.expires_unix > 0 && item.expires_unix <= Date.now()) { unavailable("Срок хранения результата истёк"); return; }
    if (item.expires_unix > 0) mediaTimer = setTimeout(renderMedia, Math.min(2147483647, Math.max(1, item.expires_unix - Date.now())));
    if (item.sensitive && !revealed.has(item.id)) {
      const button = document.createElement("button"); button.type = "button"; button.className = "button ghost"; button.textContent = "Показать результат 18+";
      button.addEventListener("click", () => { revealed.add(item.id); renderMedia(); }); media.append(button); return;
    }
    let url;
    try { url = new URL(item.url, location.origin); if (url.origin !== location.origin || !["http:", "https:"].includes(url.protocol)) throw Error(); }
    catch (_) { unavailable("Адрес результата недоступен"); return; }
    const element = document.createElement(item.media_type === "video" ? "video" : "img");
    if (element instanceof HTMLVideoElement) { element.controls = true; element.playsInline = true; element.preload = "metadata"; }
    else element.alt = item.filename || "Исходный результат генерации";
    element.addEventListener("error", () => { if (media.contains(element)) { stopMedia(); unavailable("Не удалось загрузить исходный результат"); } }, { once: true });
    element.src = url.href; media.append(element);
  }
  function renderDifferences(target, differences) {
    target.replaceChildren();
    for (const difference of differences) {
      const row = document.createElement("div"); row.className = "generation-repeat-difference";
      const label = document.createElement("strong"); label.textContent = difference.label; row.append(label);
      for (const [name, value] of [["Было", difference.before], ["Будет", difference.after]]) {
        const column = document.createElement("div"), heading = document.createElement("span"), text = document.createElement("p");
        heading.textContent = name; text.textContent = value || "Не записано"; column.append(heading, text); row.append(column);
      }
      target.append(row);
    }
  }
  function renderView(view) {
    shownView = view; mediaIndex = 0; revealed.clear();
    const facts = find("facts"); facts.replaceChildren();
    for (const [label, value] of [["Модель", view.model], ["Workflow", view.workflow], ["Seed", view.seed], ["Файлы моделей", view.files_observed ? "Проверены на хосте" : "Контрольные суммы не проверены"]]) {
      const dt = document.createElement("dt"), dd = document.createElement("dd"); dt.textContent = label; dd.textContent = value || "Не записано"; facts.append(dt, dd);
    }
    const quota = view.template_id === "minimax-h3-video" ? view.quota?.video : view.quota?.image;
    const kind = view.template_id === "minimax-h3-video" ? "видео" : "изображение";
    const limits = [];
    if (quota?.daily_limit > 0) limits.push(`сегодня осталось ${quota.daily_remaining} из ${quota.daily_limit}`);
    if (quota?.total_limit > 0) limits.push(`всего осталось ${quota.total_remaining} из ${quota.total_limit}`);
    find("quota").textContent = view.quota_known ? `Один повтор: 1 ${kind}. ${limits.length ? limits.join("; ") + "." : "Без лимита запусков."}` : "Лимиты будут уточнены после проверки.";
    find("edit").href = `/generate?${new URLSearchParams({ edit_job: view.source_id })}`;
    find("prompt").textContent = view.prompt || "Промпт не записан";
    const differences = view.differences || [];
    const human = differences.filter(item => !item.technical), technical = differences.filter(item => item.technical);
    find("changes").hidden = human.length === 0; find("technical").hidden = technical.length === 0;
    renderDifferences(find("differences"), human); renderDifferences(find("technical-differences"), technical);
    find("warnings").replaceChildren();
    for (const warning of view.warnings || []) { const li = document.createElement("li"); li.textContent = warning; find("warnings").append(li); }
    const assets = view.assets || []; find("inputs").hidden = assets.length === 0; find("assets").replaceChildren();
    for (const asset of assets) { const li = document.createElement("li"); li.textContent = `${asset.number}. ${asset.name}`; find("assets").append(li); }
    renderMedia();
  }
  function render(state) {
    const focused = document.activeElement;
    dialog.dataset.phase = state.phase;
    let text = stateMessages[state.phase] || state.view?.message || state.error || "";
    if (state.phase === "uncertain" && state.error) text += ` ${state.error}`;
    if (state.phase === "accepted") text = state.receipt.removed ? "Повтор уже был создан. Задание удалено; новый запуск не отправлен." : "Повтор создан. Его состояние доступно в задачах.";
    message.textContent = text;
    details.hidden = !state.view;
    if (state.view && shownView !== state.view) renderView(state.view);
    if (!state.view) { shownView = null; stopMedia(); }
    controls.submit.hidden = !["ready", "changed", "unverified"].includes(state.phase);
    controls.submit.disabled = !state.canSubmit;
    controls.recover.hidden = !state.pending || !["uncertain", "recovering"].includes(state.phase);
    controls.check.hidden = !["pending", "blocked", "error", "expired", "paused", "uncertain", "ready", "changed", "unverified"].includes(state.phase);
    controls.discard.hidden = !state.pending;
    controls.task.hidden = state.phase !== "accepted" || !!state.receipt?.removed;
    for (const name of ["check", "recover", "discard"]) controls[name].disabled = state.busy;
    if (!controls.task.hidden) controls.task.href = `/tasks?${new URLSearchParams({ kind: "generation", task: `generation:${state.receipt.job_id}` })}`;
    else controls.task.removeAttribute("href");
    if (state.phase === "accepted" && notifiedID !== state.receipt.job_id) {
      notifiedID = state.receipt.job_id; document.dispatchEvent(new CustomEvent("generation-repeat-accepted", { detail: state.receipt }));
    }
    if (dialog.open && dialog.contains(focused) && focused.closest("[hidden]")) message.focus({ preventScroll: true });
  }
  const controller = api.createRepeatController({
    check: (value, signal) => request("check", value, signal), submit: value => request("submit", value), recover: value => request("status", value), discard: value => request("discard", value),
    storage: { load: () => JSON.parse(sessionStorage.getItem(key) || "null"), save: value => sessionStorage.setItem(key, JSON.stringify(value)), clear: () => sessionStorage.removeItem(key) },
    newID: () => { const bytes = new Uint8Array(18); crypto.getRandomValues(bytes); return Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join(""); }, onState: render,
  });
  function open(sourceID, trigger, options) {
    if (!api.validRepeatID(sourceID)) return;
    opener = trigger || document.activeElement;
    openerJobID = opener?.closest?.("[data-job-id]")?.dataset.jobId || "";
    if (!dialog.open) dialog.showModal();
    if (focusTrap) focusTrap.activate({ trigger: opener, initialFocus: dialog.querySelector("h2"), onEscape: () => dialog.close() });
    else dialog.querySelector("h2").focus({ preventScroll: true });
    controller.open(sourceID, options);
  }
  api.openRepeat = sourceID => {
    if (api.validRepeatID(sourceID)) location.assign(`/generate?${new URLSearchParams({ edit_job: sourceID })}`);
  };
  api.openCheckedRepeat = open;
  api.openRepeatNow = (sourceID, trigger) => open(sourceID, trigger, { immediate: true });
  document.addEventListener("click", event => {
    const trigger = event.target.closest("[data-generation-repeat]");
    if (trigger) { event.preventDefault(); api.openRepeat(trigger.dataset.generationRepeat); }
  });
  controls.submit.addEventListener("click", controller.submit); controls.check.addEventListener("click", controller.check);
  controls.recover.addEventListener("click", controller.recover); controls.discard.addEventListener("click", controller.discard);
  find("close").addEventListener("click", () => dialog.close());
  find("previous").addEventListener("click", () => { mediaIndex--; renderMedia(); });
  find("next").addEventListener("click", () => { mediaIndex++; renderMedia(); });
  dialog.addEventListener("close", () => {
    controller.setVisible(false); stopMedia(); focusTrap?.deactivate({ restore: false });
    const replacement = openerJobID ? Array.from(document.querySelectorAll(`[data-job-id="${CSS.escape(openerJobID)}"] button`)).find(button => button.textContent.trim() === "Повторить") : null;
    const target = opener?.isConnected ? opener : replacement;
    target?.focus({ preventScroll: true });
  });
  document.addEventListener("visibilitychange", () => { controller.setVisible(dialog.open && !document.hidden); if (document.hidden) stopMedia(); else renderMedia(); });
  const saved = controller.snapshot();
  if (saved.pending) open(saved.sourceID);
})();
