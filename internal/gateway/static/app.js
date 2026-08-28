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
      const detail = document.getElementById(`content-event-detail-${trigger.dataset.contentEventOpen}`);
      if (!(detail instanceof HTMLTemplateElement)) return;
      contentDialogBody.replaceChildren(detail.content.cloneNode(true));
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
      const visible = history.slice(-96);
      visible.forEach((metric) => {
        const column = document.createElement("span");
        column.className = "system-history-column";
        const values = [
          ["cpu", metric.cpu_percent],
          ["memory", metric.memory_total_bytes ? metric.memory_used_bytes * 100 / metric.memory_total_bytes : 0],
          ["gpu", metric.gpu_available ? metric.gpu_percent : 0],
          ["gpu-memory", metric.gpu_memory_total_bytes ? metric.gpu_memory_used_bytes * 100 / metric.gpu_memory_total_bytes : 0],
        ];
        values.forEach(([kind, value]) => {
          const bar = document.createElement("i");
          bar.className = kind;
          bar.style.height = `${Math.max(2, percent(value))}%`;
          column.append(bar);
        });
        column.title = new Date(metric.recorded_at).toLocaleString("ru-RU");
        chart.append(column);
      });
      if (caption) caption.textContent = `${visible.length} замеров`;
    };
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
      renderHistory(overview.history || []);
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
