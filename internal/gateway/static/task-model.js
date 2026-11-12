(function bootstrapTasks(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.AIGatewayTasks = api;
})(typeof window !== "undefined" ? window : null, function taskModelFactory() {
  const kinds = { generation: "Генерация", lora_training: "Обучение LoRA", lora_caption: "Описание кадра", generation_batch: "Пакет генераций", lora_caption_series: "Серия описаний" };
  const icons = { generation: "image", lora_training: "scan-face", lora_caption: "captions", generation_batch: "layers", lora_caption_series: "captions" };
  const labels = { draft: "Создано", queued: "В очереди", waiting_for_resources: "Ожидание ресурсов", preparing: "Подготовка", uploading: "Передача", caching: "Кеширование", running: "Выполняется", postprocessing: "Постобработка", archiving: "Сохранение", installing: "Установка", completed: "Готово", failed: "Ошибка", cancelled: "Отменено", expired: "Истекло", partial: "Завершено частично", cancelling: "Отменяется" };
  const terminal = state => ["completed", "failed", "cancelled", "expired", "partial"].includes(state);
  const key = task => `${task.kind}:${task.id}`;
  const selection = value => {
    const [kind, id, extra] = String(value || "").split(":");
    return kinds[kind] && /^[A-Za-z0-9_-]{1,200}$/.test(id || "") && extra === undefined ? { kind, id } : null;
  };
  const status = task => {
    if (task.cancellation_pending && !terminal(task.state)) return { label: "Отменяется", tone: "warning" };
    const tone = task.state === "completed" ? "success" : task.state === "failed" ? "error" : ["cancelled", "expired"].includes(task.state) ? "muted" : ["partial", "cancelling"].includes(task.state) ? "warning" : "info";
    return { label: labels[task.state] || "Состояние уточняется", tone };
  };
  const rowAction = task => {
    const inspect = { code: "inspect", icon: "arrow-up-right", label: task.cancellation_pending ? "Смотреть статус" : task.state === "failed" ? "Разобрать ошибку" : task.state === "completed" ? "Открыть результат" : "Открыть задачу" };
    if (task.cancellation_pending) return inspect;
    switch (task.next_action) {
      case "cancel": return { code: "cancel", icon: "square", label: "Отменить задачу" };
      case "edit_generation": return task.kind === "generation" ? { code: "edit_generation", icon: "sliders-horizontal", label: task.state === "completed" ? "Использовать параметры" : task.state === "expired" ? "Повторить с параметрами" : "Изменить и повторить" } : inspect;
      case "open_caption": return task.kind === "lora_caption" ? { code: "open_caption", icon: "image", label: ["failed", "cancelled"].includes(task.state) ? "Исправить описание" : "Открыть кадр" } : inspect;
      case "open_training": return task.kind === "lora_training" ? { code: "open_training", icon: "folder-open", label: "Открыть исходную версию" } : inspect;
      case "download_training": return task.kind === "lora_training" ? { code: "download_training", icon: "download", label: "Скачать LoRA" } : inspect;
      default: return inspect;
    }
  };
  const seriesSummary = task => {
    const summary = task.kind === "lora_caption_series" ? task.series : null;
    if (!summary || !["total", "completed", "failed", "cancelled", "unavailable"].every(name => Number.isSafeInteger(summary[name]) && summary[name] >= 0) ||
        summary.completed + summary.failed + summary.cancelled + summary.unavailable > summary.total) return "";
    return [`Готово ${summary.completed} из ${summary.total}`,
      summary.failed ? `Ошибки: ${summary.failed}` : "", summary.cancelled ? `Отменено: ${summary.cancelled}` : "",
      summary.unavailable ? `Недоступно: ${summary.unavailable}` : ""].filter(Boolean).join(" · ");
  };
  const merge = pages => {
    const byKey = new Map();
    pages.flat().forEach(task => { if (task?.id && kinds[task.kind]) byKey.set(key(task), task); });
    return [...byKey.values()].sort((a, b) => {
      const time = new Date(b.created_at).getTime() - new Date(a.created_at).getTime();
      if (time) return time;
      const left = key(a), right = key(b);
      return left < right ? 1 : left > right ? -1 : 0;
    });
  };
  const grouped = tasks => {
    const rows = [], batches = new Map();
    tasks.forEach(task => {
      if (task.kind !== "generation" || !task.batch_id) { rows.push({ task }); return; }
      let group = batches.get(task.batch_id);
      if (!group) {
        group = { id: task.batch_id, name: task.name, children: [] };
        batches.set(task.batch_id, group);
        rows.push({ batch: group });
      }
      group.children.push(task);
    });
    return rows;
  };
  const generation = job => ({ kind: "generation", id: job.job_id, name: job.operation === "postprocess" ? job.template_id === "postprocess-video-v1" ? "Постобработка видео" : "Постобработка изображения" : job.model_name || "Генерация", state: job.job_state || job.state, message: job.message || "", cancellation_pending: job.state === "cancelling", created_at: job.created_at, updated_at: job.updated_at, finished_at: job.finished_at, expires_at: job.expires_at, batch_id: job.batch_id, batch_position: job.batch_position, media: job.media || [] });
  const detailTask = detail => {
    if (detail.caption_series) {
      const series = detail.caption_series;
      return { kind: "lora_caption_series", id: series.id, name: series.name, state: series.state, message: `${series.completed_count} готово из ${series.total_count}`, created_at: series.created_at, updated_at: series.updated_at, expires_at: series.expires_at, dataset_id: series.dataset_id };
    }
    if (detail.generation) return generation(detail.generation);
    if (detail.training) {
      const job = detail.training;
      return { kind: "lora_training", id: job.id, name: job.name, state: job.state, stage: job.stage, message: job.message, cancellation_pending: Boolean(detail.cancellation_pending), created_at: new Date(job.created_at).toISOString(), updated_at: new Date(job.updated_at).toISOString(), progress: ["preparing", "caching", "running", "installing", "completed"].includes(job.state) ? job.progress : null };
    }
    if (detail.caption) {
      const job = detail.caption;
      return { kind: "lora_caption", id: job.job_id, name: job.source?.image?.name || "Описание кадра", state: job.state, message: job.status, cancellation_pending: job.cancel_requested && !terminal(job.state), created_at: job.created_at, updated_at: job.updated_at, expires_at: job.expires_at, dataset_id: job.dataset_id, image_id: job.image_id };
    }
    if (detail.batch) {
      const job = detail.batch;
      return { kind: "generation_batch", id: job.batch_id, name: job.model_name || "Пакет генераций", state: job.state, message: `${job.completed_count} готово из ${job.total_count}`, cancellation_pending: job.cancellation_requested && !terminal(job.state), created_at: job.created_at, updated_at: job.updated_at };
    }
    throw new Error("Не удалось прочитать задачу.");
  };
  const dateBoundary = (value, nextDay) => {
    if (!value) return "";
    if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) throw new Error("Проверьте выбранные даты.");
    const [year, month, day] = value.split("-").map(Number);
    if (year < 1 || year > 9999) throw new Error("Проверьте выбранные даты.");
    const date = new Date(0);
    date.setFullYear(year, month - 1, day);
    date.setHours(0, 0, 0, 0);
    if (date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== day) throw new Error("Проверьте выбранные даты.");
    if (nextDay) date.setDate(date.getDate() + 1);
    return date.toISOString();
  };
  const apiQuery = (filters, cursor = "") => {
    const params = new URLSearchParams({ limit: "40" });
    ["kind", "state", "search"].forEach(name => { if (filters[name]) params.set(name, filters[name]); });
    const from = dateBoundary(filters.from, false), before = dateBoundary(filters.to, true);
    if (from && before && from >= before) throw new Error("Начальная дата должна быть не позже конечной.");
    if (from) params.set("from", from);
    if (before) params.set("before", before);
    if (cursor) params.set("cursor", cursor);
    return params;
  };
  return { kinds, icons, labels, terminal, key, selection, status, rowAction, seriesSummary, merge, grouped, generation, detailTask, dateBoundary, apiQuery };
});
