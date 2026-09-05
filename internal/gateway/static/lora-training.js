(() => {
  const page = document.querySelector("[data-lora-training-page]");
  if (!page) return;
  const historyList = page.querySelector("[data-lora-job-list]");
  const historyCount = page.querySelector(".lora-history-heading > span");
  const csrf = page.dataset.csrf || "";
  const terminalStates = new Set(["completed", "failed", "cancelled"]);

  const setAction = (container, selector, enabled, kind, job) => {
    let node = container.querySelector(selector);
    if (!enabled) {
      node?.remove();
      return;
    }
    if (node) {
      if (node instanceof HTMLAnchorElement) node.href = job.download_url;
      if (node instanceof HTMLFormElement) node.action = kind === "delete" ? job.delete_url : job.cancel_url;
      return;
    }
    if (kind === "download") {
      node = document.createElement("a");
      node.className = "button";
      node.dataset.jobDownload = "";
      node.href = job.download_url;
      node.textContent = "Скачать LoRA";
    } else {
      node = document.createElement("form");
      if (kind === "delete") node.dataset.jobDelete = "";
      else node.dataset.jobCancel = "";
      node.method = "post";
      node.action = kind === "delete" ? job.delete_url : job.cancel_url;
      const token = document.createElement("input");
      token.type = "hidden";
      token.name = "csrf";
      token.value = csrf;
      const button = document.createElement("button");
      button.type = "submit";
      button.className = "danger";
      button.dataset.confirm = kind === "delete"
        ? `Удалить LoRA «${job.name}»? Файл будет удалён из ComfyUI без возможности восстановления.`
        : "Остановить обучение LoRA?";
      button.textContent = kind === "delete" ? "Удалить" : "Отменить";
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
      setAction(actions, "[data-job-cancel]", job.can_cancel, "cancel", job);
      setAction(actions, "[data-job-download]", job.can_download, "download", job);
      setAction(actions, "[data-job-delete]", job.can_delete, "delete", job);
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
  const buildEmptyState = () => {
    const empty = document.createElement("div");
    empty.className = "ui-empty-state lora-history-empty";
    const mark = document.createElement("span");
    mark.setAttribute("aria-hidden", "true");
    mark.textContent = "+";
    const copy = document.createElement("div");
    const title = document.createElement("h3");
    title.textContent = "Обучений пока нет";
    const message = document.createElement("p");
    message.textContent = "После запуска здесь появятся этапы, журнал и готовый файл LoRA.";
    copy.append(title, message);
    empty.append(mark, copy);
    return empty;
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
      const currentIDs = new Set(jobs.map((job) => job.id));
      historyList.querySelectorAll("[data-lora-job]").forEach((card) => {
        if (!currentIDs.has(card.dataset.jobId)) card.remove();
      });
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
      if (!jobs.length && !historyList.querySelector(".lora-history-empty")) historyList.append(buildEmptyState());
      page.classList.remove("is-poll-stale");
    } catch (_) {
      page.classList.add("is-poll-stale");
    }
  };

  if (historyList) {
    page.addEventListener("lora-training-created", (event) => {
      historyList.querySelector(".lora-history-empty")?.remove();
      if (!historyList.querySelector(`[data-job-id="${CSS.escape(event.detail.id)}"]`)) historyList.prepend(buildCard(event.detail));
      void refreshJobs();
    });
    refreshJobs();
    const pollTimer = window.setInterval(refreshJobs, 3000);
    document.addEventListener("visibilitychange", () => { if (!document.hidden) refreshJobs(); });
    window.addEventListener("beforeunload", () => window.clearInterval(pollTimer), { once: true });
  }
})();
