(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.AIGatewayLoraQuality = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";
  const valid = q => q?.version === 1 && typeof q.low_detail === "boolean" && Number.isFinite(q.detail_score)
    && Array.isArray(q.colors) && q.colors.length === 192 && q.colors.every(v => Number.isInteger(v) && v >= 0 && v <= 255);
  const key = asset => `${asset.id}:${asset.sha256}`;
  const distance = (a, b, border = false) => {
    let total = 0, count = 0;
    for (let i = 0; i < 192; i++) {
      const cell = Math.floor(i / 3), x = cell % 8, y = Math.floor(cell / 8);
      if (border && x > 0 && x < 7 && y > 0 && y < 7) continue;
      total += Math.abs(a[i] - b[i]); count++;
    }
    return total / count;
  };
  const recommendations = (images, assets, results) => {
    const warnings = new Map(), unique = [], seen = new Set();
    let checked = 0, total = 0;
    images.forEach((item, index) => {
      if (item.excluded) return;
      total++;
      const asset = assets[item.asset_id], q = asset && results.get(key(asset));
      if (!valid(q)) return;
      checked++;
      const notes = [];
      if (q.low_detail) notes.push("Мало мелких деталей. Проверьте резкость в полном размере; для мягкого света или простого фона это может быть нормально.");
      const earlier = unique.find(other => other.asset.sha256 !== asset.sha256
        && Math.abs(Math.log((asset.width / asset.height) / (other.asset.width / other.asset.height))) < .08
        && distance(q.colors, other.q.colors) < 6);
      if (earlier) notes.push(`Похожие цвета и композиция с кадром ${earlier.index + 1}. Сравните, нужен ли в наборе каждый из них.`);
      warnings.set(item.id, notes);
      if (!seen.has(asset.sha256)) { unique.push({ asset, q, index }); seen.add(asset.sha256); }
    });
    // Compare border colors only. This does not identify the actual background,
    // faces, objects, or semantic duplicates. Exact duplicates do not vote twice.
    let background = false;
    if (unique.length >= 3) background = unique.some(candidate => unique.filter(other => distance(candidate.q.colors, other.q.colors, true) < 10).length >= Math.max(3, Math.ceil(unique.length * .7)));
    return { warnings, checked, total, background };
  };
  const createInspector = ({ request, onChange = () => {} }) => {
    const state = { running: false, hasRun: false, processed: 0, total: 0, error: "", cancelled: false, results: new Map() };
    let abort, epoch = 0;
    const cancel = () => { if (state.running) { state.cancelled = true; abort?.abort(); onChange(); } };
    const reset = (assets = {}) => {
      epoch++; abort?.abort(); abort = null;
      Object.assign(state, { running: false, hasRun: false, processed: 0, total: 0, error: "", cancelled: false }); state.results.clear();
      for (const asset of Object.values(assets)) if (valid(asset.quality)) state.results.set(key(asset), asset.quality);
      state.hasRun = state.results.size > 0; onChange();
    };
    const run = async (images, assets) => {
      if (state.running) return;
      const mine = ++epoch;
      abort = new AbortController(); const signal = abort.signal;
      const unique = new Map();
      for (const item of images) if (!item.excluded && assets[item.asset_id]) { const asset = assets[item.asset_id]; unique.set(key(asset), { ...asset }); }
      const pending = [...unique.values()].filter(asset => !valid(state.results.get(key(asset))));
      Object.assign(state, { running: true, hasRun: true, processed: unique.size - pending.length, total: unique.size, error: "", cancelled: false }); onChange();
      try {
        for (const asset of pending) {
          if (signal.aborted) break;
          const response = await request(asset.id, signal);
          if (mine !== epoch || signal.aborted) return;
          if (response.asset_id !== asset.id || response.sha256 !== asset.sha256 || !valid(response.quality)) throw new Error("Получен неполный результат проверки. Повторите проверку.");
          state.results.set(key(asset), response.quality); state.processed++; onChange();
        }
      } catch (error) {
        if (mine === epoch && !signal.aborted) state.error = error.message || "Проверка не удалась. Повторите её.";
      } finally {
        if (mine === epoch) { state.running = false; abort = null; onChange(); }
      }
    };
    return { state, run, cancel, reset };
  };
  return { valid, distance, recommendations, createInspector };
});
