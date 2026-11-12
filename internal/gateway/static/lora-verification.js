(function (root) {
  "use strict";
  const node = (tag, text, className) => { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; if (className) el.className = className; return el; };
  const icon = name => { const el = node("i"); el.dataset.lucide = name; el.setAttribute("aria-hidden", "true"); return el; };
  const button = (label, symbol, action, compact = false) => { const el = node("button", undefined, compact ? "button ghost icon-button" : "button ghost"); el.type = "button"; el.title = label; el.setAttribute("aria-label", label); el.append(icon(symbol)); if (!compact) el.append(node("span", label)); el.addEventListener("click", action); return el; };
  const field = (label, control) => { const el = node("label", undefined, "ui-field"); el.append(node("span", label, "ui-field-label"), control); return el; };
  const idOK = id => /^[A-Za-z0-9_-]{16,96}$/.test(id || "");
  const stateLabels = { draft: "В очереди", queued: "В очереди", preparing: "Подготовка", uploading: "Загрузка", waiting_for_resources: "Ожидание GPU", running: "Выполняется", postprocessing: "Обработка", archiving: "Сохранение", completed: "Готово", failed: "Ошибка", cancelled: "Отменено", expired: "Истёк срок", missing: "Результат удалён", partial: "Готово частично" };
  const request = async (url, options = {}) => {
    const timeout = AbortSignal.timeout(100000);
    const response = await fetch(url, { credentials: "same-origin", cache: "no-store", ...options, signal: options.signal ? AbortSignal.any([options.signal, timeout]) : timeout });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) { const error = new Error(data.error || "Не удалось получить проверку LoRA."); error.status = response.status; throw error; }
    return data;
  };
  const api = "/api/lora-training/verifications";
  function create({ form, root: surface, getPayload, prepareInputs, showResults, onDirty = () => {}, renderMedia, newRequestID }) {
    const panel = document.getElementById("lora-verification-setup");
    const source = form.elements.lora_verification_source, saved = form.elements.lora_verification_plan;
    const submit = document.getElementById("generation-submit");
    const positive = form.elements.positive_prompt;
    const seed = {
      get value() { return form.querySelector('[name="seed"]:not(:disabled)')?.value || "-1"; },
      set value(value) { form.querySelectorAll('[name="seed"]:not(:disabled)').forEach(input => { input.value = value; }); },
    };
    const message = panel.querySelector("[data-verification-message]");
    const casesHost = panel.querySelector("[data-verification-cases]"), weightsHost = panel.querySelector("[data-verification-weights]");
    const summary = panel.querySelector("[data-verification-summary]");
    const confirmation = document.getElementById("lora-verification-confirm");
    const comparison = document.getElementById("lora-verification-comparison");
    const compareBody = comparison.querySelector("[data-verification-results]");
    const compareStatus = comparison.querySelector("[data-verification-status]");
    const recovery = document.getElementById("lora-verification-recovery");
    const storageKey = `gateway.lora-verification.pending.${panel.dataset.owner}`;
    const fullImage = document.getElementById("lora-verification-full-image");
    let extras = [], weights = ["0.5", "1"], lastSaved = "", quote = null, busy = false, epoch = 0, editSignature = "";
    let currentBatch = "", poll = null, reading = null, viewEpoch = 0, pending = null;
    const refreshIcons = () => window.lucide?.createIcons?.();
    const active = () => Boolean(source.value);
    const savePending = value => {
      try { if (value) localStorage.setItem(storageKey, JSON.stringify(value)); else localStorage.removeItem(storageKey); }
      catch (_) { if (value) return false; }
      pending = value; recovery.hidden = !value;
      return true;
    };
    try {
      const value = JSON.parse(localStorage.getItem(storageKey) || "null");
      if (value && idOK(value.request) && idOK(value.source)) { pending = value; recovery.hidden = false; }
    } catch (_) {}
    const plan = () => ({ version: 1, cases: [{ prompt: positive.value, seed: seed.value }, ...extras], weights: [...weights] });
    const invalidate = () => { epoch++; quote = null; confirmation.querySelector('[data-verification-enqueue]').disabled = true; if (confirmation.open) confirmation.close(); };
    const changed = () => { invalidate(); sync(); onDirty(); };
    const numeric = (value, min, max, step) => { const input = node("input"); input.type = "number"; input.min = String(min); input.max = String(max); input.step = String(step); input.value = value; return input; };
    function renderEditor() {
      casesHost.replaceChildren(); weightsHost.replaceChildren();
      const firstSeed = numeric(seed.value, 0, 1125899906842624, 1); firstSeed.setAttribute("aria-label", "Seed основного промпта");
      firstSeed.addEventListener("input", () => { seed.value = firstSeed.value; changed(); });
      casesHost.append(field("Seed основного промпта", firstSeed));
      extras.forEach((item, index) => {
        const row = node("div", undefined, "lora-verification-case");
        const prompt = node("textarea"); prompt.rows = 3; prompt.maxLength = 12000; prompt.value = item.prompt;
        const value = numeric(item.seed, 0, 1125899906842624, 1);
        prompt.addEventListener("input", () => { item.prompt = prompt.value; changed(); });
        value.addEventListener("input", () => { item.seed = value.value; changed(); });
        row.append(field(`Промпт ${index + 2}`, prompt), field(`Seed ${index + 2}`, value), button(`Удалить случай ${index + 2}`, "trash-2", () => { extras.splice(index, 1); renderEditor(); changed(); }, true));
        casesHost.append(row);
      });
      weights.forEach((value, index) => {
        const row = node("div", undefined, "lora-verification-weight");
        const input = numeric(value, -4, 4, 0.05); input.setAttribute("aria-label", `Вес LoRA ${index + 1}`);
        input.addEventListener("input", () => { weights[index] = input.value; changed(); });
        row.append(field(`Вес ${index + 1}`, input));
        if (weights.length > 1) row.append(button(`Удалить вес ${index + 1}`, "minus", () => { weights.splice(index, 1); renderEditor(); changed(); }, true));
        weightsHost.append(row);
      });
      panel.querySelector('[data-verification-add-case]').disabled = extras.length >= 9;
      panel.querySelector('[data-verification-add-weight]').disabled = weights.length >= 4;
      refreshIcons();
    }
    function sync() {
      panel.hidden = !active();
      // Inactive comparison inputs must not take part in studio form validation.
      [casesHost, weightsHost].forEach(host => host.querySelectorAll("input, textarea").forEach(input => { input.disabled = !active(); }));
      const batchBuilder = document.getElementById("generation-batch-builder");
      if (batchBuilder) batchBuilder.hidden = active();
      surface.querySelectorAll('[data-studio-settings-target="lora"]').forEach(button => { button.hidden = active(); });
      if (!active()) return false;
      if (saved.value && saved.value !== lastSaved) {
        try {
          const value = JSON.parse(saved.value);
          if (value.version === 1 && Array.isArray(value.cases) && value.cases.length <= 10 && Array.isArray(value.weights) && value.weights.length <= 4) { extras = value.cases.slice(1); weights = value.weights; }
        } catch (_) {}
        renderEditor();
      }
      const batchEnabled = form.elements.batch_enabled; if (batchEnabled) batchEnabled.checked = false;
      const firstSeed = casesHost.querySelector('input[aria-label="Seed основного промпта"]');
      if (firstSeed && document.activeElement !== firstSeed) firstSeed.value = seed.value;
      const count = (extras.length + 1) * (weights.length + 1);
      saved.value = JSON.stringify(plan()); lastSaved = saved.value;
      summary.textContent = `${extras.length + 1} случаев · без LoRA и ${weights.length} весов · ${count} изображений`;
      if (submit) {
        if (submit.textContent !== "Проверить LoRA") submit.textContent = "Проверить LoRA";
        submit.disabled = busy;
      }
      return true;
    }
    function start(id, name) {
      if (!idOK(id)) return;
      source.value = id; extras = []; weights = ["0.5", "1"]; saved.value = ""; lastSaved = "";
      if (!/^\d+$/.test(seed.value) || Number(seed.value) > 1125899906842624) seed.value = "42";
      panel.querySelector('[data-verification-source-name]').textContent = name || "LoRA";
      renderEditor(); changed();
    }
    async function quotePlan() {
      if (busy || !active()) return;
      message.textContent = ""; invalidate(); busy = true; sync();
      const current = epoch;
      try {
        if (!await prepareInputs()) return;
        sync();
        const body = getPayload();
        const requestID = pending?.source === source.value ? pending.request : newRequestID();
        if (pending && pending.source !== source.value) throw new Error("Сначала уточните состояние предыдущей проверки.");
        body.set("client_request_id", requestID);
        const signature = getPayload().toString();
        editSignature = signature;
        const data = await request(`${api}/quote`, { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body });
        if (current !== epoch || signature !== getPayload().toString()) throw new Error("Настройки изменились. Подтвердите их заново.");
        if (data.source_id !== source.value || !data.confirmation || data.count !== plan().cases.length * (plan().weights.length + 1)) throw new Error("Ответ проверки не соответствует текущим параметрам.");
        quote = { body, signature, data, requestID };
        const details = confirmation.querySelector('[data-verification-quote]');
        details.replaceChildren(node("p", data.source_name), node("p", `Будет создано ${data.count} изображений. Из лимита будет списано ${data.count} генераций.`));
        const quota = data.quota?.image;
        details.append(node("p", `Остаток за день: ${quota?.daily_limit > 0 ? quota.daily_remaining : "без ограничения"}. Общий остаток: ${quota?.total_limit > 0 ? quota.total_remaining : "без ограничения"}.`, "muted"));
        details.append(node("p", `Модель: ${data.model}`, "muted"));
        const required = (data.workflow_adapters || []).filter(Boolean);
        if (required.length) details.append(node("p", `Обязательные адаптеры режима сохраняются во всех вариантах: ${required.join(", ")}`, "muted"));
        const confirm = confirmation.querySelector('[data-verification-enqueue]'); confirm.disabled = false; confirm.textContent = `Запустить ${data.count} изображений`;
        confirmation.showModal();
      } catch (error) { message.textContent = error.message || "Не удалось проверить параметры."; }
      finally { busy = false; sync(); }
    }
    async function enqueue() {
      if (!quote || busy) return;
      if (quote.signature !== getPayload().toString() || Date.now() >= quote.data.expires_at) { invalidate(); message.textContent = "Подтверждение устарело. Проверьте параметры заново."; return; }
      const current = quote; busy = true; confirmation.querySelector('[data-verification-enqueue]').disabled = true; sync();
      if (!savePending({ request: current.requestID, source: source.value })) {
        busy = false; confirmation.close(); sync();
        message.textContent = "Браузер не сохранил ID проверки. Разрешите локальное хранилище и повторите подтверждение. Задания не отправлены.";
        return;
      }
      const body = new URLSearchParams(current.body); body.set("lora_verification_confirmation", current.data.confirmation);
      try {
        const data = await request(`${api}/enqueue`, { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body });
        if (!idOK(data.batch_id)) throw new Error("Не удалось получить ID созданной проверки.");
        savePending(null); quote = null; confirmation.close(); showResults(); openComparison(data.batch_id);
      } catch (error) {
        confirmation.close();
        if (error.status && error.status < 500 && error.status !== 409) savePending(null);
        message.textContent = error.message || "Ответ потерян. Уточните состояние проверки.";
        if (pending) recover();
      } finally { busy = false; sync(); }
    }
    async function recover() {
      if (!pending) return;
      recovery.querySelector('p').textContent = "Уточняем состояние предыдущей проверки...";
      try {
        const data = await request(`${api}?${new URLSearchParams({ request: pending.request })}`);
        if (!idOK(data.batch?.batch_id)) throw new Error("Ответ проверки неполон.");
        savePending(null); showResults(); openComparison(data.batch.batch_id);
      } catch (error) { recovery.querySelector('p').textContent = error.status === 404 ? "Подтверждённого запуска пока нет. Обновите статус или повторите подтверждение с тем же ID запроса." : "Не удалось уточнить запуск. Новый запрос не отправлен."; }
    }
    function stopRead() { viewEpoch++; clearTimeout(poll); reading?.abort(); reading = null; }
    async function readComparison() {
      if (!currentBatch || !comparison.open || document.hidden) return;
      const id = currentBatch, current = ++viewEpoch, controller = new AbortController(); reading?.abort(); reading = controller;
      try {
        const data = await request(`${api}?${new URLSearchParams({ batch: id })}`, { signal: controller.signal });
        if (current !== viewEpoch || id !== currentBatch) return;
        if (data.batch?.batch_id !== id || !Array.isArray(data.plan?.cases) || !Array.isArray(data.members)) throw new Error("Ответ сравнения неполон.");
        comparison.querySelector('h2').textContent = data.source_name || "Проверка LoRA";
        compareStatus.textContent = `${stateLabels[data.batch.state] || "Выполняется"} · ${data.batch.finished_count} из ${data.batch.total_count}`;
        comparison.querySelector('[data-verification-cancel]').hidden = !data.batch.cancellable;
        const sourceLink = comparison.querySelector('[data-verification-source-link]'); sourceLink.hidden = !data.source_available || !idOK(data.source_id); if (!sourceLink.hidden) sourceLink.href = `/train-lora?job=${encodeURIComponent(data.source_id)}`;
        const fragment = document.createDocumentFragment();
        const jobs = new Map((data.batch.jobs || []).map(job => [job.job_id, job]));
        data.plan.cases.forEach((item, index) => {
          const section = node("section", undefined, "lora-verification-result-case");
          section.append(node("h3", `Случай ${index + 1} · Seed ${item.seed}`), node("p", item.prompt, "lora-verification-prompt"));
          const grid = node("div", undefined, "lora-verification-images");
          data.members.filter(member => member.case_index === index).forEach(member => {
            const figure = node("figure", undefined, "lora-verification-image");
            figure.append(node("figcaption", member.variant_index === 0 ? "Без проверяемой LoRA" : `Вес ${member.weight}`));
            const job = jobs.get(member.job_id), media = job?.media?.find(item => item.media_type === "image");
            if (media) {
              const preview = renderMedia(media);
              preview.dataset.verificationJob = member.job_id;
              preview.addEventListener("click", event => {
                event.stopImmediatePropagation();
                if (window.aiGatewaySensitiveContent?.reveal(preview)) return;
                const image = fullImage.querySelector("img"); image.src = media.url;
                fullImage.querySelector("h2").textContent = member.variant_index === 0 ? "Без проверяемой LoRA" : `Вес ${member.weight}`;
                const download = fullImage.querySelector("a"); download.href = `${media.url}?download=1`; download.download = media.filename || "result";
                fullImage.classList.remove("is-actual-size");
                fullImage.querySelector('[data-verification-image-zoom]').setAttribute("aria-pressed", "false");
                fullImage.showModal();
              }, true);
              figure.append(preview);
            }
            else figure.append(node("div", member.state === "completed" ? "Изображение больше недоступно" : stateLabels[member.state] || "Выполняется", "lora-verification-placeholder"));
            if (job?.message && member.state === "failed") figure.append(node("p", job.message, "ui-status-banner is-error"));
            if (idOK(member.job_id) && member.state !== "missing") { const link = node("a", "Задача", "text-link"); link.href = `/tasks?task=generation:${encodeURIComponent(member.job_id)}`; figure.append(link); }
            grid.append(figure);
          });
          section.append(grid); fragment.append(section);
        });
        const focusedJob = compareBody.contains(document.activeElement) ? document.activeElement.dataset.verificationJob : "";
        compareBody.replaceChildren(fragment);
        if (focusedJob) compareBody.querySelector(`[data-verification-job="${CSS.escape(focusedJob)}"]`)?.focus({ preventScroll: true });
        if (!["completed", "partial", "failed", "cancelled"].includes(data.batch.state)) poll = setTimeout(readComparison, 3000);
      } catch (error) {
        if (current !== viewEpoch || controller.signal.aborted) return;
        compareStatus.textContent = error.message || "Не удалось обновить сравнение.";
        if ([401, 403, 404].includes(error.status)) { compareBody.replaceChildren(); fullImage.close(); }
      }
    }
    function openComparison(id) {
      if (!idOK(id)) return;
      stopRead(); currentBatch = id; compareBody.replaceChildren(); compareStatus.textContent = "Загрузка сравнения...";
      const url = new URL(location.href); url.searchParams.set("lora_comparison", id); history.replaceState(null, "", url);
      if (!comparison.open) comparison.showModal(); readComparison();
    }
    function closeComparison() { stopRead(); comparison.close(); const url = new URL(location.href); url.searchParams.delete("lora_comparison"); history.replaceState(null, "", url); }
    async function openHistory(id, cursor = "0", append = false) {
      if (!idOK(id)) return;
      stopRead(); currentBatch = "";
      const current = viewEpoch, controller = new AbortController(); reading = controller;
      if (!append) compareBody.replaceChildren();
      comparison.querySelector("h2").textContent = "История проверок LoRA";
      comparison.querySelector('[data-verification-cancel]').hidden = true;
      comparison.querySelector('[data-verification-source-link]').hidden = true;
      compareStatus.textContent = "Загрузка...";
      if (!comparison.open) comparison.showModal();
      try {
        const data = await request(`${api}?${new URLSearchParams({ source: id, before: cursor })}`, { signal: controller.signal });
        if (current !== viewEpoch) return;
        if (data.source_id !== id || !Array.isArray(data.items)) throw new Error("Ответ истории неполон.");
        compareBody.querySelector('[data-verification-more]')?.remove();
        data.items.forEach(item => {
          if (!idOK(item.batch_id)) return;
          const row = node("div", undefined, "lora-verification-history-row");
          row.append(node("span", new Date(item.created_at).toLocaleString("ru-RU")), node("span", `${stateLabels[item.state] || "Выполняется"} · ${item.completed_count}/${item.total_count}`), node("span", item.model, "muted"), button("Открыть сравнение", "columns-2", () => openComparison(item.batch_id)));
          compareBody.append(row);
        });
        compareStatus.textContent = compareBody.children.length ? "Сохранённые проверки" : "Проверок пока нет";
        if (data.next_cursor && data.next_cursor !== "0") {
          const more = button("Показать ещё", "chevron-down", () => openHistory(id, data.next_cursor, true)); more.dataset.verificationMore = ""; compareBody.append(more);
        }
      } catch (error) { if (!controller.signal.aborted) compareStatus.textContent = error.message; }
    }
    panel.querySelector('[data-verification-add-case]').addEventListener("click", () => { if (extras.length < 9) extras.push({ prompt: positive.value, seed: String(43 + extras.length) }); renderEditor(); changed(); });
    panel.querySelector('[data-verification-add-weight]').addEventListener("click", () => { if (weights.length < 4) weights.push(""); renderEditor(); changed(); });
    panel.querySelector('[data-verification-exit]').addEventListener("click", () => { source.value = ""; saved.value = ""; invalidate(); sync(); onDirty(); submit.textContent = "Создать"; });
    confirmation.querySelector('[data-verification-enqueue]').addEventListener("click", enqueue);
    confirmation.querySelector('[data-verification-back]').addEventListener("click", () => confirmation.close());
    comparison.querySelector('[data-verification-close]').addEventListener("click", closeComparison);
    comparison.addEventListener("cancel", event => { event.preventDefault(); closeComparison(); });
    comparison.querySelector('[data-verification-refresh]').addEventListener("click", () => { stopRead(); readComparison(); });
    comparison.querySelector('[data-verification-cancel]').addEventListener("click", async event => {
      const button = event.currentTarget; button.disabled = true;
      try { await request("/generate/batches/cancel", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" }, body: new URLSearchParams({ csrf: form.elements.csrf.value, batch_id: currentBatch }) }); stopRead(); readComparison(); }
      catch (error) { compareStatus.textContent = error.message; }
      finally { button.disabled = false; }
    });
    recovery.querySelector('button').addEventListener("click", recover);
    fullImage.querySelector('[data-verification-image-close]').addEventListener("click", () => fullImage.close());
    fullImage.querySelector('[data-verification-image-zoom]').addEventListener("click", event => { const zoomed = fullImage.classList.toggle("is-actual-size"); event.currentTarget.setAttribute("aria-pressed", String(zoomed)); });
    fullImage.addEventListener("close", () => fullImage.querySelector("img").removeAttribute("src"));
    const formEdited = event => {
      if (panel.contains(event.target) || !active()) return;
      sync();
      const signature = getPayload().toString();
      // A delayed blur/change may repeat the input already included in the quote.
      if (signature === editSignature) return;
      editSignature = signature;
      invalidate();
    };
    form.addEventListener("input", formEdited);
    form.addEventListener("change", formEdited);
    document.addEventListener("visibilitychange", () => { stopRead(); if (!document.hidden) readComparison(); });
    window.addEventListener("pagehide", stopRead);
    window.addEventListener("pageshow", () => { if (comparison.open) readComparison(); });
    renderEditor(); sync();
    const initialBatch = new URLSearchParams(location.search).get("lora_comparison");
    const initialHistory = new URLSearchParams(location.search).get("lora_history");
    if (initialBatch) openComparison(initialBatch); else if (initialHistory) openHistory(initialHistory); else if (pending) recover();
    return { active, sync, start, quote: quotePlan, openComparison };
  }
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.loraVerification = { create };
})(window);
