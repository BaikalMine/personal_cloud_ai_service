(function (host) {
  const activeImageLimit = ({ requiresImage, allowsImages, maximum, videoMode, isVideo }) => {
    if (!requiresImage && !allowsImages) return 0;
    const limit = Math.max(0, Math.min(4, Number(maximum) || 0));
    return isVideo && videoMode === "frames" ? Math.min(2, limit) : limit;
  };
  const chooseModel = (models, defaultID) => models.find(model => model.value && model.value === defaultID && !model.disabled && !model.hidden)
    || models.find(model => model.value && !model.disabled && !model.hidden);
  const settingChanges = (before, after) => Object.keys(after).filter(name => before[name] && before[name].value !== after[name].value)
    .map(name => ({ name, label: after[name].label, before: before[name].value, after: after[name].value }));
  const fragmentBounds = (start, duration, mediaDuration) => {
    const total = Number(mediaDuration);
    if (!Number.isFinite(total) || total <= 0) return null;
    const offset = Number(String(start).replace(",", ".")) || 0;
    const from = Math.max(0, Math.min(total, offset < 0 ? total + offset : offset));
    return { start: from, end: Math.min(total, from + Math.max(0, Number(String(duration).replace(",", ".")) || 0)) };
  };
  function bindReferencePlayer({ media, button, status, start, duration, urlAPI = host.URL }) {
    if (!media) return null;
    let objectURL = "";
    let fragmentEnd = null;
    const pause = () => { media.pause(); fragmentEnd = null; };
    const clear = () => {
      pause();
      media.removeAttribute("src");
      media.load();
      if (objectURL) urlAPI.revokeObjectURL(objectURL);
      objectURL = "";
      if (button) button.disabled = true;
      if (status) status.textContent = "";
    };
    media.addEventListener("loadedmetadata", () => { if (button) button.disabled = !Number.isFinite(media.duration) || media.duration <= 0; });
    media.addEventListener("error", () => {
      if (!media.getAttribute("src")) return;
      pause();
      if (button) button.disabled = true;
      if (status) status.textContent = "Браузер не воспроизводит этот файл. Его можно оставить как референс.";
    });
    media.addEventListener("timeupdate", () => { if (fragmentEnd !== null && media.currentTime >= fragmentEnd) pause(); });
    media.addEventListener("pause", () => { fragmentEnd = null; });
    button?.addEventListener("click", async () => {
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
    return { clear, pause, setSource(source) {
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
    const narrow = window.matchMedia("(max-width: 899px)");
    const groups = [...root.querySelectorAll("[data-studio-option-group]")];
    const shortcuts = [...root.querySelectorAll("[data-studio-settings-target]")];
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
    const syncResult = () => { if (empty && result) empty.hidden = !result.hidden; };
    if (result) new window.MutationObserver(syncResult).observe(result, { attributes: true, attributeFilter: ["hidden"] });
    syncResult();
    const syncKeyboard = () => {
      const viewport = window.visualViewport;
      const editable = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName || "");
      root.classList.toggle("studio-keyboard-open", Boolean(narrow.matches && editable && viewport && window.innerHeight - viewport.height > 150));
    };
    window.visualViewport?.addEventListener("resize", syncKeyboard);
    document.addEventListener("focusin", syncKeyboard);
    document.addEventListener("focusout", syncKeyboard);
    return { show, openSettings, syncOptions, revealField(field) {
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
  const api = { activeImageLimit, chooseModel, settingChanges, fragmentBounds, bindReferencePlayer, bindUI };
  if (typeof module === "object" && module.exports) module.exports = api;
  host.AIGatewayGeneration = host.AIGatewayGeneration || {};
  host.AIGatewayGeneration.studio = api;
})(typeof window !== "undefined" ? window : globalThis);
