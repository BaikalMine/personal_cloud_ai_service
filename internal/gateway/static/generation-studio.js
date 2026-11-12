(function (host) {
  const activeImageLimit = ({ requiresImage, allowsImages, maximum, videoMode, isVideo }) => {
    if (!requiresImage && !allowsImages) return 0;
    const limit = Math.max(0, Math.min(10, Number(maximum) || 0));
    return isVideo && videoMode === "frames" ? Math.min(2, limit) : limit;
  };
  const chooseModel = (models, defaultID) => models.find(model => model.value && model.value === defaultID && !model.disabled && !model.hidden)
    || models.find(model => model.value && !model.disabled && !model.hidden);
  const settingChanges = (before, after) => Object.keys(after).filter(name => before[name] && before[name].value !== after[name].value)
    .map(name => ({ name, label: after[name].label, before: before[name].value, after: after[name].value }));
  const compactModelLabel = label => {
    const original = String(label || "");
    const filename = /[/\\_]|\.safetensors\b/i.test(original);
    let name = original.split(/\s*[/\\]\s*/).at(-1).replace(/\.safetensors\b/ig, "");
    if (filename) name = name.replace(/([a-z])([A-Z])/g, "$1 $2");
    name = name.replace(/_+/g, " ").replace(/\s+/g, " ").trim();
    return name.replace(/\b(?:bf16|fp(?:8|16|32)|int(?:4|8))\b/ig, part => part.toUpperCase())
      .replace(/^\p{Ll}/u, part => part.toUpperCase());
  };
  const fragmentBounds = (start, duration, mediaDuration) => {
    const total = Number(mediaDuration);
    if (!Number.isFinite(total) || total <= 0) return null;
    const offset = Number(String(start).replace(",", ".")) || 0;
    const from = Math.max(0, Math.min(total, offset < 0 ? total + offset : offset));
    return { start: from, end: Math.min(total, from + Math.max(0, Number(String(duration).replace(",", ".")) || 0)) };
  };
  function bindReferencePlayer({ media, button, status, start, duration, fallback, urlAPI = host.URL }) {
    if (!media) return null;
    let objectURL = "";
    let fragmentEnd = null;
    let revision = 0, fallbackAttempted = false, fallbackFailed = false, pending = null, sourceDuration = null;
    const pause = () => { media.pause(); fragmentEnd = null; };
    const clear = () => {
      revision += 1;
      pending?.abort(); pending = null;
      fallbackAttempted = fallbackFailed = false;
      sourceDuration = null;
      pause();
      media.removeAttribute("src");
      media.load();
      if (objectURL) urlAPI.revokeObjectURL(objectURL);
      objectURL = "";
      if (button) button.disabled = true;
      if (status) status.textContent = "";
    };
    const recover = async () => {
      if (!fallback || fallbackAttempted || !media.getAttribute("src")) return false;
      const expected = revision;
      fallbackAttempted = true; fallbackFailed = false;
      pending = new AbortController();
      const controller = pending;
      pause();
      if (button) button.disabled = true;
      if (status) status.textContent = "Подготавливаем совместимое превью...";
      try {
        const result = await fallback({ signal: controller.signal });
        if (revision !== expected || controller.signal.aborted) return true;
        if (objectURL) urlAPI.revokeObjectURL(objectURL);
        objectURL = urlAPI.createObjectURL(result.blob);
        sourceDuration = Number(result.duration) > 0 ? Number(result.duration) : Number.isFinite(media.duration) ? media.duration : null;
        media.src = objectURL;
        if (status) status.textContent = sourceDuration > 30 ? "Совместимое превью · первые 30 секунд" : "Совместимое превью";
      } catch (error) {
        if (revision !== expected || controller.signal.aborted) return true;
        fallbackFailed = true;
        if (button) button.disabled = false;
        if (status) status.textContent = error?.message || "Не удалось подготовить предпросмотр. Повторите попытку.";
      } finally { if (pending === controller) pending = null; }
      return true;
    };
    media.addEventListener("loadedmetadata", () => { if (button) button.disabled = !Number.isFinite(media.duration) || media.duration <= 0; });
    media.addEventListener("loadeddata", () => { if (fallback && media.videoWidth === 0) recover(); });
    media.addEventListener("error", () => {
      if (!media.getAttribute("src")) return;
      if (fallback && !fallbackAttempted) { recover(); return; }
      pause();
      if (button) button.disabled = true;
      if (status) status.textContent = "Браузер не воспроизводит этот файл. Для проверки нужен совместимый предпросмотр.";
    });
    media.addEventListener("timeupdate", () => { if (fragmentEnd !== null && media.currentTime >= fragmentEnd) pause(); });
    media.addEventListener("pause", () => { fragmentEnd = null; });
    button?.addEventListener("click", async () => {
      if (fallbackFailed) { fallbackAttempted = false; await recover(); return; }
      const bounds = fragmentBounds(start(), duration(), media.duration);
      if (!bounds || bounds.end <= bounds.start) {
        if (status) status.textContent = "Начало фрагмента находится за пределами файла.";
        return;
      }
      if (status) status.textContent = "";
      media.currentTime = bounds.start;
      fragmentEnd = bounds.end;
      try { await media.play(); } catch (_) {
        fragmentEnd = null;
        if (status) status.textContent = "Не удалось начать воспроизведение. Попробуйте кнопку плеера.";
      }
    });
    return { clear, pause, get sourceDuration() { return sourceDuration; }, setSource(source) {
      clear();
      if (!source) return;
      try {
        if (typeof source !== "string") objectURL = urlAPI.createObjectURL(source);
        media.src = objectURL || source;
      } catch (_) { if (status) status.textContent = "Предпросмотр файла недоступен."; }
    } };
  }
  function bindUI({ root, window, document }) {
    const workspace = root.querySelector(".studio-workspace");
    const tabs = [...root.querySelectorAll("[data-studio-tab]")];
    const settings = root.querySelector("#studio-settings");
    const result = root.querySelector("#generation-result");
    const empty = root.querySelector("#studio-result-empty");
    const historyResult = root.querySelector("#generation-recent-inspector");
    const narrow = window.matchMedia("(max-width: 899px)");
    const groups = [...root.querySelectorAll("[data-studio-option-group]")];
    const shortcuts = [...root.querySelectorAll("[data-studio-settings-target]")];
    const picker = root.querySelector("#studio-workflow-dialog");
    const pickerTrigger = root.querySelector("#studio-workflow-open");
    const pickerLabel = root.querySelector("#studio-workflow-current");
    const workflowChoices = () => [...root.querySelectorAll(".generation-workflow-choice")];
    const syncWorkflowPicker = () => {
      const choices = workflowChoices();
      const selected = choices.find(choice => !choice.hidden && choice.classList.contains("is-selected"));
      const label = selected?.querySelector("strong")?.textContent?.trim() || "Выберите схему";
      if (pickerLabel && pickerLabel.textContent !== label) pickerLabel.textContent = label;
      if (pickerTrigger) pickerTrigger.disabled = !choices.some(choice => !choice.hidden);
      const model = root.querySelector("#generation-model");
      if (!model) return;
      [...model.options].filter(option => option.value).forEach(option => {
        if (option.textContent !== option.dataset.compactLabel) option.dataset.fullLabel = option.textContent.trim();
        const compact = compactModelLabel(option.dataset.fullLabel);
        if (option.textContent !== compact) option.textContent = compact;
        option.dataset.compactLabel = compact;
        option.title = option.dataset.fullLabel;
      });
      model.title = model.selectedOptions[0]?.dataset.fullLabel || "";
    };
    const openWorkflowPicker = () => {
      if (!picker || !pickerTrigger || picker.open || pickerTrigger.disabled) return;
      syncWorkflowPicker();
      picker.showModal();
      document.body.classList.add("studio-picker-open");
      pickerTrigger.setAttribute("aria-expanded", "true");
      const choices = workflowChoices().filter(choice => !choice.hidden && !choice.disabled);
      (choices.find(choice => choice.classList.contains("is-selected")) || choices[0])?.focus({ preventScroll: true });
    };
    pickerTrigger?.addEventListener("click", openWorkflowPicker);
    root.querySelector("#studio-workflow-close")?.addEventListener("click", () => picker.close());
    picker?.addEventListener("close", () => {
      document.body.classList.remove("studio-picker-open");
      pickerTrigger?.setAttribute("aria-expanded", "false");
      pickerTrigger?.focus({ preventScroll: true });
    });
    picker?.addEventListener("click", event => {
      const choice = event.target.closest(".generation-workflow-choice");
      if (choice && !choice.disabled) { picker.close(); return; }
      if (event.target !== picker) return;
      const bounds = picker.getBoundingClientRect();
      if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) picker.close();
    });
    picker?.addEventListener("keydown", event => {
      if (!event.target.closest(".generation-workflow-choice") || !["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
      const choices = workflowChoices().filter(choice => !choice.hidden && !choice.disabled);
      if (!choices.length) return;
      event.preventDefault();
      const index = choices.indexOf(document.activeElement);
      const next = event.key === "Home" ? 0 : event.key === "End" ? choices.length - 1
        : (index + (event.key === "ArrowDown" ? 1 : -1) + choices.length) % choices.length;
      choices[next].focus();
    });
    window.addEventListener("pagehide", () => { if (picker?.open) picker.close(); });
    const syncOptions = () => {
      groups.forEach(group => {
        group.hidden = ![...group.querySelector(".studio-option-content").children].some(section => !section.hidden && !section.closest("[inert]"));
      });
      shortcuts.forEach(button => { button.hidden = !groups.some(group => !group.hidden && group.dataset.studioOptionGroup === button.dataset.studioSettingsTarget); });
    };
    const show = (view, { focus = false } = {}) => {
      if (!workspace) return;
      workspace.dataset.studioView = view === "result" ? "result" : "configure";
      tabs.forEach((tab) => {
        const selected = tab.dataset.studioTab === workspace.dataset.studioView;
        tab.classList.toggle("is-active", selected);
        tab.setAttribute("aria-selected", String(selected));
        tab.tabIndex = selected ? 0 : -1;
        if (focus && selected) tab.focus({ preventScroll: true });
      });
    };
    tabs.forEach((tab, index) => {
      tab.addEventListener("click", () => show(tab.dataset.studioTab));
      tab.addEventListener("keydown", (event) => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (index + 1) % tabs.length;
        show(tabs[next].dataset.studioTab, { focus: true });
      });
    });
    const openSettings = (groupID = "processing") => {
      show("configure");
      const advanced = root.querySelector(".generation-advanced");
      if (!settings || !advanced || advanced.hidden || advanced.inert) return;
      advanced.open = true;
      syncOptions();
      const group = groups.find(item => !item.hidden && item.dataset.studioOptionGroup === groupID) || groups.find(item => !item.hidden);
      if (group) group.open = true;
      if (!settings.open) settings.showModal();
      group?.scrollIntoView({ block: "start", behavior: "instant" });
    };
    shortcuts.forEach(button => button.addEventListener("click", () => openSettings(button.dataset.studioSettingsTarget)));
    root.querySelector("#studio-settings-close")?.addEventListener("click", () => settings.close());
    settings?.addEventListener("click", (event) => {
      if (event.target !== settings) return;
      const r = settings.getBoundingClientRect();
      if (event.clientX < r.left || event.clientX > r.right || event.clientY < r.top || event.clientY > r.bottom) settings.close();
    });
    const syncResult = () => { if (empty && result) empty.hidden = !result.hidden || Boolean(historyResult && !historyResult.hidden); };
    if (result) new window.MutationObserver(syncResult).observe(result, { attributes: true, attributeFilter: ["hidden"] });
    if (historyResult) new window.MutationObserver(syncResult).observe(historyResult, { attributes: true, attributeFilter: ["hidden"] });
    syncResult();
    const editor = root.querySelector(".studio-editor");
    const dock = root.querySelector(".generation-run-dock");
    const mobileNav = document.querySelector(".workspace-mobile-nav");
    const syncDock = () => {
      if (!editor || !dock || !narrow.matches || !editor.getClientRects().length) return;
      const bounds = editor.getBoundingClientRect();
      const navHeight = mobileNav?.getBoundingClientRect().height || 0;
      root.style.setProperty("--studio-dock-bottom", `${navHeight}px`);
      root.style.setProperty("--studio-dock-left", `${bounds.left}px`);
      root.style.setProperty("--studio-dock-width", `${bounds.width}px`);
      root.style.setProperty("--studio-dock-height", `${dock.getBoundingClientRect().height}px`);
      root.classList.add("studio-dock-ready");
    };
    if (editor && dock && window.ResizeObserver) {
      const dockObserver = new window.ResizeObserver(syncDock);
      [editor, dock, mobileNav].filter(Boolean).forEach(element => dockObserver.observe(element));
      window.addEventListener("resize", syncDock);
      syncDock();
    }
    const syncKeyboard = () => {
      const viewport = window.visualViewport;
      const editable = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName || "");
      root.classList.toggle("studio-keyboard-open", Boolean(narrow.matches && editable && viewport && window.innerHeight - viewport.height > 150));
    };
    window.visualViewport?.addEventListener("resize", syncKeyboard);
    document.addEventListener("focusin", syncKeyboard);
    document.addEventListener("focusout", syncKeyboard);
    return { show, openSettings, syncOptions, syncWorkflowPicker, revealField(field) {
      show("configure");
      if (field?.closest("#studio-settings")) openSettings();
      let parent = field?.parentElement;
      while (parent && parent !== root) {
        if (parent.tagName === "DETAILS") parent.open = true;
        parent = parent.parentElement;
      }
      field?.scrollIntoView({ block: "center", behavior: "instant" });
      field?.focus({ preventScroll: true });
    } };
  }
  const api = { activeImageLimit, chooseModel, settingChanges, compactModelLabel, fragmentBounds, bindReferencePlayer, bindUI };
  if (typeof module === "object" && module.exports) module.exports = api;
  host.AIGatewayGeneration = host.AIGatewayGeneration || {};
  host.AIGatewayGeneration.studio = api;
})(typeof window !== "undefined" ? window : globalThis);
