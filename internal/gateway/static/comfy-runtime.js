(function () {
  'use strict';
  const root = document.querySelector('[data-comfy-runtime]');
  if (!root) return;
  const page = root.closest('[data-resource-page]');
  const $ = selector => root.querySelector(selector);
  const dialog = $('[data-runtime-confirm]');
  const labels = { ready: 'Работает', stopped: 'Остановлен', starting_or_unresponsive: 'Запускается или не отвечает', ambiguous: 'Найдено несколько процессов', foreign_listener: 'Порт занят другим процессом' };
  const actions = { start: 'Запустить ComfyUI', stop: 'Остановить ComfyUI', restart: 'Перезапустить ComfyUI' };
  const key = 'gateway:comfy-runtime:' + page.dataset.owner;
  let status = null, pending = null, sending = false, reading = false, active = true, timer = 0, confirm = null, serial = 0;
  try { const value = JSON.parse(sessionStorage.getItem(key)); if (/^[a-f0-9]{32}$/.test(value?.id)) pending = value; } catch {}
  const message = text => { const el = $('[data-runtime-message]'); el.textContent = text; el.hidden = !text; };
  function render() {
    $('[data-runtime-state]').textContent = sending ? 'Выполняем команду' : labels[status?.state] || 'Состояние не подтверждено';
    $('[data-runtime-detail]').textContent = status?.pid ? 'PID: ' + status.pid + (status.ready ? ' · API готов' : ' · API не отвечает') : '';
    const exit = status?.last_exit;
    $('[data-runtime-exit]').textContent = exit ? 'Последнее завершение: ' + new Date(exit.at).toLocaleString('ru-RU') + ' · код 0x' + Number(exit.code).toString(16).toUpperCase().padStart(8, '0') + (exit.requested ? ' · по команде администратора' : ' · без команды сервиса') : '';
    root.querySelectorAll('[data-runtime-action]').forEach(button => {
      const start = button.dataset.runtimeAction === 'start';
      button.disabled = sending || !!pending || !status?.available || !status.version || (start ? status.running : !status.running);
    });
  }
  async function request(body, id = '') {
    const response = await fetch('/admin/services/comfyui/runtime' + (id ? '?id=' + id : ''), { method: body ? 'POST' : 'GET', credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal: AbortSignal.timeout(40000), headers: body ? { 'Content-Type': 'application/json', 'X-CSRF-Token': page.dataset.csrf } : {}, body: body ? JSON.stringify(body) : undefined });
    if (response.status === 401 || response.status === 403) { status = null; if (dialog.open) dialog.close(); render(); throw new Error('Сеанс завершён или доступ отозван.'); }
    const value = await response.json();
    return { value, code: response.status, ok: response.ok };
  }
  function settle(value) {
    const receipt = value.receipt;
    if (!pending || receipt?.id !== pending.id || !['completed', 'failed'].includes(receipt.state)) return;
    pending = null; try { sessionStorage.removeItem(key); } catch {}
    message(receipt.state === 'failed' ? 'Команда не выполнена. Проверьте состояние процесса.' : receipt.action === 'stop' ? 'ComfyUI остановлен.' : value.ready ? 'ComfyUI готов.' : 'Процесс запущен. Ожидаем готовности ComfyUI.');
  }
  async function refresh() {
    if (reading || sending || !active || document.hidden || !root.isConnected) return;
    reading = true;
    const epoch = serial;
    try {
      const response = await request(null, pending?.id || '');
      if (epoch !== serial || !active || !root.isConnected) return;
      if (response.ok && response.value.available) {
        status = response.value; settle(status);
        if (pending && status.receipt?.state === 'unconfirmed' && Date.now() > pending.deadline) {
          pending = null; try { sessionStorage.removeItem(key); } catch {}
          message('Завершение команды не подтверждено. Срок её выполнения истёк; проверьте текущее состояние процесса.');
        }
      }
      else if (response.code === 404 && pending && Date.now() > pending.deadline) {
        pending = null; try { sessionStorage.removeItem(key); } catch {}
        message('Приём команды не подтверждён. Повторная команда автоматически не отправляется.');
        const latest = await request();
        if (epoch !== serial || !active || !root.isConnected) return;
        status = latest.ok ? latest.value : null;
      } else { status = null; message('Нет ответа Windows-агента. Проверка повторится автоматически.'); }
    } catch (err) { if (epoch === serial && active && root.isConnected) { status = null; message(err.message || 'Нет связи. Повторяем проверку.'); } }
    finally { reading = false; render(); clearTimeout(timer); if (active && root.isConnected && !document.hidden) timer = setTimeout(refresh, 5000); }
  }
  async function send(action, version, force) {
    if (sending || pending) return;
    serial++;
    pending = { id: crypto.randomUUID().replaceAll('-', ''), deadline: Date.now() + 40000 };
    try { sessionStorage.setItem(key, JSON.stringify(pending)); } catch {}
    sending = true; render(); message('Проверяем очередь и выполняем команду…');
    try {
      const response = await request({ id: pending.id, action, version, force, expires_at: status.command_until });
      if (response.value.available) status = response.value;
      settle(response.value);
      if (!response.ok && response.value.error === 'active_jobs') {
        pending = null; try { sessionStorage.removeItem(key); } catch {} message('Есть активные задания, установка модели или операция хранения. Дождитесь завершения.');
      } else if (!response.ok && ['busy', 'command_rejected', 'invalid_request', 'control_unavailable'].includes(response.value.error)) {
        pending = null; try { sessionStorage.removeItem(key); } catch {}
        message(response.value.error === 'busy' ? 'ComfyUI занят. Дождитесь завершения задания или операции.' : 'Команда отклонена. Обновляем состояние; повторного запуска не будет.');
      } else if (!response.ok && pending) message('Результат команды пока не подтверждён. Проверяем сохранённый запрос; повторно не отправляем.');
    } catch { message('Ответ не получен. Проверяем сохранённую команду без повторного запуска.'); }
    finally { sending = false; render(); refresh(); }
  }
  root.querySelectorAll('[data-runtime-action]').forEach(button => button.addEventListener('click', () => {
    if (button.disabled || !status?.version) return;
    const action = button.dataset.runtimeAction;
    if (action === 'start') { send(action, status.version, false); return; }
    confirm = { action, version: status.version };
    $('[data-runtime-title]').textContent = actions[action];
    $('[data-runtime-force-label]').hidden = status.ready;
    $('[data-runtime-force]').checked = false;
    $('[data-runtime-submit]').disabled = !status.ready;
    dialog.showModal(); $('[data-runtime-cancel]').focus();
  }));
  $('[data-runtime-force]').addEventListener('change', () => { $('[data-runtime-submit]').disabled = !$('[data-runtime-force-label]').hidden && !$('[data-runtime-force]').checked; });
  $('[data-runtime-cancel]').addEventListener('click', () => dialog.close());
  $('[data-runtime-submit]').addEventListener('click', () => { if (!confirm) return; const chosen = confirm; const force = $('[data-runtime-force]').checked; dialog.close(); send(chosen.action, chosen.version, force); });
  dialog.addEventListener('close', () => { confirm = null; });
  $('[data-runtime-refresh]').addEventListener('click', refresh);
  window.addEventListener('pagehide', () => { active = false; clearTimeout(timer); });
  window.addEventListener('pageshow', () => { active = true; refresh(); });
  document.addEventListener('visibilitychange', () => { clearTimeout(timer); if (!document.hidden) refresh(); });
  refresh();
})();
