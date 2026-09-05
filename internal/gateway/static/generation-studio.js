(function (host) {
  const activeImageLimit = ({ requiresImage, allowsImages, maximum, videoMode, isVideo }) => {
    if (!requiresImage && !allowsImages) return 0;
    const limit = Math.max(0, Math.min(4, Number(maximum) || 0));
    return isVideo && videoMode === "frames" ? Math.min(2, limit) : limit;
  };
  function bindUI({ root, window, document }) {
    const workspace = root.querySelector(".studio-workspace");
    const tabs = [...root.querySelectorAll("[data-studio-tab]")];
    const settings = root.querySelector("#studio-settings");
    const result = root.querySelector("#generation-result");
    const empty = root.querySelector("#studio-result-empty");
    const narrow = window.matchMedia("(max-width: 899px)");
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
    const openSettings = () => {
      show("configure");
      const advanced = root.querySelector(".generation-advanced");
      if (!settings || !advanced || advanced.hidden || advanced.inert) return;
      advanced.open = true;
      if (!settings.open) settings.showModal();
    };
    root.querySelector("#studio-settings-open")?.addEventListener("click", openSettings);
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
    return { show, openSettings, revealField(field) {
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
  const api = { activeImageLimit, bindUI };
  if (typeof module === "object" && module.exports) module.exports = api;
  host.AIGatewayGeneration = host.AIGatewayGeneration || {};
  host.AIGatewayGeneration.studio = api;
})(typeof window !== "undefined" ? window : globalThis);
