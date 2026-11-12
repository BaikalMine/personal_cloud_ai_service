(function bootstrapAssistantProgress(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) (root.AIGatewayGeneration ||= {}).assistantProgress = api;
})(typeof window !== "undefined" ? window : null, function assistantProgressFactory() {
  const duration = ms => {
    const seconds = Math.floor(Math.max(0, Number(ms) || 0) / 1000);
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  };
  const metrics = (job, now = Date.now()) => {
    const progress = job?.progress || {};
    const completed = job?.state === "completed";
    const terminal = ["completed", "failed", "cancelled"].includes(job?.state);
    const started = Date.parse(progress.started_at || "");
    const created = Date.parse(job?.created_at || "");
    const finished = Date.parse(progress.finished_at || "");
    const usage = completed ? job.result?.usage || progress.usage || {} : progress.phase === "finishing" ? progress.usage || {} : {};
    const elapsed = Number.isFinite(started)
      ? Math.max(0, (Number.isFinite(finished) ? finished : now) - started)
      : Number(usage.total_duration_ms) || (!terminal && Number.isFinite(created) ? Math.max(0, now - created) : 0);
    const input = Number(usage.prompt_tokens), output = Number(usage.completion_tokens);
    const known = Number.isFinite(input) && Number.isFinite(output) && input >= 0 && output >= 0;
    const rate = output > 0 && Number(usage.completion_duration_ms) > 0 ? output * 1000 / usage.completion_duration_ms : null;
    const phase = terminal ? completed ? "Вариант готов" : job.state === "cancelled" ? "Запрос отменён" : "Запрос завершён с ошибкой"
      : progress.phase === "thinking" ? "Модель обдумывает запрос"
      : progress.phase === "writing" ? "Пишем промпт"
      : progress.phase === "finishing" ? "Проверяем ответ"
      : Number.isFinite(started) ? "Подготовка модели" : "Ожидание запуска";
    return { phase, time: `${!terminal && !Number.isFinite(started) ? "Ожидание" : "Время"} ${duration(elapsed)}`,
      tokens: known ? `Токены: ${input.toLocaleString("ru-RU")} вход · ${output.toLocaleString("ru-RU")} выход` : "Токены: ожидаем данные модели",
      speed: rate === null ? "" : `${rate.toLocaleString("ru-RU", { maximumFractionDigits: 1 })} ток/с`,
      prompt: terminal ? "" : typeof progress.prompt === "string" ? progress.prompt : "", terminal };
  };
  const bindUI = ({ root, now = Date.now }) => {
    const panel = root.querySelector("#prompt-assistant-live");
    if (!panel) return { update() {}, reset() {} };
    const text = root.querySelector("#prompt-assistant-live-text");
    const fields = ["phase", "time", "tokens", "speed"].map(name => [name, root.querySelector(`#prompt-assistant-live-${name}`)]);
    return {
      reset() { panel.hidden = true; text.textContent = ""; },
      update(job) {
        if (!job) return;
        panel.hidden = false;
        const view = metrics(job, now());
        for (const [name, node] of fields) {
          if (node.textContent !== view[name]) node.textContent = view[name];
          node.hidden = name === "speed" && !view.speed;
        }
        const following = text.scrollHeight - text.scrollTop - text.clientHeight < 32;
        if (text.textContent !== view.prompt) {
          text.textContent = view.prompt;
          if (following) text.scrollTop = text.scrollHeight;
        }
        text.hidden = !view.prompt;
        panel.dataset.phase = view.terminal ? "finished" : "pending";
      },
    };
  };
  return { duration, metrics, bindUI };
});
