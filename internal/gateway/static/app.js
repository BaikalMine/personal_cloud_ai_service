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

  document.querySelectorAll("table").forEach((table) => {
    const labels = [...table.querySelectorAll("thead th")].map((cell) => cell.textContent.trim());
    table.querySelectorAll("tbody tr").forEach((row) => {
      [...row.children].forEach((cell, index) => {
        if (labels[index]) cell.dataset.label = labels[index];
      });
    });
  });

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
