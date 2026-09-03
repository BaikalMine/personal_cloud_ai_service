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
  const historyList = page.querySelector("[data-lora-job-list]");
  const historyCount = page.querySelector(".lora-history-heading > span");
  const csrf = page.dataset.csrf || "";
  const allowedTypes = new Set(["image/png", "image/jpeg", "image/webp"]);
  const terminalStates = new Set(["completed", "failed", "cancelled"]);
  let files = [];
  let previewURLs = [];
  let outputNameEdited = false;

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
        files = files.filter((candidate) => fileKey(candidate) !== key);
        syncFileInput();
        renderFiles(currentCaptions);
      });
      media.append(image, indexNode, remove);
      const body = document.createElement("div");
      body.className = "lora-dataset-body";
      const heading = document.createElement("div");
      const name = document.createElement("strong");
      name.textContent = file.name;
      name.title = file.name;
      const meta = document.createElement("small");
      meta.textContent = formatBytes(file.size);
      image.addEventListener("load", () => { meta.textContent = `${image.naturalWidth} × ${image.naturalHeight} · ${formatBytes(file.size)}`; });
      heading.append(name, meta);
      const caption = document.createElement("textarea");
      caption.name = "caption";
      caption.rows = 3;
      caption.maxLength = 1000;
      caption.placeholder = "Что меняется именно в этом кадре: ракурс, одежда, фон, свет";
      caption.value = captions.get(key) || "";
      caption.setAttribute("aria-label", `Описание кадра ${index + 1}`);
      body.append(heading, caption);
      card.append(media, body);
      grid.append(card);
    });
  };
  const addFiles = (incoming) => {
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
      input.setCustomValidity("");
      syncFileInput();
      renderFiles(new Map());
    });
  }

  const nameInput = form?.querySelector('input[name="name"]');
  const outputInput = form?.querySelector('input[name="output_name"]');
  outputInput?.addEventListener("input", () => { outputNameEdited = outputInput.value.trim() !== ""; });
  nameInput?.addEventListener("input", () => {
    if (!outputNameEdited && outputInput) outputInput.value = transliterate(nameInput.value);
  });
  form?.addEventListener("submit", (event) => {
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
