(function bootstrapTaskGPU(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.AIGatewayTaskGPU = api;
})(typeof window !== "undefined" ? window : null, function taskGPUFactory() {
  const pollInterval = 5000, maxAge = 10000;
  const reasons = {
    gpu_reserved: "GPU зарезервирован. Запуск ещё не подтверждён.",
    gpu_running: "Исполнитель подтвердил запуск на GPU.",
    executor_unconfirmed: "Завершение выполнения на GPU ещё не подтверждено.",
    cancellation_pending: "Ожидаем подтверждения отмены на GPU.",
    source_terminal: "Задача завершена. Запрос к GPU ещё проверяется.",
    owner_unavailable: "Доступ аккаунта к выполнению задач сейчас недоступен.",
    intent_expired: "Запрос к GPU ожидает повторного подтверждения.",
    gpu_in_use: "GPU занят другой задачей.",
    external_work: "GPU занят внешней задачей.",
    executor_unavailable: "Состояние исполнителя GPU пока не подтверждено.",
    waiting_turn: "Ожидаем очереди к GPU.",
    admission_pending: "Ожидаем подтверждения допуска к GPU."
  };
  const waiting = new Set(["executor_unconfirmed", "gpu_in_use", "external_work", "executor_unavailable", "waiting_turn", "admission_pending"]);
  const families = new Set(["generation", "lora_training", "lora_caption"]);
  const normalize = value => {
    if (value === null) return null;
    if (!value || !Object.hasOwn(reasons, value.code) || typeof value.snapshot_at !== "string" ||
        !/^\d{4}-\d{2}-\d{2}T/.test(value.snapshot_at) || !Number.isFinite(Date.parse(value.snapshot_at))) return undefined;
    return { code: value.code, snapshot_at: value.snapshot_at, position: waiting.has(value.code) && Number.isSafeInteger(value.position) && value.position > 0 ? value.position : 0 };
  };

  // Timeliness is bounded from request start using a monotonic clock. The server
  // timestamp labels historical evidence; client clock skew cannot renew it.
  // select updates source lifecycle only for an existing identity. Its seed is
  // used once; independent reads own later samples, so full-detail races cannot
  // replace a newer GPU observation. Consumers render onChange, not source state.
  const createMonitor = ({ fetchSnapshot, onChange, onAccessError = () => {}, now = () => performance.now(), setTimer = setTimeout, clearTimer = clearTimeout }) => {
    let source = null, sample, received = false, startedAt = 0, invalidated = true, failed = false;
    let active = true, connected = false, pending = null, epoch = 0, failures = 0, pollTimer, expiryTimer;
    const canWatch = () => source && (!source.terminal || sample !== null);
    const emit = () => {
      const fresh = received && !invalidated && active && connected && now() - startedAt < maxAge;
      let message = "Проверяем состояние GPU...", tone = "info";
      if (!connected || !active) { message = "Обновление GPU приостановлено. Позиция сейчас не подтверждена."; tone = "warning"; }
      else if (failed) { message = "Не удалось обновить состояние GPU. Позиция сейчас не подтверждена."; tone = "warning"; }
      else if (received && !fresh) { message = "Данные GPU устарели. Проверяем состояние..."; tone = "warning"; }
      else if (received) {
        message = sample ? reasons[sample.code] : "Позиция в общей очереди GPU пока не подтверждена.";
        if (sample && ["executor_unconfirmed", "executor_unavailable", "intent_expired", "owner_unavailable", "cancellation_pending"].includes(sample.code)) tone = "warning";
      }
      onChange({ hidden: !source || Boolean(source.terminal && sample === null), message, tone,
        position: fresh && sample ? sample.position : 0, timestamp: sample?.snapshot_at || "",
        fresh, pending: Boolean(pending), paused: !active });
    };
    const stop = () => {
      clearTimer(pollTimer); clearTimer(expiryTimer); pollTimer = expiryTimer = undefined;
      pending?.controller.abort(); pending = null; epoch++;
    };
    const armExpiry = () => {
      clearTimer(expiryTimer); expiryTimer = undefined;
      if (!active || !connected || !received || invalidated || !canWatch()) return;
      const remaining = maxAge - (now() - startedAt);
      if (remaining > 0) expiryTimer = setTimer(emit, remaining + 1);
    };
    const schedule = () => {
      clearTimer(pollTimer); pollTimer = undefined;
      if (active && connected && canWatch()) pollTimer = setTimer(refresh, Math.min(30000, pollInterval * (2 ** Math.min(failures, 3))));
    };
    const clear = () => {
      stop(); source = null; sample = undefined; received = false; invalidated = true; failed = false; failures = 0; emit();
    };
    const refresh = async () => {
      if (!active || !connected || !canWatch() || pending) return;
      clearTimer(pollTimer); pollTimer = undefined;
      const selected = { ...source }, operation = { controller: new AbortController(), epoch: ++epoch };
      const began = now(); pending = operation; emit();
      try {
        const response = await fetchSnapshot(selected, operation.controller.signal);
        if (operation.controller.signal.aborted || operation.epoch !== epoch) return;
        const next = normalize(response?.gpu);
        if (response?.kind !== selected.kind || response?.id !== selected.id || next === undefined) throw new Error("invalid_gpu_snapshot");
        sample = next; received = true; startedAt = began; invalidated = false; failed = false; failures = 0;
      } catch (error) {
        if (operation.controller.signal.aborted || operation.epoch !== epoch) return;
        if ([401, 403, 404].includes(error.status)) {
          clear(); onAccessError(error, selected); return;
        }
        failed = true; invalidated = true; failures++;
      } finally {
        if (pending === operation) { pending = null; armExpiry(); emit(); schedule(); }
      }
    };
    const select = (next, initial, requestStartedAt) => {
      if (!next || !families.has(next.kind)) { clear(); return; }
      const same = source && next.kind === source.kind && next.id === source.id;
      const watching = canWatch();
      if (same) {
        source = { ...next };
        if (!canWatch()) stop();
        emit();
        if (!watching && canWatch()) refresh();
        return;
      }
      stop(); source = { ...next }; sample = normalize(initial); failures = 0; failed = false;
      received = sample !== undefined; startedAt = Number.isFinite(requestStartedAt) ? requestStartedAt : -Infinity;
      invalidated = !active || !connected;
      emit(); armExpiry();
      if (!received || now() - startedAt >= maxAge) refresh(); else schedule();
    };
    const setConnected = value => {
      if (connected === value) return;
      connected = value;
      if (!value) { stop(); invalidated = true; }
      emit();
      if (value) refresh();
    };
    const setActive = value => {
      if (active === value) return;
      active = value;
      if (!value) { stop(); invalidated = true; }
      emit();
      if (value) refresh();
    };
    return { select, clear, refresh, setConnected, setActive };
  };
  const listLimit = 32;
  const rowReasons = {
    gpu_reserved: "GPU зарезервирован",
    gpu_running: "Выполняется на GPU",
    executor_unconfirmed: "GPU: выполнение уточняется",
    cancellation_pending: "GPU: ожидаем отмены",
    source_terminal: "GPU: завершение уточняется",
    owner_unavailable: "GPU: нет доступа к запуску",
    intent_expired: "GPU: запрос уточняется",
    gpu_in_use: "GPU занят другой задачей",
    external_work: "GPU занят внешней задачей",
    executor_unavailable: "GPU: состояние не подтверждено",
    waiting_turn: "Ожидаем GPU",
    admission_pending: "Ожидаем допуска к GPU"
  };
  const keyOf = source => `${source.kind}:${source.id}`;
  const validTime = value => typeof value === "string" && /^\d{4}-\d{2}-\d{2}T/.test(value) && Number.isFinite(Date.parse(value));
  const normalizeBatch = (response, sources) => {
    if (!validTime(response?.snapshot_at) || !Array.isArray(response.tasks) || response.tasks.length !== sources.length) return null;
    const expected = new Set(sources.map(keyOf)), entries = new Map();
    for (const value of response.tasks) {
      if (!value || typeof value.kind !== "string" || typeof value.id !== "string" || typeof value.available !== "boolean") return null;
      const key = keyOf(value), sample = normalize(value.gpu);
      if (!expected.has(key) || entries.has(key) || sample === undefined || (!value.available && sample !== null) ||
          (sample && sample.snapshot_at !== response.snapshot_at)) return null;
      entries.set(key, { sample, available: value.available });
    }
    return { timestamp: response.snapshot_at, entries };
  };

  // Only viewport members are retained. One request, one poll timer and one
  // earliest-expiry timer cover the bounded set, independently of task SSE.
  const createListMonitor = ({ fetchSnapshots, onChange, onUnavailable = () => {}, onAccessError = () => {},
    now = () => performance.now(), setTimer = setTimeout, clearTimer = clearTimeout }) => {
    let records = new Map(), active = true, connected = false, pending = null, epoch = 0, failures = 0, pollTimer, expiryTimer;
    const watching = record => record.available !== false && (!record.source.terminal || record.sample !== null || record.invalidated);
    const emit = () => onChange([...records].map(([key, record]) => {
      const fresh = record.received && !record.invalidated && active && connected && now() - record.startedAt < maxAge;
      let message = "GPU: проверяем состояние", tone = "muted";
      if (!active || !connected) message = "GPU: обновление приостановлено";
      else if (record.failed) message = "GPU: не удалось обновить";
      else if (record.received && !fresh) message = "GPU: данные устарели";
      else if (record.received && record.sample) { message = rowReasons[record.sample.code]; tone = "info"; }
      if (!fresh || record.sample && ["executor_unconfirmed", "executor_unavailable", "intent_expired", "owner_unavailable", "cancellation_pending"].includes(record.sample.code)) tone = "warning";
      return { key, hidden: record.available === false || record.sample === null || (record.source.terminal && !record.received),
        message, tone, fresh, position: fresh && record.sample ? record.sample.position : 0, timestamp: record.timestamp || "" };
    }));
    const stop = () => {
      clearTimer(pollTimer); clearTimer(expiryTimer); pollTimer = expiryTimer = undefined;
      pending?.controller.abort(); pending = null; epoch++;
    };
    const armExpiry = () => {
      clearTimer(expiryTimer); expiryTimer = undefined;
      if (!active || !connected) return;
      const deadlines = [...records.values()].filter(record => record.received && !record.invalidated)
        .map(record => maxAge - (now() - record.startedAt)).filter(delay => delay > 0);
      if (deadlines.length) expiryTimer = setTimer(() => { emit(); armExpiry(); }, Math.min(...deadlines) + 1);
    };
    const schedule = () => {
      clearTimer(pollTimer); pollTimer = undefined;
      if (active && connected && [...records.values()].some(watching)) {
        pollTimer = setTimer(refresh, Math.min(30000, pollInterval * (2 ** Math.min(failures, 3))));
      }
    };
    const clear = () => { stop(); records.clear(); failures = 0; emit(); };
    const refresh = async ({ force = false } = {}) => {
      if (!active || !connected || pending) return;
      const sources = [...records.values()].filter(record => force || watching(record)).map(record => ({ ...record.source }));
      if (!sources.length) return;
      clearTimer(pollTimer); pollTimer = undefined;
      const operation = { controller: new AbortController(), epoch: ++epoch }, began = now();
      pending = operation;
      try {
        const response = await fetchSnapshots(sources, operation.controller.signal);
        if (operation.controller.signal.aborted || operation.epoch !== epoch) return;
        const next = normalizeBatch(response, sources);
        if (!next) throw new Error("invalid_gpu_snapshots");
        const unavailable = [];
        next.entries.forEach((value, key) => {
          const record = records.get(key);
          Object.assign(record, value, { received: true, invalidated: false, failed: false, startedAt: began, timestamp: next.timestamp });
          if (!value.available) unavailable.push({ ...record.source });
        });
        failures = 0;
        if (unavailable.length) onUnavailable(unavailable);
      } catch (error) {
        if (operation.controller.signal.aborted || operation.epoch !== epoch) return;
        if ([401, 403].includes(error.status)) { clear(); onAccessError(error); return; }
        sources.forEach(source => Object.assign(records.get(keyOf(source)), { failed: true, invalidated: true }));
        failures++;
      } finally {
        if (pending === operation) { pending = null; emit(); armExpiry(); schedule(); }
      }
    };
    const select = sources => {
      const next = new Map();
      for (const source of sources || []) {
        if (!source || !families.has(source.kind) || typeof source.id !== "string" || !/^[A-Za-z0-9_-]{1,200}$/.test(source.id)) continue;
        const key = keyOf(source);
        if (next.has(key)) continue;
        if (next.size === listLimit) break;
        next.set(key, { kind: source.kind, id: source.id, terminal: Boolean(source.terminal) });
      }
      if (next.size === records.size && [...next].every(([key, source]) => records.get(key)?.source.terminal === source.terminal)) return;
      stop(); failures = 0;
      const retained = new Map();
      next.forEach((source, key) => {
        const previous = records.get(key);
        retained.set(key, previous && previous.source.terminal === source.terminal ? previous :
          { source, sample: undefined, received: false, invalidated: true, failed: false });
      });
      records = retained; emit(); armExpiry(); refresh();
    };
    const invalidate = () => records.forEach(record => {
      record.invalidated = true;
      // A fresh read must precede any claim after a page/connection pause.
      if (record.available === false) record.available = undefined;
    });
    const setActive = value => {
      if (active === value) return;
      active = value;
      if (!value) { stop(); invalidate(); }
      emit(); if (value) refresh();
    };
    const setConnected = value => {
      if (connected === value) return;
      connected = value;
      if (!value) { stop(); invalidate(); }
      emit(); if (value) refresh();
    };
    return { select, clear, refresh, setActive, setConnected };
  };
  return { reasons, normalize, createMonitor, pollInterval, maxAge, listLimit, normalizeBatch, createListMonitor };
});
