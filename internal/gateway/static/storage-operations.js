(function (root) {
  "use strict";
  root.createStorageOperations = function ({ page, request, element, button, facts, status, bytes, date, refresh, onChange }) {
    const model = root.AIGatewayStorage;
    const dialog = page.querySelector("[data-storage-operation]");
    const body = page.querySelector("[data-storage-operation-body]");
    const message = page.querySelector("[data-storage-operation-message] p");
    const footer = page.querySelector("[data-storage-operation-actions]");
    const closeButton = page.querySelector("[data-storage-operation-close]");
    const banner = page.querySelector("[data-storage-recovery]");
    const bannerText = page.querySelector("[data-storage-recovery-text]");
    const resume = page.querySelector("[data-storage-resume]");
    const trap = root.AIGatewayDialogFocus.createFocusTrap({ root: dialog });
    const key = `ai-gateway.storage-operation.${page.dataset.owner}`;
    let saved, persistenceError = "", pointer = null, current = null, envelope = null;
    let busy = false, epoch = 0, lastMissing = false, checkedFiles = null, expiryTimer, coordinationChanged = false, locked = false;
    try { saved = model.storagePointer(root.localStorage, key); pointer = saved.read(); }
    catch (_) { persistenceError = "Не удалось прочитать сохранённую операцию. Новая очистка недоступна."; }
    const endpoint = () => `/admin/storage/${current.kind}/operations`;
    function recovery() {
      banner.hidden = !pointer && !persistenceError;
      bannerText.textContent = persistenceError || "Есть незакрытая проверка очистки.";
      resume.hidden = !pointer;
      onChange?.();
    }
    function persist(value, expected = null) {
      if (!saved || persistenceError) throw new Error("recovery_unavailable");
      try { pointer = saved.write(value, expected); recovery(); }
      catch (error) { persistenceError = "Не удалось сохранить операцию или она изменена в другой вкладке. Удаление не отправлено."; recovery(); throw error; }
    }
    function release(id) {
      try { saved?.clear(id); pointer = saved?.read() || null; }
      catch (_) { persistenceError = "Результат сохранён на сервере, но браузер не смог закрыть отметку операции."; }
      recovery();
    }
    function notify(text, tone = "info") {
      message.textContent = text;
      message.parentElement.dataset.tone = tone;
    }
    function show(trigger) {
      if (!dialog.open) { dialog.showModal(); trap.activate({ trigger, initialFocus: closeButton, onEscape: close }); }
    }
    function close() {
      dialog.close(); trap.deactivate(); clearTimeout(expiryTimer);
      if (!busy) epoch++;
      recovery();
      const active = document.activeElement;
      if (active === document.body || dialog.contains(active) || active?.disabled || !active?.getClientRects().length) {
        (pointer ? resume : page.querySelector("[data-storage-refresh]"))?.focus({ preventScroll: true });
      }
    }
    closeButton.addEventListener("click", close);
    dialog.addEventListener("cancel", event => { event.preventDefault(); close(); });
    function operationSelection(data) {
      return current.kind === "comfy" ? (data.operation.selection || []).map(({ key, version }) => ({ key, version })) : (data.operation.preview?.items || []).map(item => item.id);
    }
    function ensurePointer(phase) {
      const local = saved?.read();
      if (local && (local.id !== current.id || local.kind !== current.kind)) throw new Error("recovery_conflict");
      const selection = local?.selection || operationSelection(envelope);
      persist({ v: 1, ...current, selection, phase }, local?.id || null);
    }
    function validEnvelope(data) {
      const op = data?.operation;
      if (!op || op.id !== current.id || !["preview", "dispatching", "unconfirmed", "completed", "partial", "rejected", "closed_unconfirmed", "cancelled", "expired"].includes(op.state)) throw new Error("invalid_response");
      if (["can_confirm", "can_cancel", "can_reconcile"].some(key => typeof data[key] !== "boolean")) throw new Error("invalid_response");
      if (op.state === "preview" && (!model.version(op.request_hash) || !Number.isFinite(Date.parse(op.expires_at)))) throw new Error("invalid_response");
      if (pointer?.id === op.id && op.state === "preview") {
        const canonical = list => list.map(item => typeof item === "string" ? item : `${item.key}:${item.version}`).sort();
        if (JSON.stringify(canonical(operationSelection(data))) !== JSON.stringify(canonical(pointer.selection))) throw new Error("invalid_response");
      }
      return data;
    }
    function addAction(label, icon, action, disabled = false, danger = false) {
      const control = button(label, icon, action, danger ? "danger" : "ghost");
      control.disabled = disabled || busy;
      footer.append(control);
      return control;
    }
    function inspected() {
      const op = envelope?.operation;
      if (!op) return false;
      if (current.kind === "training") return op.preview?.complete && op.preview.items?.length === op.item_count && op.preview.items.every(item => item.can_cleanup);
      return checkedFiles?.complete && checkedFiles.items?.length === (envelope.files || []).length && checkedFiles.items.every(item => ["matched", "missing"].includes(item.status));
    }
    function render() {
      clearTimeout(expiryTimer);
      if (coordinationChanged && !busy) notify("Операция изменена в другой вкладке. Обновите состояние.", "warning");
      body.replaceChildren(); footer.replaceChildren();
      const op = envelope?.operation;
      if (!op) {
        if (current) body.append(facts([["Категория", current.kind === "comfy" ? "ComfyUI" : "Обучение"], ["Номер операции", current.id]]));
        if (current && !busy) addAction("Проверить состояние", "refresh-cw", () => read());
        if (lastMissing && pointer?.id === current?.id && pointer.phase === "prepare") {
          addAction("Повторить проверку", "list-checks", () => prepare(pointer.kind, pointer.selection, null, pointer));
          addAction("Забыть проверку", "x", () => {
            const live = saved?.read();
            if (live?.id !== current.id || live.phase !== "prepare" || busy) return;
            release(current.id); close();
          });
        }
        addAction("Закрыть", "x", close, false);
        return;
      }
      const title = page.querySelector("#storage-operation-title");
      title.textContent = op.state === "preview" ? "Подтверждение очистки" : "Операция очистки";
      const expired = op.state === "preview" && Date.parse(op.expires_at) <= Date.now();
      body.append(status(expired ? "expired" : op.state));
      body.append(facts([["Категория", current.kind === "comfy" ? "ComfyUI" : "Обучение"], ["Автор", op.origin === "retention" ? "Автоматическая очистка" : op.actor_name || "Удалённый администратор"], ["Выбрано объектов", op.item_count], ["Ожидаемый объём", bytes(op.expected_bytes)], ["Подтверждено удаление", bytes(op.freed_bytes)], ["Удалено файлов", op.deleted_files], ["Создана", date(op.created_at)], [op.state === "preview" ? "Подтвердить до" : "Последняя проверка", date(op.state === "preview" ? op.expires_at : op.checked_at || op.finished_at)]]));
      if (op.last_code) body.append(element("p", "muted", model.label(op.last_code)));
      const rows = current.kind === "comfy" ? (envelope.files || []).map(item => ({ id: item.key, label: [item.file.storage_type, item.file.subfolder, item.file.filename].filter(Boolean).join("/"), size: item.file.size_bytes })) : (op.preview?.items || []).map(item => ({ id: item.id, label: `${model.label(item.kind)}: ${item.label}`, size: item.size_bytes }));
      const outcomes = new Map((op.result?.items || []).map(item => [current.kind === "comfy" ? item.selection?.key : item.id, item]));
      const inspection = new Map((checkedFiles?.items || []).map(item => [item.key, item.status]));
      const registryKeys = new Map((op.selection || []).map(item => [item.host_key, item.key]));
      const list = element("ul", "storage-operation-files");
      for (const row of rows) {
        const li = element("li");
        li.append(element("strong", "", row.label), element("span", "", bytes(row.size)));
        const outcome = outcomes.get(row.id);
        const checked = inspection.get(registryKeys.get(row.id));
        if (outcome) li.append(status(outcome.outcome), element("small", "", `Подтверждено: ${bytes(outcome.freed_bytes)}`));
        else if (checked) li.append(status(checked));
        list.append(li);
      }
      if (rows.length) body.append(list);
      else body.append(element("p", "muted", "Подробности файлов больше не хранятся."));
      const diagnostics = element("details", "ui-disclosure");
      diagnostics.append(element("summary", "", "Номер и контрольная сумма"), facts([["Операция", op.id], ["Проверка", op.request_hash || "Нет данных"]]));
      body.append(diagnostics);
      const conflict = persistenceError || coordinationChanged || pointer && (pointer.id !== current.id || pointer.kind !== current.kind);
      if (envelope.can_confirm && !expired) addAction("Удалить выбранное", "trash-2", () => mutate("confirm"), !inspected() || !!conflict, true);
      if (envelope.can_cancel) addAction("Отменить очистку", "x", () => mutate("cancel"), !!conflict);
      if (envelope.can_reconcile) addAction("Проверить на хосте", "refresh-cw", () => mutate("reconcile"), !!conflict);
      if (!model.terminal(op)) addAction("Обновить состояние", "rotate-ccw", () => read());
      addAction("Закрыть", "x", close);
      if (op.state === "preview" && !expired && dialog.open) expiryTimer = setTimeout(render, Math.min(2147483647, Math.max(50, Date.parse(op.expires_at) - Date.now() + 10)));
    }
    async function accept(data, token) {
      if (token !== epoch) return;
      envelope = validEnvelope(data);
      const op = envelope.operation;
      if (model.terminal(op)) release(op.id);
      if (op.state === "preview" && current.kind === "comfy" && data.can_confirm) {
        try {
          const result = await request("/admin/storage/comfy/inspect", { method: "POST", body: { items: operationSelection(data) } });
          if (token !== epoch) return;
          checkedFiles = result.inspection;
          const expected = new Map(operationSelection(data).map(item => [item.key, item.version]));
          if (!checkedFiles || !Array.isArray(checkedFiles.items) || checkedFiles.items.length !== expected.size || new Set(checkedFiles.items.map(item => item.key)).size !== expected.size || checkedFiles.items.some(item => expected.get(item.key) !== item.version)) throw new Error("invalid_response");
        } catch (error) {
          if (token !== epoch) return;
          checkedFiles = null; notify(`${model.label(error.code || error.message)} Очистка не подтверждена.`, "warning");
          return;
        }
      }
      if (token !== epoch) return;
      notify(op.state === "preview" ? "Удаление выбранных файлов с диска необратимо. Проверьте перечень перед подтверждением." : model.active(op) ? "Итог ожидает подтверждения хоста." : model.label(op.state), model.tone(op.state));
    }
    async function read() {
      if (busy || !current || locked) return;
      coordinationChanged = false;
      const token = ++epoch; busy = true; checkedFiles = null; lastMissing = false;
      notify("Проверяем состояние операции…"); render();
      try { await accept(await request(`${endpoint()}/${current.id}`), token); }
      catch (error) {
        if (token !== epoch) return;
        if (envelope) envelope.can_confirm = false;
        lastMissing = error.status === 404;
        notify(lastMissing ? "Операция пока не найдена. Сохранённый номер оставлен для повторной проверки." : model.label(error.code || error.message), "warning");
      } finally { if (token === epoch) { busy = false; render(); recovery(); } }
    }
    async function prepare(kind, selection, trigger, retry = null) {
      if (busy || locked) return;
      let value;
      try {
        const live = saved?.read();
        if (live && !retry) { pointer = live; recovery(); open(live.kind, live.id, trigger); return; }
        const id = retry?.id || Array.from(root.crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join("");
        value = { v: 1, kind, id, phase: "prepare", selection };
        persist(value, retry?.id || null);
      } catch (_) { persistenceError ||= "Не удалось прочитать или сохранить операцию. Запрос не отправлен."; recovery(); return; }
      current = { kind, id: value.id }; envelope = null; checkedFiles = null; lastMissing = false;
      coordinationChanged = false;
      show(trigger); busy = true; const token = ++epoch; notify("Проверяем выбранные объекты…"); render();
      try {
        const input = kind === "comfy" ? { id: value.id, items: selection } : { id: value.id, ids: selection };
        await accept(await request(endpoint(), { method: "POST", body: input }), token);
      } catch (error) { if (token === epoch) notify(`${model.label(error.code || error.message)} Номер проверки сохранён.`, "warning"); }
      finally { if (token === epoch) { busy = false; render(); recovery(); } }
    }
    async function mutate(action) {
      if (busy || !envelope || !current || locked || coordinationChanged) return;
      if (action === "confirm" && (!envelope.can_confirm || !inspected() || Date.parse(envelope.operation.expires_at) <= Date.now())) { render(); return; }
      if (action === "cancel" && !envelope.can_cancel || action === "reconcile" && !envelope.can_reconcile) return;
      try { ensurePointer(action === "confirm" ? "confirm" : pointer?.phase || "known"); }
      catch (_) { notify("Не удалось сохранить операцию. Запрос не отправлен.", "warning"); render(); recovery(); return; }
      const token = ++epoch; busy = true; notify(action === "confirm" ? "Ожидаем результат очистки…" : "Проверяем операцию…"); render();
      try {
        const body = action === "confirm" ? { request_hash: envelope.operation.request_hash, confirm: true } : {};
        await accept(await request(`${endpoint()}/${current.id}/${action}`, { method: "POST", body }), token);
        refresh();
      } catch (error) {
        if (token === epoch) {
          envelope.can_confirm = false;
          notify(`${model.label(error.code || error.message)} Результат запроса неизвестен; проверьте ту же операцию.`, "warning");
        }
      } finally { if (token === epoch) { busy = false; render(); recovery(); } }
    }
    function open(kind, id, trigger) {
      if (busy || locked || !model.operationID(id, kind) || !["comfy", "training"].includes(kind)) return;
      current = { kind, id }; envelope = null; checkedFiles = null; lastMissing = false;
      show(trigger); read();
    }
    resume.addEventListener("click", () => { if (pointer) open(pointer.kind, pointer.id, resume); });
    root.addEventListener("storage", event => {
      if (event.key !== key) return;
      coordinationChanged = true;
      try { pointer = saved.read(); persistenceError = ""; }
      catch (_) { persistenceError = "Сохранённая операция недоступна. Новая очистка заблокирована."; }
      if (!busy && envelope) envelope.can_confirm = false;
      recovery(); if (dialog.open && !busy) { notify("Операция изменена в другой вкладке. Обновите состояние.", "warning"); render(); }
    });
    recovery();
    return { prepare, open, blocked: () => locked || !!pointer || !!persistenceError || busy, lock() { locked = true; resume.disabled = true; epoch++; busy = false; envelope = null; current = null; body.replaceChildren(); footer.replaceChildren(); close(); } };
  };
})(window);
