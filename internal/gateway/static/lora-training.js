(() => {
  const page = document.querySelector("[data-lora-training-page]");
  if (!page) return;
  const historyList = page.querySelector("[data-lora-job-list]");
  const connection = page.querySelector('[data-training-connection]');
  let selectedRun = new URLSearchParams(location.search).get('run') || '';
  let pollTimer;
  let requestEpoch = 0;
  let inFlight = false;
  let finished = false;
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
      if (node instanceof HTMLFormElement) node.action = job.cancel_url;
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
      const returnView = document.createElement('input');
      returnView.type = 'hidden'; returnView.name = 'return_view'; returnView.value = 'workspace';
      node.append(returnView);
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
    card.querySelector('[data-job-retention]').hidden = job.state !== 'failed';
    const actions = card.querySelector("footer > div");
    if (actions) {
      setAction(actions, "[data-job-cancel]", job.can_cancel, "cancel", job);
      setAction(actions, "[data-job-download]", job.can_download, "download", job);
    }
    const logWrap = card.querySelector("[data-job-log-wrap]");
    const log = card.querySelector("[data-job-log]");
    if (logWrap && Array.isArray(job.log_tail) && job.log_tail.length) {
      logWrap.hidden = false;
      log.textContent = job.log_tail.join("\n");
      log.scrollTop = log.scrollHeight;
    } else if (logWrap && log && ["completed", "failed", "cancelled"].includes(job.state)) {
      log.textContent = "";
      logWrap.hidden = true;
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
    bar.setAttribute('aria-label', 'Прогресс обучения');
    const message = document.createElement("p"); message.dataset.jobMessage = "";
    progress.append(progressHead, bar, message);
    const error = document.createElement("p"); error.className = "lora-job-error"; error.dataset.jobError = ""; error.hidden = true;
    const retention = document.createElement('p'); retention.className = 'muted'; retention.dataset.jobRetention = '';
    retention.textContent = 'Ошибочные записи очищаются спустя сутки после завершения.';
    const logWrap = document.createElement("details"); logWrap.className = "lora-job-log"; logWrap.dataset.jobLogWrap = ""; logWrap.hidden = true;
    const logSummary = document.createElement("summary"); logSummary.textContent = "Технический журнал";
    const log = document.createElement("pre"); log.dataset.jobLog = "";
    logWrap.append(logSummary, log);
    const footer = document.createElement("footer");
    const created = document.createElement("span"); created.textContent = `Создано ${new Date(job.created_at).toLocaleString("ru-RU")}`;
    const actions = document.createElement("div");
    footer.append(created, actions);
    const library = document.createElement('a');
    library.href = `/train-lora?job=${encodeURIComponent(job.id)}`;
    library.textContent = 'Открыть LoRA';
    actions.append(library);
    if (job.dataset_snapshot_id) {
      const source = document.createElement('a');
      source.href = `/train-lora?version=${encodeURIComponent(job.dataset_snapshot_id)}`;
      source.textContent = 'Исходная версия набора';
      actions.append(source);
    }
    card.append(header, facts, progress, error, retention, logWrap, footer);
    updateCard(card, job);
    return card;
  };
  const refreshJobs = async () => {
    clearTimeout(pollTimer);
    if (!historyList || document.hidden || !selectedRun || inFlight || finished) return;
    const epoch = requestEpoch;
    const run = selectedRun;
    inFlight = true;
    try {
      const response = await fetch(`/api/lora-training/jobs/${encodeURIComponent(run)}`, { headers: { Accept: 'application/json' }, credentials: 'same-origin', cache: 'no-store', signal: AbortSignal.timeout(15000) });
      if (epoch !== requestEpoch) return;
      if (response.status === 404) { finished = true; throw new Error('Обучение не найдено или недоступно. Выберите его в «Мои LoRA».'); }
      if (!response.ok) throw new Error('Связь с сервером потеряна. Состояние обучения пока не обновлено.');
      const job = await response.json();
      if (epoch !== requestEpoch) return;
      if (job.id !== run || !job.state) throw new Error('Сервер не подтвердил состояние выбранного обучения.');
      let card = historyList.querySelector('[data-lora-job]');
      if (!card || card.dataset.jobId !== run) { card = buildCard(job); historyList.replaceChildren(card); }
      else updateCard(card, job);
      finished = terminalStates.has(job.state);
      connection.hidden = true;
    } catch (error) {
      if (epoch === requestEpoch) {
        connection.hidden = false;
        connection.textContent = error.name === 'TimeoutError' ? 'Сервер не ответил вовремя. Проверим состояние ещё раз.'
          : ['TypeError', 'SyntaxError'].includes(error.name) ? 'Не удалось получить состояние обучения. Проверим связь ещё раз.'
          : error.message || 'Не удалось обновить состояние обучения.';
      }
    } finally {
      inFlight = false;
      if (selectedRun && !finished && !document.hidden) pollTimer = setTimeout(refreshJobs, epoch === requestEpoch ? 3000 : 0);
    }
  };

  if (historyList) {
    const emptyState = historyList.firstElementChild.cloneNode(true);
    page.addEventListener('lora-workspace-reset', () => {
      selectedRun = ''; requestEpoch += 1; finished = false;
      clearTimeout(pollTimer); connection.hidden = true;
      historyList.replaceChildren(emptyState.cloneNode(true));
    });
    page.addEventListener("lora-training-created", (event) => {
      selectedRun = event.detail.id;
      requestEpoch += 1;
      finished = false;
      connection.hidden = true;
      historyList.replaceChildren(buildCard(event.detail));
      void refreshJobs();
    });
    if (selectedRun) { historyList.replaceChildren(); connection.hidden = false; connection.textContent = 'Загружаем выбранное обучение…'; }
    void refreshJobs();
    document.addEventListener('visibilitychange', () => { if (document.hidden) clearTimeout(pollTimer); else void refreshJobs(); });
    window.addEventListener('online', () => void refreshJobs());
    window.addEventListener('pagehide', () => { clearTimeout(pollTimer); requestEpoch += 1; });
    window.addEventListener('pageshow', event => { if (event.persisted) { finished = false; void refreshJobs(); } });
  }
})();
