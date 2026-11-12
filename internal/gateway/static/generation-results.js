(function bootstrapGenerationResults(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.results = api;
})(typeof window !== "undefined" ? window : null, function generationResultsFactory() {
  const stateLabels = { draft: "Создано", submitting: "Подготовка", preparing: "Подготовка", uploading: "Загрузка файлов",
    waiting_for_resources: "Ожидает ресурсы", waiting_resources: "Ожидает ресурсы", queued: "В очереди", running: "В работе",
    cancelling: "Отменяется", postprocessing: "Обработка результата", archiving: "Сохранение результата", completed: "Готово",
    failed: "Ошибка", error: "Ошибка", cancelled: "Отменено", expired: "Истекло" };
  const templateLabels = { "text-to-image": "Текст в изображение", "image-to-image": "Фото и промт", "minimax-h3-video": "Видео MiniMax H3",
    "postprocess-image-v1": "Постобработка изображения", "postprocess-video-v1": "Постобработка видео" };
  const batchLabels = { queued: "Ожидает запуска", running: "Выполняется", cancelling: "Отменяется", completed: "Готов",
    partial: "Завершён частично", failed: "Ошибка", cancelled: "Отменён" };
  const stateLabel = state => stateLabels[state] || "Подготовка";
  const templateLabel = template => templateLabels[template] || "Генерация";
  const formatDuration = seconds => {
    const value = Math.max(0, Number(seconds) || 0);
    if (value < 1) return "меньше 1 сек.";
    if (value < 60) return `${Math.round(value)} сек.`;
    if (value < 3600) return `${Math.max(1, Math.round(value / 60))} мин.`;
    const hours = Math.floor(value / 3600), minutes = Math.round((value % 3600) / 60);
    return minutes ? `${hours} ч. ${minutes} мин.` : `${hours} ч.`;
  };
  const formatTime = value => {
    const date = new Date(value);
    return Number.isFinite(date.getTime()) ? new Intl.DateTimeFormat("ru-RU", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" }).format(date) : "";
  };
  const formatExpiry = milliseconds => {
    if (!Number.isFinite(milliseconds)) return "Срок хранения неизвестен";
    if (milliseconds <= 0) return "Удаляется при следующей очистке";
    const total = Math.ceil(milliseconds / 60000), hours = Math.floor(total / 60), minutes = total % 60;
    if (hours >= 24) return `Удалится через ${Math.floor(hours / 24)} дн. ${hours % 24} ч.`;
    return hours > 0 ? `Удалится через ${hours} ч. ${minutes} мин.` : `Удалится через ${Math.max(1, minutes)} мин.`;
  };
  const cleanModelName = value => String(value || "Модель не определена").replaceAll("\\", "/").split("/").pop().replace(/\.(safetensors|ckpt|gguf)$/i, "");
  const countLabel = count => {
    const value = Math.abs(Number(count) || 0), lastTwo = value % 100, last = value % 10;
    return `${value} ${lastTwo >= 11 && lastTwo <= 14 ? "заданий" : last === 1 ? "задание" : last >= 2 && last <= 4 ? "задания" : "заданий"}`;
  };
  const imageCountLabel = count => {
    const value = Math.max(0, Number(count) || 0), lastTwo = value % 100, last = value % 10;
    return `${value} ${lastTwo >= 11 && lastTwo <= 14 ? "изображений" : last === 1 ? "изображение" : last >= 2 && last <= 4 ? "изображения" : "изображений"}`;
  };
  const busyStates = new Set(["submitting", "preparing", "uploading", "queued", "waiting_for_resources", "waiting_resources", "running", "postprocessing", "archiving", "cancelling"]);
  const recentRuns = (state, selectedKey = "") => {
    const all = [...(state.groups || []).map(group => ({ key: `batch:${group.batch.batch_id}`, group, created: group.batch.created_at })),
      ...(state.jobs || []).map(job => ({ key: `job:${job.job_id}`, job, created: job.created_at }))];
    const time = value => Number.isFinite(Date.parse(value)) ? Date.parse(value) : 0;
    all.sort((a, b) => time(b.created) - time(a.created) || a.key.localeCompare(b.key));
    const selected = all.find(run => run.key === selectedKey) || null;
    const items = all.slice(0, 8);
    if (selected && !items.includes(selected)) items.splice(7, 1, selected);
    return { all, items, selected };
  };
  const runCover = run => run.job || run.group.jobs.find(job => job.job_id === run.group.batch.winner_job_id && job.preview)
    || run.group.jobs.find(job => job.preview && !job.preview.sensitive) || run.group.jobs.find(job => job.preview) || run.group.jobs[0];

  /**
   * DOM-only output/history view. All reads and mutations are delegated to history;
   * the existing lightbox controller owns playback, download and focus trapping.
   * @param {Object} options
   * @param {Document} options.documentRef
   * @param {Window} options.windowRef
   * @param {Object} options.viewer Existing lightbox controller.
   * @param {Function} options.downloadURL Existing lightbox URL adapter.
   * @param {Object} options.batchAPI Existing comparison helpers.
   * @param {Object} options.postprocess Existing postprocess presentation helpers.
   * @returns {{bind:Function, render:Function, outputs:Function, jobMedia:Function,
   * clear:Function, setVisible:Function, suspend:Function, refreshExpiry:Function}}
   */
  const createView = ({ documentRef: document, windowRef: window, viewer, downloadURL, batchAPI, feedback,
    sensitiveContent, postprocess, retention = "24 часа", openLoraComparison = () => {}, onReveal = () => {} }) => {
    const element = (tag, className = "", text = "") => {
      const node = document.createElement(tag);
      if (className) node.className = className;
      if (text) node.textContent = text;
      return node;
    };
    const byID = id => document.getElementById(id);
    const section = byID("generation-variants"), list = byID("generation-variant-list"), count = byID("generation-variant-count");
    const content = byID("generation-history-content"), toggle = byID("generation-history-toggle");
    const recent = byID("generation-recent-list"), inspector = byID("generation-recent-inspector"), inspectorTitle = byID("generation-recent-title");
    const selectedTask = byID("generation-recent-task"), viewerStatus = byID("generation-batch-viewer-status");
    const viewerMore = byID("generation-batch-viewer-more"), inspectorClose = byID("generation-recent-close");
    const inspectorHome = inspector?.parentNode, inspectorNext = inspector?.nextSibling;
    const outputGrid = byID("generation-output-grid"), previous = byID("generation-previous-result");
    const compare = byID("generation-batch-compare"), compareBody = byID("generation-batch-compare-body");
    const errorBanner = byID("generation-history-error"), errorText = byID("generation-history-error-message");
    const refresh = byID("generation-history-refresh"), login = byID("generation-history-login");
    let controller = null, last = null, fingerprint = "", timer = null, comparisonID = "", comparisonSignature = "", visible = true;
    let selectedKey = "", selectionMade = false;
    const cardCache = new Map(), tileCache = new Map(), batchPositions = new Map(), detailSignatures = new WeakMap();
    const icon = name => {
      const key = name.replace(/(^|-)([a-z0-9])/g, (_, _dash, letter) => letter.toUpperCase());
      const definition = window.lucide?.icons?.[key];
      const node = definition ? window.lucide.createElement(definition) : element("span");
      node.setAttribute("aria-hidden", "true"); return node;
    };
    const button = (label, action, invoke, name, className = "button ghost") => {
      const node = element("button", className); node.type = "button"; node.dataset.resultAction = action;
      if (name) node.append(icon(name));
      node.append(element("span", "", label)); node.addEventListener("click", () => invoke(node)); return node;
    };
    const pause = root => root?.querySelectorAll?.("video").forEach(video => video.pause());
    const release = root => root?.querySelectorAll?.("video").forEach(video => { video.pause(); video.removeAttribute("src"); video.load(); });
    const sensitiveCover = () => {
      const cover = element("span", "sensitive-media-cover");
      cover.append(element("b", "", "Контент 18+"), element("small", "", "Нажмите, чтобы показать")); return cover;
    };
    const mediaButton = (media, kind) => {
      const isVideo = media.media_type === "video", output = kind === "output";
      const preview = element("button", output ? "generation-output-preview" : "generation-job-preview");
      preview.type = "button";
      if (!isVideo) preview.classList.add("generation-image-preview");
      preview.title = isVideo ? "Открыть видеоплеер" : "Открыть и увеличить изображение";
      preview.setAttribute("aria-label", isVideo ? "Открыть видео" : "Открыть изображение");
      if (media.sensitive) {
        preview.dataset.sensitiveMedia = "";
        if (!output) preview.classList.add("sensitive-media");
      }
      const node = element(isVideo ? "video" : "img");
      const permitted = () => !media.sensitive || document.body.classList.contains("show-sensitive-content") || preview.closest(".sensitive-media")?.classList.contains("is-revealed");
      const load = () => { if (permitted() && !node.getAttribute("src")) node.src = media.url; };
      load();
      preview.addEventListener("click", event => {
        if (media.sensitive && sensitiveContent?.reveal?.(preview)) {
          load();
          preview.dispatchEvent(new CustomEvent("generation-media-revealed", { bubbles: true }));
          event.stopImmediatePropagation();
        }
        else load();
      });
      if (isVideo) {
        node.muted = true; node.playsInline = true; node.loop = output; node.preload = output ? "auto" : "metadata";
        if (output) preview.classList.add("generation-video-preview");
        const play = element("span", output ? "generation-video-play" : "generation-job-play");
        play.setAttribute("aria-hidden", "true"); play.textContent = "▶";
        preview.append(node, play); viewer.wireVideoPreview(preview, media, { canPlay: () => visible && !document.hidden && permitted() });
      } else {
        node.loading = "lazy"; node.alt = output ? media.filename : "Результат генерации";
        const cue = element("span", "generation-zoom-cue");
        cue.setAttribute("aria-hidden", "true");
        cue.append(icon("zoom-in"));
        preview.append(node, cue);
        preview.addEventListener("touchstart", event => {
          if (event.touches.length < 2 || !permitted()) return;
          event.preventDefault();
          load();
          viewer.open(media);
        }, { passive: false });
        preview.addEventListener("click", () => { if (!sensitiveContent?.reveal?.(preview)) viewer.open(media); });
      }
      if (media.sensitive) preview.append(sensitiveCover());
      return preview;
    };
    const outputCard = media => {
      const card = element("figure", "generation-output" + (media.sensitive ? " sensitive-media" : ""));
      const caption = element("figcaption"), download = element("a", "generation-output-download", "Скачать файл");
      download.href = downloadURL(media.url, window.location.origin); download.download = media.filename;
      caption.append(element("span", "", media.filename), download); card.append(mediaButton(media, "output"), caption);
      return card;
    };
    const isPhotoSeries = values => values?.length > 1 && values.every(media => media.media_type === "image");
    const photoCarousel = values => {
      const carousel = element("section", "generation-photo-carousel");
      carousel.setAttribute("aria-roledescription", "карусель");
      carousel.setAttribute("aria-label", "Фотокарусель результата");
      const stage = element("div", "generation-photo-stage");
      const toolbar = element("div", "generation-photo-toolbar");
      const counter = element("span", "generation-photo-counter");
      counter.setAttribute("aria-live", "polite"); counter.setAttribute("aria-atomic", "true");
      const thumbs = element("div", "generation-photo-thumbs");
      thumbs.setAttribute("aria-label", "Кадры результата");
      let index = 0, pointer = null, suppressClick = false;
      const slides = values.map((media, i) => {
        const card = outputCard(media);
        card.setAttribute("role", "group"); card.setAttribute("aria-roledescription", "слайд");
        card.setAttribute("aria-label", `${i + 1} из ${values.length}`);
        card.hidden = i !== 0;
        card.querySelector("img")?.setAttribute("draggable", "false");
        stage.append(card); return card;
      });
      const select = next => {
        index = Math.max(0, Math.min(values.length - 1, next));
        slides.forEach((slide, i) => { slide.hidden = i !== index; });
        [...thumbs.children].forEach((thumb, i) => {
          thumb.setAttribute("aria-pressed", String(i === index));
        });
        counter.textContent = `${index + 1} / ${values.length}`;
        carousel.dispatchEvent(new CustomEvent("generation-photo-change", { bubbles: true, detail: { index } }));
        back.disabled = index === 0; forward.disabled = index === values.length - 1;
        const thumb = thumbs.children[index];
        if (thumb) {
          const left = thumb.offsetLeft - thumbs.offsetLeft;
          if (left < thumbs.scrollLeft) thumbs.scrollLeft = left;
          else if (left + thumb.offsetWidth > thumbs.scrollLeft + thumbs.clientWidth) thumbs.scrollLeft = left + thumb.offsetWidth - thumbs.clientWidth;
        }
      };
      const arrow = (label, action, name, direction) => {
        const control = button(label, action, () => select(index + direction), name, "button ghost generation-photo-arrow");
        control.setAttribute("aria-label", label); control.title = label;
        control.replaceChildren(icon(name)); return control;
      };
      const back = arrow("Предыдущее фото", "photo-previous", "chevron-left", -1);
      const forward = arrow("Следующее фото", "photo-next", "chevron-right", 1);
      values.forEach((media, i) => {
        const thumb = element("button", "generation-photo-thumb"); thumb.type = "button";
        thumb.setAttribute("aria-label", `Фото ${i + 1}`); thumb.title = `Фото ${i + 1}`;
        if (media.sensitive) thumb.append(icon("eye-off"));
        else {
          const image = element("img"); image.src = media.url; image.alt = "";
          image.loading = "lazy"; image.draggable = false; thumb.append(image);
        }
        thumb.addEventListener("click", () => select(i)); thumbs.append(thumb);
      });
      carousel.addEventListener("keydown", event => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        select(event.key === "Home" ? 0 : event.key === "End" ? values.length - 1 : index + (event.key === "ArrowLeft" ? -1 : 1));
      });
      stage.addEventListener("pointerdown", event => {
        if (event.isPrimary === false || event.button !== 0 || !event.target.closest(".generation-output-preview")) return;
        suppressClick = false; pointer = { id: event.pointerId, x: event.clientX, y: event.clientY };
      });
      stage.addEventListener("pointercancel", () => { pointer = null; });
      stage.addEventListener("pointerup", event => {
        if (!pointer || pointer.id !== event.pointerId) return;
        const dx = event.clientX - pointer.x, dy = event.clientY - pointer.y; pointer = null;
        if (Math.abs(dx) < 45 || Math.abs(dx) < Math.abs(dy) * 1.5) return;
        suppressClick = true; select(index + (dx > 0 ? 1 : -1));
      });
      stage.addEventListener("click", event => {
        if (!suppressClick) return;
        suppressClick = false; event.preventDefault(); event.stopImmediatePropagation();
      }, true);
      toolbar.append(back, counter, forward); carousel.append(stage, toolbar, thumbs); select(0);
      return carousel;
    };
    const jobMedia = job => {
      const values = (job.media || []).map(media => ({ ...media,
        comparison_job_id: job.template_id === "image-to-image" ? job.job_id : "" }));
      if (isPhotoSeries(values)) return photoCarousel(values);
      const media = values.find(item => item.media_type === "image") || values[0];
      if (media) return mediaButton(media, "job");
      const placeholder = element("div", "generation-job-placeholder"), mark = element("span");
      mark.setAttribute("aria-hidden", "true"); mark.textContent = job.template_id === "minimax-h3-video" ? "▶" : "◇";
      placeholder.append(mark, element("small", "", job.state === "completed" ? "Результат недоступен" : stateLabel(job.state)));
      return placeholder;
    };
    const outputs = values => {
      if (!outputGrid) return;
      selectedKey = ""; selectionMade = true;
      if (inspector) inspector.hidden = true;
      release(list); list?.replaceChildren(); cardCache.clear(); fingerprint = "";
      if (previous) previous.hidden = true;
      release(outputGrid);
      const media = Array.isArray(values) ? values : [];
      outputGrid.replaceChildren(...(isPhotoSeries(media) ? [photoCarousel(media)] : media.map(outputCard)));
      if (last) render(last);
    };
    const actionPending = (operation, id) => last?.actions.includes(`${operation}:${id}`);
    const createCard = (job, parent) => {
      const card = element("article", `generation-job ui-media-card${parent ? " generation-job--batch" : ""}`);
      card.dataset.state = job.state || "submitting"; card.dataset.jobId = job.job_id || "";
      if (parent) card.dataset.batchId = parent.batch_id;
      if (busyStates.has(job.state)) card.setAttribute("aria-busy", "true");
      else if (job.state === "completed") card.classList.add("is-success");
      else if (job.state === "failed" || job.state === "error") card.classList.add("is-error");
      if (parent?.winner_job_id === job.job_id) card.classList.add("is-winner");
      card.append(jobMedia(job));
      const body = element("div", "generation-job-body"), heading = element("div", "generation-job-heading"), copy = element("span");
      const model = element("small", "", parent ? `${parent.parameter_label || "Seed"}: ${job.experiment_value || job.seed} · ${cleanModelName(job.model_name)}` : cleanModelName(job.model_name));
      model.hidden = Boolean(postprocess?.isJob(job));
      copy.append(element("strong", "", parent ? `Вариант ${job.batch_position}` : templateLabel(job.template_id)), model);
      heading.append(copy, element("span", "generation-job-state", parent?.winner_job_id === job.job_id ? "Лучший" : stateLabel(job.state)));
      const message = element("p", "generation-job-message", job.message || "Состояние задания обновляется");
      const prompt = element("p", "generation-job-prompt", postprocess?.isJob(job) ? postprocess.summary(job.postprocess) : job.prompt || "Промт пока не сохранён");
      const seed = postprocess?.isJob(job) ? "" : Number(job.seed) >= 0 ? `Seed ${job.seed}` : "Случайный seed";
      const meta = element("small", "generation-job-meta", [formatTime(job.created_at), seed, formatDuration(job.duration_seconds)].filter(Boolean).join(" · "));
      if (job.expires_at) {
        const expiry = element("time"); expiry.dataset.generationExpiry = String(new Date(job.expires_at).getTime());
        expiry.textContent = formatExpiry(Number(expiry.dataset.generationExpiry) - Date.now()); meta.append(" · ", expiry);
      }
      const actions = element("div", "generation-job-actions");
      if (parent && job.state === "completed") {
        const choice = element("label", "generation-batch-compare-toggle ui-choice ui-choice--compact"), check = element("input");
        check.type = "checkbox"; check.dataset.resultAction = "compare-select";
        check.checked = (last.batchState.selections?.[parent.batch_id] || []).includes(job.job_id);
        check.addEventListener("change", () => controller.toggleCompare(parent.batch_id, job.job_id));
        choice.title = check.checked ? "Убрать из сравнения" : "Добавить к сравнению";
        choice.append(check, icon("columns-2"), element("span", "", "Сравнить")); actions.append(choice);
        const winner = button(parent.winner_job_id === job.job_id ? "Выбран" : actionPending("winner", parent.batch_id) ? "Сохраняем..." : "Выбрать лучшим", "winner",
          () => controller.selectWinner(parent.batch_id, job.job_id), "trophy");
        winner.disabled = parent.winner_job_id === job.job_id || Boolean(actionPending("winner", parent.batch_id)); actions.append(winner);
      } else if (!parent) {
        if (job.retryable && !postprocess?.isJob(job)) {
          actions.append(button("Повторить", "repeat", trigger => controller.repeat(job.job_id, trigger), "rotate-ccw"));
          actions.append(button("Повторить сразу", "repeat-now", trigger => window.AIGatewayGeneration?.openRepeatNow?.(job.job_id, trigger), "play"));
        }
        if (job.cancellable) {
          const cancel = button(actionPending("cancel", job.job_id) ? "Отменяем..." : "Отменить", "cancel", () => controller.cancelJob(job.job_id), "square", "button danger ghost");
          cancel.disabled = Boolean(actionPending("cancel", job.job_id)); actions.append(cancel);
        }
      }
      const details = element("details", "generation-job-details"), summary = element("summary", "", "Этапы");
      summary.dataset.resultAction = "details";
      const detailContent = element("div"); detailContent.dataset.jobDetailsContent = "";
      details.append(summary, detailContent); details.addEventListener("toggle", () => { if (details.open) void controller.details(job.job_id); });
      body.append(heading, message, prompt, meta);
      const footer = element("div", "generation-job-footer"); footer.append(actions); card.append(body, footer, details); return card;
    };
    const cachedCard = (job, parent) => {
      const id = job.job_id, signature = JSON.stringify([job, parent?.batch_id, parent?.winner_job_id, parent?.parameter_label,
        parent && (last.batchState.selections?.[parent.batch_id] || []).includes(id), actionPending("cancel", id), parent && actionPending("winner", parent.batch_id)]);
      const previous = cardCache.get(id);
      if (previous?.signature === signature) return previous.node;
      const node = createCard(job, parent);
      if (previous?.node.querySelector("details")?.open) node.querySelector("details").open = true;
      if (previous) release(previous.node);
      cardCache.set(id, { signature, node }); return node;
    };
    const createGroup = ({ batch: parent, jobs }) => {
      const group = element("section", "generation-batch-group"); group.dataset.state = parent.state || "queued"; group.dataset.batchId = parent.batch_id;
      const header = element("header", "generation-batch-header"), heading = element("div", "generation-batch-heading");
      const title = parent.mode === "lora_verification" ? `${parent.total_count} изображений · проверка LoRA` : parent.mode === "parameter"
        ? `${parent.total_count} вариантов · меняется ${parent.parameter_label || parent.parameter_name}` : `${parent.total_count} вариантов · разные seed`;
      heading.append(element("span", "", `${templateLabel(parent.template_id)} · пакет`), element("h3", "", title), element("small", "", `${cleanModelName(parent.model_name)} · ${formatTime(parent.created_at)}`));
      header.append(heading, element("span", "generation-batch-state", batchLabels[parent.state] || "Выполняется"));
      const parts = [`${parent.finished_count || 0} из ${parent.total_count || 0}`];
      if (parent.completed_count) parts.push(`готово: ${parent.completed_count}`);
      if (parent.failed_count) parts.push(`ошибок: ${parent.failed_count}`);
      if (parent.cancelled_count) parts.push(`отменено: ${parent.cancelled_count}`);
      if (parent.missing_count) parts.push(`удалено: ${parent.missing_count}`);
      if (Number(parent.estimated_finish_seconds) > 0 && !["completed", "partial", "failed", "cancelled"].includes(parent.state)) parts.push(`до завершения ${formatDuration(parent.estimated_finish_seconds)}`);
      const progress = element("div", "generation-batch-progress"), actions = element("div", "generation-batch-actions");
      const track = element("span", "generation-batch-progress-track"), fill = element("i");
      const percent = Math.max(0, Math.min(100, Number(parent.progress_percent) || 0));
      track.setAttribute("role", "progressbar"); track.setAttribute("aria-label", "Прогресс пакета вариантов");
      track.setAttribute("aria-valuemin", "0"); track.setAttribute("aria-valuemax", "100"); track.setAttribute("aria-valuenow", String(percent));
      fill.style.width = `${percent}%`; track.append(fill); progress.append(element("span", "", parts.join(" · ")), track);
      const selected = batchAPI.selectedJobs(last.batchState, parent);
      const verification = parent.mode === "lora_verification";
      const compareButton = button(verification ? "Проверка LoRA" : "Сравнить", "compare",
        () => verification ? openLoraComparison(parent.batch_id) : openComparison(parent.batch_id), "columns-2");
      compareButton.append(element("small", "", verification ? "Результаты проверки" : selected.length === 2 ? "2 варианта рядом" : `Выбрано ${selected.length} из 2`));
      compareButton.disabled = !verification && selected.length !== 2;
      const winner = (parent.jobs || []).find(job => job.job_id === parent.winner_job_id);
      if (winner && !postprocess?.isJob(winner)) {
        const branch = button(actionPending("branch", winner.job_id) ? "Готовим ветку..." : "Новая ветка", "branch", () => {
          closeSelected();
          controller.branch(parent.batch_id, winner.job_id);
        }, "git-branch");
        branch.disabled = Boolean(actionPending("branch", winner.job_id)); actions.append(branch);
      }
      if (parent.cancellable) {
        const cancel = button(actionPending("cancelBatch", parent.batch_id) ? "Отменяем..." : "Отменить пакет", "cancel-batch", () => controller.cancelBatch(parent.batch_id), "square", "button danger ghost");
        cancel.disabled = Boolean(actionPending("cancelBatch", parent.batch_id)); actions.append(cancel);
      }
      const carousel = element("div", "generation-batch-carousel");
      carousel.setAttribute("aria-roledescription", "карусель"); carousel.setAttribute("aria-label", "Варианты пакета");
      const stage = element("div", "generation-batch-stage"), toolbar = element("div", "generation-photo-toolbar");
      const counter = element("span", "generation-photo-counter");
      counter.setAttribute("aria-live", "polite"); counter.setAttribute("aria-atomic", "true");
      const thumbs = element("div", "generation-batch-thumbs"); thumbs.setAttribute("aria-label", "Варианты пакета");
      const peeks = [element("div", "generation-batch-peek generation-batch-peek--middle"),
        element("div", "generation-batch-peek generation-batch-peek--back")];
      peeks.forEach(peek => { peek.setAttribute("aria-hidden", "true"); stage.append(peek); });
      const slides = jobs.map((job, index) => {
        const slide = element("div", "generation-batch-slide");
        slide.setAttribute("role", "group"); slide.setAttribute("aria-roledescription", "слайд");
        slide.setAttribute("aria-label", `Вариант ${job.batch_position || index + 1} из ${jobs.length}`);
        slide.append(cachedCard(job, parent)); stage.append(slide); return slide;
      });
      let index = Math.max(0, jobs.findIndex(job => job.job_id === batchPositions.get(parent.batch_id)));
      let finishTransition = null;
      const updatePeeks = () => peeks.forEach((peek, depth) => {
        const job = jobs.length > depth + 1 ? jobs[(index + depth + 1) % jobs.length] : null;
        const media = job?.media?.find(item => item.media_type === "image") || job?.preview;
        peek.hidden = !job;
        peek.replaceChildren();
        if (media?.url && !media.sensitive && !job?.preview?.sensitive &&
            (media.media_type === "image" || media.source_type === "image")) {
          const image = element("img"); image.src = media.url; image.alt = ""; image.loading = "lazy"; peek.append(image);
        }
      });
      const select = next => {
        if (!jobs.length) return;
        if (finishTransition) finishTransition();
        const previousIndex = index;
        index = Math.max(0, Math.min(jobs.length - 1, next));
        batchPositions.set(parent.batch_id, jobs[index].job_id);
        const outgoing = slides[previousIndex], incoming = slides[index];
        const activeMedia = incoming?.querySelector(".generation-job-preview img, .generation-job-preview video");
        stage.style.removeProperty("--batch-aspect");
        const updateAspect = () => {
          if (slides[index] !== incoming) return;
          const width = activeMedia?.naturalWidth || activeMedia?.videoWidth;
          const height = activeMedia?.naturalHeight || activeMedia?.videoHeight;
          if (width > 0 && height > 0) stage.style.setProperty("--batch-aspect", String(Math.max(.56, Math.min(1.6, width / height))));
        };
        updateAspect();
        activeMedia?.addEventListener(activeMedia.tagName === "VIDEO" ? "loadedmetadata" : "load", updateAspect, { once: true });
        const animate = previousIndex !== index && !window.matchMedia?.("(prefers-reduced-motion: reduce)")?.matches;
        const oldMedia = outgoing?.querySelector(".generation-job-preview, .generation-job-placeholder, .generation-photo-carousel");
        const newMedia = incoming?.querySelector(".generation-job-preview, .generation-job-placeholder, .generation-photo-carousel");
        slides.forEach((slide, position) => {
          slide.hidden = position !== index && !(animate && position === previousIndex);
          if (slide.hidden) pause(slide);
        });
        if (animate && oldMedia?.animate && newMedia?.animate) {
          outgoing.classList.add("is-leaving");
          const direction = next > previousIndex ? 1 : -1;
          const oldAnimation = oldMedia.animate([
            { transform: "translate3d(0, 0, 0) rotate(0deg) scale(1)", opacity: 1 },
            { transform: `translate3d(${direction * 66}%, -8%, 0) rotate(${direction * 6}deg) scale(.94)`, opacity: 0 }
          ], { duration: 390, easing: "cubic-bezier(.24,.74,.25,1)", fill: "forwards" });
          const newAnimation = newMedia.animate([
            { transform: `translate3d(${-direction * 5}%, 10px, 0) scale(.97)`, opacity: .72 },
            { transform: "translate3d(0, 0, 0) scale(1)", opacity: 1 }
          ], { duration: 390, easing: "cubic-bezier(.18,.78,.25,1)", fill: "forwards" });
          const done = () => {
            oldAnimation.cancel(); newAnimation.cancel(); outgoing.classList.remove("is-leaving");
            outgoing.hidden = true; pause(outgoing);
            if (finishTransition === done) finishTransition = null;
          };
          finishTransition = done;
          Promise.allSettled([oldAnimation.finished, newAnimation.finished]).then(() => {
            if (finishTransition === done) done();
          });
        }
        updatePeeks();
        [...thumbs.children].forEach((thumb, position) => thumb.setAttribute("aria-pressed", String(position === index)));
        counter.textContent = `Вариант ${jobs[index].batch_position || index + 1} / ${jobs.length}`;
        back.disabled = index === 0; forward.disabled = index === jobs.length - 1;
        const thumb = thumbs.children[index];
        if (thumb) {
          const left = thumb.offsetLeft - thumbs.offsetLeft;
          if (left < thumbs.scrollLeft) thumbs.scrollLeft = left;
          else if (left + thumb.offsetWidth > thumbs.scrollLeft + thumbs.clientWidth) thumbs.scrollLeft = left + thumb.offsetWidth - thumbs.clientWidth;
        }
        updateActions();
      };
      const arrow = (label, action, name, direction) => {
        const control = button(label, action, () => select(index + direction), name, "button ghost generation-photo-arrow");
        control.setAttribute("aria-label", label); control.title = label; control.replaceChildren(icon(name)); return control;
      };
      const back = arrow("Предыдущий вариант", "batch-previous", "chevron-left", -1);
      const forward = arrow("Следующий вариант", "batch-next", "chevron-right", 1);
      jobs.forEach((job, position) => {
        const thumb = element("button", "generation-batch-thumb"); thumb.type = "button";
        thumb.dataset.state = job.state || "queued";
        thumb.setAttribute("aria-label", `Вариант ${job.batch_position || position + 1}: ${stateLabel(job.state)}`);
        thumb.title = thumb.getAttribute("aria-label");
        thumb.addEventListener("click", () => select(position)); thumbs.append(thumb);
      });
      carousel.addEventListener("keydown", event => {
        if (event.target.closest(".generation-photo-carousel") || !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        select(event.key === "Home" ? 0 : event.key === "End" ? jobs.length - 1 : index + (event.key === "ArrowLeft" ? -1 : 1));
      });
      let pointer = null, suppressClick = false;
      stage.addEventListener("pointerdown", event => {
        if (event.isPrimary === false || event.button !== 0 || !event.target.closest(".generation-job-preview")) return;
        suppressClick = false; pointer = { id: event.pointerId, x: event.clientX, y: event.clientY };
      });
      stage.addEventListener("pointercancel", () => { pointer = null; });
      stage.addEventListener("pointerup", event => {
        if (!pointer || pointer.id !== event.pointerId) return;
        const dx = event.clientX - pointer.x, dy = event.clientY - pointer.y; pointer = null;
        if (Math.abs(dx) < 45 || Math.abs(dx) < Math.abs(dy) * 1.5) return;
        suppressClick = true; select(index + (dx > 0 ? 1 : -1));
      });
      stage.addEventListener("click", event => {
        if (!suppressClick) return;
        suppressClick = false; event.preventDefault(); event.stopImmediatePropagation();
      }, true);
      const position = element("div", "generation-batch-position"); position.append(counter, thumbs);
      toolbar.append(back, position, forward); carousel.append(stage, toolbar);
      const dock = element("div", "generation-batch-viewer-dock");
      const download = element("a", "button ghost generation-batch-viewer-download");
      download.append(icon("download"), element("span", "", "Сохранить"), element("small", "", "Скачать вариант"));
      const pickWinner = button("Выбрать лучшим", "winner", () => controller.selectWinner(parent.batch_id, jobs[index].job_id), "trophy");
      dock.append(compareButton, download); actions.prepend(pickWinner);
      const details = element("details", "generation-batch-viewer-details");
      details.id = "generation-batch-viewer-details";
      const detailSummary = element("summary", "", "Сведения и действия");
      detailSummary.dataset.resultAction = "batch-viewer-details";
      const activeMeta = element("div", "generation-batch-viewer-meta");
      const activeTimeline = element("details", "generation-job-details generation-batch-viewer-timeline");
      const timelineSummary = element("summary", "", "Этапы выбранного варианта");
      timelineSummary.dataset.resultAction = "batch-viewer-timeline";
      const timelineContent = element("div"); timelineContent.dataset.jobDetailsContent = "";
      activeTimeline.append(timelineSummary, timelineContent);
      activeTimeline.addEventListener("toggle", () => {
        if (activeTimeline.open && activeTimeline.dataset.activeJobId) void controller.details(activeTimeline.dataset.activeJobId);
      });
      const taskLink = element("a", "button ghost generation-batch-viewer-task");
      taskLink.href = `/tasks?${new URLSearchParams({ task: `generation_batch:${parent.batch_id}` })}`;
      taskLink.append(icon("list-checks"), element("span", "", "Открыть задачу"));
      details.append(detailSummary, activeMeta, header, progress, actions, activeTimeline, taskLink);
      const previousDetails = list.querySelector(".generation-batch-viewer-details");
      details.open = Boolean(previousDetails?.open);
      details.addEventListener("toggle", () => viewerMore?.setAttribute("aria-expanded", String(details.open)));
      activeTimeline.open = Boolean(previousDetails?.querySelector(".generation-batch-viewer-timeline")?.open);
      const updateActions = () => {
        const job = jobs[index];
        const media = job?.media?.find(item => item.media_type === "image") || job?.media?.[0];
        const revealed = slides[index]?.querySelector(".generation-job-preview")?.classList.contains("is-revealed");
        download.hidden = !media?.url || Boolean(media?.sensitive && !document.body.classList.contains("show-sensitive-content") && !revealed);
        if (!download.hidden) {
          download.href = downloadURL(media.url, window.location.origin);
          download.download = media.filename || "";
        }
        pickWinner.hidden = !job || job.state !== "completed";
        const pendingWinner = Boolean(actionPending("winner", parent.batch_id));
        pickWinner.disabled = Boolean(job && (parent.winner_job_id === job.job_id || pendingWinner));
        pickWinner.querySelector("span").textContent = parent.winner_job_id === job?.job_id ? "Выбран" : pendingWinner ? "Сохраняем..." : "Выбрать лучшим";
        activeTimeline.hidden = !job;
        activeTimeline.dataset.activeJobId = job?.job_id || "";
        if (activeTimeline.open && job) void controller.details(job.job_id);
        activeMeta.replaceChildren(
          element("strong", "", `Вариант ${job?.batch_position || index + 1}`),
          element("span", "", [cleanModelName(job?.model_name || parent.model_name), job?.seed != null ? `Seed ${job.seed}` : ""].filter(Boolean).join(" · ")),
          element("p", "", job?.prompt || job?.message || "Промт пока не сохранён")
        );
      };
      stage.addEventListener("click", event => {
        if (event.target.closest(".generation-job-preview")) window.requestAnimationFrame(updateActions);
      });
      stage.addEventListener("generation-media-revealed", updateActions);
      if (!jobs.length) stage.append(element("p", "generation-variant-empty ui-empty-state", "По этим фильтрам вариантов нет."));
      select(index);
      group.append(carousel, dock, details); return group;
    };
    const createSingleView = job => {
      const view = element("section", "generation-single-view");
      const card = cachedCard(job), media = Array.isArray(job.media) ? job.media : [];
      const stage = element("div", "generation-single-stage");
      stage.append(card);
      const stageMedia = card.querySelector(".generation-job-preview img, .generation-job-preview video, .generation-photo-stage img");
      const updateAspect = () => {
        const width = stageMedia?.naturalWidth || stageMedia?.videoWidth;
        const height = stageMedia?.naturalHeight || stageMedia?.videoHeight;
        if (width > 0 && height > 0) stage.style.setProperty("--single-aspect", String(Math.max(.56, Math.min(1.8, width / height))));
      };
      updateAspect();
      if (stageMedia && !stage.style.getPropertyValue("--single-aspect")) {
        stageMedia.addEventListener(stageMedia.tagName === "VIDEO" ? "loadedmetadata" : "load", updateAspect, { once: true });
      }
      const dock = element("div", "generation-batch-viewer-dock generation-single-view-dock");
      const repeat = job.retryable && !postprocess?.isJob(job)
        ? button("Повторить", "repeat", trigger => { closeSelected(); controller.repeat(job.job_id, trigger); }, "rotate-ccw")
        : null;
      if (repeat) {
        repeat.append(element("small", "", "Изменить параметры"));
        dock.append(repeat);
      }
      const save = element("a", "button ghost generation-batch-viewer-download");
      save.append(icon("download"), element("span", "", "Сохранить"), element("small", "", "Скачать файл"));
      dock.append(save);
      const details = element("details", "generation-batch-viewer-details generation-single-view-details");
      details.id = "generation-batch-viewer-details";
      const summary = element("summary", "", "Сведения и действия");
      summary.dataset.resultAction = "batch-viewer-details";
      const meta = element("div", "generation-batch-viewer-meta");
      meta.append(
        element("strong", "", templateLabel(job.template_id)),
        element("span", "", [cleanModelName(job.model_name), job.seed != null ? `Seed ${job.seed}` : "", formatTime(job.created_at)].filter(Boolean).join(" · ")),
        element("span", "", job.message || stateLabel(job.state)),
        element("p", "", job.prompt || job.message || "Промт пока не сохранён")
      );
      const actions = element("div", "generation-batch-actions");
      if (job.retryable && !postprocess?.isJob(job)) {
        actions.append(button("Повторить сразу", "repeat-now", trigger => {
          closeSelected(); window.AIGatewayGeneration?.openRepeatNow?.(job.job_id, trigger);
        }, "play"));
      }
      if (job.cancellable) {
        const cancel = button(actionPending("cancel", job.job_id) ? "Отменяем..." : "Отменить генерацию",
          "cancel", () => controller.cancelJob(job.job_id), "square", "button danger ghost");
        cancel.disabled = Boolean(actionPending("cancel", job.job_id)); actions.append(cancel);
      }
      const timeline = element("details", "generation-job-details generation-batch-viewer-timeline");
      timeline.dataset.activeJobId = job.job_id;
      const timelineSummary = element("summary", "", "Этапы задания");
      timelineSummary.dataset.resultAction = "batch-viewer-timeline";
      const timelineContent = element("div"); timelineContent.dataset.jobDetailsContent = "";
      timeline.append(timelineSummary, timelineContent);
      timeline.addEventListener("toggle", () => { if (timeline.open) void controller.details(job.job_id); });
      const taskLink = element("a", "button ghost generation-batch-viewer-task");
      taskLink.href = `/tasks?${new URLSearchParams({ task: `generation:${job.job_id}` })}`;
      taskLink.append(icon("list-checks"), element("span", "", "Открыть задачу"));
      details.append(summary, meta, actions, timeline, taskLink);
      const oldDetails = list.querySelector(".generation-single-view-details");
      details.open = Boolean(oldDetails?.open);
      timeline.open = Boolean(oldDetails?.querySelector(".generation-batch-viewer-timeline")?.open);
      details.addEventListener("toggle", () => viewerMore?.setAttribute("aria-expanded", String(details.open)));
      let mediaIndex = 0;
      const updateSave = () => {
        const item = media[mediaIndex];
        const preview = card.querySelector(".generation-output:not([hidden]) .generation-output-preview, .generation-job-preview");
        const revealed = preview?.classList.contains("is-revealed") || preview?.closest(".sensitive-media")?.classList.contains("is-revealed")
          || document.body.classList.contains("show-sensitive-content");
        save.hidden = !item?.url || Boolean(item.sensitive && !revealed);
        dock.hidden = !repeat && save.hidden;
        if (!save.hidden) {
          save.href = downloadURL(item.url, window.location.origin);
          save.download = item.filename || "";
          save.querySelector("small").textContent = item.media_type === "video" ? "Скачать видео" : "Скачать изображение";
        }
      };
      stage.addEventListener("generation-photo-change", event => {
        mediaIndex = event.detail.index; updateSave();
      });
      stage.addEventListener("click", event => {
        if (event.target.closest(".generation-job-preview, .generation-output-preview")) window.requestAnimationFrame(updateSave);
      });
      stage.addEventListener("generation-media-revealed", updateSave);
      updateSave();
      view.append(stage, dock, details);
      return view;
    };
    const renderDetails = () => list?.querySelectorAll?.("[data-job-id], [data-active-job-id]").forEach(card => {
      const id = card.dataset.jobId || card.dataset.activeJobId, record = last.details[id], details = card.matches("details") ? card : card.querySelector("details");
      const target = details?.querySelector("[data-job-details-content]");
      if (!target) return;
      const signature = JSON.stringify(record || null);
      if (detailSignatures.get(target) !== signature) {
        detailSignatures.set(target, signature); target.classList.toggle("has-error", record?.status === "error");
        if (!record || record.status === "loading") target.textContent = "Загружаем этапы...";
        else if (record.status === "error") target.replaceChildren(element("p", "", record.error), button("Повторить запрос", "detail-retry", () => controller.details(id, true), "refresh-cw"));
        else {
          const timeline = element("ol", "generation-job-timeline");
          timeline.append(...record.transitions.map(transition => {
            const row = element("li"), time = element("time", "", formatTime(transition.created_at)), copy = element("span");
            time.dateTime = transition.created_at || "";
            copy.append(element("strong", "", stateLabel(transition.state)), element("small", "", transition.message || "Состояние обновлено"));
            row.append(time, copy); return row;
          }));
          target.replaceChildren(record.transitions.length ? timeline : element("p", "", "Сохранённых этапов пока нет."));
        }
      }
      if (details.open && !record) Promise.resolve().then(() => { if (details.isConnected && details.open) void controller.details(id); });
    });
    const restoreFocus = focused => {
      if (!focused || document.activeElement === focused.node) return;
      if (focused.action === "batch-viewer-details" || focused.action === "batch-viewer-timeline") {
        list.querySelector(`[data-result-action="${focused.action}"]`)?.focus({ preventScroll: true }); return;
      }
      const cards = [...list.querySelectorAll("[data-job-id]")], groups = [...list.querySelectorAll("section[data-batch-id]")];
      const owner = focused.job ? cards.find(node => node.dataset.jobId === focused.job) : groups.find(node => node.dataset.batchId === focused.batch);
      const target = owner && [...owner.querySelectorAll("[data-result-action]")].find(node => node.dataset.resultAction === focused.action && !node.disabled);
      (target || owner?.querySelector("summary") || inspectorTitle)?.focus({ preventScroll: true });
    };
    const reconcile = (parent, nodes) => {
      nodes.forEach((node, index) => { if (parent.children[index] !== node) parent.insertBefore(node, parent.children[index] || null); });
      const kept = new Set(nodes); [...parent.children].forEach(node => { if (!kept.has(node)) { release(node); node.remove(); } });
    };
    const refreshExpiry = () => {
      list?.querySelectorAll?.("[data-generation-expiry]").forEach(node => {
        const text = formatExpiry(Number(node.dataset.generationExpiry) - Date.now()); if (node.textContent !== text) node.textContent = text;
      });
      recent?.querySelectorAll?.("img[data-recent-expiry]").forEach(node => { if (Number(node.dataset.recentExpiry) <= Date.now()) node.remove(); });
    };
    const renderComparison = () => {
      if (!comparisonID || !compare?.open) return;
      const value = controller.comparison(comparisonID);
      if (!value) { closeComparison(); return; }
      const signature = JSON.stringify(value); if (signature === comparisonSignature) return;
      comparisonSignature = signature; release(compareBody);
      const comparison = element("div", "generation-batch-comparison");
      comparison.append(...value.jobs.map(job => {
        const item = element("article", "generation-batch-comparison-item"), copy = element("div");
        copy.append(element("strong", "", `Вариант ${job.batch_position}`), element("span", "", `${value.batch.parameter_label || "Seed"}: ${job.experiment_value || job.seed}`), element("p", "", job.prompt || "Промт не сохранён"));
        item.append(jobMedia(job), copy); return item;
      }));
      const facts = element("dl", "generation-batch-differences");
      facts.append(...value.differences.map(difference => {
        const row = element("div"); row.append(element("dt", "", difference.label), element("dd", "", difference.values.map(item => `Вариант ${item.position}: ${item.value}`).join(" · "))); return row;
      }));
      compareBody.replaceChildren(comparison, facts);
    };
    const closeComparison = () => { comparisonID = ""; comparisonSignature = ""; release(compareBody); compare?.close?.(); };
    const openComparison = id => {
      if (!compare || !controller.comparison(id)) return;
      comparisonID = id; comparisonSignature = "";
      if (typeof compare.showModal === "function") compare.showModal(); else compare.setAttribute("open", "");
      renderComparison();
    };
    const selectRun = key => {
      if (!last || !recentRuns(last).all.some(run => run.key === key)) return;
      selectedKey = key; selectionMade = true;
      inspector.dataset.supersedesResult = "true";
      if (last.filters.collapsed) controller.setCollapsed(false); else render(last);
      onReveal(); inspectorTitle?.focus({ preventScroll: true });
      if (!inspector.classList.contains("is-batch-viewer")) inspector?.scrollIntoView({ block: "nearest", behavior: "instant" });
    };
    const closeSelected = () => {
      const previousKey = selectedKey; selectedKey = ""; selectionMade = true;
      if (last) render(last);
      const tiles = [...(recent?.querySelectorAll("[data-recent-key]") || [])];
      (tiles.find(node => node.dataset.recentKey === previousKey) || tiles[0] || toggle)?.focus({ preventScroll: true });
    };
    const createTile = run => {
      const parent = run.group?.batch, job = runCover(run), preview = job?.preview;
      const state = parent?.state || job?.state, title = parent ? `${parent.total_count} вариантов` : templateLabel(job?.template_id);
      const label = parent ? batchLabels[state] || "Выполняется" : stateLabel(state);
      const tile = element("button", "studio-recent-tile"); tile.type = "button";
      tile.dataset.recentKey = run.key; tile.dataset.state = state || "";
      tile.setAttribute("aria-pressed", String(run.key === selectedKey)); tile.setAttribute("aria-controls", "generation-recent-inspector");
      tile.title = `${title} · ${label} · ${formatTime(run.created)} · ${cleanModelName(parent?.model_name || job?.model_name)}`;
      tile.setAttribute("aria-label", tile.title);
      const media = element("span", "studio-recent-cover");
      media.append(icon(preview?.sensitive ? "eye-off" : parent ? "layers" : preview?.source_type === "video" || job?.template_id === "minimax-h3-video" ? "video" : "image"));
      if (preview?.sensitive) media.append(element("span", "studio-recent-sensitive", "18+"));
      else if (preview?.url && Number(preview.expires_unix) > Date.now()) {
        try {
          const url = new URL(preview.url, window.location.origin);
          if (url.origin === window.location.origin && url.pathname === "/api/tasks/thumbnail") {
            const image = element("img"); image.alt = ""; image.loading = "lazy"; image.decoding = "async";
            image.dataset.recentExpiry = String(preview.expires_unix);
            image.addEventListener("error", () => image.remove(), { once: true }); image.src = url.href; media.append(image);
          }
        } catch (_) {}
      }
      tile.append(media, element("strong", "", title), element("small", "", label));
      tile.addEventListener("click", () => selectRun(run.key)); return tile;
    };
    const render = state => {
      last = { ...state, target: undefined, targetBatch: undefined }; if (!section || !list || !recent) return;
      if (state.targetBatch || state.target) {
        inspector.dataset.supersedesResult = "true";
        const group = state.groups.find(value => value.jobs.some(job => job.job_id === state.target));
        selectedKey = state.targetBatch ? `batch:${state.targetBatch}` : group ? `batch:${group.batch.batch_id}` : `job:${state.target}`;
        if (group && state.target) batchPositions.set(group.batch.batch_id, state.target);
        selectionMade = true;
      }
      let runs = recentRuns(state, selectedKey);
      const selectionLostFocus = Boolean(selectedKey && !runs.selected && inspector.contains(document.activeElement));
      if (!selectionMade && state.loaded) selectionMade = true;
      if (selectedKey && !runs.selected) selectedKey = "";
      if (content) content.hidden = state.filters.collapsed;
      if (toggle) {
        const label = state.filters.collapsed ? "Показать последние результаты" : "Свернуть последние результаты";
        toggle.title = label; toggle.setAttribute("aria-label", label); toggle.setAttribute("aria-expanded", String(!state.filters.collapsed));
      }
      const connection = state.authRequired ? "требуется вход" : state.error ? "не обновлены" : state.jobState.live ? "обновляются" : "подключение";
      if (count) count.textContent = `${runs.items.length} из ${runs.all.length} запусков · ${connection}`;
      const emptyCopy = byID("studio-result-empty")?.querySelector("p");
      if (emptyCopy) emptyCopy.textContent = runs.all.length ? "Выберите запуск в последних результатах" : "Пока ничего не создано";
      if (errorBanner) {
        errorBanner.hidden = !state.error; feedback.present(errorBanner, state.error ? "error" : ""); feedback.text(errorText, state.error);
        refresh.hidden = state.authRequired; feedback.button(refresh, { busy: state.jobState.loading, disabled: state.jobState.loading });
        login.hidden = !state.authRequired;
        login.href = `/login?${new URLSearchParams({ next: window.location.pathname + window.location.search })}`;
      }
      recent.setAttribute("aria-busy", String(state.jobState.loading));
      const tileFocus = recent.contains(document.activeElement) ? document.activeElement.dataset.recentKey : "";
      const tiles = runs.items.map(run => {
        const signature = JSON.stringify([run, run.key === selectedKey]), cached = tileCache.get(run.key);
        if (cached?.signature === signature) return cached.node;
        const node = createTile(run); tileCache.set(run.key, { signature, node }); return node;
      });
      if (!tiles.length) tiles.push(element("p", "generation-variant-empty ui-empty-state", state.authRequired ? "Войдите в аккаунт, чтобы открыть задания."
        : !state.loaded ? state.error ? "Задания пока не загружены." : "Загружаем задания..." : "Недавних запусков пока нет."));
      reconcile(recent, tiles);
      for (const [key] of tileCache) if (!runs.items.some(run => run.key === key)) tileCache.delete(key);
      if (tileFocus && document.activeElement === document.body) (tiles.find(node => node.dataset.recentKey === tileFocus) || tiles[0])?.focus({ preventScroll: true });
      const selected = runs.selected;
      for (const id of batchPositions.keys()) if (!state.groups.some(group => group.batch.batch_id === id)) batchPositions.delete(id);
      const resultViewer = Boolean(selected && !state.filters.collapsed);
      const batchViewer = Boolean(selected?.group && resultViewer);
      if (resultViewer && inspector.parentNode !== document.body) document.body.append(inspector);
      else if (!resultViewer && inspector.parentNode !== inspectorHome) inspectorHome?.insertBefore(inspector, inspectorNext);
      inspector.classList.toggle("is-batch-viewer", resultViewer);
      inspector.classList.toggle("is-single-viewer", Boolean(resultViewer && !batchViewer));
      document.body.classList.toggle("batch-viewer-open", resultViewer);
      inspector.dataset.batchState = selected?.group?.batch.state || selected?.job?.state || "";
      if (viewerMore) {
        viewerMore.hidden = !resultViewer;
        viewerMore.setAttribute("aria-expanded", String(Boolean(list.querySelector(".generation-batch-viewer-details")?.open)));
      }
      if (inspectorClose && inspectorClose.dataset.batchIcon !== String(resultViewer)) {
        inspectorClose.replaceChildren(icon(resultViewer ? "arrow-left" : "x"));
        inspectorClose.dataset.batchIcon = String(resultViewer);
        inspectorClose.title = resultViewer ? "Назад к результатам" : "Закрыть выбранный результат";
        inspectorClose.setAttribute("aria-label", inspectorClose.title);
      }
      if (resultViewer) {
        inspector.setAttribute("role", "dialog"); inspector.setAttribute("aria-modal", "true");
      } else {
        inspector.removeAttribute("role"); inspector.removeAttribute("aria-modal");
      }
      inspector.hidden = !selected || state.filters.collapsed;
      if (selected) {
        inspectorTitle.textContent = selected.group ? "Результаты пакета" : selected.job?.state === "completed" ? "Результат" : "Генерация";
        if (viewerStatus) {
          viewerStatus.hidden = !resultViewer;
          if (batchViewer) viewerStatus.textContent = selected.group.batch.state === "completed"
            ? `Готово · ${selected.group.batch.template_id === "minimax-h3-video" ? `${selected.group.batch.total_count} видео` : imageCountLabel(selected.group.batch.total_count)}`
            : `${batchLabels[selected.group.batch.state] || "Выполняется"} · ${selected.group.batch.completed_count || 0} из ${selected.group.batch.total_count || 0} готово`;
          else viewerStatus.textContent = selected.job?.state === "completed"
            ? `Готово · ${selected.job.media?.some(item => item.media_type === "video") ? "видео" : imageCountLabel(selected.job.media?.length || 1)}`
            : stateLabel(selected.job?.state);
        }
        selectedTask.href = `/tasks?${new URLSearchParams({ task: selected.group ? `generation_batch:${selected.group.batch.batch_id}` : `generation:${selected.job.job_id}` })}`;
      } else if (viewerStatus) viewerStatus.hidden = true;
      const signature = JSON.stringify([selected, state.batchState.selections, state.actions]);
      if (signature !== fingerprint) {
        fingerprint = signature;
        const active = document.activeElement, focused = list.contains(active) ? { node: active, action: active.dataset.resultAction,
          job: active.closest("[data-job-id]")?.dataset.jobId, batch: active.closest("section[data-batch-id]")?.dataset.batchId } : null;
        const nodes = [];
        if (selected?.group) nodes.push(createGroup(selected.group));
        else if (selected?.job) nodes.push(createSingleView(selected.job));
        list.classList.toggle("has-batches", Boolean(selected?.group));
        reconcile(list, nodes);
        const currentIDs = new Set((selected?.group?.jobs || (selected?.job ? [selected.job] : [])).map(job => job.job_id));
        for (const [id, cached] of cardCache) if (!currentIDs.has(id)) { release(cached.node); cardCache.delete(id); }
        restoreFocus(focused);
      }
      if (inspector.hidden) pause(list);
      renderDetails(); refreshExpiry(); renderComparison();
      if (selectionLostFocus) (tiles[0] || toggle)?.focus({ preventScroll: true });
      if (state.target || state.targetBatch) {
        const target = state.target ? [...list.querySelectorAll("[data-job-id]")].find(node => node.dataset.jobId === state.target) : null;
        if (target) {
          target.classList.add("is-notification-target");
          const details = list.querySelector(".generation-single-view-details");
          if (details) {
            details.open = true;
            const timeline = details.querySelector(".generation-batch-viewer-timeline");
            if (timeline) timeline.open = true;
          }
        }
        window.requestAnimationFrame(() => {
          if (inspector.hidden) return;
          onReveal(); inspectorTitle?.focus({ preventScroll: true });
        });
      }
    };
    const clear = () => {
      last = null; outputs([]); release(list); list?.replaceChildren(); recent?.replaceChildren(); cardCache.clear(); tileCache.clear(); batchPositions.clear(); fingerprint = "";
      document.body.classList.remove("batch-viewer-open");
      inspector?.classList.remove("is-batch-viewer");
      inspector?.classList.remove("is-single-viewer");
      if (inspector?.parentNode !== inspectorHome) inspectorHome?.insertBefore(inspector, inspectorNext);
      closeComparison(); viewer.close();
    };
    const setVisible = value => {
      visible = Boolean(value);
      if (timer !== null) { window.clearInterval(timer); timer = null; }
      if (visible) { refreshExpiry(); timer = window.setInterval(refreshExpiry, 30000); }
      else { pause(outputGrid); pause(list); pause(compareBody); }
    };
    const bind = value => {
      controller = value;
      byID("generation-recent-close")?.addEventListener("click", closeSelected);
      viewerMore?.addEventListener("click", () => {
        const details = list.querySelector(".generation-batch-viewer-details");
        if (!details) return;
        details.open = !details.open;
        viewerMore.setAttribute("aria-expanded", String(details.open));
      });
      inspector?.addEventListener("keydown", event => {
        if (!inspector.classList.contains("is-batch-viewer")) return;
        if (event.key === "Escape") { event.preventDefault(); closeSelected(); return; }
        if (event.key !== "Tab") return;
        const focusable = [...inspector.querySelectorAll("a[href], button:not([disabled]):not([hidden]), input:not([disabled]), summary")]
          .filter(node => node.getClientRects().length);
        if (!focusable.length) return;
        const first = focusable[0], last = focusable[focusable.length - 1];
        if (event.shiftKey && (document.activeElement === first || document.activeElement === inspectorTitle)) {
          event.preventDefault(); last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault(); first.focus();
        }
      });
      recent?.addEventListener("keydown", event => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        const tiles = [...recent.querySelectorAll("[data-recent-key]")], index = tiles.indexOf(event.target);
        if (index < 0) return;
        event.preventDefault();
        const next = event.key === "Home" ? 0 : event.key === "End" ? tiles.length - 1 : Math.max(0, Math.min(tiles.length - 1, index + (event.key === "ArrowRight" ? 1 : -1)));
        tiles[next].focus({ preventScroll: true }); tiles[next].scrollIntoView({ block: "nearest", inline: "nearest", behavior: "instant" });
      });
      toggle?.addEventListener("click", () => controller.setCollapsed(!controller.snapshot().filters.collapsed));
      refresh?.addEventListener("click", () => controller.refresh());
      byID("generation-batch-compare-close")?.addEventListener("click", closeComparison);
      compare?.addEventListener("click", event => { if (event.target === compare) closeComparison(); });
      compare?.addEventListener("close", () => { comparisonID = ""; release(compareBody); });
      document.addEventListener("generation-repeat-accepted", () => controller.refresh());
    };
    const submissionPhase = phase => {
      if (inspector && !["idle", "finished"].includes(phase)) inspector.dataset.supersedesResult = "false";
      if (phase === "sending" && inspector?.classList.contains("is-batch-viewer")) closeSelected();
    };
    return { bind, render, outputs, jobMedia, clear, setVisible, refreshExpiry, submissionPhase,
      openJob: id => {
        const key = `job:${id}`;
        if (!last || !recentRuns(last).all.some(run => run.key === key)) return false;
        selectRun(key); return true;
      },
      suspend: () => { closeComparison(); viewer.close(); setVisible(false); } };
  };
  return { stateLabel, templateLabel, formatDuration, formatTime, formatExpiry, cleanModelName, countLabel, recentRuns, runCover, createView };
});
