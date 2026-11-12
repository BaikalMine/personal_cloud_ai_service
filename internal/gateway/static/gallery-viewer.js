(function () {
  "use strict";
  const api = window.AIGatewayGallery;
  if (!api) return;
  const element = (tag, text, className) => { const node = document.createElement(tag); if (text !== undefined) node.textContent = text; if (className) node.className = className; return node; };
  const text = (card, selector) => card.querySelector(selector)?.textContent.trim() || "";
  api.cardSource = card => {
    const trigger = card?.querySelector("[data-gallery-open]");
    if (!trigger) return null;
    let postprocess = null;
    try { const value = JSON.parse(card.dataset.postprocess || "null"); if (window.AIGatewayPostprocess?.valid(value)) postprocess = value; } catch (_) {}
    return { card, trigger, operation: card.dataset.operation || "generation", postprocess, jobID: card.dataset.jobId || "", mediaID: card.querySelector("[data-media-id]")?.dataset.mediaId || "",
      type: trigger.dataset.mediaType, url: trigger.dataset.url, filename: trigger.dataset.filename, expiresAt: Number(trigger.dataset.expires),
      sizeBytes: Number(trigger.dataset.size), sensitive: card.dataset.sensitive === "true", prompt: text(card, ".media-library-prompt"),
      model: text(card, ".media-library-model"), seed: text(card, "[data-gallery-seed]"), workflow: text(card, "[data-gallery-workflow-name]") };
  };
  const isRevealed = source => !source.sensitive || source.card.classList.contains("is-revealed") || document.body.classList.contains("show-sensitive-content");
  const date = value => { const time = new Date(value); return Number.isFinite(time.getTime()) ? time.toLocaleString("ru-RU") : "Не записано"; };
  const number = value => typeof value === "number" && Number.isFinite(value) ? value.toLocaleString("ru-RU", { maximumFractionDigits: 3 }) : String(value ?? "");
  const bytes = value => `${number(value / 1024 / 1024)} МБ`;
  const facts = (host, entries) => {
    host.replaceChildren();
    for (const [label, value] of entries) if (value !== undefined && value !== null && value !== "") host.append(element("dt", label), element("dd", String(value)));
  };
  const request = async (url, signal) => {
    const response = await fetch(url, { signal, credentials: "same-origin", headers: { Accept: "application/json" }, cache: "no-store" });
    const raw = await response.text();
    if (!response.ok) { const error = new Error("read_failed"); error.status = response.status; throw error; }
    return { data: JSON.parse(raw), raw };
  };

  api.createViewer = function ({ root, cards, use, organize, status, onSelect = () => {} }) {
    const dialog = root.querySelector("#gallery-lightbox"), compare = root.querySelector("#gallery-compare-dialog");
    const q = selector => dialog.querySelector(selector);
    const stage = q("[data-viewer-stage]");
    let selected = null, viewport = null, passportURL = "", trigger = null, compareGroup = null, playbackCleanup = null;
    let compared = [], compareTrigger = null;
    const trap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: dialog, documentRef: document });
    const compareTrap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: compare, documentRef: document });
    function clearPassportURL() { if (passportURL) URL.revokeObjectURL(passportURL); passportURL = ""; q("[data-inspector-download-passport]").removeAttribute("href"); }
    function clearMedia(message) {
      viewport?.destroy(); viewport = null;
      stage.replaceChildren(element("p", message, "gallery-media-message"));
    }
    function link(host, label, url) { const anchor = element("a", label, "button ghost"); anchor.href = url; host.append(anchor); }
    function renderInspector(state) {
      clearPassportURL();
      const source = state.source;
      if (!source) return;
      const passport = state.passport, execution = passport?.execution, input = execution?.resolved_input;
      const postprocess = source.operation === "postprocess", processing = passport?.postprocess || source.postprocess;
      q("[data-inspector-status]").textContent = ({ idle: "", loading: "Загружаем паспорт...", paused: "Просмотр приостановлен", legacy: "Паспорт запуска не сохранён.", error: "Не удалось загрузить паспорт.", unavailable: "Результат недоступен или срок хранения истёк." })[state.phase]
        ?? (passport?.evidence_state === "submission_recorded" ? passport.submission_rejected ? "Отправка запуска была отклонена." : !passport.submission_confirmed ? "Приём запуска не подтверждён." : "Параметры записаны при отправке запуска." : "Исторические параметры запуска не записаны.");
      q("[data-inspector-retry]").hidden = state.phase !== "error";
      const available = state.phase !== "unavailable";
      q("[data-inspector-prompt]").closest("section").hidden = postprocess;
      q("[data-inspector-prompt]").textContent = available ? input?.Positive || source.prompt || "Описание не записано" : "";
      q("[data-inspector-copy]").disabled = !available || !(input?.Positive || source.prompt);
      const parameters = [];
      if (postprocess) {
        if (available) parameters.push(...(window.AIGatewayPostprocess?.facts(processing) || []));
      } else if (input) {
        parameters.push(["Модель", input.ModelName || source.model], ["Seed", execution.seed || passport.seed], ["Workflow", source.workflow || execution.workflow?.id]);
        if (input.Negative) parameters.push(["Исключить", input.Negative]);
        for (const [key, label] of [["Width", "Ширина (задано)"], ["Height", "Высота (задано)"], ["Steps", "Шаги"], ["CFG", "CFG"], ["Sampler", "Сэмплер"], ["Scheduler", "Расписание"], ["BaseMegapixels", "Базовый размер, Мп"], ["OutputMegapixels", "Итоговый размер, Мп"], ["UpscaleFactor", "Множитель апскейла"], ["VideoDurationSeconds", "Длительность (задано), с"], ["VideoResolution", "Видеоразрешение (задано)"]]) {
          if (input[key] !== undefined && input[key] !== "" && input[key] !== 0) parameters.push([label, number(input[key])]);
        }
        const loras = [...new Set((execution.components || []).filter(item => item.kind === "lora").map(item => item.name))];
        if (loras.length) parameters.push(["LoRA", loras.map(name => {
          const component = execution.components.find(item => item.kind === "lora" && item.name === name);
          const controls = execution.graph?.[component?.node_id]?.inputs;
          const weights = [];
          for (const [key, label] of [["strength_model", "Model"], ["strength_clip", "CLIP"], ["strength", "Сила"]]) if (typeof controls?.[key] === "number") weights.push(`${label}: ${number(controls[key])}`);
          return weights.length ? `${name}\n${weights.join(" · ")}` : name;
        }).join("\n\n")]);
        if (source.type === "image") {
          const fluxMode = { none: "Нет", ultimate: "Ultimate", seedvr2: "SeedVR2", both: "Ultimate + SeedVR2" }[input.FluxUpscaleMode];
          if (fluxMode) parameters.push(["Постобработка", fluxMode]);
          if (input.UpscaleSteps > 0) parameters.push(["Шаги апскейла", number(input.UpscaleSteps)], ["Denoise апскейла", number(input.UpscaleDenoise)]);
          if (input.PreserveOriginalSize) parameters.push(["Размер исходника", "Сохранить"]);
        }
        if (input.VideoRIFEEnabled) parameters.push(["Интерполяция", `RIFE ${number(input.VideoRIFEMultiplier)}×`]);
        if (input.VideoRTXEnabled) parameters.push(["Видеоапскейл", `RTX ${number(input.VideoRTXScale)}×`]);
        if (input.AIHubDLSSEnabled) parameters.push(["DLSS 5", `${Math.round(Number(input.AIHubDLSSStrength) * 100)}%`]);
        if (input.AIHubFGEnabled) parameters.push(["Плавность движения", `DLSS FG · ${number(24 * Number(input.AIHubFGMultiplier))} FPS`]);
      } else if (available) parameters.push(["Модель (библиотека)", source.model], ["Seed (библиотека)", source.seed]);
      facts(q("[data-inspector-parameters]"), parameters);
      const measured = state.measurement;
      q("[data-inspector-measurement-status]").textContent = measured ? "Измерено по готовому файлу."
        : state.measurementPhase === "loading" ? "Проверяем готовый файл..." : state.measurementPhase === "error" ? "Измерения недоступны. Размер и частота кадров не подтверждены." : "Измерения готового файла не записаны.";
      if (postprocess && measured) q("[data-inspector-measurement-status]").textContent += " " + (window.AIGatewayPostprocess?.differences(processing, measured) || []).join(" ");
      q("[data-inspector-measure]").hidden = state.phase !== "ready" || state.measurementPhase !== "error";
      facts(q("[data-inspector-measurements]"), measured ? [["Размер кадра", `${measured.width} × ${measured.height}`], ["Файл", bytes(measured.size_bytes)], ["Кодек", measured.codec], ["Длительность, с", measured.duration_seconds], ["Средняя частота, FPS", measured.average_fps], ["Пропорции пикселя", measured.sample_aspect_ratio], ["Поворот, °", measured.rotation_degrees], ["Проверен", date(measured.measured_at)]] : []);
      facts(q("[data-inspector-lineage]"), available ? [["Создан", date(source.card.dataset.created)], ["Хранится до", source.expiresAt > 0 ? date(source.expiresAt) : "Не записано"], ["Закреплён", source.card.dataset.pinned === "true" ? "Да" : "Нет"], ["Исходный запуск", passport?.parent_job_id ? passport.parent_available ? "Доступен" : "Больше не доступен" : "Не записан"]] : []);
      const links = q("[data-inspector-links]"); links.replaceChildren();
      if (available && api.validID(source.jobID)) link(links, postprocess ? "Открыть задачу" : "Открыть запуск", postprocess ? `/tasks?task=generation:${encodeURIComponent(source.jobID)}` : `/generate?job=${encodeURIComponent(source.jobID)}`);
      if (available && passport?.parent_available && api.validID(passport.parent_job_id)) link(links, postprocess ? "Исходная задача" : "Исходный запуск", postprocess ? `/tasks?task=generation:${encodeURIComponent(passport.parent_job_id)}` : `/generate?job=${encodeURIComponent(passport.parent_job_id)}`);
      if (available && postprocess) for (const source of window.AIGatewayPostprocess?.sources(processing) || []) {
        if (source.href) link(links, source.label, source.href);
        else links.append(element("p", source.message));
      }
      if (available) for (const asset of execution?.assets || []) {
        const role = { first_frame: "Первый кадр", last_frame: "Последний кадр", base_scene: "Основная сцена", identity: "Внешность", wardrobe_object: "Одежда или предмет", pose_composition: "Поза и композиция", style: "Стиль", background: "Фон", details: "Детали", motion_timing: "Движение", voice_sound: "Звук" }[asset.role] || "Исходный материал";
        const label = `${role}: ${asset.source_name || asset.input_name || asset.number}`;
        if (asset.source === "gallery" && api.validMediaID(asset.source_id)) link(links, label, `/gallery?media=${encodeURIComponent(asset.source_id)}`);
        else links.append(element("p", label));
      }
      if (available) for (const reference of source.card.querySelectorAll("[data-reference-job]")) if (api.validID(reference.dataset.referenceJob)) link(links, `Референс ${reference.dataset.referenceNumber || ""}`, `/generate?job=${encodeURIComponent(reference.dataset.referenceJob)}`);
      q("[data-inspector-organization]").textContent = available ? [...source.card.querySelectorAll("[data-gallery-tag], [data-gallery-collection-id]")].map(item => item.textContent).join(" · ") || "Без тегов и коллекций" : "";
      q("[data-inspector-organize]").disabled = !available;
      const retainedPlan = postprocess && source.postprocess && state.phase === "legacy";
      const raw = retainedPlan ? JSON.stringify({ evidence_state: "retained_plan_only", postprocess: source.postprocess }, null, 2) : state.raw;
      const technicalAvailable = Boolean(raw) && (state.phase === "ready" || retainedPlan);
      q("[data-inspector-technical]").hidden = !technicalAvailable;
      q("[data-inspector-raw]").textContent = raw || "";
      q("[data-inspector-evidence]").textContent = retainedPlan ? "Сохранённый план обработки. Снимок исполнения больше недоступен."
        : "Снимок отправленного запуска и наблюдения файлов. Они не подтверждают побитовую идентичность вычислений на GPU.";
      if (technicalAvailable) {
        passportURL = URL.createObjectURL(new Blob([raw], { type: "application/json" }));
        const download = q("[data-inspector-download-passport]"); download.href = passportURL; download.download = `passport-${source.jobID || source.mediaID}.json`;
      }
      if (!available) { clearMedia("Результат больше не доступен"); q("[data-gallery-lightbox-download]").removeAttribute("href"); q("[data-viewer-use]").disabled = true; q("[data-viewer-postprocess]").disabled = true; }
    }
    const controller = api.createInspectorController({
      readPassport: (source, signal) => request(`/generate/jobs/passport?job=${encodeURIComponent(source.jobID)}`, signal),
      readMeasurement: async (source, signal) => (await request(`/generate/jobs/output-measurement?job=${encodeURIComponent(source.jobID)}&media=${encodeURIComponent(source.mediaID)}`, signal)).data,
      onState: renderInspector,
    });
    function tab(name, focus = false) {
      dialog.dataset.tab = name;
      for (const button of dialog.querySelectorAll("[data-viewer-tab]")) {
        const active = button.dataset.viewerTab === name;
        button.setAttribute("aria-selected", String(active)); button.tabIndex = active ? 0 : -1;
        if (active && focus) button.focus();
      }
      viewport?.render();
    }
    function close() {
      controller.close(); viewport?.destroy(); viewport = null; selected = null; clearPassportURL();
      if (dialog.open) dialog.close(); dialog.hidden = true; trap?.deactivate();
    }
    function navigation() {
      const sources = cards().filter(card => !card.hidden && api.cardSource(card));
      const index = sources.indexOf(selected?.card);
      q("[data-viewer-index]").textContent = index >= 0 ? `${index + 1} / ${sources.length}` : "";
      q("[data-viewer-previous]").disabled = index <= 0;
      q("[data-viewer-next]").disabled = index < 0 || index >= sources.length - 1;
      return { sources, index };
    }
    function showMedia(source) {
      viewport?.destroy();
      viewport = api.createViewportGroup({ toolbar: q("[data-viewer-zoom]"), comparisonToggle: q("[data-viewer-original]"), onError: (_, host) => { host.replaceChildren(element("p", "Не удалось загрузить файл. Закройте и откройте результат для повторной попытки.", "gallery-media-message")); } });
      viewport.add(stage, source, { originalJobID: source.card.dataset.templateId === "image-to-image" ? source.jobID : "" });
      void controller.select(source);
    }
    function select(card) {
      controller.close(); viewport?.destroy(); viewport = null;
      q("[data-viewer-original]").hidden = true;
      selected = api.cardSource(card);
      if (!selected) return;
      const source = selected;
      q("[data-gallery-lightbox-name]").textContent = source.filename;
      q("[data-viewer-use]").hidden = source.type !== "image" || !card.querySelector("[data-gallery-use-open]");
      q("[data-viewer-use]").disabled = false;
      q("[data-viewer-postprocess]").hidden = !api.validID(source.jobID) || !api.validMediaID(source.mediaID) || !["image", "video"].includes(source.type);
      q("[data-viewer-postprocess]").disabled = api.expired(source, Date.now());
      q("[data-gallery-lightbox-repeat]").hidden = source.operation === "postprocess" || !api.validID(source.jobID);
      q("[data-gallery-lightbox-repeat-now]").hidden = source.operation === "postprocess" || !api.validID(source.jobID);
      const url = api.safeURL(source.url, location.origin);
      const download = q("[data-gallery-lightbox-download]");
      if (url) { const target = new URL(url, location.origin); target.searchParams.set("download", "1"); download.href = target.pathname + target.search; download.download = source.filename; } else download.removeAttribute("href");
      navigation(); onSelect(source);
      if (api.expired(source, Date.now())) { void controller.select(source); return; }
      if (!isRevealed(source)) {
        renderInspector({ phase: "idle", source });
        q("[data-inspector-status]").textContent = "Контент 18+ скрыт.";
        const reveal = element("button", "Показать контент 18+", "button"); reveal.type = "button";
        stage.replaceChildren(reveal);
        reveal.addEventListener("click", () => { window.aiGatewaySensitiveContent?.reveal(source.card); showMedia(source); });
      } else showMedia(source);
    }
    function open(card, from, inspector = false) {
      trigger = from || card.querySelector("[data-gallery-open]");
      dialog.hidden = false; if (!dialog.open) dialog.showModal();
      tab(inspector ? "facts" : "media"); select(card);
      trap?.activate({ trigger, initialFocus: q("[data-gallery-close]"), onEscape: close });
    }
    function move(delta) { const { sources, index } = navigation(); if (sources[index + delta]) select(sources[index + delta]); }
    q("[data-viewer-previous]").addEventListener("click", () => move(-1));
    q("[data-viewer-next]").addEventListener("click", () => move(1));
    q("[data-gallery-close]").addEventListener("click", close);
    dialog.addEventListener("cancel", event => { event.preventDefault(); close(); });
    dialog.addEventListener("keydown", event => {
      if (event.key === "Escape" || event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || event.target.closest("input, textarea, select, video, [role=tablist]")) return;
      if (["ArrowLeft", "ArrowRight"].includes(event.key)) { event.preventDefault(); move(event.key === "ArrowLeft" ? -1 : 1); }
    });
    for (const button of dialog.querySelectorAll("[data-viewer-tab]")) {
      button.addEventListener("click", () => tab(button.dataset.viewerTab));
      button.addEventListener("keydown", event => { if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) { event.preventDefault(); tab(event.key === "Home" ? "media" : event.key === "End" ? "facts" : button.dataset.viewerTab === "media" ? "facts" : "media", true); } });
    }
    q("[data-inspector-retry]").addEventListener("click", () => controller.retry());
    q("[data-inspector-measure]").addEventListener("click", () => controller.measure());
    q("[data-inspector-copy]").addEventListener("click", async () => { try { await navigator.clipboard.writeText(q("[data-inspector-prompt]").textContent); status("Описание скопировано."); } catch (_) { status("Не удалось скопировать описание.", "error"); } });
    q("[data-viewer-use]").addEventListener("click", () => { const card = selected?.card, from = trigger; close(); if (card) use(card, from); });
    q("[data-inspector-organize]").addEventListener("click", () => { const card = selected?.card, from = trigger; close(); if (card) organize(card, from); });
    q("[data-gallery-lightbox-repeat]").addEventListener("click", () => { if (selected?.operation === "postprocess") return; const id = selected?.jobID, from = trigger; close(); window.AIGatewayGeneration?.openRepeat?.(id, from); });
    q("[data-gallery-lightbox-repeat-now]").addEventListener("click", () => { if (selected?.operation === "postprocess") return; const id = selected?.jobID, from = trigger; close(); window.AIGatewayGeneration?.openRepeatNow?.(id, from); });
    q("[data-viewer-postprocess]").addEventListener("click", () => { const source = selected, from = trigger; close(); window.AIGatewayGeneration?.openPostprocess?.(source, from); });

    function closeCompare() {
      playbackCleanup?.(); playbackCleanup = null; compareGroup?.destroy(); compareGroup = null; compared = [];
      compare.querySelector("[data-gallery-compare-grid]").replaceChildren();
      if (compare.open) compare.close(); compare.hidden = true; compareTrap?.deactivate();
    }
    function playback(videos) {
      const bar = compare.querySelector("[data-compare-playback]"); bar.hidden = videos.length !== 2;
      if (videos.length !== 2) return;
      const play = bar.querySelector("[data-compare-play]"), seek = bar.querySelector("[data-compare-time]"), label = bar.querySelector("[data-compare-time-label]");
      const playbackStatus = message => { const target = bar.querySelector("[data-compare-playback-status]"); target.textContent = message; target.hidden = !message; };
      playbackStatus("");
      let frame = 0, playing = false, starting = false, disposed = false, playEpoch = 0;
      const duration = () => videos.every(video => Number.isFinite(video.duration) && video.duration > 0) ? Math.min(...videos.map(video => video.duration)) : 0;
      const drawButton = () => { const active = playing || starting; play.replaceChildren(); const icon = element("i"); icon.dataset.lucide = active ? "pause" : "play"; icon.setAttribute("aria-hidden", "true"); play.append(icon); play.title = active ? "Приостановить оба видео" : "Воспроизвести оба видео"; play.setAttribute("aria-label", play.title); window.lucide?.createIcons(); };
      const pause = () => { playEpoch++; playing = starting = false; videos.forEach(video => video.pause()); cancelAnimationFrame(frame); drawButton(); };
      const update = () => {
        if (disposed) return;
        const end = duration(), time = videos[0].currentTime;
        seek.disabled = play.disabled = end <= 0;
        seek.value = String(end > 0 ? Math.round(time / end * 1000) : 0); label.textContent = `${time.toFixed(1)} / ${end.toFixed(1)} с`;
        if (playing && time >= end - 0.03) { pause(); return; }
        if (playing && !videos[1].seeking && Math.abs(videos[1].currentTime - time) > 0.08) videos[1].currentTime = time;
        if (playing) frame = requestAnimationFrame(update);
      };
      const toggle = async () => {
        if (playing || starting) { pause(); return; }
        if (!duration()) return;
        const token = ++playEpoch;
        playbackStatus("");
        if (videos[0].currentTime >= duration() - 0.05) videos[0].currentTime = 0;
        if (Math.abs(videos[1].currentTime - videos[0].currentTime) > 0.01) videos[1].currentTime = videos[0].currentTime;
        starting = true; drawButton();
        try {
          await Promise.all(videos.map(video => video.play()));
          if (disposed || token !== playEpoch) return;
          starting = false; playing = true; drawButton(); update();
        } catch (_) { if (!disposed && token === playEpoch) { pause(); playbackStatus("Не удалось запустить оба видео. Повторите воспроизведение."); } }
      };
      const seekTo = () => { pause(); const time = duration() * Number(seek.value) / 1000; videos.forEach(video => { video.currentTime = time; }); update(); };
      const stalled = () => { if (playing && videos.some(video => video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA)) { pause(); playbackStatus("Оба видео приостановлены: ожидается загрузка. Повторите воспроизведение после загрузки."); } };
      videos.forEach(video => { video.muted = true; video.addEventListener("loadedmetadata", update); video.addEventListener("waiting", stalled); video.addEventListener("ended", pause); });
      play.addEventListener("click", toggle); seek.addEventListener("input", seekTo); drawButton(); update();
      playbackCleanup = () => { disposed = true; pause(); play.removeEventListener("click", toggle); seek.removeEventListener("input", seekTo); videos.forEach(video => { video.removeEventListener("loadedmetadata", update); video.removeEventListener("waiting", stalled); video.removeEventListener("ended", pause); }); };
    }
    function openCompare(pair, from) {
      if (pair.length !== 2 || pair[0] === pair[1]) { status("Выберите два результата для сравнения."); return; }
      const sources = pair.map(api.cardSource);
      if (sources.some(source => !source || api.expired(source, Date.now()))) { status("Один из результатов больше не доступен.", "error"); return; }
      closeCompare(); compared = sources; compareTrigger = from;
      compare.hidden = false; compare.showModal();
      const host = compare.querySelector("[data-gallery-compare-grid]");
      compareGroup = api.createViewportGroup({ toolbar: compare.querySelector("[data-compare-zoom]"), onError: (_, pane) => { playbackCleanup?.(); playbackCleanup = null; compare.querySelector("[data-compare-playback]").hidden = true; pane.replaceChildren(element("p", "Файл не удалось загрузить", "gallery-media-message")); } });
      const videos = [];
      compare.querySelector("[data-compare-playback]").hidden = true;
      let loaded = 0;
      sources.forEach((source, index) => {
        const figure = element("figure"), pane = element("div", undefined, "gallery-media-viewport"); pane.tabIndex = 0; pane.setAttribute("aria-label", `Вариант ${index === 0 ? "A" : "B"}`);
        const caption = element("figcaption"); caption.append(element("strong", `${index === 0 ? "A" : "B"} · ${source.model}`), element("span", source.filename));
        figure.append(pane, caption); host.append(figure);
        const load = () => { const media = compareGroup.add(pane, source, { nativeControls: sources.some(item => item.type !== "video") }); if (media instanceof HTMLVideoElement) videos[index] = media; loaded++; if (loaded === 2 && sources.every(item => item.type === "video")) playback(videos); };
        if (!isRevealed(source)) { const reveal = element("button", "Показать контент 18+", "button"); pane.append(reveal); reveal.addEventListener("click", () => { window.aiGatewaySensitiveContent?.reveal(source.card); load(); }, { once: true }); } else load();
      });
      compareTrap?.activate({ trigger: compareTrigger, initialFocus: compare.querySelector("[data-gallery-compare-close]"), onEscape: closeCompare });
    }
    function compareFrom(card, from) {
      const chosen = cards().filter(item => item.querySelector("[data-gallery-select]")?.checked && item !== card);
      const sibling = chosen[0] || cards().find(item => item !== card && !item.hidden && item.dataset.variantId === card.dataset.variantId && api.cardSource(item));
      if (sibling) openCompare([card, sibling], from);
      else { const checkbox = card.querySelector("[data-gallery-select]"); if (checkbox) { checkbox.checked = true; checkbox.dispatchEvent(new Event("change", { bubbles: true })); } status("Выберите второй результат и нажмите сравнение в панели выбора."); }
    }
    q("[data-viewer-compare]").addEventListener("click", () => { const card = selected?.card, from = trigger; close(); if (card) compareFrom(card, from); });
    compare.querySelector("[data-gallery-compare-close]").addEventListener("click", closeCompare);
    compare.addEventListener("cancel", event => { event.preventDefault(); closeCompare(); });
    document.addEventListener("visibilitychange", () => {
      if (dialog.open) { controller.setVisible(!document.hidden); if (document.hidden) viewport?.pause(); }
      if (document.hidden && compare.open) { playbackCleanup?.(); playbackCleanup = null; compareGroup?.pause(); closeCompare(); }
    });
    window.setInterval(() => {
      if (selected && api.expired(selected, Date.now())) void controller.select(selected);
      if (compared.some(source => api.expired(source, Date.now()))) { closeCompare(); status("Срок хранения одного из результатов истёк.", "error"); }
    }, 10000);
    return { open, close, compareFrom, openCompare, closeCompare, selected: () => selected };
  };
})();
