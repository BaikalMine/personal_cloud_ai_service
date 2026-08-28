(() => {
  const currentPath = window.location.pathname.replace(/\/$/, "") || "/";
  const navigationLinks = [...document.querySelectorAll(".admin-nav a[href], .primary-nav a[href]")];
  const matching = navigationLinks
    .filter((link) => {
      const path = new URL(link.href, window.location.origin).pathname.replace(/\/$/, "") || "/";
      return currentPath === path || (path !== "/admin" && currentPath.startsWith(`${path}/`));
    })
    .sort((a, b) => b.getAttribute("href").length - a.getAttribute("href").length)[0];
  if (matching) {
    matching.setAttribute("aria-current", "page");
    window.requestAnimationFrame(() => matching.scrollIntoView({ block: "nearest", inline: "center" }));
  }

  document.querySelectorAll(".menu-toggle[aria-controls]").forEach((button) => {
    const target = document.getElementById(button.getAttribute("aria-controls"));
    if (!target) return;
    const isAdmin = target.classList.contains("admin-sidebar");
    const container = isAdmin ? target : button.closest(".user-topbar");
    const close = () => {
      container?.classList.remove("menu-open");
      button.setAttribute("aria-expanded", "false");
      if (isAdmin) document.body.classList.remove("admin-nav-open");
    };
    button.addEventListener("click", () => {
      const opened = !container?.classList.contains("menu-open");
      container?.classList.toggle("menu-open", opened);
      button.setAttribute("aria-expanded", String(opened));
      if (isAdmin) document.body.classList.toggle("admin-nav-open", opened);
    });
    target.addEventListener("click", (event) => {
      if (event.target.closest("a[href]")) close();
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") close();
    });
    if (isAdmin) {
      document.addEventListener("click", (event) => {
        if (document.body.classList.contains("admin-nav-open") && !target.contains(event.target) && !button.contains(event.target)) close();
      });
    }
  });

  document.querySelectorAll('input[type="password"]').forEach((input) => {
    const wrapper = document.createElement("span");
    wrapper.className = "password-field";
    input.parentNode.insertBefore(wrapper, input);
    wrapper.appendChild(input);
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "password-toggle";
    toggle.textContent = "Показать";
    toggle.setAttribute("aria-label", "Показать пароль");
    toggle.addEventListener("click", () => {
      const visible = input.type === "text";
      input.type = visible ? "password" : "text";
      toggle.textContent = visible ? "Показать" : "Скрыть";
      toggle.setAttribute("aria-label", visible ? "Показать пароль" : "Скрыть пароль");
    });
    wrapper.appendChild(toggle);
  });

  document.querySelectorAll("form[data-quick-generation-priority]").forEach((form) => {
    const toggle = form.querySelector('input[name="enabled"]');
    if (!toggle) return;
    toggle.addEventListener("change", () => form.requestSubmit());
  });

  const sensitiveContentStorageKey = "ai-gateway.show-sensitive-content";
  const sensitiveToggles = [...document.querySelectorAll("[data-sensitive-toggle]")];
  const applySensitiveContentPreference = (enabled) => {
    document.body.classList.toggle("show-sensitive-content", enabled);
    sensitiveToggles.forEach((toggle) => { toggle.checked = enabled; });
    try { window.localStorage.setItem(sensitiveContentStorageKey, enabled ? "true" : "false"); } catch (_) {}
  };
  let showSensitiveContent = false;
  try { showSensitiveContent = window.localStorage.getItem(sensitiveContentStorageKey) === "true"; } catch (_) {}
  applySensitiveContentPreference(showSensitiveContent);
  sensitiveToggles.forEach((toggle) => toggle.addEventListener("change", () => applySensitiveContentPreference(toggle.checked)));
  const revealSensitiveMedia = (trigger) => {
    const media = trigger?.closest?.(".sensitive-media");
    if (!media || document.body.classList.contains("show-sensitive-content") || media.classList.contains("is-revealed")) return false;
    media.classList.add("is-revealed");
    trigger.setAttribute("aria-label", "Контент показан. Нажмите, чтобы открыть результат");
    return true;
  };
  window.aiGatewaySensitiveContent = { reveal: revealSensitiveMedia };

  const contentGallery = document.querySelector("[data-admin-content-gallery]");
  const contentDialog = document.getElementById("content-detail-dialog");
  const contentDialogBody = document.getElementById("content-detail-body");
  if (contentGallery && contentDialog && contentDialogBody) {
    let contentDialogTrigger = null;
    const closeContentDialog = () => {
      if (contentDialog.hidden) return;
      contentDialog.hidden = true;
      contentDialogBody.replaceChildren();
      document.body.classList.remove("content-detail-open");
      contentDialogTrigger?.focus({ preventScroll: true });
      contentDialogTrigger = null;
    };
    contentGallery.addEventListener("click", (event) => {
      const trigger = event.target.closest("[data-content-event-open]");
      if (!trigger) return;
      if (revealSensitiveMedia(trigger)) return;
      const detail = document.getElementById(`content-event-detail-${trigger.dataset.contentEventOpen}`);
      if (!(detail instanceof HTMLTemplateElement)) return;
      contentDialogBody.replaceChildren(detail.content.cloneNode(true));
      if (trigger.closest(".sensitive-media")?.classList.contains("is-revealed")) {
        contentDialogBody.querySelectorAll(".sensitive-media").forEach((media) => media.classList.add("is-revealed"));
      }
      contentDialogTrigger = trigger;
      contentDialog.hidden = false;
      document.body.classList.add("content-detail-open");
      contentDialog.querySelector("[data-content-detail-close]")?.focus();
    });
    contentDialog.querySelectorAll("[data-content-detail-close]").forEach((button) => button.addEventListener("click", closeContentDialog));
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") closeContentDialog();
    });
  }

  document.querySelectorAll(".content-gallery-preview img, .content-detail-media-grid img").forEach((image) => {
    let retries = 0;
    image.addEventListener("error", () => {
      if (retries >= 2 || !image.src) return;
      retries += 1;
      const url = new URL(image.src, window.location.origin);
      url.searchParams.set("retry", `${Date.now()}-${retries}`);
      window.setTimeout(() => { image.src = url.toString(); }, 300 * retries);
    });
  });

  document.querySelectorAll("table").forEach((table) => {
    const labels = [...table.querySelectorAll("thead th")].map((cell) => cell.textContent.trim());
    table.querySelectorAll("tbody tr").forEach((row) => {
      [...row.children].forEach((cell, index) => {
        if (labels[index]) cell.dataset.label = labels[index];
      });
    });
  });

  const systemMonitoring = document.querySelector("[data-system-monitoring]");
  if (systemMonitoring) {
    const percent = (value) => Math.max(0, Math.min(100, Number(value) || 0));
    const bytes = (value) => {
      const size = Number(value) || 0;
      const units = ["Б", "КБ", "МБ", "ГБ", "ТБ"];
      let index = 0;
      let displayed = size;
      while (displayed >= 1024 && index < units.length - 1) {
        displayed /= 1024;
        index += 1;
      }
      return `${displayed.toFixed(1)} ${units[index]}`;
    };
    const setGauge = (name, value, main, detail = "") => {
      const gauge = systemMonitoring.querySelector(`[data-gauge="${name}"]`);
      if (gauge) gauge.style.setProperty("--value", String(percent(value)));
      systemMonitoring.querySelectorAll(`[data-gauge-value="${name}"]`).forEach((node) => { node.textContent = main; });
      systemMonitoring.querySelectorAll(`[data-gauge-detail="${name}"]`).forEach((node) => { node.textContent = detail; });
    };
    const renderOnlineUsers = (users) => {
      const target = systemMonitoring.querySelector("[data-online-users]");
      systemMonitoring.querySelectorAll("[data-online-count]").forEach((node) => { node.textContent = String(users.length); });
      if (!target) return;
      target.replaceChildren();
      if (!users.length) {
        const empty = document.createElement("p");
        empty.className = "muted";
        empty.textContent = "Нет активных сессий за последние 5 минут.";
        target.append(empty);
        return;
      }
      users.forEach((user) => {
        const item = document.createElement("div");
        item.className = "online-user";
        const dot = document.createElement("i");
        const copy = document.createElement("div");
        const name = document.createElement("strong");
        name.textContent = user.username;
        const detail = document.createElement("small");
        const role = user.role === "admin" ? "администратор" : "пользователь";
        const seen = user.last_seen_at ? new Date(user.last_seen_at).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" }) : "только что";
        detail.textContent = `${role} · активен ${seen}`;
        copy.append(name, detail);
        item.append(dot, copy);
        target.append(item);
      });
    };
    const historySeries = [
      { key: "cpu", label: "ЦП", color: "#56d6ea", value: (metric) => metric.cpu_percent },
      { key: "memory", label: "ОЗУ", color: "#62ddb5", value: (metric) => metric.memory_total_bytes ? metric.memory_used_bytes * 100 / metric.memory_total_bytes : 0 },
      { key: "gpu", label: "GPU", color: "#e5b75d", value: (metric) => metric.gpu_available ? metric.gpu_percent : null },
      { key: "gpu-memory", label: "VRAM", color: "#a78bfa", value: (metric) => metric.gpu_memory_total_bytes ? metric.gpu_memory_used_bytes * 100 / metric.gpu_memory_total_bytes : null },
    ];
    const activeHistorySeries = new Set(historySeries.map(({ key }) => key));
    const chartWidth = 960;
    const chartHeight = 248;
    const chartInset = { top: 14, right: 18, bottom: 30, left: 42 };
    const svgNode = (name, attributes = {}) => {
      const node = document.createElementNS("http://www.w3.org/2000/svg", name);
      Object.entries(attributes).forEach(([key, value]) => node.setAttribute(key, String(value)));
      return node;
    };
    const compactHistory = (history, maximum = 240) => {
      if (history.length <= maximum) return history;
      const step = (history.length - 1) / (maximum - 1);
      return Array.from({ length: maximum }, (_, index) => history[Math.round(index * step)]);
    };
    const historyValue = (metric, series) => {
      const raw = series.value(metric);
      return raw === null || raw === undefined ? null : percent(raw);
    };
    const historyTime = (value) => new Date(value).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
    const historyDateTime = (value) => new Date(value).toLocaleString("ru-RU", { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" });
    const renderHistory = (history) => {
      const chart = systemMonitoring.querySelector("[data-system-history]");
      const caption = systemMonitoring.querySelector("[data-system-history-caption]");
      const count = systemMonitoring.querySelector("[data-history-count]");
      if (count) count.textContent = String(history.length);
      if (!chart) return;
      chart.replaceChildren();
      if (history.length < 2) {
        const empty = document.createElement("p");
        empty.className = "muted";
        empty.textContent = "График появится после нескольких замеров.";
        chart.append(empty);
        if (caption) caption.textContent = "Сбор истории";
        return;
      }
      const visible = compactHistory(history);
      const plotWidth = chartWidth - chartInset.left - chartInset.right;
      const plotHeight = chartHeight - chartInset.top - chartInset.bottom;
      const xFor = (index) => chartInset.left + (visible.length > 1 ? index * plotWidth / (visible.length - 1) : 0);
      const yFor = (value) => chartInset.top + (100 - value) * plotHeight / 100;
      const svg = svgNode("svg", { viewBox: `0 0 ${chartWidth} ${chartHeight}`, preserveAspectRatio: "none", role: "img", "aria-label": "Линейный график нагрузки сервера" });
      const grid = svgNode("g", { class: "system-history-grid" });
      [0, 25, 50, 75, 100].forEach((value) => {
        const y = yFor(value);
        grid.append(svgNode("line", { x1: chartInset.left, x2: chartWidth - chartInset.right, y1: y, y2: y }));
        const label = svgNode("text", { x: chartInset.left - 9, y: y + 4, "text-anchor": "end" });
        label.textContent = `${value}%`;
        grid.append(label);
      });
      const firstTime = svgNode("text", { class: "system-history-time", x: chartInset.left, y: chartHeight - 8, "text-anchor": "start" });
      firstTime.textContent = historyTime(visible[0].recorded_at);
      const lastTime = svgNode("text", { class: "system-history-time", x: chartWidth - chartInset.right, y: chartHeight - 8, "text-anchor": "end" });
      lastTime.textContent = historyTime(visible[visible.length - 1].recorded_at);
      grid.append(firstTime, lastTime);
      svg.append(grid);
      const lines = svgNode("g", { class: "system-history-lines" });
      historySeries.filter(({ key }) => activeHistorySeries.has(key)).forEach((series) => {
        let segment = [];
        const appendSegment = () => {
          if (segment.length > 1) lines.append(svgNode("polyline", { points: segment.join(" "), stroke: series.color }));
          segment = [];
        };
        visible.forEach((metric, index) => {
          const value = historyValue(metric, series);
          if (value === null) {
            appendSegment();
            return;
          }
          segment.push(`${xFor(index).toFixed(2)},${yFor(value).toFixed(2)}`);
        });
        appendSegment();
      });
      svg.append(lines);
      const cursor = svgNode("g", { class: "system-history-cursor", hidden: "hidden" });
      const cursorLine = svgNode("line", { x1: 0, x2: 0, y1: chartInset.top, y2: chartHeight - chartInset.bottom });
      cursor.append(cursorLine);
      svg.append(cursor);
      const interaction = svgNode("rect", { class: "system-history-hit", x: chartInset.left, y: chartInset.top, width: plotWidth, height: plotHeight });
      svg.append(interaction);
      const tooltip = document.createElement("div");
      tooltip.className = "system-history-tooltip";
      tooltip.hidden = true;
      const showPoint = (event) => {
        const bounds = svg.getBoundingClientRect();
        const relative = Math.max(chartInset.left / chartWidth, Math.min(1 - chartInset.right / chartWidth, (event.clientX - bounds.left) / bounds.width));
        const index = Math.round((relative * chartWidth - chartInset.left) * (visible.length - 1) / plotWidth);
        const metric = visible[Math.max(0, Math.min(visible.length - 1, index))];
        const x = xFor(index);
        cursor.removeAttribute("hidden");
        cursorLine.setAttribute("x1", x);
        cursorLine.setAttribute("x2", x);
        tooltip.replaceChildren();
        const title = document.createElement("strong");
        title.textContent = historyDateTime(metric.recorded_at);
        tooltip.append(title);
        historySeries.filter(({ key }) => activeHistorySeries.has(key)).forEach((series) => {
          const value = historyValue(metric, series);
          if (value === null) return;
          const row = document.createElement("span");
          row.innerHTML = `<i style="--series-color:${series.color}"></i>${series.label}<b>${Math.round(value)}%</b>`;
          tooltip.append(row);
        });
        tooltip.hidden = false;
        tooltip.style.left = `${Math.max(8, Math.min(chart.clientWidth - tooltip.offsetWidth - 8, (x / chartWidth) * chart.clientWidth + 12))}px`;
        tooltip.style.top = "18px";
      };
      interaction.addEventListener("pointermove", showPoint);
      interaction.addEventListener("pointerenter", showPoint);
      interaction.addEventListener("pointerdown", showPoint);
      interaction.addEventListener("pointerleave", () => { cursor.setAttribute("hidden", "hidden"); tooltip.hidden = true; });
      chart.append(svg, tooltip);
      if (caption) caption.textContent = `${historyDateTime(visible[0].recorded_at)} - ${historyDateTime(visible[visible.length - 1].recorded_at)} · ${history.length} замеров`;
    };
    systemMonitoring.querySelectorAll("[data-history-series]").forEach((button) => {
      button.addEventListener("click", () => {
        const key = button.dataset.historySeries;
        if (!key) return;
        if (activeHistorySeries.has(key) && activeHistorySeries.size === 1) return;
        if (activeHistorySeries.has(key)) activeHistorySeries.delete(key); else activeHistorySeries.add(key);
        systemMonitoring.querySelectorAll("[data-history-series]").forEach((node) => {
          const active = activeHistorySeries.has(node.dataset.historySeries);
          node.classList.toggle("is-active", active);
          node.setAttribute("aria-pressed", String(active));
        });
        renderHistory(systemMonitoring._history || []);
      });
    });
    const renderSystem = (overview) => {
      const host = overview.host;
      const status = systemMonitoring.querySelector("[data-system-status]");
      const state = systemMonitoring.querySelector("[data-agent-state]");
      const message = systemMonitoring.querySelector("[data-agent-message]");
      const database = systemMonitoring.querySelector("[data-database-size]");
      if (database) database.textContent = bytes(overview.database_bytes);
      if (status) {
        status.textContent = overview.agent_available ? "Данные получены" : "Агент недоступен";
        status.classList.toggle("online", Boolean(overview.agent_available));
        status.classList.toggle("offline", !overview.agent_available);
      }
      if (state) state.textContent = overview.agent_available ? "готов" : "ожидание";
      if (message) message.textContent = overview.agent_message || "локальный Windows-агент";
      if (host) {
        setGauge("cpu", host.cpu_percent, `${Math.round(percent(host.cpu_percent))}%`);
        const memoryPercent = host.memory_total_bytes ? host.memory_used_bytes * 100 / host.memory_total_bytes : 0;
        setGauge("memory", memoryPercent, bytes(host.memory_used_bytes), `из ${bytes(host.memory_total_bytes)}`);
        setGauge("gpu", host.gpu_percent, host.gpu_available ? `${Math.round(percent(host.gpu_percent))}%` : "-", "GPU");
        const gpuMemoryPercent = host.gpu_memory_total_bytes ? host.gpu_memory_used_bytes * 100 / host.gpu_memory_total_bytes : 0;
        setGauge("gpu_memory", gpuMemoryPercent, host.gpu_available ? bytes(host.gpu_memory_used_bytes) : "-", host.gpu_available ? `из ${bytes(host.gpu_memory_total_bytes)}` : "");
        const gpuName = systemMonitoring.querySelector("[data-gpu-name]");
        if (gpuName) gpuName.textContent = host.gpu_name || "Вычисления GPU";
      }
      renderOnlineUsers(overview.online_users || []);
      systemMonitoring._history = overview.history || [];
      renderHistory(systemMonitoring._history);
    };
    const refreshSystem = async () => {
      try {
        const response = await fetch("/admin/system/overview", { headers: { Accept: "application/json" }, cache: "no-store" });
        if (!response.ok) return;
        renderSystem(await response.json());
      } catch (_) {
        // The last valid dashboard state remains visible while the host agent restarts.
      }
    };
    refreshSystem();
    window.setInterval(refreshSystem, 10000);
  }

  const approvedSubmissions = new WeakSet();
  const confirmDialog = document.createElement("dialog");
  confirmDialog.className = "confirm-dialog";
  confirmDialog.setAttribute("aria-labelledby", "confirm-dialog-title");

  const confirmBody = document.createElement("div");
  confirmBody.className = "confirm-dialog__body";
  const confirmMark = document.createElement("span");
  confirmMark.className = "confirm-dialog__mark";
  confirmMark.setAttribute("aria-hidden", "true");
  confirmMark.textContent = "!";
  const confirmCopy = document.createElement("div");
  const confirmTitle = document.createElement("h2");
  confirmTitle.id = "confirm-dialog-title";
  confirmTitle.textContent = "\u041f\u043e\u0434\u0442\u0432\u0435\u0440\u0434\u0438\u0442\u0435 \u0434\u0435\u0439\u0441\u0442\u0432\u0438\u0435";
  const confirmMessage = document.createElement("p");
  confirmCopy.append(confirmTitle, confirmMessage);
  confirmBody.append(confirmMark, confirmCopy);

  const confirmActions = document.createElement("div");
  confirmActions.className = "confirm-dialog__actions";
  const cancelConfirm = document.createElement("button");
  cancelConfirm.type = "button";
  cancelConfirm.className = "ghost";
  cancelConfirm.textContent = "\u041e\u0442\u043c\u0435\u043d\u0430";
  const acceptConfirm = document.createElement("button");
  acceptConfirm.type = "button";
  acceptConfirm.className = "danger";
  acceptConfirm.textContent = "\u041f\u043e\u0434\u0442\u0432\u0435\u0440\u0434\u0438\u0442\u044c";
  confirmActions.append(cancelConfirm, acceptConfirm);
  confirmDialog.append(confirmBody, confirmActions);
  document.body.appendChild(confirmDialog);

  let pendingConfirmation = null;
  let confirmationTrigger = null;
  const closeConfirmation = () => {
    if (confirmDialog.open) confirmDialog.close();
  };
  const requestConfirmation = (trigger, message, callback) => {
    if (typeof confirmDialog.showModal !== "function") {
      if (window.confirm(message)) callback();
      return;
    }
    pendingConfirmation = callback;
    confirmationTrigger = trigger;
    confirmMessage.textContent = message;
    confirmDialog.showModal();
    cancelConfirm.focus();
  };
  cancelConfirm.addEventListener("click", closeConfirmation);
  acceptConfirm.addEventListener("click", () => {
    const callback = pendingConfirmation;
    pendingConfirmation = null;
    closeConfirmation();
    callback?.();
  });
  confirmDialog.addEventListener("click", (event) => {
    if (event.target === confirmDialog) closeConfirmation();
  });
  confirmDialog.addEventListener("close", () => {
    pendingConfirmation = null;
    confirmationTrigger?.focus({ preventScroll: true });
    confirmationTrigger = null;
  });

  document.addEventListener("submit", (event) => {
    const form = event.target;
    const submitter = event.submitter;
    if (!(form instanceof HTMLFormElement) || !(submitter instanceof HTMLElement)) return;

    if (submitter.dataset.confirm && !approvedSubmissions.has(submitter)) {
      event.preventDefault();
      requestConfirmation(submitter, submitter.dataset.confirm, () => {
        approvedSubmissions.add(submitter);
        form.requestSubmit(submitter);
      });
      return;
    }
    approvedSubmissions.delete(submitter);
    window.setTimeout(() => {
      if (event.defaultPrevented) return;
      submitter.classList.add("is-loading");
      submitter.setAttribute("aria-busy", "true");
    }, 0);
  });

  document.addEventListener("click", async (event) => {
    const copyButton = event.target.closest("[data-copy-target]");
    if (copyButton) {
      const target = document.querySelector(copyButton.dataset.copyTarget);
      if (!target) return;
      const value = target.value || target.textContent || "";
      try {
        await navigator.clipboard.writeText(value.trim());
        const previous = copyButton.textContent;
        copyButton.textContent = "Скопировано";
        copyButton.dataset.copied = "true";
        window.setTimeout(() => { copyButton.textContent = previous; }, 1600);
        window.setTimeout(() => { delete copyButton.dataset.copied; }, 1600);
      } catch (_) {
        window.getSelection()?.selectAllChildren(target);
      }
      return;
    }

  });
})();
