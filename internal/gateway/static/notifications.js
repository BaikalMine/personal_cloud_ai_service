(() => {
  const center = document.querySelector("[data-notification-center]");
  if (center) {
    const trigger = center.querySelector(".notification-trigger");
    const panel = center.querySelector(".notification-panel");
    const closeButton = center.querySelector("[data-notification-close]");
    const list = center.querySelector("[data-notification-list]");
    const readAll = center.querySelector("[data-notification-read-all]");
    const connection = center.querySelector("[data-notification-connection]");
    const activeCount = center.querySelector("[data-notification-active-count]");
    const unreadCount = center.querySelector("[data-notification-unread-count]");
    const activeTotal = center.querySelector("[data-notification-active-total]");
    const unreadTotal = center.querySelector("[data-notification-unread-total]");
    const csrf = center.dataset.notificationCsrf || "";
    let revision = Number(center.dataset.notificationRevision) || 0;
    let knownIDs = new Set();
    let initialized = false;
    let loading = false;
    let pendingLoad = false;
    let preferences = { in_app_enabled: true, success_enabled: true, browser_enabled: false };
    let events = null;
    const focusTrap = window.AIGatewayDialogFocus?.createFocusTrap?.({ root: panel });

    const setConnection = (label, state = "") => {
      if (!connection) return;
      connection.textContent = label;
      connection.classList.toggle("is-live", state === "live");
      connection.classList.toggle("is-offline", state === "offline");
    };

    const setCount = (node, value) => {
      if (!node) return;
      const count = Math.max(0, Number(value) || 0);
      node.textContent = String(count);
      node.hidden = count === 0;
    };

    const formatTime = (value) => {
      const date = new Date(value);
      if (!Number.isFinite(date.getTime())) return "";
      const sameDay = date.toDateString() === new Date().toDateString();
      return new Intl.DateTimeFormat("ru-RU", sameDay
        ? { hour: "2-digit", minute: "2-digit" }
        : { day: "2-digit", month: "2-digit" }).format(date);
    };

    const browserNotificationWasShown = (id) => {
      const key = `ai-gateway.notification-shown.${id}`;
      try {
        if (window.localStorage.getItem(key)) return true;
        window.localStorage.setItem(key, String(Date.now()));
      } catch (_) {}
      return false;
    };

    const showBrowserNotification = (item) => {
      if (!preferences.browser_enabled
        || !("Notification" in window)
        || window.Notification.permission !== "granted"
        || browserNotificationWasShown(item.id)) return;
      const notification = new window.Notification(item.title, {
        body: item.message || "Откройте AI Gateway, чтобы посмотреть задачу.",
        tag: `generation-${item.generation_job_id}`,
      });
      notification.addEventListener("click", () => {
        window.focus();
        window.location.assign(item.href);
        notification.close();
      });
    };

    const render = (items, summary) => {
      const active = Math.max(0, Number(summary?.active_count) || 0);
      const unread = Math.max(0, Number(summary?.unread_count) || 0);
      setCount(activeCount, active);
      setCount(unreadCount, unread);
      if (activeTotal) activeTotal.textContent = String(active);
      if (unreadTotal) unreadTotal.textContent = String(unread);
      trigger?.classList.toggle("has-unread", unread > 0);
      trigger?.setAttribute("aria-label", `Задачи: активных ${active}, непрочитанных ${unread}`);
      if (readAll) readAll.disabled = unread === 0;
      if (!list) return;

      if (!preferences.in_app_enabled) {
        const empty = document.createElement("p");
        empty.className = "notification-empty";
        empty.textContent = "Уведомления отключены. Активные задачи всё равно отображаются в счётчике.";
        list.replaceChildren(empty);
        return;
      }
      if (!items.length) {
        const empty = document.createElement("p");
        empty.className = "notification-empty";
        empty.textContent = active > 0
          ? "Задачи выполняются. Здесь появятся их результаты."
          : "Новых событий пока нет.";
        list.replaceChildren(empty);
        return;
      }

      list.replaceChildren(...items.map((item) => {
        const link = document.createElement("a");
        link.className = "notification-item";
        if (!item.read) link.classList.add("is-unread");
        if (item.kind === "generation_failed") link.classList.add("is-failed");
        link.href = item.href;
        link.dataset.notificationId = String(item.id);
        link.dataset.notificationRead = String(Boolean(item.read));

        const marker = document.createElement("i");
        marker.className = "notification-item-marker";
        marker.setAttribute("aria-hidden", "true");
        const copy = document.createElement("span");
        copy.className = "notification-item-copy";
        const title = document.createElement("strong");
        title.textContent = item.title;
        const message = document.createElement("span");
        message.textContent = item.message || "Откройте задачу, чтобы посмотреть подробности.";
        copy.append(title, message);
        const time = document.createElement("time");
        time.dateTime = item.created_at || "";
        time.textContent = formatTime(item.created_at);
        link.append(marker, copy, time);
        return link;
      }));
    };

    const load = async () => {
      if (loading) {
        pendingLoad = true;
        return;
      }
      loading = true;
      try {
        const response = await fetch("/notifications?limit=20", {
          credentials: "same-origin",
          cache: "no-store",
        });
        const payload = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error(payload.error || "Не удалось загрузить уведомления");
        const items = Array.isArray(payload.notifications) ? payload.notifications : [];
        preferences = payload.preferences || preferences;
        revision = Math.max(revision, Number(payload.summary?.revision) || 0);
        if (initialized) {
          items
            .filter((item) => !item.read && !knownIDs.has(String(item.id)))
            .forEach(showBrowserNotification);
        }
        knownIDs = new Set(items.map((item) => String(item.id)));
        initialized = true;
        render(items, payload.summary || {});
      } catch (error) {
        setConnection("Обновления временно недоступны", "offline");
        if (list && !initialized) {
          const empty = document.createElement("p");
          empty.className = "notification-empty";
          empty.textContent = error.message || "Не удалось загрузить уведомления.";
          list.replaceChildren(empty);
        }
      } finally {
        loading = false;
        if (pendingLoad) {
          pendingLoad = false;
          load();
        }
      }
    };

    const close = ({ restore = true } = {}) => {
      if (!panel || panel.hidden) return;
      panel.hidden = true;
      trigger?.setAttribute("aria-expanded", "false");
      if (focusTrap) focusTrap.deactivate({ restore });
      else if (restore) trigger?.focus({ preventScroll: true });
    };

    const open = () => {
      if (!panel) return;
      panel.hidden = false;
      trigger?.setAttribute("aria-expanded", "true");
      if (focusTrap) focusTrap.activate({ trigger, initialFocus: closeButton, onEscape: () => close() });
      else closeButton?.focus({ preventScroll: true });
      load();
    };

    trigger?.addEventListener("click", () => panel?.hidden ? open() : close());
    closeButton?.addEventListener("click", close);
    document.addEventListener("click", (event) => {
      if (panel && !panel.hidden && !center.contains(event.target)) close({ restore: false });
    });
    if (!focusTrap) document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") close();
    });

    list?.addEventListener("click", (event) => {
      const link = event.target.closest("[data-notification-id]");
      if (!link || link.dataset.notificationRead === "true") return;
      const body = new URLSearchParams({
        csrf,
        notification_id: link.dataset.notificationId || "",
      });
      fetch("/notifications/read", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
        body,
        credentials: "same-origin",
        keepalive: true,
      }).catch(() => {});
    });

    readAll?.addEventListener("click", async () => {
      if (readAll.disabled) return;
      readAll.disabled = true;
      try {
        const body = new URLSearchParams({ csrf, all: "true" });
        const response = await fetch("/notifications/read", {
          method: "POST",
          headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
          body,
          credentials: "same-origin",
        });
        if (!response.ok) throw new Error();
        await load();
      } catch (_) {
        readAll.disabled = false;
        setConnection("Не удалось отметить уведомления", "offline");
      }
    });

    const connect = () => {
      if (!("EventSource" in window)) {
        setConnection("Обновляем периодически", "offline");
        return;
      }
      events?.close();
      events = new EventSource(`/notifications/events?since=${encodeURIComponent(revision)}`);
      events.addEventListener("open", () => setConnection("Обновляются автоматически", "live"));
      events.addEventListener("ready", () => {
        setConnection("Обновляются автоматически", "live");
        if (!initialized) load();
      });
      events.addEventListener("notifications", (event) => {
        try {
          revision = Math.max(revision, Number(JSON.parse(event.data).revision) || 0);
        } catch (_) {}
        setConnection("Обновляются автоматически", "live");
        load();
      });
      events.addEventListener("error", () => setConnection("Переподключаем обновления", "offline"));
    };

    load().finally(connect);
    window.setInterval(load, 30000);
    window.addEventListener("beforeunload", () => events?.close(), { once: true });
  }

  const settings = document.querySelector("[data-notification-settings]");
  if (!settings) return;
  const inApp = settings.elements.in_app_enabled;
  const browserToggle = settings.querySelector("[data-browser-notification-toggle]");
  const browserStatus = settings.querySelector("[data-browser-notification-status]");

  const syncBrowserPermission = () => {
    if (!browserToggle || !browserStatus) return;
    if (!("Notification" in window)) {
      browserToggle.checked = false;
      browserToggle.disabled = true;
      browserStatus.textContent = "Этот браузер не поддерживает системные уведомления.";
      return;
    }
    browserToggle.disabled = !inApp?.checked;
    if (window.Notification.permission === "granted") {
      browserStatus.textContent = "Разрешены в этом браузере.";
    } else if (window.Notification.permission === "denied") {
      browserStatus.textContent = "Заблокированы в настройках браузера.";
    } else {
      browserStatus.textContent = "Разрешение появится после включения этого пункта.";
    }
  };

  browserToggle?.addEventListener("change", async () => {
    if (!browserToggle.checked || !("Notification" in window)) {
      syncBrowserPermission();
      return;
    }
    try {
      const permission = await window.Notification.requestPermission();
      if (permission !== "granted") browserToggle.checked = false;
    } catch (_) {
      browserToggle.checked = false;
    }
    syncBrowserPermission();
  });

  inApp?.addEventListener("change", () => {
    if (!inApp.checked && browserToggle) browserToggle.checked = false;
    syncBrowserPermission();
  });

  settings.addEventListener("submit", () => {
    if (browserToggle && (window.Notification?.permission !== "granted" || !inApp?.checked)) {
      browserToggle.checked = false;
    }
  });
  syncBrowserPermission();
})();
