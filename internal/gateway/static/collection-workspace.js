(() => {
  const api = window.AIGatewayGeneration?.collections;
  if (!api) return;
  const icons = () => window.lucide?.createIcons();
  const showStatus = (node, text, tone = "ready") => {
    if (!node) return;
    node.textContent = text || ""; node.hidden = !text; node.dataset.tone = tone;
  };
  const date = value => {
    const parsed = new Date(value);
    return Number.isFinite(parsed.getTime()) ? parsed.toLocaleDateString("ru-RU") : "";
  };
  const icon = name => { const span = document.createElement("span"); span.className = "collection-asset-icon"; const glyph = document.createElement("i"); glyph.dataset.lucide = name; glyph.setAttribute("aria-hidden", "true"); span.append(glyph); return span; };
  const renderContext = (host, record, expanded) => {
    const context = host.querySelector("[data-collection-record]");
    context.hidden = !record;
    if (!record) { context.querySelector("[data-collection-assets]").replaceChildren(); return; }
    context.querySelector("[data-collection-empty]").hidden = record.has_context;
    context.querySelector("[data-collection-facts]").hidden = !record.has_context;
    const values = record.values || {};
    context.querySelector("[data-collection-model]").textContent = values.model || values.generation_workflow || "Не выбрана";
    context.querySelector("[data-collection-date]").textContent = date(record.saved_at) || "Не сохранено";
    context.querySelector("[data-collection-prompt]").textContent = values.positive_prompt || "Промпт не задан";
    const loras = context.querySelector("[data-collection-loras]"); loras.replaceChildren();
    Object.entries(values).filter(([name, value]) => /^lora_[0-9]+$/.test(name) && value).forEach(([name, value]) => {
      const item = document.createElement("li"), strength = values[`lora_model_strength_${name.slice(5)}`];
      item.textContent = strength ? `${value} · ${strength}` : value; loras.append(item);
    });
    if (!loras.childElementCount) { const empty = document.createElement("li"); empty.textContent = "Без LoRA"; loras.append(empty); }
    const assets = context.querySelector("[data-collection-assets]"); assets.replaceChildren();
    (record.assets || []).forEach(asset => {
      const item = document.createElement("li"), description = document.createElement("div"), expiry = document.createElement("small");
      const available = asset.available && /^\/generate\/draft\/asset\?id=/.test(asset.url || "");
      const name = document.createElement(available ? "a" : "span"); name.textContent = asset.name || "Исходник";
      if (available) { name.href = asset.url; name.target = "_blank"; name.rel = "noopener"; }
      expiry.textContent = available ? date(asset.expires_at) ? `До ${date(asset.expires_at)}` : "Сохранён" : "Недоступен. Нужна замена.";
      let preview = icon(asset.field === "input_audio" ? "music" : asset.field === "input_video" ? "film" : "image");
      if (available && expanded && /^input_image/.test(asset.field)) {
        preview = document.createElement("img"); preview.alt = ""; preview.loading = "lazy"; preview.src = asset.url;
        preview.addEventListener("error", () => { preview.replaceWith(icon("image-off")); expiry.textContent = "Не удалось открыть исходник"; icons(); }, { once: true });
      }
      description.append(name, expiry); item.append(preview, description); assets.append(item);
    });
    if (!assets.childElementCount) { const empty = document.createElement("li"); empty.className = "collection-empty-item"; empty.textContent = "Без исходников"; assets.append(empty); }
    icons();
  };
  const nameDialog = ({ submit, recover }) => {
    const dialog = document.querySelector("[data-collection-name-dialog]");
    const name = dialog.querySelector("[data-collection-name]"), accept = dialog.querySelector("[data-collection-dialog-submit]");
    const status = dialog.querySelector("[data-collection-dialog-status]"), refresh = dialog.querySelector("[data-collection-dialog-refresh]");
    const closeButtons = [...dialog.querySelectorAll("[data-collection-dialog-close], [data-collection-dialog-cancel]")];
    const trap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: dialog, documentRef: document });
    let mode = "create", expected = null, busy = false, locked = false, returnFocus = null;
    const close = () => {
      if (busy) return;
      dialog.close(); trap?.deactivate();
      if (returnFocus?.disabled) returnFocus.closest("[data-collection-studio], [data-collection-gallery]")?.querySelector("[data-collection-refresh]")?.focus();
    };
    const sync = () => { accept.disabled = busy || locked; name.disabled = busy; refresh.disabled = busy; closeButtons.forEach(button => { button.disabled = busy; }); dialog.setAttribute("aria-busy", String(busy)); };
    const execute = async () => {
      if (busy || locked || !name.reportValidity() || !name.value.trim()) return;
      busy = true; sync(); showStatus(status, "Сохраняем...");
      try { await submit(mode, name.value.trim(), expected); busy = false; close(); }
      catch (error) { locked = Boolean(error.uncertain || error.status === 409 || error.status === 404); refresh.hidden = !locked; showStatus(status, error.message, "error"); }
      finally { busy = false; sync(); }
    };
    accept.addEventListener("click", execute);
    name.addEventListener("keydown", event => { if (event.key === "Enter" && !event.isComposing) { event.preventDefault(); void execute(); } });
    closeButtons.forEach(button => button.addEventListener("click", close));
    dialog.addEventListener("cancel", event => { event.preventDefault(); close(); });
    refresh.addEventListener("click", async () => {
      if (busy) return;
      busy = true; sync();
      try {
        const result = await recover(mode, name.value.trim());
        if (result?.completed) { busy = false; close(); return; }
        expected = result?.record || expected; locked = false; refresh.hidden = true;
        showStatus(status, result?.record ? `Текущее название: «${result.record.name}».` : "Состояние обновлено. Проверьте название.");
      } catch (error) { showStatus(status, error.message, "error"); }
      finally { busy = false; sync(); }
    });
    return { open(action, trigger, record = null) {
      mode = action; expected = record; locked = false; busy = false; refresh.hidden = true; returnFocus = trigger;
      dialog.querySelector("h2").textContent = mode === "create" ? "Новая коллекция" : "Название коллекции";
      accept.textContent = mode === "create" ? "Создать" : "Сохранить";
      name.value = record?.name || ""; showStatus(status, ""); sync();
      dialog.showModal();
      if (trap) trap.activate({ trigger, initialFocus: name, onEscape: close }); else name.focus();
    }};
  };
  const bindStudio = ({ root, form, draft: getDraft, validate, revealDraft }) => {
    const host = root.querySelector("[data-collection-studio]");
    if (!host) return null;
    const select = host.querySelector("[data-collection-select]"), field = form.elements.namedItem("collection_id");
    const details = host.querySelector("[data-collection-snapshot]"), status = host.querySelector("[data-collection-status]");
    const save = host.querySelector("[data-collection-save]"), open = host.querySelector("[data-collection-open]");
    const transport = api.createTransport({ csrf: () => form.elements.csrf?.value });
    let items = [], busy = false, listing = false, notice = "", tone = "ready", needsDraftRefresh = false, presented = null, expanded = false;
    let pageActive = true, pageEpoch = 0;
    const option = (id, name) => { const item = document.createElement("option"); item.value = id; item.textContent = name; return item; };
    const fill = () => {
      const id = field.value;
      select.replaceChildren(option("", "Без коллекции"), ...items.map(item => option(item.id, item.name)));
      if (id && !items.some(item => item.id === id)) select.append(option(id, "Коллекция недоступна"));
      select.value = id;
      select.title = items.find(item => item.id === id)?.name || "";
    };
    const render = state => {
      const blocked = busy || listing || state.pending;
      select.disabled = blocked;
      host.querySelector("[data-collection-create]").disabled = blocked;
      host.querySelector("[data-collection-refresh]").disabled = blocked;
      host.querySelector("[data-collection-actions]").hidden = !state.id;
      details.hidden = !state.id;
      save.disabled = blocked || state.status !== "ready" || state.uncertain;
      open.disabled = save.disabled || !state.record?.has_context;
      const results = host.querySelector("[data-collection-results]");
      if (state.id) results.href = `/gallery?collection=${encodeURIComponent(state.id)}`; else results.removeAttribute("href");
      if (presented !== state.record || expanded !== details.open) { renderContext(host, state.record, details.open && !document.hidden); presented = state.record; expanded = details.open; }
      const failed = ["error", "conflict", "missing", "access"].includes(state.status);
      showStatus(status, failed && state.message ? state.message : notice || state.message || (listing ? "Загружаем коллекции..." : state.pending ? "Сохраняем..." : state.status === "loading" ? "Загружаем коллекцию..." : ""), failed ? "error" : notice ? tone : "ready");
    };
    const controller = api.createController({ transport, onState: render, onRecord: record => { items = items.map(item => item.id === record.id ? record : item); fill(); } });
    const choose = (id, dirty = false) => {
      field.value = api.validID(id) ? String(id) : ""; fill(); notice = "";
      if (dirty) getDraft()?.markDirty();
      return controller.select(field.value);
    };
    const refreshList = async () => {
      listing = true; render(controller.snapshot());
      try {
        const payload = await transport("list");
        if (!Array.isArray(payload.collections)) throw new Error("Не удалось прочитать список коллекций.");
        items = payload.collections.map(api.normalize); fill(); return items;
      } finally { listing = false; render(controller.snapshot()); }
    };
    const dialog = nameDialog({
      submit: async (mode, name) => {
        const result = await transport("create", { name });
        let record;
        try { record = api.normalize(result.collection); await refreshList(); }
        catch (cause) { throw Object.assign(new Error("Результат создания неизвестен. Обновите список перед повтором."), { uncertain: true, cause }); }
        await choose(record.id, true);
        notice = `Коллекция «${record.name}» выбрана.`; tone = "ready"; render(controller.snapshot());
      },
      recover: async (_, name) => {
        await refreshList();
        const record = items.find(item => item.name.toLocaleLowerCase("ru-RU") === name.toLocaleLowerCase("ru-RU"));
        if (record) { await choose(record.id, true); return { completed: true }; }
        return {};
      },
    });
    const prepare = async () => {
      const draft = getDraft();
      if (!draft || !await draft.settleFiles()) { revealDraft(); throw new Error("Сначала сохраните или выберите актуальную версию черновика."); }
      return draft;
    };
    const run = async action => {
      if (busy) return;
      const expected = controller.snapshot().record;
      if (!expected) return;
      const stamp = pageEpoch;
      if (action === "open") {
        try { validate(expected.values || {}); }
        catch (error) { notice = error.message; tone = "error"; render(controller.snapshot()); return; }
        if (!window.confirm(`Открыть настройки коллекции «${expected.name}» вместо текущих? Промпт, параметры и исходники в редакторе будут заменены. Результаты останутся.`)) return;
      }
      busy = true; notice = "Сохраняем текущий черновик и исходники..."; tone = "ready"; render(controller.snapshot());
      try {
        const draft = await prepare();
        if (action === "open") {
          validate(expected.values || {});
          const applied = await draft.replace(async revision => {
            const payload = await controller.write("open", { draft_revision: revision }, expected);
            if (!pageActive || stamp !== pageEpoch) throw Object.assign(new Error("Страница была закрыта во время открытия. Обновите состояние черновика."), { uncertain: true });
            return payload.draft;
          });
          notice = applied ? `Настройки «${expected.name}» открыты.` : "Есть новые правки. Выберите версию черновика.";
          if (!applied) revealDraft({ focus: false, scroll: false });
        } else {
          await controller.write("save_draft", { draft_revision: draft.snapshot().revision, name: expected.name }, expected);
          notice = `Настройки «${expected.name}» сохранены.`;
        }
        tone = "ready";
      } catch (error) {
        notice = error.message; tone = "error";
        needsDraftRefresh = needsDraftRefresh || error.code === "draft_conflict" || action === "open" && Boolean(error.uncertain);
      } finally { busy = false; render(controller.snapshot()); }
    };
    select.addEventListener("change", () => { void choose(select.value, true); });
    save.addEventListener("click", () => { void run("save_draft"); });
    open.addEventListener("click", () => { void run("open"); });
    details.addEventListener("toggle", () => render(controller.snapshot()));
    host.querySelector("[data-collection-create]").addEventListener("click", event => dialog.open("create", event.currentTarget));
    host.querySelector("[data-collection-refresh]").addEventListener("click", async () => {
      notice = "";
      try {
        await refreshList(); await controller.refresh();
        if (needsDraftRefresh) { await getDraft()?.load({ preserveLocal: true }); revealDraft(); needsDraftRefresh = false; }
      } catch (error) { notice = error.message; tone = "error"; }
      render(controller.snapshot());
    });
    window.addEventListener("pagehide", () => { pageActive = false; pageEpoch++; controller.setActive(false); renderContext(host, null, false); presented = null; });
    window.addEventListener("pageshow", event => { if (event.persisted) { pageActive = true; controller.setActive(true); } });
    return { restore: id => choose(id), request: async id => { await choose(id, true); details.open = true; host.scrollIntoView({ block: "nearest" }); }, start: async () => { try { await refreshList(); await controller.select(field.value); } catch (error) { notice = error.message; tone = "error"; render(controller.snapshot()); } } };
  };
  const bindGallery = ({ root, onRenamed }) => {
    const host = root.querySelector("[data-collection-gallery]");
    if (!host) return null;
    const details = host.querySelector("[data-collection-snapshot]"), status = host.querySelector("[data-collection-status]");
    const transport = api.createTransport({ csrf: () => root.dataset.csrf });
    let presented = null, expanded = true;
    const render = state => {
      host.hidden = !state.id;
      host.querySelector("[data-collection-title]").textContent = state.record?.name || "Коллекция";
      const link = host.querySelector("[data-collection-studio-link]");
      link.hidden = !state.record;
      if (state.record) link.href = `/generate?collection=${encodeURIComponent(state.id)}`;
      host.querySelector("[data-collection-rename]").disabled = state.pending || state.status !== "ready" || state.uncertain;
      host.querySelector("[data-collection-refresh]").disabled = state.pending || state.status === "loading";
      details.hidden = !state.record;
      if (presented !== state.record || expanded !== details.open) { renderContext(host, state.record, details.open && !document.hidden); presented = state.record; expanded = details.open; }
      showStatus(status, state.message || (state.status === "loading" ? "Загружаем коллекцию..." : state.pending ? "Сохраняем..." : ""), ["missing", "error", "conflict", "access"].includes(state.status) ? "error" : "ready");
    };
    const controller = api.createController({ transport, onState: render, onRecord: onRenamed });
    const dialog = nameDialog({ submit: async (_, name, expected) => controller.write("rename", { name }, expected), recover: async () => { const record = await controller.refresh(); if (!record) throw new Error("Коллекция недоступна. Закройте окно и проверьте список."); return { record }; } });
    host.querySelector("[data-collection-rename]").addEventListener("click", event => dialog.open("rename", event.currentTarget, controller.snapshot().record));
    host.querySelector("[data-collection-refresh]").addEventListener("click", () => { void controller.refresh(); });
    details.addEventListener("toggle", () => render(controller.snapshot()));
    window.addEventListener("pagehide", () => { controller.setActive(false); renderContext(host, null, false); presented = null; });
    window.addEventListener("pageshow", event => { if (event.persisted) controller.setActive(true); });
    return { select: id => controller.select(id) };
  };
  window.AIGatewayGeneration.collectionWorkspace = { bindStudio, bindGallery };
})();
