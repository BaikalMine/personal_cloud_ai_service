(() => {
  "use strict";
  const dialog = document.getElementById("generation-postprocess"), api = window.AIGatewayGeneration;
  if (!dialog?.dataset.owner || !api?.createPostprocessController) return;
  const q = name => dialog.querySelector(`[data-pp-${name}]`);
  const key = `gateway.generation-postprocess.pending.${dialog.dataset.owner}`;
  const element = (tag, text) => { const node = document.createElement(tag); if (text !== undefined) node.textContent = text; return node; };
  const option = (host, value, label) => { const node = element("option", label); node.value = value; host.append(node); };
  const number = value => Number(value).toLocaleString("ru-RU", { maximumFractionDigits: 3 });
  const dimensions = value => `${value.width} × ${value.height}`;
  const fps = value => value.fps_denominator > 0 ? value.fps_numerator / value.fps_denominator : 0;
  const identity = source => ({ source_job_id: source.jobID, source_media_id: source.mediaID });
  const trap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: dialog, documentRef: document });
  const qualityLabels = { LOW: "Низкое", MEDIUM: "Среднее", HIGH: "Высокое", ULTRA: "Максимальное" };
  let source = null, opener = null, available = null, availableKey = "", sizes = [], motions = [], colors = [];
  let references = [], nextReference = "", referenceEpoch = 0, referenceAbort = null, referenceLoaded = false;
  let mediaTimer = null, accessLost = false, notifiedID = "", lastState = null;
  const reveals = new Set();
  async function request(endpoint, value, signal) {
    const controller = new AbortController(), timeout = setTimeout(() => controller.abort(), 130000);
    const abort = () => controller.abort(); signal?.addEventListener("abort", abort, { once: true }); if (signal?.aborted) controller.abort();
    const read = ["status", "references"].includes(endpoint), body = new URLSearchParams();
    for (const [name, item] of Object.entries(value || {})) {
      if (name === "options") { for (const [field, option] of Object.entries(item)) if (option !== "" && option !== undefined) body.set(field, option); }
      else if (item !== "" && item !== undefined) body.set(name, item);
    }
    if (!read) body.set("csrf", dialog.dataset.csrf);
    try {
      const response = await fetch(`/generate/postprocess/${endpoint}${read ? `?${body}` : ""}`, { method: read ? "GET" : "POST", body: read ? undefined : body,
        credentials: "same-origin", cache: "no-store", signal: controller.signal, headers: { Accept: "application/json", ...(!read ? { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" } : {}) } });
      const data = await response.json().catch(() => ({}));
      if (!response.ok || response.redirected) {
        if (response.redirected || [401, 403, 404].includes(response.status)) { accessLost = true; clearMedia(); }
        throw Object.assign(Error(data.error || "Не удалось получить ответ сервера."), { status: response.redirected ? 401 : response.status });
      }
      return data;
    } finally { clearTimeout(timeout); signal?.removeEventListener("abort", abort); }
  }
  function clearHost(host) {
    host.querySelectorAll("video").forEach(video => { video.pause(); video.removeAttribute("src"); video.load(); });
    host.querySelectorAll("img").forEach(img => img.removeAttribute("src")); host.replaceChildren();
  }
  function clearMedia() { clearTimeout(mediaTimer); mediaTimer = null; clearHost(q("media")); clearHost(q("reference-preview")); }
  function preview(host, item, prefix) {
    clearHost(host);
    if (!dialog.open || document.hidden) return;
    if (!item || accessLost) { host.append(element("p", "Предпросмотр недоступен")); return; }
    if (item.expiresAt > 0 && item.expiresAt <= Date.now()) { host.append(element("p", "Срок хранения файла истёк")); return; }
    if (item.expiresAt > 0) {
      const delay = Math.min(2147483647, Math.max(1, item.expiresAt - Date.now()));
      // One timer refreshes both media slots at the earliest retention deadline.
      const remaining = [source?.expiresAt, ...references.filter(r => r.mediaID === q("reference").value).map(r => r.expiresAt)].filter(value => value > Date.now()).map(value => value - Date.now());
      clearTimeout(mediaTimer); mediaTimer = setTimeout(renderMedia, Math.min(delay, ...remaining));
    }
    const revealed = reveals.has(prefix + item.mediaID) || item.card?.classList.contains("is-revealed") || document.body.classList.contains("show-sensitive-content");
    if (item.sensitive && !revealed) {
      const reveal = element("button", "Показать файл 18+"); reveal.className = "ghost"; reveal.type = "button";
      reveal.addEventListener("click", () => { reveals.add(prefix + item.mediaID); renderMedia(); }); host.append(reveal); return;
    }
    let url;
    try { url = new URL(item.url, location.origin); if (url.origin !== location.origin || !["http:", "https:"].includes(url.protocol)) throw Error(); }
    catch (_) { host.append(element("p", "Адрес файла недоступен")); return; }
    const media = element(item.type === "video" ? "video" : "img");
    if (media instanceof HTMLVideoElement) { media.controls = true; media.playsInline = true; media.preload = "metadata"; }
    else media.alt = item.filename || "Исходный файл";
    media.addEventListener("error", () => { if (host.contains(media)) { clearHost(host); host.append(element("p", "Не удалось загрузить файл")); } }, { once: true });
    media.src = url.href; host.append(media);
  }
  function renderMedia() {
    clearTimeout(mediaTimer); mediaTimer = null;
    preview(q("media"), source, "source:");
    const reference = q("color").checked && !q("form").hidden ? references.find(item => item.mediaID === q("reference").value) : null;
    q("reference-preview").hidden = !reference;
    q("reference-name").hidden = !reference; q("reference-name").textContent = reference?.filename || "";
    if (reference) preview(q("reference-preview"), reference, "reference:"); else clearHost(q("reference-preview"));
    updateSelection();
  }
  function cancelReferences() { referenceEpoch++; referenceAbort?.abort(); referenceAbort = null; }
  async function loadReferences(append = false) {
    if (!dialog.open || document.hidden || !q("color").checked || !lastState?.canQuote) return;
    cancelReferences(); const version = referenceEpoch; referenceAbort = new AbortController();
    q("reference-status").textContent = "Ищем эталоны..."; q("reference-find").disabled = true; q("reference-more").disabled = true;
    try {
      const data = await request("references", { exclude: lastState.current.source_media_id, search: q("reference-search").value.trim(), before: append ? nextReference : "" }, referenceAbort.signal);
      if (version !== referenceEpoch) return;
      if (!Array.isArray(data.items) || data.items.length > 50 || typeof data.next !== "string" || data.next && !api.validPostprocessMediaID(data.next)) throw Error("Список эталонов не подтверждён.");
      const items = data.items.map(item => {
        if (!api.validPostprocessMediaID(item.media_id) || typeof item.job_id !== "string" || !/^[A-Za-z0-9_-]{16,96}$/.test(item.job_id) || item.media_id === lastState.current.source_media_id) throw Error("Эталон не подтверждён.");
        return { mediaID: item.media_id, jobID: item.job_id, filename: item.filename, url: item.url, sensitive: item.sensitive !== false, type: "image", expiresAt: Date.parse(item.expires_at) };
      });
      const selected = q("reference").value;
      references = [...new Map([...(append ? references : []), ...items].map(item => [item.mediaID, item])).values()]; nextReference = data.next; referenceLoaded = true;
      q("reference").replaceChildren(); option(q("reference"), "", "Выберите изображение");
      for (const item of references) option(q("reference"), item.mediaID, `${item.filename || "Изображение"}${item.sensitive ? " · 18+" : ""}`);
      if (references.some(item => item.mediaID === selected)) q("reference").value = selected;
      q("reference-status").textContent = references.length ? `Найдено: ${references.length}` : "Подходящих изображений нет";
      q("reference-more").hidden = !nextReference; renderMedia(); updateSelection();
    } catch (err) { if (version === referenceEpoch) q("reference-status").textContent = err.message || "Не удалось загрузить эталоны"; }
    finally { if (version === referenceEpoch) { referenceAbort = null; q("reference-find").disabled = false; q("reference-more").disabled = false; } }
  }
  function sizeKey(o) { return `${o.width}x${o.height}`; }
  function populateOptions(value) {
    available = value; const measured = value.source.measurement;
    sizes = value.upscale; motions = [...new Map(value.interpolation.map(item => [item.options.interpolate, item])).values()]; colors = value.color;
    q("measured").textContent = `Измерено: ${dimensions(measured)}${fps(measured) ? ` · ${number(fps(measured))} FPS` : ""}`;
    q("size").replaceChildren(); option(q("size"), "", `Без увеличения · ${dimensions(measured)}`);
    for (const item of [...new Map(sizes.map(item => [sizeKey(item.options), item])).values()]) option(q("size"), sizeKey(item.options), dimensions(item.options));
    q("motion").replaceChildren(); option(q("motion"), "", fps(measured) ? `Без изменений · ${number(fps(measured))} FPS` : "Не требуется для изображения");
    q("motion").closest("section").hidden = !fps(measured);
    for (const [index, item] of motions.entries()) option(q("motion"), String(index), `${number(fps(measured) * item.options.interpolate)} FPS · ${item.options.interpolate}×`);
    q("method").replaceChildren(); for (const [index, item] of colors.entries()) option(q("method"), String(index), item.options.color_method === "adain" ? "Адаптивный" : "Средние тона");
    for (const [name, field, reason, enabled] of [["size", "size", "upscale", sizes.length], ["motion", "motion", "interpolation", motions.length], ["color", "color", "color", colors.length]]) {
      q(field).disabled = !enabled; q(`${name}-reason`).hidden = !!enabled; q(`${name}-reason`).textContent = value.reasons?.[reason] || "Операция недоступна";
    }
    q("color").checked = false; q("strength").value = "70"; references = []; nextReference = ""; referenceLoaded = false;
    q("reference").replaceChildren(); option(q("reference"), "", "Выберите изображение"); q("reference-search").value = ""; q("reference-status").textContent = ""; q("reference-more").hidden = true;
    updateQualities(); updateSelection(); renderMedia();
  }
  function selectedMotion() { return q("motion").value === "" ? null : motions[Number(q("motion").value)]; }
  function selectedColor() { return q("color").checked ? colors[Number(q("method").value)] : null; }
  function updateQualities() {
    const previous = q("quality").value; q("quality").replaceChildren();
    for (const item of sizes.filter(item => sizeKey(item.options) === q("size").value)) option(q("quality"), item.options.rtx_quality, qualityLabels[item.options.rtx_quality]);
    if ([...q("quality").options].some(item => item.value === previous)) q("quality").value = previous;
    q("quality-field").hidden = !q("size").value;
  }
  function draft() {
    if (!available || !lastState?.current || accessLost || source?.expiresAt > 0 && source.expiresAt <= Date.now()) return null;
    const measured = available.source.measurement, size = sizes.find(item => sizeKey(item.options) === q("size").value && item.options.rtx_quality === q("quality").value);
    const motion = selectedMotion(), color = selectedColor();
    if (!size && !motion && !color) return null;
    const options = { width: size?.options.width || measured.width, height: size?.options.height || measured.height, interpolate: motion?.options.interpolate || 1 };
    if (size) options.rtx_quality = size.options.rtx_quality;
    if (motion) options.rife_checkpoint = motion.options.rife_checkpoint;
    const result = { source_job_id: lastState.current.source_job_id, source_media_id: lastState.current.source_media_id, options };
    if (color) {
      const reference = references.find(item => item.mediaID === q("reference").value);
      if (!reference || reference.expiresAt <= Date.now()) return null;
      options.color_method = color.options.color_method; options.color_strength = Number(q("strength").value) / 100;
      result.reference_job_id = reference.jobID; result.reference_media_id = reference.mediaID;
    }
    return result;
  }
  function updateSelection() {
    q("color-fields").hidden = !q("color").checked; q("strength-value").textContent = `${q("strength").value}%`;
    q("calculate").disabled = !lastState?.canQuote || !draft();
  }
  function dependencies() {
    const required = [selectedMotion(), selectedColor()].find(item => item?.requires_upscale);
    if (required && !q("size").value) { q("size").value = sizeKey(required.options); updateQualities(); q("quality").value = required.options.rtx_quality; }
    updateSelection();
  }
  function renderQuote(view) {
    const host = q("facts"); host.replaceChildren(); const o = view.plan.options;
    const entries = [["Исходный файл", dimensions(view.sources[0])], ["Размер (задано)", dimensions(o)]];
    if (fps(view.plan)) entries.push(["Частота (задано)", `${number(fps(view.plan))} FPS`]);
    if (o.rtx_quality) entries.push(["Качество увеличения", qualityLabels[o.rtx_quality]]);
    if (o.color_method) entries.push(["Перенос цвета", `${o.color_method === "adain" ? "Адаптивный" : "Средние тона"}, ${number(o.color_strength * 100)}%`]);
    for (const [label, value] of entries) host.append(element("dt", label), element("dd", value));
    q("cost").textContent = `Из лимита запусков: ${view.cost} ${view.plan.media_type === "video" ? "видео" : "изображение"}.`;
    q("expiry").textContent = `Подтверждение до ${new Date(view.expires_at).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit", second: "2-digit" })}`;
    q("measured").textContent = `Измерено: ${dimensions(view.sources[0])}${fps(view.sources[0]) ? ` · ${number(fps(view.sources[0]))} FPS` : ""}`;
  }
  function render(state) {
    const focused = document.activeElement; lastState = state; dialog.dataset.phase = state.phase;
    const messages = { loading: "Проверяем исходный файл и доступные операции...", editing: "", quoting: "Измеряем файлы и рассчитываем обработку...", quoted: "Проверьте новый результат и подтвердите обработку.",
      confirming: "Сохраняем подтверждённую обработку...", recovering: "Уточняем сохранённый запрос...", discarding: "Отменяем расчёт...", uncertain: "Ответ сервера не подтверждён.", expired: "Срок расчёта истёк. Отмените его перед новым расчётом.",
      cancelled: "Расчёт отменён. Можно изменить параметры и рассчитать заново.", paused: "Проверка приостановлена.", storage_error: "Не удалось сохранить запрос в браузере. Разрешите хранение данных сайта и обновите страницу." };
    q("message").textContent = state.error || messages[state.phase] || "";
    if (state.phase === "accepted") q("message").textContent = state.receipt.removed ? "Обработка уже была создана. Задача удалена; новый запуск не отправлен." : state.receipt.publication_pending ? "Обработка принята. Подготовка очереди продолжится автоматически." : "Обработка принята. Её состояние доступно в задачах.";
    q("form").hidden = !state.options || !!state.quote || state.phase === "accepted";
    q("fields").disabled = !state.canQuote;
    q("quote").hidden = !state.quote;
    if (state.quote) renderQuote(state.quote);
    q("calculate").hidden = !state.canQuote;
    q("confirm").hidden = state.phase !== "quoted"; q("confirm").disabled = !state.canConfirm;
    q("recover").hidden = !state.pending || !["uncertain", "recovering", "expired"].includes(state.phase);
    q("discard").hidden = !state.pending; q("discard").textContent = state.phase === "quoted" ? "Изменить параметры" : "Отменить расчёт";
    q("reload").hidden = !["error", "paused", "cancelled"].includes(state.phase) || state.pending;
    for (const name of ["recover", "discard", "reload"]) q(name).disabled = state.busy;
    q("task").hidden = state.phase !== "accepted" || !!state.receipt?.removed;
    if (!q("task").hidden) q("task").href = `/tasks?${new URLSearchParams({ task: `generation:${state.receipt.job_id}` })}`; else q("task").removeAttribute("href");
    const nextKey = state.options ? `${state.options.source.job_id}:${state.options.source.media_id}:${state.options.checked_at}` : "";
    if (state.options && nextKey !== availableKey) { availableKey = nextKey; populateOptions(state.options); }
    updateSelection();
    if (state.phase === "accepted" && notifiedID !== state.receipt.job_id) { notifiedID = state.receipt.job_id; document.dispatchEvent(new CustomEvent("generation-postprocess-accepted", { detail: state.receipt })); }
    if (dialog.open && dialog.contains(focused) && focused.closest("[hidden]")) q("message").focus({ preventScroll: true });
  }
  const controller = api.createPostprocessController({
    options: (value, signal) => request("options", { source_job_id: value.source_job_id, source_media_id: value.source_media_id }, signal), quote: value => request("quote", value),
    confirm: value => request("confirm", value), status: value => request("status", value), discard: value => request("discard", value), onState: render,
    newID: () => { const bytes = new Uint8Array(18); crypto.getRandomValues(bytes); return Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join(""); },
    storage: { load: () => JSON.parse(sessionStorage.getItem(key) || "null"), save: value => sessionStorage.setItem(key, JSON.stringify(value)), clear: () => sessionStorage.removeItem(key) },
  });
  function open(value, trigger) {
    const state = controller.snapshot(), current = state.pending ? state.current : value && identity(value);
    if (!current) return;
    cancelReferences();
    opener = trigger || document.activeElement;
    source = value && value.jobID === current.source_job_id && value.mediaID === current.source_media_id ? value : Array.from(document.querySelectorAll("[data-gallery-item]")).map(card => window.AIGatewayGallery?.cardSource?.(card)).find(item => item?.jobID === current.source_job_id && item?.mediaID === current.source_media_id) || null;
    q("filename").textContent = source?.filename || "Сохранённый исходник"; q("measured").textContent = "";
    accessLost = false; reveals.clear(); availableKey = "";
    if (!dialog.open) dialog.showModal(); trap?.activate({ trigger: opener, initialFocus: dialog.querySelector("h2"), onEscape: () => dialog.close() });
    if (!trap) dialog.querySelector("h2").focus({ preventScroll: true });
    renderMedia(); void controller.open(current);
  }
  api.openPostprocess = open;
  q("form").addEventListener("submit", event => event.preventDefault());
  q("size").addEventListener("change", () => { updateQualities(); dependencies(); }); q("quality").addEventListener("change", updateSelection);
  q("motion").addEventListener("change", dependencies); q("method").addEventListener("change", dependencies); q("strength").addEventListener("input", updateSelection);
  q("color").addEventListener("change", () => { dependencies(); if (q("color").checked && !referenceLoaded) void loadReferences(); else if (!q("color").checked) cancelReferences(); renderMedia(); });
  q("reference").addEventListener("change", () => { updateSelection(); renderMedia(); });
  q("reference-find").addEventListener("click", () => loadReferences()); q("reference-more").addEventListener("click", () => loadReferences(true));
  q("reference-search").addEventListener("keydown", event => { if (event.key === "Enter") { event.preventDefault(); void loadReferences(); } });
  q("calculate").addEventListener("click", () => { const value = draft(); if (value) { cancelReferences(); void controller.calculate(value); } });
  q("confirm").addEventListener("click", controller.confirm); q("recover").addEventListener("click", controller.recover);
  q("discard").addEventListener("click", async () => { await controller.discard(); if (controller.snapshot().phase === "cancelled" && dialog.open) { availableKey = ""; await controller.loadOptions(); } });
  q("reload").addEventListener("click", () => { accessLost = false; availableKey = ""; void controller.loadOptions(); });
  q("close").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => { controller.setVisible(false); cancelReferences(); clearMedia(); trap?.deactivate({ restore: false }); if (opener?.isConnected) opener.focus({ preventScroll: true }); });
  document.addEventListener("visibilitychange", () => { controller.setVisible(dialog.open && !document.hidden); if (document.hidden) { cancelReferences(); clearMedia(); } else renderMedia(); });
  if (controller.snapshot().pending) {
    if (document.readyState === "complete") open(null);
    else document.addEventListener("DOMContentLoaded", () => open(null), { once: true });
  }
})();
