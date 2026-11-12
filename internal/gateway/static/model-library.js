(function () {
  'use strict';
  const root = document.querySelector('[data-model-library]');
  if (!root) return;
  const $ = (selector, parent = root) => parent.querySelector(selector);
  const admin = root.dataset.admin === 'true';
  const filters = $('[data-filters]');
  const detail = $('[data-detail]');
  const install = $('[data-install]');
  const settings = $('[data-settings]');
  const deletion = $('[data-delete-dialog]');
  let deleteQuote = null, deleteBusy = false, checkBatch = null;
  const pointerKey = `gateway:model-install:${root.dataset.owner}`;
  let page = 1, pages = 1, catalogSerial = 0, detailSerial = 0, versionsSerial = 0, basesSerial = 0;
  let departed = false, timer = 0, queueBusy = false, queueRefresh = false, acceptanceSerial = 0, selected = null, selectedGroup = null, versions = null, quote = '', pending = null;
  if (admin) { try { const raw = localStorage.getItem(pointerKey); if (raw) { const saved = raw.startsWith('{') ? JSON.parse(raw) : { id: raw }; if (/^[a-f0-9]{32}$/.test(saved.id)) pending = { id: saved.id, quote: typeof saved.quote === 'string' ? saved.quote : '' }; } } catch { /* The server retains accepted jobs. */ } }
  const icons = () => window.lucide?.createIcons();
  const downloads = admin ? window.ModelDownloads.create({ container: $('[data-download-notifications]'), opener: $('[data-download-open]'), icons, onComplete: () => { loadCatalog(); loadBases(); }, focusFallback: () => $('[data-refresh-jobs]').focus({ preventScroll: true }) }) : null;
  const size = bytes => new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 2 }).format(Number(bytes) / (1024 ** 3)) + ' ГБ';
  const node = (tag, text, className) => { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; if (className) el.className = className; return el; };
  function icon(name) { const el = node('i'); el.dataset.lucide = name; el.setAttribute('aria-hidden', 'true'); return el; }
  function message(text, parent = root, error = false) { const el = $('[data-message]', parent); el.textContent = text; el.hidden = !text; el.classList.toggle('is-error', error); el.setAttribute('role', error ? 'alert' : 'status'); }
  async function api(path, body) {
    const timeout = ['delete-preview', 'delete-model', 'delete-lora'].includes(path) ? 25 * 60 * 1000 : 25000;
    const response = await fetch('/models/api/' + path, { method: body === undefined ? 'GET' : 'POST', credentials: 'same-origin', redirect: 'error', signal: AbortSignal.timeout(timeout), headers: body === undefined ? {} : { 'Content-Type': 'application/json', 'X-CSRF-Token': root.dataset.csrf }, body: body === undefined ? undefined : JSON.stringify(body) });
    if (!response.ok) { const text = await response.text(); const err = new Error(text.length < 1000 && !text.includes('<html') ? text.trim() : 'Не удалось получить ответ сервера.'); err.status = response.status; throw err; }
    return response.json();
  }
  function facts(el, pairs) { el.replaceChildren(); for (const [label, value] of pairs) { if (value === undefined || value === null || value === '') continue; el.append(node('dt', label), node('dd', value)); } }
  async function loadBases() {
    const serial = ++basesSerial, select = filters.elements.base, status = $('#model-base-status');
    const selectedBase = select.value;
    status.hidden = false; status.textContent = 'Загружаем список моделей...';
    try {
      const result = await api('base-models?' + new URLSearchParams({ kind: filters.elements.kind.value }));
      if (departed || serial !== basesSerial) return;
      const names = [...new Set((result.base_models || []).filter(value => typeof value === 'string' && value.trim() && value.length <= 100))].sort((a, b) => a.localeCompare(b, 'ru', { numeric: true }));
      select.replaceChildren(new Option('Все модели', ''), ...names.map(name => new Option(name, name)));
      if (names.includes(selectedBase)) select.value = selectedBase;
      else if (selectedBase) { page = 1; loadCatalog(); }
      status.hidden = true; status.textContent = '';
    } catch (err) {
      if (departed || serial !== basesSerial) return;
      status.textContent = 'Не удалось загрузить список. Обновите каталог.';
    }
  }
  function open(dialog) { message('', dialog); if (!dialog.open) dialog.showModal(); }
  root.querySelectorAll('[data-close]').forEach(button => button.addEventListener('click', () => button.closest('dialog').close()));
  detail.addEventListener('close', () => { detailSerial += 1; });
  const previewURL = item => '/models/api/preview?token=' + encodeURIComponent(item.token);
  const installedVersions = item => Array.isArray(item.versions) && item.versions.length ? item.versions : [item];
  const showSensitive = () => document.body.classList.contains('show-sensitive-content');
  function cover(item) {
    const el = node('div', undefined, 'model-cover');
    if (item.preview && (!item.mature || showSensitive())) { const img = node('img'); img.src = previewURL(item); img.alt = ''; img.loading = 'lazy'; img.addEventListener('error', () => { img.remove(); el.append(node('span', 'Превью недоступно', 'model-cover-empty')); }); el.append(img); }
    else { const empty = node('span', undefined, 'model-cover-empty'); empty.append(icon(item.mature ? 'eye-off' : 'image'), node('span', item.mature ? 'Превью скрыто · 18+' : 'Нет превью')); el.append(empty); }
    return el;
  }
  async function loadCatalog() {
    const serial = ++catalogSerial; const query = new URLSearchParams(new FormData(filters)); query.set('page', String(page)); query.set('grouped', 'true');
    if (admin && $('[data-updates-only]').checked) query.set('updates', 'true');
    $('[data-catalog]').setAttribute('aria-busy', 'true');
    try {
      const result = await api('catalog?' + query);
      if (departed || serial !== catalogSerial) return;
      const fragment = document.createDocumentFragment();
      for (const item of result.items) {
        item.kind = filters.elements.kind.value;
        const installed = installedVersions(item), representative = installed[0];
        const article = node('article', undefined, 'model-card'); const button = node('button'); button.type = 'button'; button.setAttribute('aria-label', 'Открыть ' + item.name);
        const body = node('span', undefined, 'model-card-body'); body.append(node('span', item.name || item.filename, 'model-card-title'));
        const meta = node('span', undefined, 'model-card-meta'); meta.append(node('span', representative.base || 'База не указана'), node('span', size(representative.size)), node('span', `Установлено файлов: ${installed.length}`)); body.append(meta);
        if (installed.some(version => version.update) && admin) body.append(node('span', 'Есть обновление', 'model-card-update'));
        button.append(cover(representative), body); button.addEventListener('click', () => showDetail(item)); article.append(button);
        if (admin) { const remove = node('button', undefined, 'model-card-delete'); remove.type = 'button'; remove.title = (installed.length > 1 ? 'Выбрать версию для удаления: ' : 'Удалить ') + (item.name || representative.filename); remove.setAttribute('aria-label', remove.title); remove.append(icon('trash-2')); remove.addEventListener('click', () => { showDetail(item); if (installed.length === 1) $('[data-delete-lora]').click(); }); article.append(remove); }
        fragment.append(article);
      }
      if (!result.items.length) fragment.append(node('p', 'Модели не найдены.', 'ui-empty-state'));
      $('[data-catalog]').replaceChildren(fragment); pages = Math.max(1, result.pages); page = result.page || page;
      $('[data-count]').textContent = `Найдено: ${result.total}`; $('[data-page]').textContent = `${page} / ${pages}`;
      $('[data-prev]').disabled = page <= 1; $('[data-next]').disabled = page >= pages; message(''); icons();
    } catch (err) { if (!departed && serial === catalogSerial) message(err.message || 'Нет связи с сервером. Повторите проверку.', root, true); }
    finally { if (serial === catalogSerial) $('[data-catalog]').setAttribute('aria-busy', 'false'); }
  }
  filters.addEventListener('submit', event => { event.preventDefault(); page = 1; loadCatalog(); });
  filters.elements.kind.addEventListener('change', () => { page = 1; filters.elements.base.replaceChildren(new Option('Все модели', '')); loadCatalog(); loadBases(); });
  filters.elements.base.addEventListener('change', () => { page = 1; loadCatalog(); });
  $('[data-updates-only]')?.addEventListener('change', () => { page = 1; loadCatalog(); });
  $('[data-refresh]').addEventListener('click', () => { loadCatalog(); loadBases(); });
  $('[data-prev]').addEventListener('click', () => { if (page > 1) { page -= 1; loadCatalog(); } });
  $('[data-next]').addEventListener('click', () => { if (page < pages) { page += 1; loadCatalog(); } });
  function showDetail(group, index = 0) {
    selectedGroup = group;
    const installed = installedVersions(group), select = $('[data-installed-version]');
    select.replaceChildren(...installed.map((item, i) => new Option([item.version_name || (item.version_id ? 'CivitAI ' + item.version_id : ''), item.filename || item.name || 'Файл ' + (i + 1)].filter(Boolean).join(' · '), String(i))));
    select.value = String(index); $('[data-installed-versions]').hidden = installed.length <= 1;
    renderDetail(installed[index] || installed[0]);
  }
  $('[data-installed-version]').addEventListener('change', event => renderDetail(installedVersions(selectedGroup)[Number(event.target.value)]));
  async function renderDetail(item) {
    selected = item; const serial = ++detailSerial; $('#model-detail-title').textContent = item.name || item.filename;
    if (admin) { $('[data-check-update]').hidden = !item.model_id; $('[data-check-update]').disabled = false; $('[data-check-update]').removeAttribute('aria-busy'); }
    if (admin) { $('[data-delete-lora]').disabled = false; $('[data-refresh-metadata]').disabled = false; }
    facts($('[data-detail-facts]'), [['Базовая модель', item.base], ['Версия CivitAI', item.version_id || 'Не определена'], ['Файл', item.filename], ['Размер', size(item.size)], ['SHA-256', item.hash]]);
    const media = $('[data-detail-media]'); media.replaceChildren();
    const showPreview = () => { const img = node('img'); img.src = previewURL(item); img.alt = item.name; img.addEventListener('error', () => { if (media.contains(img)) media.replaceChildren(node('p', 'Превью недоступно.')); }); media.replaceChildren(img); };
    if (item.preview) { if (item.mature && !showSensitive()) { const button = node('button', 'Показать превью 18+', 'ghost'); button.type = 'button'; button.addEventListener('click', showPreview); media.append(button); } else showPreview(); }
    const words = Array.isArray(item.words) ? item.words.join(', ') : ''; $('[data-words]').textContent = words; $('[data-words-section]').hidden = !words;
    $('[data-description]').textContent = 'Загрузка описания...'; const link = $('[data-civitai-link]'); link.hidden = !item.model_id;
    if (item.model_id) link.href = 'https://civitai.com/models/' + item.model_id;
    if (admin) $('[data-detail-versions]').hidden = !item.model_id;
    open(detail); icons();
    try { const result = await api('item?token=' + encodeURIComponent(item.token)); if (!departed && serial === detailSerial) $('[data-description]').textContent = result.description || 'Описание пока не добавлено.'; }
    catch (err) { if (!departed && serial === detailSerial) { $('[data-description]').textContent = 'Описание недоступно.'; message(err.message, detail, true); } }
  }
  $('[data-copy-words]').addEventListener('click', async () => { try { await navigator.clipboard.writeText($('[data-words]').textContent); message('Скопировано.', detail); } catch { message('Не удалось скопировать текст.', detail, true); } });
  function invalidateQuote() { quote = ''; $('[data-quote-view]').hidden = true; $('[data-confirm]').disabled = true; }
  function fillFiles() {
    versionsSerial += 1; invalidateQuote(); const v = versions?.modelVersions.find(item => String(item.id) === $('[data-version]').value);
    const files = $('[data-file]'); files.replaceChildren();
    for (const file of v?.files || []) {
      if (!file.name.toLowerCase().endsWith('.safetensors') || file.metadata?.format !== 'SafeTensor' || !['Model', 'UNet', 'Diffusion Model', 'LoRA', 'LoCon', 'DoRA'].includes(file.type)) continue;
      const precision = String(file.metadata?.fp || '').trim().toUpperCase();
      const type = ['UNet', 'Diffusion Model'].includes(file.type) ? 'Diffusion model' : file.type === 'Model' ? (['lora', 'locon', 'dora'].includes((versions?.type || '').toLowerCase()) ? 'LoRA' : 'Модель') : file.type;
      const opt = node('option', `${precision || 'Точность не указана'} · ${size(file.sizeKB * 1024)} · ${type}${file.metadata?.size ? ' · ' + file.metadata.size : ''}`);
      opt.value = String(file.id); opt.title = file.name; files.append(opt);
    }
    syncTarget();
    $('[data-quote]').disabled = !files.options.length || Boolean(v?.existsLocally);
  }
  function syncTarget() {
    const v = versions?.modelVersions.find(item => String(item.id) === $('[data-version]').value);
    const file = v?.files.find(item => String(item.id) === $('[data-file]').value);
    $('[data-file-name]').textContent = file?.name || '';
    const lora = ['lora', 'locon', 'dora'].includes((versions?.type || '').toLowerCase());
    const moss = /^moss[\s._-]*image/i.test(v?.baseModel || '');
    const diffusion = !lora && (['UNet', 'Diffusion Model'].includes(file?.type) || moss);
    const target = $('[data-target]');
    for (const option of target.options) option.disabled = lora ? option.value !== 'lora' : option.value === 'lora' || (diffusion && option.value === 'checkpoint');
    target.value = lora ? 'lora' : diffusion ? 'diffusion' : '';
  }
  function modelID(source) {
    if (/^[1-9][0-9]*$/.test(source)) return source;
    try { const u = new URL(source); if (u.protocol !== 'https:' || !['civitai.com', 'www.civitai.com', 'civitai.red', 'www.civitai.red'].includes(u.hostname) || u.username || u.password || u.port) return ''; return u.pathname.match(/^\/models\/([1-9][0-9]*)(?:\/|$)/)?.[1] || ''; } catch { return ''; }
  }
  async function loadVersions() {
    const form = $('[data-source-form]'); const source = form.elements.source.value.trim(); const id = modelID(source); if (!id) { message('Введите ссылку CivitAI (.com или .red) или ID модели.', install, true); return; }
    let requestedVersion = ''; try { requestedVersion = new URL(source).searchParams.get('modelVersionId') || ''; } catch {}
    const serial = ++versionsSerial; invalidateQuote(); $('[data-version-fields]').hidden = true; message('Получаем версии...', install);
    try {
      const result = await api('versions?' + new URLSearchParams({ kind: form.elements.kind.value, id }));
      if (departed || serial !== versionsSerial) return; versions = result; versions.modelID = Number(id);
      $('[data-remote-name]').textContent = result.name; $('[data-remote-description]').textContent = result.description || '';
      const select = $('[data-version]'); select.replaceChildren(); for (const v of result.modelVersions || []) { const opt = node('option', `${v.name} · ${v.baseModel}${v.existsLocally ? ' · Установлено' : ''}`); opt.value = String(v.id); select.append(opt); }
      if ([...select.options].some(option => option.value === requestedVersion)) select.value = requestedVersion;
      $('[data-version-fields]').hidden = !select.options.length; fillFiles(); message(select.options.length ? '' : 'Доступных версий нет.', install);
    } catch (err) { if (serial === versionsSerial) message(err.message, install, true); }
  }
  async function loadSettings() {
    try { const result = await api('settings'); if (!settings.open || departed) return; $('[data-key-status]').textContent = result.civitai_api_key_set ? 'API-ключ задан' : 'API-ключ не задан'; facts($('[data-roots]'), [['LoRA', result.default_lora_root || 'Не настроено'], ['Checkpoint', result.default_checkpoint_root || 'Не настроено'], ['Diffusion model', result.default_unet_root || 'Не настроено']]); }
    catch (err) { if (settings.open) message(err.message, settings, true); }
  }
  const labels = { queued: 'В очереди', running: 'Загрузка', reconciling: 'Проверяем состояние', completed: 'Установлено', failed: 'Ошибка', cancelled: 'Отменено' };
  function acceptJob(job) {
    if (!pending || job.id !== pending.id || !Object.hasOwn(labels, job.state)) return false;
    try { localStorage.removeItem(pointerKey); } catch { /* No API key is retained. */ }
    pending = null; acceptanceSerial += 1; invalidateQuote();
    if (job.state === 'cancelled') message('Запрос отменён. Поздняя отправка не запустит установку.', install);
    else { downloads.reveal(job); if (install.open) install.close(); }
    $('[data-check-acceptance]').hidden = true; $('[data-close-request]').hidden = true;
    return true;
  }
  async function loadJobs() {
    if (!admin || departed) return;
    if (queueBusy) { queueRefresh = true; return; }
    queueBusy = true; queueRefresh = false;
    const serial = acceptanceSerial;
    try {
      const result = await api('installs'); if (departed || serial !== acceptanceSerial) return;
      const container = $('[data-jobs]'); const existing = new Map(Array.from(container.children).map(el => [el.dataset.id, el]));
      for (const job of result.jobs) {
        let row = existing.get(job.id); existing.delete(job.id);
        if (!row) { row = node('article', undefined, 'model-download-row'); row.dataset.id = job.id; const text = node('div'); text.append(node('strong', job.name), node('p')); row.append(text, node('span'), node('progress'), node('p', undefined, 'model-download-stats')); container.append(row); }
        row.querySelector('p').textContent = job.message || job.filename; row.querySelector('span').textContent = labels[job.state] || job.state;
        const data = window.ModelDownloads.metrics(job);
        const progress = row.querySelector('progress'); progress.max = 100; if (data.known) progress.value = data.progress; else progress.removeAttribute('value'); progress.setAttribute('aria-label', job.name); progress.hidden = !['running', 'reconciling'].includes(job.state);
        row.querySelector('.model-download-stats').textContent = [data.percent, data.volume, data.speed, data.eta ? 'Осталось ≈ ' + data.eta : ''].filter(Boolean).join(' · ');
        let cancel = row.querySelector('button');
        if (job.state === 'queued' && !cancel) { cancel = node('button', 'Отменить ожидание', 'ghost'); cancel.type = 'button'; cancel.addEventListener('click', async () => { cancel.disabled = true; try { await api('cancel', { id: job.id }); } catch (err) { $('[data-job-message]').textContent = err.message; } finally { cancel.disabled = false; loadJobs(); } }); row.append(cancel); }
        else if (job.state !== 'queued') cancel?.remove();
      }
      for (const row of existing.values()) row.remove();
      let found = result.jobs.find(job => job.id === pending?.id);
      if (pending && !found) { try { found = await api('installs?id=' + pending.id); } catch (err) { if (err.status !== 404) throw err; } }
      if (departed || serial !== acceptanceSerial) return;
      if (found) { acceptJob(found); if (!result.jobs.some(job => job.id === found.id)) result.jobs.unshift(found); }
      downloads.render(result.jobs); downloads.connectionRestored();
      $('[data-check-acceptance]').hidden = !pending; $('[data-close-request]').hidden = !pending;
      $('[data-job-message]').textContent = pending ? 'Приём последней установки пока не подтверждён. Проверьте или отмените запрос в окне установки.' : result.jobs.length ? '' : 'Загрузок пока нет.';
    } catch (err) { if (!departed) { $('[data-job-message]').textContent = err.message || 'Нет связи. Состояние загрузок будет проверено повторно.'; downloads.connectionLost(); } }
    finally { queueBusy = false; clearTimeout(timer); if (!departed && !document.hidden) timer = setTimeout(loadJobs, queueRefresh || serial !== acceptanceSerial ? 0 : 5000); }
  }
  async function submitPending() {
    if (!pending?.quote) return;
    const request = { ...pending };
    try { const job = await api('installs', request); if (!departed && !acceptJob(job)) message('Ответ получен. Проверяем состояние установки.', install); }
    catch (err) { if (!departed) message(err.status ? err.message : 'Ответ о приёме установки не получен. Можно проверить тот же запрос или отменить его.', install, true); }
    if (!departed) { $('[data-check-acceptance]').hidden = !pending; $('[data-close-request]').hidden = !pending; loadJobs(); }
  }
  if (admin) {
    const batchKey = 'gateway:model-check:' + root.dataset.owner;
    let savedBatch = null;
    try { savedBatch = JSON.parse(localStorage.getItem(batchKey)); } catch {}
    const renderBatch = state => {
      if (departed) return;
      $('[data-check-all]').disabled = state.running;
      $('[data-check-pause]').hidden = !state.running;
      $('[data-check-resume]').hidden = state.running || !state.pending.length;
      const progress = $('[data-check-progress]');
      progress.hidden = state.status === 'idle';
      progress.textContent = (state.status === 'completed' ? 'Проверка завершена. ' : state.status === 'paused' ? 'Проверка приостановлена. ' : '') +
        (state.kind === 'loras' ? 'LoRA' : 'Модели') + ': ' + state.done + ' / ' + state.total + '. С обновлениями: ' + state.updates + (state.error ? '. ' + state.error : '');
    };
    checkBatch = window.ModelMaintenance.create({
      initial: savedBatch,
      check: async token => { await new Promise(resolve => setTimeout(resolve, 350)); return api('check-update', { token }); },
      save: state => { try { localStorage.setItem(batchKey, JSON.stringify(state)); } catch {} },
      onState: renderBatch,
    });
    renderBatch(checkBatch.snapshot());
    const runBatch = async () => { await checkBatch.run(); if (!departed) loadCatalog(); };
    $('[data-check-pause]').addEventListener('click', () => checkBatch.pause());
    $('[data-check-resume]').addEventListener('click', runBatch);
    $('[data-check-all]').addEventListener('click', async () => {
      if (checkBatch.snapshot().running) return;
      if (checkBatch.snapshot().pending.length && !window.confirm('Начать новую проверку вместо приостановленной?')) return;
      const kind = filters.elements.kind.value, button = $('[data-check-all]');
      button.disabled = true; message('Получаем список моделей для проверки...');
      try {
        const tokens = [], ids = new Set();
        for (let next = 1; next <= 10000; next++) {
          const result = await api('catalog?' + new URLSearchParams({ kind, page: String(next) }));
          if (departed) return;
          for (const item of result.items) if (item.model_id && !ids.has(item.model_id)) { ids.add(item.model_id); tokens.push(item.token); }
          if (tokens.length > 10000) throw new Error('Каталог слишком большой для одной проверки.');
          if (next >= result.pages) break;
        }
        message('');
        checkBatch.start(kind, tokens); await runBatch();
      } catch (err) { if (!departed) message(err.message, root, true); }
      finally { if (!departed) button.disabled = checkBatch.snapshot().running; }
    });
    $('[data-refresh-metadata]').addEventListener('click', async () => {
      const item = selected, serial = detailSerial, button = $('[data-refresh-metadata]');
      if (!item) return;
      button.disabled = true; message('Обновляем описание и метаданные CivitAI...', detail);
      try {
        await api('refresh-metadata', { token: item.token });
        if (departed || serial !== detailSerial) return;
        const result = await api('item?token=' + encodeURIComponent(item.token));
        if (departed || serial !== detailSerial) return;
        $('[data-description]').textContent = result.description || 'Описание не найдено.';
        message('Метаданные обновлены. Файл модели не изменён.', detail); loadCatalog();
      } catch (err) { if (!departed && serial === detailSerial) message(err.message, detail, true); }
      finally { if (serial === detailSerial) button.disabled = false; }
    });
    $('[data-delete-lora]').addEventListener('click', async () => {
      const item = selected, serial = detailSerial, button = $('[data-delete-lora]');
      if (!item || deleteBusy) return;
      button.disabled = true; message('Проверяем выбранный файл перед удалением...', detail);
      try {
        const quote = await api('delete-preview', { token: item.token });
        if (departed || serial !== detailSerial) return;
        deleteQuote = quote;
        facts($('[data-delete-facts]'), [['Файл', quote.filename], ['Папка', quote.relative_path], ['Размер', size(quote.size)]]);
        $('[data-delete-consent]').checked = false; $('[data-delete-confirm]').disabled = true;
        detail.close(); open(deletion);
      } catch (err) { if (!departed && serial === detailSerial) message(err.message, detail, true); }
      finally { if (serial === detailSerial) button.disabled = false; }
    });
    $('[data-delete-consent]').addEventListener('change', () => { $('[data-delete-confirm]').disabled = deleteBusy || !deleteQuote || !$('[data-delete-consent]').checked; });
    deletion.addEventListener('close', () => { if (!deleteBusy) deleteQuote = null; });
    $('[data-delete-confirm]').addEventListener('click', async () => {
      if (deleteBusy || !deleteQuote || !$('[data-delete-consent]').checked) return;
      const quote = deleteQuote; deleteQuote = null; deleteBusy = true;
      $('[data-delete-confirm]').disabled = true; message('Проверяем размер и SHA-256 файла. Для больших моделей это может занять несколько минут; после проверки файл будет удалён.', deletion);
      try {
        await api('delete-model', { token: quote.token, confirm: quote.filename });
        if (departed) return;
        deletion.close(); await loadCatalog(); loadBases(); message('Удалён файл: ' + quote.filename);
      } catch (err) { if (!departed) message(err.message + ' Для повторного действия откройте карточку заново.', deletion, true); }
      finally { deleteBusy = false; }
    });
    $('[data-check-update]').addEventListener('click', async () => {
      if (!selected?.model_id) return;
      const item = selected, serial = detailSerial, button = $('[data-check-update]');
      button.disabled = true; button.setAttribute('aria-busy', 'true'); message('Проверяем CivitAI...', detail);
      try {
        const result = await api('check-update', { token: item.token });
        if (departed || serial !== detailSerial) return;
        item.update = result.update;
        message(result.update ? 'Есть доступные обновления. Выберите версию для установки.' : 'Доступных обновлений не найдено.', detail);
        loadCatalog();
      } catch (err) { if (!departed && serial === detailSerial) message(err.message, detail, true); }
      finally { if (serial === detailSerial) { button.disabled = false; button.removeAttribute('aria-busy'); } }
    });
    $('[data-open-settings]').addEventListener('click', () => { open(settings); loadSettings(); });
    $('[data-key-form]').addEventListener('submit', async event => { event.preventDefault(); const form = event.currentTarget; const button = form.querySelector('button'); button.disabled = true; try { await api('settings', { key: form.elements.key.value }); form.reset(); message('Ключ сохранён.', settings); loadSettings(); } catch (err) { message(err.message, settings, true); } finally { button.disabled = false; } });
    $('[data-open-install]').addEventListener('click', () => { open(install); if (pending) { message('Проверьте приём предыдущего запроса или отмените его перед новой установкой.', install); $('[data-check-acceptance]').hidden = false; $('[data-close-request]').hidden = false; } });
    $('[data-detail-versions]').addEventListener('click', () => { $('[data-source-form]').elements.source.value = String(selected.model_id); $('[data-source-form]').elements.kind.value = filters.elements.kind.value; detail.close(); open(install); loadVersions(); });
    $('[data-source-form]').addEventListener('submit', event => { event.preventDefault(); loadVersions(); });
    $('[data-source-form]').addEventListener('input', () => { versionsSerial += 1; versions = null; invalidateQuote(); $('[data-version-fields]').hidden = true; });
    $('[data-version]').addEventListener('change', fillFiles);
    $('[data-file]').addEventListener('change', () => { versionsSerial += 1; invalidateQuote(); syncTarget(); });
    $('[data-target]').addEventListener('change', () => { versionsSerial += 1; invalidateQuote(); });
    $('[data-quote]').addEventListener('click', async () => {
      if (!versions || pending) return;
      if (!$('[data-target]').value) { message('Укажите, является выбранный файл checkpoint или diffusion model.', install, true); return; }
      const button = $('[data-quote]'); button.disabled = true; const serial = ++versionsSerial;
      const body = { kind: $('[data-source-form]').elements.kind.value, model_id: versions.modelID, version_id: Number($('[data-version]').value), file_id: Number($('[data-file]').value), target: $('[data-target]').value };
      try { const result = await api('quote', body); if (serial !== versionsSerial || departed) return; quote = result.quote; facts($('[data-quote-facts]'), [['Папка', result.destination], ['Файл', result.filename], ['Размер', size(result.size)], ['Свободно', size(result.free_bytes)]]); $('[data-quote-view]').hidden = false; $('[data-confirm]').disabled = false; message('', install); }
      catch (err) { if (serial === versionsSerial) message(err.message, install, true); } finally { button.disabled = false; }
    });
    $('[data-confirm]').addEventListener('click', async () => {
      if (!quote || pending) return; const id = crypto.randomUUID().replaceAll('-', ''); pending = { id, quote }; $('[data-confirm]').disabled = true;
      try { localStorage.setItem(pointerKey, JSON.stringify(pending)); } catch { /* The accepted server job survives browser closure. */ }
      quote = ''; await submitPending();
    });
    $('[data-check-acceptance]').addEventListener('click', async () => {
      if (!pending) return; const button = $('[data-check-acceptance]'); button.disabled = true;
      try { await api('installs?id=' + pending.id); loadJobs(); }
      catch (err) { if (err.status === 404 && pending?.quote) await submitPending(); else message(err.status === 404 ? 'Запрос не принят. Можно отменить его и подготовить установку заново.' : err.message, install, true); }
      finally { button.disabled = false; }
    });
    $('[data-close-request]').addEventListener('click', async () => {
      if (!pending) return; const button = $('[data-close-request]'); button.disabled = true;
      try { await api('close-request', { id: pending.id }); loadJobs(); }
      catch (err) { message(err.message, install, true); loadJobs(); }
      finally { button.disabled = false; }
    });
    $('[data-refresh-jobs]').addEventListener('click', loadJobs);
  }
  window.addEventListener('pagehide', () => { departed = true; checkBatch?.pause(); catalogSerial += 1; detailSerial += 1; versionsSerial += 1; clearTimeout(timer); });
  window.addEventListener('pageshow', () => { if (departed) { departed = false; checkBatch?.refresh(); loadCatalog(); loadBases(); loadJobs(); } });
  document.addEventListener('visibilitychange', () => { clearTimeout(timer); if (!document.hidden && !departed) loadJobs(); });
  let lastSensitive = showSensitive();
  new MutationObserver(() => {
    const current = showSensitive();
    if (departed || current === lastSensitive) return;
    lastSensitive = current;
    loadCatalog();
    if (detail.open && selected) renderDetail(selected);
  }).observe(document.body, { attributes: true, attributeFilter: ['class'] });
  loadCatalog(); loadBases(); loadJobs();
})();
