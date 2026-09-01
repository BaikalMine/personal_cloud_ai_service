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

  const accountExpiryNodes = [...document.querySelectorAll("[data-account-expiry]")];
  if (accountExpiryNodes.length) {
    const formatAccountExpiry = (deadline) => {
      const secondsLeft = Math.ceil((deadline - Date.now()) / 1000);
      if (!Number.isFinite(secondsLeft) || secondsLeft <= 0) return "Срок истёк · ожидает удаления";
      const days = Math.floor(secondsLeft / 86400);
      const hours = Math.floor((secondsLeft % 86400) / 3600);
      const minutes = Math.floor((secondsLeft % 3600) / 60);
      const seconds = secondsLeft % 60;
      if (days > 0) return `Удаление через ${days} д. ${hours} ч.`;
      if (hours > 0) return `Удаление через ${hours} ч. ${minutes} мин.`;
      if (minutes > 0) return `Удаление через ${minutes} мин. ${seconds} сек.`;
      return `Удаление через ${seconds} сек.`;
    };
    const refreshAccountExpiry = () => {
      accountExpiryNodes.forEach((node) => {
        const deadline = Number(node.dataset.accountExpiry);
        const expired = Number.isFinite(deadline) && deadline <= Date.now();
        node.textContent = formatAccountExpiry(deadline);
        node.classList.toggle("is-expired", expired);
      });
    };
    refreshAccountExpiry();
    window.setInterval(refreshAccountExpiry, 1000);
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
    let saving = false;
    toggle.addEventListener("change", async () => {
      if (saving) return;
      const previous = !toggle.checked;
      const body = new URLSearchParams(new FormData(form));
      body.set("enabled", toggle.checked ? "on" : "");
      saving = true;
      toggle.disabled = true;
      form.classList.add("is-saving");
      try {
        const response = await fetch(form.action, {
          method: "POST",
          headers: { Accept: "application/json", "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
          body,
          credentials: "same-origin",
        });
        const payload = await response.json().catch(() => ({}));
        if (!response.ok || typeof payload.enabled !== "boolean") throw new Error(payload.error || "Не удалось сохранить приоритет");
        toggle.checked = payload.enabled;
        form.title = payload.enabled
          ? "Приоритет включён: быстрая генерация будет временно приостанавливать майнинг."
          : "Приоритет выключен: майнинг не будет останавливаться для быстрой генерации.";
      } catch (_) {
        toggle.checked = previous;
        form.title = "Не удалось сохранить настройку. Проверьте соединение и повторите попытку.";
      } finally {
        toggle.disabled = false;
        form.classList.remove("is-saving");
        saving = false;
      }
    });
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

  const bindContentImageRetries = (root = document) => {
    root.querySelectorAll(".content-gallery-preview img, .content-detail-media-grid img").forEach((image) => {
      if (image.dataset.retryBound === "true") return;
      image.dataset.retryBound = "true";
      let retries = 0;
      image.addEventListener("error", () => {
        if (retries >= 2 || !image.src) return;
        retries += 1;
        const url = new URL(image.src, window.location.origin);
        url.searchParams.set("retry", `${Date.now()}-${retries}`);
        window.setTimeout(() => { image.src = url.toString(); }, 300 * retries);
      });
    });
  };

  const adminContentPage = document.querySelector("[data-admin-content-page]");
  const contentGallery = document.querySelector("[data-admin-content-gallery]");
  const contentDialog = document.getElementById("content-detail-dialog");
  const contentDialogBody = document.getElementById("content-detail-body");
  if (contentGallery && contentDialog && contentDialogBody) {
    let contentDialogTrigger = null;
    let contentDialogTaskKey = "";
    const contentDialogFocusTrap = window.AIGatewayDialogFocus?.createFocusTrap({ root: contentDialog, documentRef: document }) || null;
    const closeContentDialog = () => {
      if (contentDialog.hidden) return;
      contentDialogFocusTrap?.deactivate();
      contentDialog.hidden = true;
      contentDialogBody.replaceChildren();
      document.body.classList.remove("content-detail-open");
      if (!contentDialogFocusTrap) contentDialogTrigger?.focus({ preventScroll: true });
      contentDialogTrigger = null;
      contentDialogTaskKey = "";
    };
    const renderContentDialog = (trigger, preserveScroll = false) => {
      if (!(trigger instanceof HTMLElement)) return false;
      const taskKey = trigger.dataset.contentTaskOpen || "";
      const detail = document.getElementById(`content-task-detail-${taskKey}`);
      if (!(detail instanceof HTMLTemplateElement)) return false;
      const scrollTop = preserveScroll ? contentDialogBody.scrollTop : 0;
      contentDialogBody.replaceChildren(detail.content.cloneNode(true));
      if (trigger.closest(".sensitive-media")?.classList.contains("is-revealed")) {
        contentDialogBody.querySelectorAll(".sensitive-media").forEach((media) => media.classList.add("is-revealed"));
      }
      bindContentImageRetries(contentDialogBody);
      contentDialogBody.scrollTop = scrollTop;
      contentDialogTrigger = trigger;
      contentDialogTaskKey = taskKey;
      contentDialog.hidden = false;
      document.body.classList.add("content-detail-open");
      const closeButton = contentDialog.querySelector("[data-content-detail-close]");
      if (contentDialogFocusTrap) {
        contentDialogFocusTrap.activate({ trigger, initialFocus: closeButton, onEscape: closeContentDialog });
      } else {
        closeButton?.focus();
      }
      return true;
    };
    contentGallery.addEventListener("click", (event) => {
      const trigger = event.target.closest("[data-content-task-open]");
      if (!trigger) return;
      if (revealSensitiveMedia(trigger)) return;
      renderContentDialog(trigger);
    });
    contentDialog.querySelectorAll("[data-content-detail-close]").forEach((button) => button.addEventListener("click", closeContentDialog));
    if (!contentDialogFocusTrap) document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") closeContentDialog();
    });

    if (adminContentPage && "EventSource" in window) {
      const liveIndicator = adminContentPage.querySelector("[data-content-live-indicator]");
      const liveLabel = adminContentPage.querySelector("[data-content-live-label]");
      let contentRevision = Number(adminContentPage.dataset.contentRevision) || 0;
      let refreshInFlight = false;
      let refreshPending = false;
      let retryTimer = 0;
      const setLiveState = (state, label) => {
        liveIndicator?.classList.toggle("is-connecting", state === "connecting");
        liveIndicator?.classList.toggle("is-offline", state === "offline");
        if (liveLabel) liveLabel.textContent = label;
      };
      const replaceChildrenFrom = (current, next) => {
        if (!current || !next) return;
        current.replaceChildren(...[...next.childNodes].map((node) => document.importNode(node, true)));
      };
      const contentTaskCard = (root, taskKey) => [...root.querySelectorAll("[data-content-task-key]")].find((card) => card.dataset.contentTaskKey === taskKey) || null;
      const contentTaskDetail = (root, taskKey) => [...root.querySelectorAll("template[data-content-task-detail-key]")].find((detail) => detail.dataset.contentTaskDetailKey === taskKey) || null;
      const reconcileContentGallery = (nextGallery) => {
        const currentCards = new Map([...contentGallery.querySelectorAll("[data-content-task-key]")].map((card) => [card.dataset.contentTaskKey, card]));
        const currentDetails = new Map([...contentGallery.querySelectorAll("template[data-content-task-detail-key]")].map((detail) => [detail.dataset.contentTaskDetailKey, detail]));
        const revealed = new Set([...currentCards].filter(([, card]) => card.classList.contains("is-revealed")).map(([key]) => key));
        const kept = new Set();
        const changed = new Set();
        const nextCards = [...nextGallery.querySelectorAll("[data-content-task-key]")];

        if (nextCards.length === 0) {
          const empty = nextGallery.querySelector("[data-content-empty]");
          contentGallery.replaceChildren(empty ? document.importNode(empty, true) : document.createTextNode(""));
          return changed;
        }

        nextCards.forEach((nextCard) => {
          const taskKey = nextCard.dataset.contentTaskKey || "";
          const nextDetail = contentTaskDetail(nextGallery, taskKey);
          if (!taskKey || !nextDetail) return;
          const currentCard = currentCards.get(taskKey);
          const currentDetail = currentDetails.get(taskKey);
          const unchanged = currentCard && currentDetail && currentCard.dataset.contentVersion === nextCard.dataset.contentVersion && currentDetail.dataset.contentVersion === nextDetail.dataset.contentVersion;
          const card = unchanged ? currentCard : document.importNode(nextCard, true);
          const detail = unchanged ? currentDetail : document.importNode(nextDetail, true);
          if (!unchanged) changed.add(taskKey);
          if (!currentCard) {
            card.classList.add("is-new");
            window.setTimeout(() => card.classList.remove("is-new"), 450);
          }
          if (revealed.has(taskKey)) card.classList.add("is-revealed");
          contentGallery.append(card, detail);
          kept.add(card);
          kept.add(detail);
        });
        [...contentGallery.children].forEach((node) => {
          if (!kept.has(node)) node.remove();
        });
        return changed;
      };
      const refreshContent = async () => {
        if (refreshInFlight) {
          refreshPending = true;
          return;
        }
        refreshInFlight = true;
        refreshPending = false;
        let succeeded = false;
        try {
          const url = new URL(window.location.href);
          url.searchParams.set("live", "1");
          const response = await fetch(url, { credentials: "same-origin", cache: "no-store", headers: { Accept: "text/html" } });
          if (!response.ok) throw new Error(`content refresh failed: ${response.status}`);
          const nextDocument = new DOMParser().parseFromString(await response.text(), "text/html");
          const nextPage = nextDocument.querySelector("[data-admin-content-page]");
          const nextGallery = nextDocument.querySelector("[data-admin-content-gallery]");
          const nextOverview = nextDocument.querySelector("[data-content-overview]");
          const nextHeading = nextDocument.querySelector("[data-content-list-heading]");
          if (!nextPage || !nextGallery || !nextOverview || !nextHeading) throw new Error("content refresh payload is incomplete");

          const preserveAnchor = window.scrollY > 220 && contentDialog.hidden;
          const anchor = preserveAnchor ? [...contentGallery.querySelectorAll("[data-content-task-key]")].find((card) => card.getBoundingClientRect().bottom > 0) : null;
          const anchorKey = anchor?.dataset.contentTaskKey || "";
          const anchorTop = anchor?.getBoundingClientRect().top || 0;

          replaceChildrenFrom(document.querySelector("[data-content-overview]"), nextOverview);
          replaceChildrenFrom(document.querySelector("[data-content-list-heading]"), nextHeading);
          const changedTasks = reconcileContentGallery(nextGallery);
          contentRevision = Number(nextPage.dataset.contentRevision) || contentRevision;
          adminContentPage.dataset.contentRevision = String(contentRevision);
          bindContentImageRetries(contentGallery);

          if (anchorKey) {
            const nextAnchor = contentTaskCard(contentGallery, anchorKey);
            if (nextAnchor) window.scrollBy(0, nextAnchor.getBoundingClientRect().top - anchorTop);
          }
          if (!contentDialog.hidden && contentDialogTaskKey) {
            const nextTrigger = contentTaskCard(contentGallery, contentDialogTaskKey)?.querySelector("[data-content-task-open]");
            if (!nextTrigger) closeContentDialog();
            else if (changedTasks.has(contentDialogTaskKey)) renderContentDialog(nextTrigger, true);
          }
          succeeded = true;
          setLiveState("online", "Онлайн · задания обновляются сразу");
        } catch (_) {
          setLiveState("offline", "Не удалось обновить · повторяем подключение");
        } finally {
          refreshInFlight = false;
          if (refreshPending) {
            window.setTimeout(refreshContent, 0);
          } else if (!succeeded) {
            window.clearTimeout(retryTimer);
            retryTimer = window.setTimeout(refreshContent, 1800);
          }
        }
      };

      const eventsURL = new URL("/admin/content/events", window.location.origin);
      eventsURL.searchParams.set("since", String(contentRevision));
      const contentEvents = new EventSource(eventsURL, { withCredentials: true });
      contentEvents.addEventListener("open", () => setLiveState("online", "Онлайн · задания обновляются сразу"));
      contentEvents.addEventListener("ready", (event) => {
        const revision = Number(event.data);
        if (Number.isFinite(revision)) contentRevision = Math.max(contentRevision, revision);
        setLiveState("online", "Онлайн · задания обновляются сразу");
      });
      contentEvents.addEventListener("content", (event) => {
        const revision = Number(event.data);
        if (!Number.isFinite(revision) || revision <= contentRevision) return;
        refreshPending = refreshInFlight;
        refreshContent();
      });
      contentEvents.addEventListener("error", () => setLiveState("offline", "Связь потеряна · переподключаемся"));
      window.addEventListener("beforeunload", () => contentEvents.close(), { once: true });
    }
  }

  bindContentImageRetries();

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
    const initialHistory = [...systemMonitoring.querySelectorAll("[data-history-point]")].map((node) => ({
      recorded_at: node.dataset.recordedAt,
      cpu_percent: Number(node.dataset.cpu),
      memory_used_bytes: Number(node.dataset.memoryUsed),
      memory_total_bytes: Number(node.dataset.memoryTotal),
      gpu_available: node.dataset.gpuAvailable === "true",
      gpu_percent: Number(node.dataset.gpu),
      gpu_memory_used_bytes: Number(node.dataset.gpuMemoryUsed),
      gpu_memory_total_bytes: Number(node.dataset.gpuMemoryTotal),
    }));
    const initialMarkers = [...systemMonitoring.querySelectorAll("[data-generation-marker]")].map((node) => ({
      created_at: node.dataset.createdAt,
      public_id: node.dataset.publicId,
      state: node.dataset.state,
      model_name: node.dataset.model,
      workflow_id: node.dataset.workflow,
    }));
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
      const summary = systemMonitoring.querySelector("[data-system-history-summary]");
      const markers = systemMonitoring._markers || [];
      if (count) count.textContent = String(history.length);
      if (!chart) return;
      chart.replaceChildren();
      if (summary) summary.replaceChildren();
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
      const firstTimestamp = Date.parse(visible[0].recorded_at);
      const lastTimestamp = Date.parse(visible[visible.length - 1].recorded_at);
      const timeSpan = Math.max(1, lastTimestamp - firstTimestamp);
      const xForTime = (value) => chartInset.left + Math.max(0, Math.min(1, (Date.parse(value) - firstTimestamp) / timeSpan)) * plotWidth;
      const visibleMarkers = markers.filter((marker) => {
        const timestamp = Date.parse(marker.created_at);
        return Number.isFinite(timestamp) && timestamp >= firstTimestamp && timestamp <= lastTimestamp;
      });
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
        visible.forEach((metric) => {
          const value = historyValue(metric, series);
          if (value === null) {
            appendSegment();
            return;
          }
          segment.push(`${xForTime(metric.recorded_at).toFixed(2)},${yFor(value).toFixed(2)}`);
        });
        appendSegment();
      });
      svg.append(lines);
      const markerGroup = svgNode("g", { class: "system-history-markers", "aria-label": "Запуски генераций" });
      visibleMarkers.forEach((marker) => {
        const line = svgNode("line", {
          class: `is-${marker.state || "unknown"}`,
          x1: xForTime(marker.created_at), x2: xForTime(marker.created_at),
          y1: chartHeight - chartInset.bottom - 13, y2: chartHeight - chartInset.bottom,
        });
        const title = svgNode("title");
        title.textContent = `${historyDateTime(marker.created_at)} · ${marker.model_name || marker.workflow_id || "Генерация"}`;
        line.append(title);
        markerGroup.append(line);
      });
      svg.append(markerGroup);
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
        const targetTimestamp = firstTimestamp + ((relative * chartWidth - chartInset.left) / plotWidth) * timeSpan;
        let metric = visible[0];
        visible.forEach((candidate) => {
          if (Math.abs(Date.parse(candidate.recorded_at) - targetTimestamp) < Math.abs(Date.parse(metric.recorded_at) - targetTimestamp)) metric = candidate;
        });
        const x = xForTime(metric.recorded_at);
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
        const nearbyMarkers = visibleMarkers.filter((marker) => Math.abs(Date.parse(marker.created_at) - Date.parse(metric.recorded_at)) <= 90000);
        if (nearbyMarkers.length) {
          const row = document.createElement("span");
          row.innerHTML = `<i style="--series-color:#69dfb9"></i>Запуски<b>${nearbyMarkers.length}</b>`;
          tooltip.append(row);
        }
        tooltip.hidden = false;
        tooltip.style.left = `${Math.max(8, Math.min(chart.clientWidth - tooltip.offsetWidth - 8, (x / chartWidth) * chart.clientWidth + 12))}px`;
        tooltip.style.top = "18px";
      };
      interaction.addEventListener("pointermove", showPoint);
      interaction.addEventListener("pointerenter", showPoint);
      interaction.addEventListener("pointerdown", showPoint);
      interaction.addEventListener("pointerleave", () => { cursor.setAttribute("hidden", "hidden"); tooltip.hidden = true; });
      chart.append(svg, tooltip);
      if (summary) {
        historySeries.forEach((series) => {
          const values = visible.map((metric) => historyValue(metric, series)).filter((value) => value !== null);
          if (!values.length) return;
          const item = document.createElement("div");
          item.style.setProperty("--series-color", series.color);
          const label = document.createElement("span");
          label.innerHTML = "<i></i>";
          label.append(series.label);
          const current = document.createElement("strong");
          current.textContent = `${Math.round(values[values.length - 1])}% сейчас`;
          const range = document.createElement("small");
          range.textContent = `мин. ${Math.round(Math.min(...values))}% · макс. ${Math.round(Math.max(...values))}%`;
          item.append(label, current, range);
          summary.append(item);
        });
      }
      if (caption) caption.textContent = `${historyDateTime(visible[0].recorded_at)} - ${historyDateTime(visible[visible.length - 1].recorded_at)} · ${history.length} замеров · ${visibleMarkers.length} запусков`;
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
    const dependencyStateClasses = ["online", "stale", "offline", "misconfigured"];
    const setDependencyState = (node, state) => {
      if (!node) return;
      dependencyStateClasses.forEach((name) => node.classList.toggle(name, name === state));
    };
    const dependencyTime = (value) => value ? new Date(value).toLocaleString("ru-RU", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" }) : "-";
    const workerStateClasses = ["waiting", "running", "healthy", "retrying", "stopped"];
    const setWorkerState = (node, state) => {
      if (!node) return;
      workerStateClasses.forEach((name) => node.classList.toggle(`is-${name}`, name === state));
    };
    const workerDuration = (milliseconds) => {
      const value = Number(milliseconds) || 0;
      if (value <= 0) return "-";
      if (value < 1000) return `${Math.round(value)} мс`;
      if (value < 60000) return `${(value / 1000).toFixed(value < 10000 ? 1 : 0)} сек.`;
      return `${Math.floor(value / 60000)} мин. ${Math.round(value % 60000 / 1000)} сек.`;
    };
    const refreshWorkerCountdowns = () => {
      systemMonitoring.querySelectorAll("[data-worker-next]").forEach((node) => {
        const deadline = Date.parse(node.dataset.nextRun || "");
        if (!Number.isFinite(deadline)) return;
        const seconds = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
        if (seconds <= 0) node.textContent = "сейчас";
        else if (seconds < 60) node.textContent = `через ${seconds} сек.`;
        else node.textContent = `через ${Math.ceil(seconds / 60)} мин.`;
      });
    };
    const renderWorkers = (workers) => {
      let running = 0;
      let retrying = 0;
      workers.forEach((worker) => {
        const row = systemMonitoring.querySelector(`[data-worker-key="${CSS.escape(worker.key)}"]`);
        if (!row) return;
        setWorkerState(row, worker.status);
        const status = row.querySelector("[data-worker-status]");
        if (status) {
          status.textContent = worker.status_label;
          setWorkerState(status, worker.status);
        }
        const success = row.querySelector("[data-worker-success]");
        if (success) success.textContent = dependencyTime(worker.last_success_at);
        const duration = row.querySelector("[data-worker-duration]");
        if (duration) duration.textContent = workerDuration(worker.last_duration_ms);
        const items = row.querySelector("[data-worker-items]");
        if (items) items.textContent = Number(worker.last_items || 0).toLocaleString("ru-RU");
        const next = row.querySelector("[data-worker-next]");
        if (next) {
          if (worker.next_run_at) next.dataset.nextRun = worker.next_run_at;
          else {
            delete next.dataset.nextRun;
            next.textContent = worker.running ? "после завершения" : "-";
          }
        }
        const error = row.querySelector("[data-worker-error]");
        if (error) {
          error.textContent = worker.last_error || "";
          error.hidden = !worker.last_error;
        }
        if (worker.status === "running") running += 1;
        if (worker.status === "retrying") retrying += 1;
      });
      const summary = systemMonitoring.querySelector("[data-worker-summary]");
      if (summary) {
        const details = [];
        if (running) details.push(`выполняется: ${running}`);
        if (retrying) details.push(`с ошибкой: ${retrying}`);
        summary.textContent = details.length ? `${workers.length} · ${details.join(" · ")}` : `${workers.length} процессов · без ошибок`;
      }
      refreshWorkerCountdowns();
    };
    const refreshDependencyCountdowns = () => {
      document.querySelectorAll("[data-dependency-next]").forEach((node) => {
        const deadline = Date.parse(node.dataset.nextCheck || "");
        if (!Number.isFinite(deadline)) {
          node.textContent = "после настройки";
          return;
        }
        const seconds = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
        node.textContent = seconds > 0 ? `через ${seconds} сек.` : "сейчас";
      });
    };
    const renderDependencies = (dependencies) => {
      dependencies.forEach((dependency) => {
        const row = systemMonitoring.querySelector(`[data-dependency-key="${CSS.escape(dependency.key)}"]`);
        if (row) {
          const stateNode = row.querySelector("[data-dependency-state]");
          if (stateNode) {
            stateNode.textContent = dependency.state_label;
            setDependencyState(stateNode, dependency.state);
          }
          const detail = row.querySelector("[data-dependency-detail]");
          if (detail) detail.textContent = dependency.detail || "Проверяем состояние.";
          const success = row.querySelector("[data-dependency-success]");
          if (success) success.textContent = dependencyTime(dependency.last_success_at);
          const data = row.querySelector("[data-dependency-data]");
          if (data) data.textContent = dependencyTime(dependency.last_data_at);
          const error = row.querySelector("[data-dependency-error]");
          if (error) {
            error.textContent = dependency.last_error || "";
            error.hidden = !dependency.last_error;
          }
          const next = row.querySelector("[data-dependency-next]");
          if (next) {
            if (dependency.next_check_at) next.dataset.nextCheck = dependency.next_check_at;
            else delete next.dataset.nextCheck;
          }
        }
        const summary = document.querySelector(`[data-dependency-summary="${CSS.escape(dependency.key)}"]`);
        if (summary) {
          setDependencyState(summary, dependency.state);
          const label = summary.querySelector("b");
          if (label) label.textContent = dependency.state_label;
        }
      });
      refreshDependencyCountdowns();
    };
    const renderSystem = (overview) => {
      const host = overview.host;
      const status = systemMonitoring.querySelector("[data-system-status]");
      const stateNode = systemMonitoring.querySelector("[data-agent-state]");
      const message = systemMonitoring.querySelector("[data-agent-message]");
      const lastData = systemMonitoring.querySelector("[data-system-last-data]");
      const database = systemMonitoring.querySelector("[data-database-size]");
      const agent = overview.agent || { state: overview.agent_available ? "online" : "offline", state_label: overview.agent_available ? "В сети" : "Нет связи", detail: overview.agent_message || "" };
      if (database) database.textContent = bytes(overview.database_bytes);
      if (status) {
        status.textContent = agent.state_label;
        setDependencyState(status, agent.state);
      }
      dependencyStateClasses.forEach((name) => systemMonitoring.classList.toggle(`is-${name}`, name === agent.state));
      if (stateNode) stateNode.textContent = agent.state_label;
      if (message) message.textContent = agent.detail || "Проверяем Windows-агент.";
      if (lastData) lastData.textContent = agent.last_data_at ? `Последние данные: ${dependencyTime(agent.last_data_at)}` : "Свежих данных пока нет";
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
      renderDependencies(overview.dependencies || []);
      renderWorkers(overview.workers || []);
      systemMonitoring._history = overview.history || [];
      systemMonitoring._markers = overview.generation_markers || [];
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
    systemMonitoring._history = initialHistory;
    systemMonitoring._markers = initialMarkers;
    renderHistory(initialHistory);
    refreshSystem();
    window.setInterval(refreshSystem, 10000);
    refreshDependencyCountdowns();
    window.setInterval(refreshDependencyCountdowns, 1000);
    refreshWorkerCountdowns();
    window.setInterval(refreshWorkerCountdowns, 1000);
  }

  const inviteComposer = document.querySelector("[data-invite-composer]");
  if (inviteComposer instanceof HTMLFormElement) {
    const accountTypes = Array.from(inviteComposer.querySelectorAll('input[name="account_type"]'));
    const temporaryLifetime = inviteComposer.querySelector("[data-temporary-lifetime]");
    const temporarySelect = temporaryLifetime?.querySelector("select");
    const quickAccess = inviteComposer.querySelector("[data-quick-generation-access]");
    const scenarios = inviteComposer.querySelector("[data-quick-scenarios]");
    const quickPolicy = inviteComposer.querySelector("[data-quick-policy]");
    const imagePolicy = inviteComposer.querySelector("[data-image-policy]");
    const videoPolicy = inviteComposer.querySelector("[data-video-policy]");
    const setPolicyEnabled = (target, enabled) => {
      if (!(target instanceof HTMLElement)) return;
      target.classList.toggle("is-disabled", !enabled);
      target.querySelectorAll("input, select").forEach((control) => { control.disabled = !enabled; });
    };
    const syncInviteComposer = () => {
      const temporary = accountTypes.some((input) => input instanceof HTMLInputElement && input.checked && input.value === "temporary");
      if (temporaryLifetime instanceof HTMLElement) temporaryLifetime.hidden = !temporary;
      if (temporarySelect instanceof HTMLSelectElement) temporarySelect.disabled = !temporary;
      accountTypes.forEach((input) => input.closest(".invite-type-option")?.classList.toggle("is-selected", input instanceof HTMLInputElement && input.checked));

      const quickEnabled = quickAccess instanceof HTMLInputElement && quickAccess.checked;
      if (scenarios instanceof HTMLElement) {
        scenarios.classList.toggle("is-disabled", !quickEnabled);
        scenarios.querySelectorAll("input").forEach((input) => { input.disabled = !quickEnabled; });
      }
      const textToImage = inviteComposer.elements.grant_text_to_image;
      const imageToImage = inviteComposer.elements.grant_image_to_image;
      const video = inviteComposer.elements.grant_video;
      const imageEnabled = quickEnabled && Boolean(textToImage?.checked || imageToImage?.checked);
      const videoEnabled = quickEnabled && Boolean(video?.checked);
      if (quickPolicy instanceof HTMLElement) quickPolicy.classList.toggle("is-disabled", !quickEnabled);
      setPolicyEnabled(imagePolicy, imageEnabled);
      setPolicyEnabled(videoPolicy, videoEnabled);
      quickPolicy?.querySelectorAll(".invite-runtime-group input").forEach((input) => { input.disabled = !quickEnabled; });
    };
    accountTypes.forEach((input) => input.addEventListener("change", syncInviteComposer));
    quickAccess?.addEventListener("change", syncInviteComposer);
    scenarios?.addEventListener("change", syncInviteComposer);
    syncInviteComposer();
  }

  const approvedSubmissions = new WeakSet();
  const confirmDialog = document.createElement("dialog");
  confirmDialog.className = "confirm-dialog ui-dialog";
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
