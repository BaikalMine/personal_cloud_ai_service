(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  if (root) root.ModelDownloads = api;
})(typeof window !== 'undefined' ? window : null, function () {
  'use strict';
  const labels = { queued: 'В очереди', running: 'Загрузка', reconciling: 'Проверяем файл', completed: 'Установлено', failed: 'Ошибка загрузки', cancelled: 'Отменено' };
  const number = value => new Intl.NumberFormat('ru-RU', { maximumFractionDigits: 1 }).format(value);
  function bytes(value) {
    if (!Number.isFinite(value) || value < 0) return '';
    const units = ['Б', 'КБ', 'МБ', 'ГБ'];
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
    return number(value) + ' ' + units[unit];
  }
  function metrics(job) {
    const known = job.metrics_available || job.progress > 0 || job.state === 'completed';
    const progress = Number.isFinite(job.progress) ? Math.max(0, Math.min(100, job.progress)) : 0;
    const total = job.total_bytes || job.size_bytes;
    const volume = Number.isFinite(job.bytes_downloaded) ? bytes(job.bytes_downloaded) + (total > 0 ? ' / ' + bytes(total) : '') : total > 0 ? bytes(total) : '';
    const speed = job.state === 'running' && progress < 100 && Number.isFinite(job.bytes_per_second) && job.bytes_per_second >= 0 ? bytes(job.bytes_per_second) + '/с' : '';
    let eta = '';
    if (speed && job.bytes_per_second > 0 && Number.isFinite(job.bytes_downloaded) && total >= job.bytes_downloaded) {
      const seconds = Math.ceil((total - job.bytes_downloaded) / job.bytes_per_second);
      eta = seconds >= 60 ? Math.ceil(seconds / 60) + ' мин' : seconds + ' сек';
    }
    return { known, progress, percent: known ? number(progress) + ' %' : 'Ожидание данных', volume, speed, eta };
  }
  function create({ container, opener, icons = () => {}, onComplete = () => {}, focusFallback = () => {} }) {
    const document = container.ownerDocument;
    const list = container.querySelector('[data-download-notice-list]');
    const toggle = container.querySelector('[data-download-notice-toggle]');
    const states = new Map(), visible = new Set(), dismissed = new Map(), timers = new Map();
    let jobs = [], collapsed = true, offline = false;
    const node = (tag, text, className) => { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; if (className) el.className = className; return el; };
    const icon = name => { const el = node('i'); el.dataset.lucide = name; el.setAttribute('aria-hidden', 'true'); return el; };
    toggle.addEventListener('click', () => { collapsed = !collapsed; paint(); });
    opener.addEventListener('click', () => { collapsed = !collapsed; paint(); });
    function paint() {
      const hadFocus = container.contains(document.activeElement);
      const displayed = jobs.filter(job => visible.has(job.id) && dismissed.get(job.id) !== job.state);
      container.hidden = !displayed.length || collapsed;
      opener.hidden = !displayed.length;
      opener.setAttribute('aria-expanded', String(!collapsed));
      opener.replaceChildren(icon('download'), node('span', 'Загрузки · ' + displayed.length));
      toggle.setAttribute('aria-expanded', String(!collapsed));
      toggle.replaceChildren(icon(collapsed ? 'chevron-down' : 'chevron-up'), node('span', 'Загрузки · ' + displayed.length));
      list.hidden = collapsed;
      const existing = new Map([...list.children].map(row => [row.dataset.id, row]));
      for (const job of displayed) {
        let row = existing.get(job.id); existing.delete(job.id);
        if (!row) {
          row = node('article', undefined, 'model-download-notice'); row.dataset.id = job.id;
          const heading = node('header'); heading.append(node('strong'), node('button', undefined, 'ghost task-icon-button'));
          const close = heading.querySelector('button'); close.type = 'button'; close.append(icon('x'));
          close.title = 'Скрыть уведомление'; close.setAttribute('aria-label', 'Скрыть уведомление: ' + job.name);
          close.addEventListener('click', () => { dismissed.set(job.id, states.get(job.id)); paint(); if (container.hidden) focusFallback(); else toggle.focus(); });
          row.append(heading, node('p', undefined, 'model-download-notice-state'), node('progress'), node('div', undefined, 'model-download-notice-metrics'), node('p', undefined, 'model-download-notice-message'));
          list.append(row);
        }
        const data = metrics(offline ? { ...job, bytes_per_second: null } : job);
        row.dataset.state = job.state;
        row.querySelector('strong').textContent = job.name || job.filename;
        row.querySelector('.model-download-notice-state').textContent = (labels[job.state === 'running' && data.progress >= 100 ? 'reconciling' : job.state] || job.state) + (['running', 'reconciling'].includes(job.state) ? ' · ' + data.percent : '');
        const progress = row.querySelector('progress'); progress.max = 100;
        if (data.known) progress.value = data.progress; else progress.removeAttribute('value');
        progress.hidden = !['running', 'reconciling'].includes(job.state); progress.setAttribute('aria-label', 'Загрузка: ' + job.name);
        const stats = row.querySelector('.model-download-notice-metrics');
        stats.replaceChildren(node('span', data.volume), node('span', data.speed || (job.state === 'running' && data.progress < 100 ? 'Скорость недоступна' : '')));
        if (data.eta) stats.append(node('span', 'Осталось ≈ ' + data.eta));
        row.querySelector('.model-download-notice-message').textContent = job.message || '';
      }
      for (const row of existing.values()) row.remove();
      if (hadFocus && container.hidden) { if (opener.hidden) focusFallback(); else opener.focus(); }
      icons();
    }
    return {
      reveal(job) { visible.add(job.id); dismissed.delete(job.id); if (!jobs.some(item => item.id === job.id)) jobs.unshift(job); collapsed = false; paint(); },
      render(next) {
        jobs = next;
        for (const job of jobs) {
          const previous = states.get(job.id);
          if (['queued', 'running', 'reconciling'].includes(job.state) || previous && previous !== job.state) visible.add(job.id);
          if (previous !== job.state && visible.has(job.id)) {
            container.querySelector('[data-download-announcement]').textContent = (job.name || job.filename) + ': ' + (labels[job.state] || job.state);
            if (job.state === 'completed') onComplete();
            if (['completed', 'cancelled'].includes(job.state) && !timers.has(job.id)) timers.set(job.id, setTimeout(() => { timers.delete(job.id); visible.delete(job.id); paint(); }, 8000));
          }
          states.set(job.id, job.state);
        }
        paint();
      },
      connectionLost() { offline = true; container.querySelector('[data-download-connection]').hidden = false; paint(); },
      connectionRestored() { offline = false; container.querySelector('[data-download-connection]').hidden = true; paint(); },
      destroy() { for (const timer of timers.values()) clearTimeout(timer); timers.clear(); },
    };
  }
  return { create, metrics, bytes, labels };
});
