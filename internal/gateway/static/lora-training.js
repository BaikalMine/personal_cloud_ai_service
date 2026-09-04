(() => {
  const page = document.querySelector("[data-lora-training-page]");
  if (!page) return;

  const form = page.querySelector("[data-lora-training-form]");
  const input = page.querySelector("[data-lora-images]");
  const dropzone = page.querySelector("[data-lora-dropzone]");
  const grid = page.querySelector("[data-lora-dataset-grid]");
  const summary = page.querySelector("[data-lora-dataset-summary]");
  const countNode = page.querySelector("[data-lora-image-count]");
  const sizeNode = page.querySelector("[data-lora-image-size]");
  const clearButton = page.querySelector("[data-lora-clear-images]");
  const submitButton = page.querySelector("[data-lora-submit]");
  const captionAssistant = page.querySelector("[data-lora-caption-assistant]");
  const captionAllButton = page.querySelector("[data-lora-caption-all]");
  const captionAssistantStatus = page.querySelector("[data-lora-caption-status]");
  const triggerInput = form?.querySelector('input[name="trigger_word"]');
  const conceptInputs = [...(form?.querySelectorAll('input[name="concept_type"]') || [])];
  const historyList = page.querySelector("[data-lora-job-list]");
  const historyCount = page.querySelector(".lora-history-heading > span");
  const csrf = page.dataset.csrf || "";
  const allowedTypes = new Set(["image/png", "image/jpeg", "image/webp"]);
  const terminalStates = new Set(["completed", "failed", "cancelled"]);
  let files = [];
  let previewURLs = [];
  let outputNameEdited = false;
  let captionRun = null;
  let previousTrigger = triggerInput?.value.trim() || "";
  const captionStates = new Map();

  const fileKey = (file) => `${file.name}:${file.size}:${file.lastModified}`;
  const formatBytes = (bytes) => {
    if (!Number.isFinite(bytes) || bytes <= 0) return "0 МБ";
    const mb = bytes / (1024 * 1024);
    return `${mb >= 10 ? mb.toFixed(0) : mb.toFixed(1)} МБ`;
  };
  const pluralImages = (count) => {
    const mod10 = count % 10;
    const mod100 = count % 100;
    if (mod10 === 1 && mod100 !== 11) return `${count} изображение`;
    if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return `${count} изображения`;
    return `${count} изображений`;
  };
  const transliterate = (value) => {
    const map = { а: "a", б: "b", в: "v", г: "g", д: "d", е: "e", ё: "e", ж: "zh", з: "z", и: "i", й: "y", к: "k", л: "l", м: "m", н: "n", о: "o", п: "p", р: "r", с: "s", т: "t", у: "u", ф: "f", х: "h", ц: "c", ч: "ch", ш: "sh", щ: "sch", ъ: "", ы: "y", ь: "", э: "e", ю: "yu", я: "ya" };
    return value.toLowerCase().split("").map((char) => map[char] ?? char).join("")
      .replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "").slice(0, 64);
  };
  const replaceLeadingTrigger = (value, before, after) => {
    const caption = value.trim();
    if (!caption || !before || !after || caption.slice(0, before.length).toLocaleLowerCase() !== before.toLocaleLowerCase()) return value;
    const boundary = caption.slice(before.length, before.length + 1);
    if (boundary && !/[\s,.;:!?-]/u.test(boundary)) return value;
    const rest = caption.slice(before.length).replace(/^[\s,.;:!?-]+/u, "");
    return rest ? `${after}, ${rest}` : after;
  };

  const syncFileInput = () => {
    const transfer = new DataTransfer();
    files.forEach((file) => transfer.items.add(file));
    input.files = transfer.files;
  };
  const collectCaptions = () => {
    const captions = new Map();
    grid?.querySelectorAll("[data-file-key]").forEach((card) => {
      captions.set(card.dataset.fileKey, card.querySelector('textarea[name="caption"]')?.value || "");
    });
    return captions;
  };
  const setAssistantMessage = (message, tone = "") => {
    if (!captionAssistantStatus) return;
    captionAssistantStatus.textContent = message;
    captionAssistantStatus.classList.remove("is-success", "is-error", "is-warning");
    if (tone) captionAssistantStatus.classList.add(`is-${tone}`);
  };
  const updateCaptionCard = (key) => {
    const card = grid?.querySelector(`[data-file-key="${CSS.escape(key)}"]`);
    if (!card) return;
    const textarea = card.querySelector('textarea[name="caption"]');
    const action = card.querySelector("[data-lora-caption-one]");
    const status = card.querySelector("[data-lora-caption-item-status]");
    const state = captionStates.get(key);
    card.classList.toggle("is-caption-loading", state?.tone === "loading");
    card.classList.toggle("is-caption-error", state?.tone === "error");
    card.classList.toggle("is-caption-success", state?.tone === "success");
    if (action) {
      action.textContent = textarea?.value.trim() ? "Описать заново" : "Описать";
      action.disabled = Boolean(captionRun);
    }
    if (status) {
      status.textContent = state?.message || (textarea?.value.trim() ? "Подпись заполнена" : "Ожидает описания");
      status.classList.remove("is-success", "is-error", "is-loading", "is-manual");
      if (state?.tone) status.classList.add(`is-${state.tone}`);
    }
  };
  const setCaptionState = (key, tone, message) => {
    captionStates.set(key, { tone, message });
    updateCaptionCard(key);
  };
  const updateAssistantControls = () => {
    const busy = Boolean(captionRun);
    captionAssistant?.setAttribute("aria-busy", String(busy));
    captionAllButton?.classList.toggle("danger", captionRun?.kind === "batch");
    if (captionAllButton) {
      captionAllButton.textContent = captionRun?.kind === "batch" ? "Остановить" : "Описать пустые";
      captionAllButton.disabled = files.length === 0 || (busy && captionRun.kind !== "batch");
    }
    clearButton && (clearButton.disabled = busy);
    if (input) input.disabled = busy;
    dropzone?.classList.toggle("is-disabled", busy);
    grid?.querySelectorAll("[data-lora-caption-one], .lora-dataset-remove").forEach((button) => { button.disabled = busy; });
    grid?.querySelectorAll("[data-file-key]").forEach((card) => updateCaptionCard(card.dataset.fileKey));
  };
  const captionMetadata = () => {
    if (!triggerInput?.checkValidity()) {
      triggerInput?.reportValidity();
      setAssistantMessage("Сначала укажите триггер длиной от 2 до 80 символов.", "error");
      return null;
    }
    const concept = conceptInputs.find((inputNode) => inputNode.checked)?.value || "";
    if (!concept) {
      setAssistantMessage("Выберите тип LoRA.", "error");
      return null;
    }
    return { trigger: triggerInput.value.trim(), concept };
  };
  const responseError = async (response) => {
    const payload = await response.json().catch(() => ({}));
    const error = new Error(payload.error || `Сервис ответил HTTP ${response.status}.`);
    error.status = response.status;
    throw error;
  };
  const waitForCaptionPoll = (signal) => new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => {
      signal?.removeEventListener("abort", abort);
      resolve();
    }, 1200);
    const abort = () => {
      window.clearTimeout(timer);
      signal.removeEventListener("abort", abort);
      reject(new DOMException("Описание остановлено", "AbortError"));
    };
    if (signal?.aborted) {
      abort();
      return;
    }
    signal?.addEventListener("abort", abort, { once: true });
  });
  const waitForCaptionJob = async (jobID, key, signal) => {
    for (;;) {
      const response = await fetch(`/api/lora-training/caption/${encodeURIComponent(jobID)}`, {
        credentials: "same-origin",
        headers: { Accept: "application/json" },
        signal,
      });
      if (!response.ok) await responseError(response);
      const payload = await response.json();
      if (payload.state === "completed") return payload;
      if (payload.state === "failed") {
        const error = new Error(payload.error || "Ассистент не смог подготовить описание.");
        error.status = 502;
        throw error;
      }
      setCaptionState(key, "loading", payload.status || "Ассистент анализирует этот кадр");
      await waitForCaptionPoll(signal);
    }
  };
  const renderFiles = (captions = collectCaptions()) => {
    previewURLs.forEach((url) => URL.revokeObjectURL(url));
    previewURLs = [];
    grid.replaceChildren();
    const total = files.reduce((sum, file) => sum + file.size, 0);
    summary.hidden = files.length === 0;
    countNode.textContent = pluralImages(files.length);
    sizeNode.textContent = formatBytes(total);
    dropzone.classList.toggle("has-files", files.length > 0);

    files.forEach((file, index) => {
      const key = fileKey(file);
      const url = URL.createObjectURL(file);
      previewURLs.push(url);
      const card = document.createElement("article");
      card.className = "lora-dataset-item";
      card.dataset.fileKey = key;
      const media = document.createElement("div");
      media.className = "lora-dataset-media";
      const image = document.createElement("img");
      image.src = url;
      image.alt = `Кадр ${index + 1}: ${file.name}`;
      const indexNode = document.createElement("span");
      indexNode.textContent = String(index + 1).padStart(2, "0");
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "lora-dataset-remove";
      remove.setAttribute("aria-label", `Убрать ${file.name}`);
      remove.title = "Убрать изображение";
      remove.textContent = "×";
      remove.addEventListener("click", () => {
        const currentCaptions = collectCaptions();
        currentCaptions.delete(key);
        captionStates.delete(key);
        files = files.filter((candidate) => fileKey(candidate) !== key);
        syncFileInput();
        renderFiles(currentCaptions);
        setAssistantMessage(files.length ? `${pluralImages(files.length)} готовы к раздельному описанию.` : "Добавьте изображения и укажите триггер.");
      });
      media.append(image, indexNode, remove);
      const body = document.createElement("div");
      body.className = "lora-dataset-body";
      const heading = document.createElement("div");
      heading.className = "lora-dataset-heading";
      const identity = document.createElement("div");
      const name = document.createElement("strong");
      name.textContent = file.name;
      name.title = file.name;
      const meta = document.createElement("small");
      meta.textContent = formatBytes(file.size);
      image.addEventListener("load", () => { meta.textContent = `${image.naturalWidth} × ${image.naturalHeight} · ${formatBytes(file.size)}`; });
      identity.append(name, meta);
      const describe = document.createElement("button");
      describe.type = "button";
      describe.className = "button ghost lora-caption-one";
      describe.dataset.loraCaptionOne = "";
      describe.addEventListener("click", () => { void runSingleCaption(file, key); });
      heading.append(identity, describe);
      const caption = document.createElement("textarea");
      caption.name = "caption";
      caption.rows = 3;
      caption.maxLength = 1000;
      caption.placeholder = "Что меняется именно в этом кадре: ракурс, одежда, фон, свет";
      caption.value = captions.get(key) || "";
      caption.setAttribute("aria-label", `Описание кадра ${index + 1}`);
      caption.addEventListener("input", () => {
        caption.setCustomValidity("");
        setCaptionState(key, "manual", "Изменено вручную");
      });
      const captionStatus = document.createElement("small");
      captionStatus.className = "lora-caption-item-status";
      captionStatus.dataset.loraCaptionItemStatus = "";
      captionStatus.setAttribute("role", "status");
      body.append(heading, caption, captionStatus);
      card.append(media, body);
      grid.append(card);
      updateCaptionCard(key);
    });
    updateAssistantControls();
  };
  const requestCaption = async (file, key, metadata, signal) => {
    setCaptionState(key, "loading", "Ассистент анализирует этот кадр");
    const requestBody = new FormData();
    requestBody.append("csrf", csrf);
    requestBody.append("trigger_word", metadata.trigger);
    requestBody.append("concept_type", metadata.concept);
    requestBody.append("image", file, file.name);
    const response = await fetch("/api/lora-training/caption", {
      method: "POST",
      body: requestBody,
      credentials: "same-origin",
      headers: { Accept: "application/json" },
      signal,
    });
    if (!response.ok) await responseError(response);
    let payload = await response.json();
    if (response.status === 202) {
      const jobID = typeof payload.job_id === "string" ? payload.job_id : "";
      if (!jobID) throw new Error("Сервис не вернул идентификатор задачи описания.");
      setCaptionState(key, "loading", payload.status || "Описание поставлено в очередь ассистента");
      payload = await waitForCaptionJob(jobID, key, signal);
    }
    const captionValue = typeof payload.caption === "string" ? payload.caption.trim() : "";
    if (!captionValue) throw new Error("Ассистент вернул пустое описание.");
    const card = grid?.querySelector(`[data-file-key="${CSS.escape(key)}"]`);
    const textarea = card?.querySelector('textarea[name="caption"]');
    if (!textarea) throw new Error("Карточка изображения больше недоступна.");
    textarea.value = captionValue;
    textarea.setCustomValidity("");
    setCaptionState(key, "success", "Описание готово, его можно изменить");
    return payload;
  };
  const runSingleCaption = async (file, key) => {
    if (captionRun) return;
    const metadata = captionMetadata();
    if (!metadata) return;
    const controller = new AbortController();
    captionRun = { kind: "single", controller };
    updateAssistantControls();
    setAssistantMessage(`Описываем ${file.name} отдельно от остальных кадров.`);
    try {
      const payload = await requestCaption(file, key, metadata, controller.signal);
      setAssistantMessage(payload.warning || "Описание кадра готово.", payload.warning ? "warning" : "success");
    } catch (error) {
      if (error.name === "AbortError") {
        setCaptionState(key, "manual", "Описание остановлено");
        setAssistantMessage("Описание остановлено.", "warning");
      } else {
        setCaptionState(key, "error", error.message || "Не удалось описать кадр");
        setAssistantMessage(error.message || "Не удалось описать кадр.", "error");
      }
    } finally {
      captionRun = null;
      updateAssistantControls();
    }
  };
  const runBatchCaptions = async () => {
    if (captionRun) return;
    const metadata = captionMetadata();
    if (!metadata) return;
    const captions = collectCaptions();
    const targets = files.filter((file) => !(captions.get(fileKey(file)) || "").trim());
    if (!targets.length) {
      setAssistantMessage("Пустых подписей нет. Для замены используйте кнопку в нужной карточке.", "success");
      return;
    }
    const controller = new AbortController();
    captionRun = { kind: "batch", controller };
    updateAssistantControls();
    let completed = 0;
    let failed = 0;
    let stoppedByService = false;
    for (let index = 0; index < targets.length; index += 1) {
      const file = targets[index];
      const key = fileKey(file);
      setAssistantMessage(`Кадр ${index + 1} из ${targets.length}: ${file.name}. В модель отправлен только этот кадр.`);
      try {
        await requestCaption(file, key, metadata, controller.signal);
        completed += 1;
      } catch (error) {
        if (error.name === "AbortError") {
          setCaptionState(key, "manual", "Описание остановлено");
          break;
        }
        failed += 1;
        setCaptionState(key, "error", error.message || "Не удалось описать кадр");
        if (error.status === 429 || error.status >= 500) {
          stoppedByService = true;
          break;
        }
      }
    }
    const aborted = controller.signal.aborted;
    captionRun = null;
    updateAssistantControls();
    if (aborted) {
      setAssistantMessage(`Остановлено. Готово ${completed} из ${targets.length}.`, "warning");
    } else if (stoppedByService) {
      setAssistantMessage(`Сервис временно недоступен. Готово ${completed}, ошибок ${failed}; остальные кадры не отправлялись.`, "error");
    } else if (failed) {
      setAssistantMessage(`Готово ${completed}, ошибок ${failed}. Ошибочные кадры можно повторить отдельно.`, "warning");
    } else {
      setAssistantMessage(`Готово ${completed}. Каждый кадр был обработан отдельным запросом.`, "success");
    }
  };
  const addFiles = (incoming) => {
    if (captionRun) return;
    const captions = collectCaptions();
    const byKey = new Map(files.map((file) => [fileKey(file), file]));
    let rejected = "";
    [...incoming].forEach((file) => {
      if (!allowedTypes.has(file.type)) rejected ||= "Поддерживаются только PNG, JPG и WebP.";
      else if (file.size > 24 * 1024 * 1024) rejected ||= `${file.name}: файл больше 24 МБ.`;
      else byKey.set(fileKey(file), file);
    });
    const next = [...byKey.values()].slice(0, 100);
    const total = next.reduce((sum, file) => sum + file.size, 0);
    if (total > 512 * 1024 * 1024) rejected ||= "Общий размер датасета превышает 512 МБ.";
    else files = next;
    if (byKey.size > 100) rejected ||= "Можно добавить не более 100 изображений.";
    input.setCustomValidity(rejected);
    if (rejected) input.reportValidity();
    else input.setCustomValidity("");
    syncFileInput();
    renderFiles(captions);
    if (files.length) setAssistantMessage(`${pluralImages(files.length)} готовы к раздельному описанию.`);
  };

  if (input && grid && summary && dropzone) {
    input.addEventListener("change", () => addFiles(input.files));
    ["dragenter", "dragover"].forEach((eventName) => dropzone.addEventListener(eventName, (event) => {
      event.preventDefault();
      dropzone.classList.add("is-dragging");
    }));
    ["dragleave", "drop"].forEach((eventName) => dropzone.addEventListener(eventName, (event) => {
      event.preventDefault();
      dropzone.classList.remove("is-dragging");
    }));
    dropzone.addEventListener("drop", (event) => addFiles(event.dataTransfer?.files || []));
    clearButton?.addEventListener("click", () => {
      files = [];
      captionStates.clear();
      input.setCustomValidity("");
      syncFileInput();
      renderFiles(new Map());
      setAssistantMessage("Добавьте изображения и укажите триггер.");
    });
  }

  captionAllButton?.addEventListener("click", () => {
    if (captionRun?.kind === "batch") {
      captionRun.controller.abort();
      return;
    }
    void runBatchCaptions();
  });
  triggerInput?.addEventListener("change", () => {
    const nextTrigger = triggerInput.value.trim();
    if (previousTrigger && nextTrigger && previousTrigger !== nextTrigger) {
      grid?.querySelectorAll('textarea[name="caption"]').forEach((caption) => {
        caption.value = replaceLeadingTrigger(caption.value, previousTrigger, nextTrigger);
      });
    }
    previousTrigger = nextTrigger;
  });

  const nameInput = form?.querySelector('input[name="name"]');
  const outputInput = form?.querySelector('input[name="output_name"]');
  outputInput?.addEventListener("input", () => { outputNameEdited = outputInput.value.trim() !== ""; });
  nameInput?.addEventListener("input", () => {
    if (!outputNameEdited && outputInput) outputInput.value = transliterate(nameInput.value);
  });
  form?.addEventListener("submit", (event) => {
    if (captionRun) {
      event.preventDefault();
      setAssistantMessage("Дождитесь окончания описания или остановите очередь.", "warning");
      return;
    }
    const globalCaption = form.querySelector('textarea[name="global_caption"]')?.value.trim() || "";
    const captions = [...form.querySelectorAll('textarea[name="caption"]')];
    if (files.length < 5 || files.length > 100) {
      event.preventDefault();
      input.setCustomValidity("Добавьте от 5 до 100 изображений.");
      input.reportValidity();
      return;
    }
    input.setCustomValidity("");
    if (!globalCaption && captions.some((caption) => !caption.value.trim())) {
      event.preventDefault();
      const target = captions.find((caption) => !caption.value.trim());
      target?.setCustomValidity("Заполните это описание или добавьте общее описание датасета.");
      target?.reportValidity();
      target?.addEventListener("input", () => target.setCustomValidity(""), { once: true });
      return;
    }
    submitButton.disabled = true;
    submitButton.textContent = "Загружаем датасет…";
    form.classList.add("is-submitting");
  });

  const setAction = (container, selector, enabled, kind, job) => {
    let node = container.querySelector(selector);
    if (!enabled) {
      node?.remove();
      return;
    }
    if (node) return;
    if (kind === "download") {
      node = document.createElement("a");
      node.className = "button";
      node.dataset.jobDownload = "";
      node.href = job.download_url;
      node.textContent = "Скачать LoRA";
    } else {
      node = document.createElement("form");
      node.dataset.jobCancel = "";
      node.method = "post";
      node.action = job.cancel_url;
      const token = document.createElement("input");
      token.type = "hidden";
      token.name = "csrf";
      token.value = csrf;
      const button = document.createElement("button");
      button.type = "submit";
      button.className = "danger";
      button.dataset.confirm = "Остановить обучение LoRA?";
      button.textContent = "Отменить";
      node.append(token, button);
    }
    container.append(node);
  };
  const updateCard = (card, job) => {
    const previousState = card.dataset.jobState || "";
    card.dataset.jobState = job.state;
    card.classList.remove("is-active", "is-complete", "is-error", "is-muted");
    card.classList.add(job.state_class || "is-active");
    const state = card.querySelector(".lora-job-state");
    if (state) state.textContent = job.state_label;
    const stage = card.querySelector("[data-job-stage]");
    const progress = card.querySelector("[data-job-progress]");
    const progressLabel = card.querySelector("[data-job-progress-label]");
    const message = card.querySelector("[data-job-message]");
    const error = card.querySelector("[data-job-error]");
    const value = Math.max(0, Math.min(100, Number(job.progress) || 0));
    if (stage) stage.textContent = job.stage || job.state_label;
    if (progress) progress.value = value;
    if (progressLabel) progressLabel.textContent = `${value}%`;
    if (message) message.textContent = job.message || "";
    if (error) {
      error.textContent = job.error || "";
      error.hidden = !job.error;
    }
    const actions = card.querySelector("footer > div");
    if (actions) {
      setAction(actions, "[data-job-cancel], form", job.can_cancel, "cancel", job);
      setAction(actions, "[data-job-download], a", job.can_download, "download", job);
    }
    const logWrap = card.querySelector("[data-job-log-wrap]");
    const log = card.querySelector("[data-job-log]");
    if (logWrap && Array.isArray(job.log_tail) && job.log_tail.length) {
      logWrap.hidden = false;
      log.textContent = job.log_tail.join("\n");
      log.scrollTop = log.scrollHeight;
    }
    if (previousState && previousState !== job.state) {
      card.classList.add("state-changed");
      window.setTimeout(() => card.classList.remove("state-changed"), 700);
    }
  };
  const buildCard = (job) => {
    const card = document.createElement("article");
    card.className = `lora-job ${job.state_class || "is-active"}`;
    card.dataset.loraJob = "";
    card.dataset.jobId = job.id;
    card.dataset.jobState = job.state;
    const header = document.createElement("header");
    const identity = document.createElement("div");
    const family = document.createElement("span"); family.className = "lora-job-family"; family.textContent = job.family_label;
    const title = document.createElement("h3"); title.textContent = job.name;
    const model = document.createElement("small"); model.textContent = job.base_model;
    identity.append(family, title, model);
    const state = document.createElement("span"); state.className = "badge lora-job-state"; state.textContent = job.state_label;
    header.append(identity, state);
    const facts = document.createElement("div"); facts.className = "lora-job-facts";
    [job.concept_label, `${job.sample_count} фото`, `${job.resolution} px`, job.preset_label, `${job.max_train_steps} шагов`].forEach((value) => {
      const node = document.createElement("span"); node.textContent = value; facts.append(node);
    });
    const progress = document.createElement("div"); progress.className = "lora-job-progress";
    const progressHead = document.createElement("div");
    const stage = document.createElement("span"); stage.dataset.jobStage = "";
    const percent = document.createElement("b"); percent.dataset.jobProgressLabel = "";
    progressHead.append(stage, percent);
    const bar = document.createElement("progress"); bar.max = 100; bar.dataset.jobProgress = "";
    const message = document.createElement("p"); message.dataset.jobMessage = "";
    progress.append(progressHead, bar, message);
    const error = document.createElement("p"); error.className = "lora-job-error"; error.dataset.jobError = ""; error.hidden = true;
    const logWrap = document.createElement("details"); logWrap.className = "lora-job-log"; logWrap.dataset.jobLogWrap = ""; logWrap.hidden = true;
    const logSummary = document.createElement("summary"); logSummary.textContent = "Технический журнал";
    const log = document.createElement("pre"); log.dataset.jobLog = "";
    logWrap.append(logSummary, log);
    const footer = document.createElement("footer");
    const created = document.createElement("span"); created.textContent = `Создано ${new Date(job.created_at).toLocaleString("ru-RU")}`;
    const actions = document.createElement("div");
    footer.append(created, actions);
    card.append(header, facts, progress, error, logWrap, footer);
    updateCard(card, job);
    return card;
  };
  const refreshJobs = async () => {
    if (!historyList || document.hidden) return;
    try {
      const response = await fetch("/api/lora-training/jobs", { headers: { Accept: "application/json" }, credentials: "same-origin", cache: "no-store" });
      if (!response.ok) throw new Error("status");
      const payload = await response.json();
      const jobs = Array.isArray(payload.jobs) ? payload.jobs : [];
      if (historyCount) historyCount.textContent = String(jobs.length);
      if (jobs.length) historyList.querySelector(".lora-history-empty")?.remove();
      for (const summaryJob of jobs) {
        let card = historyList.querySelector(`[data-job-id="${CSS.escape(summaryJob.id)}"]`);
        if (!card) {
          card = buildCard(summaryJob);
          historyList.prepend(card);
        }
        let job = summaryJob;
        if (!terminalStates.has(summaryJob.state) && summaryJob.state !== "queued") {
          const detailResponse = await fetch(`/api/lora-training/jobs/${encodeURIComponent(summaryJob.id)}`, { headers: { Accept: "application/json" }, credentials: "same-origin", cache: "no-store" });
          if (detailResponse.ok) job = await detailResponse.json();
        }
        updateCard(card, job);
      }
      page.classList.remove("is-poll-stale");
    } catch (_) {
      page.classList.add("is-poll-stale");
    }
  };

  if (historyList) {
    refreshJobs();
    const pollTimer = window.setInterval(refreshJobs, 3000);
    document.addEventListener("visibilitychange", () => { if (!document.hidden) refreshJobs(); });
    window.addEventListener("beforeunload", () => window.clearInterval(pollTimer), { once: true });
  }
})();
