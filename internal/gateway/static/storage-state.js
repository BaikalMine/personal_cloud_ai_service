(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.AIGatewayStorage = api;
})(typeof window === "undefined" ? null : window, function () {
  "use strict";
  const labels = {
    not_attempted: "Попыток ещё не было", not_queued: "Не поставлен на удаление",
    pending: "Ожидает удаления", cleanup_pending: "Ожидает удаления", retained: "Хранится", tracked: "Учтён", stored: "Загружен", reserved: "Загрузка не подтверждена",
    preview: "Ожидает подтверждения", dispatching: "Отправлено на хост", unconfirmed: "Результат не подтверждён", completed: "Завершено", partial: "Завершено частично", rejected: "Не выполнено", closed_unconfirmed: "Закрыто без подтверждения", cancelled: "Отменено", expired: "Срок истёк",
    present: "На диске", missing: "Файл отсутствует", matched: "Содержимое совпадает", mismatched: "Содержимое изменилось", deleted: "Удалён", unavailable: "Нет данных с хоста", host_unavailable: "Хост недоступен", invalid_response: "Ответ хоста не прошёл проверку", io_error: "Ошибка чтения", not_started: "Удаление не начиналось",
    original_model: "Исходная модель", normalized_model: "Копия модели", trained_artifact: "Обученная LoRA", dataset_archive: "Архив датасета", dataset_files: "Файлы датасета", training_cache: "Кэш обучения", training_output: "Выход обучения", unmanaged_file: "Неуправляемый объект",
    active_job: "Используется обучением", training_busy: "Обучение активно", submission_in_progress: "Принимается задача", execution_unconfirmed: "Исполнение не подтверждено", storage_cleanup_active: "Очистка уже выполняется", source_model_missing: "Исходная модель отсутствует", source_model_unavailable: "Исходная модель недоступна", source_profile_missing: "Исходный профиль отсутствует", artifact_lifecycle: "Удаление через владельца LoRA", unmanaged_path: "Вне управляемой очистки", original_model_alias: "Совпадает с исходной моделью",
    generation_active: "Есть активная генерация", executor_occupied: "Исполнитель занят", generation_inputs_unknown: "Входы активной задачи неизвестны", generation_input_reference: "Вход активной генерации", postprocess_reference: "Используется постобработкой", upload_unconfirmed: "Загрузка не подтверждена", fingerprint_unavailable: "Нет подтверждённого размера или хэша", executor_not_fenced: "Безопасная очистка не включена",
    retention: "Срок хранения", profile_hidden: "Скрыт из результатов", upload_reservation: "Резерв загрузки", legacy_retry: "Исторический повтор без квитанции", legacy_confirmed: "Историческое подтверждение без объёма", confirmation_required: "Требуется подтверждение",
    storage_admission_busy: "Ресурс занят другой работой", storage_cleanup_disabled: "Безопасная очистка не включена", storage_read_only: "Восстановленный стенд: только чтение", restore_drill: "Восстановленный стенд: только чтение", storage_operation_changed: "Выбранные данные изменились. Обновите список.", storage_selection_changed: "Выбранные данные изменились. Обновите список.", storage_operation_expired: "Срок проверки истёк", storage_operation_missing: "Операция пока не найдена", storage_operation_denied: "Этой операцией управляет другой администратор", storage_operation_failed: "Не удалось получить результат операции", invalid_storage_request: "Некорректный запрос", csrf_failed: "Сессия устарела. Обновите страницу.", access_denied: "Доступ закрыт",
    training_storage_unavailable: "Агент обучения недоступен", training_storage_not_configured: "Агент обучения не настроен", training_storage_unsupported: "Агент обучения требует обновления", training_storage_invalid_response: "Данные агента обучения не прошли проверку", comfy_storage_unavailable: "Windows-agent или реестр недоступен", comfy_storage_invalid_response: "Данные Windows-agent не прошли проверку", host_inventory_unavailable: "Не удалось прочитать файлы на хосте", registry_unavailable: "Реестр временно недоступен",
    root_missing: "Каталог отсутствует", root_unavailable: "Каталог недоступен", root_rejected: "Каталог не прошёл проверку", scan_limit: "Достигнут предел обхода", scan_error: "Не все пути удалось прочитать", scan_changed: "Файлы менялись во время обхода", inventory_changed: "Список файлов изменился. Обновите его с первой страницы.", not_configured: "Агент не настроен", unavailable_directory: "Каталог не прочитан", link: "Ссылка: без обхода", other: "Необычный тип файла", file: "Файл", unregistered_path: "Не учтён в реестре", registered_path: "Путь есть в реестре", host_changed: "Изменился хост", host_running: "Хост продолжает работу", host_unconfirmed: "Хост не подтвердил завершение", result_write_failed: "Результат ещё не сохранён", cancelled_before_dispatch: "Отменено до отправки"
  };
  const label = code => labels[code] || "Требуется проверка";
  const active = op => ["dispatching", "unconfirmed"].includes(op?.state);
  const terminal = op => ["completed", "partial", "rejected", "closed_unconfirmed", "cancelled", "expired"].includes(op?.state);
  const tone = code => ["completed", "deleted", "matched"].includes(code) ? "success" : ["rejected", "mismatched", "io_error"].includes(code) ? "danger" : ["unconfirmed", "partial", "closed_unconfirmed", "reserved", "unavailable", "host_unavailable", "expired"].includes(code) ? "warning" : "info";
  const operationID = (value, kind = "comfy") => typeof value === "string" && (kind === "training" ? /^[A-Za-z0-9_-]{32,96}$/ : /^[a-f0-9]{32}$/).test(value);
  const version = value => typeof value === "string" && /^[a-f0-9]{64}$/.test(value);
  const registryKey = value => typeof value === "string" && (/^(?:cleanup|output):[1-9][0-9]*$/.test(value) || /^input:[A-Za-z0-9_-]{32,128}$/.test(value));
  const trainingID = value => typeof value === "string" && /^(?:original_model|normalized_model|trained_artifact|dataset_archive|dataset_files|training_cache|training_output|unmanaged_file):[A-Za-z0-9_-]{1,96}$/.test(value);
  function canPrepare(item, now) {
    return !!item?.pending && !!item.can_inspect && !item.reference_protected && !String(item.key).startsWith("output:") &&
      registryKey(item.key) && version(item.version) && Number.isFinite(Date.parse(item.eligible_at)) && Date.parse(item.eligible_at) <= now &&
      Array.isArray(item.protection) && item.protection.every(code => code === "executor_not_fenced");
  }
  function unresolvedInputs(data) {
    const identity = value => value === null || typeof value === "string" && /^[1-9][0-9]*$/.test(value);
    if (data?.scope !== "unresolved_input_registry" || !Array.isArray(data.items) || data.items.length > 100 || typeof data.next_after !== "string" || data.next_after.length > 192) throw new Error("invalid_response");
    const seen = new Set();
    const items = data.items.map(item => {
      if (!item || typeof item.id !== "string" || !item.id || item.id.length > 192 || seen.has(item.id) ||
          !identity(item.user_id) || !identity(item.job_id) || item.can_cleanup !== false ||
          typeof item.filename !== "string" || !item.filename || typeof item.subfolder !== "string" ||
          !Number.isSafeInteger(item.size_bytes) || item.size_bytes < 0 || item.size_bytes > 536870912 ||
          typeof item.sha256 !== "string" || !/^[a-f0-9]{64}$/.test(item.sha256) ||
          typeof item.state !== "string" || !Number.isFinite(Date.parse(item.created_at))) throw new Error("invalid_response");
      seen.add(item.id);
      return { id: item.id, user_id: item.user_id, job_id: item.job_id, filename: item.filename, subfolder: item.subfolder,
        size_bytes: item.size_bytes, sha256: item.sha256, state: item.state, created_at: item.created_at, can_cleanup: false };
    });
    if (data.next_after && (!items.length || data.next_after !== items[items.length - 1].id)) throw new Error("invalid_response");
    return { items, next: data.next_after };
  }
  function history(data) {
    return { items: data.items || data.Items || [], total: data.total ?? data.Total ?? 0, freed: data.freed_bytes ?? data.FreedBytes ?? 0, since: data.since || data.Since, checked: data.checked_at || data.CheckedAt };
  }
  function readPointer(raw) {
    try {
      if (typeof raw !== "string" || raw.length > 16384) return null;
      const p = JSON.parse(raw);
      if (p?.v !== 1 || !["comfy", "training"].includes(p.kind) || !operationID(p.id, p.kind) || !["prepare", "confirm", "known"].includes(p.phase)) return null;
      if (!Array.isArray(p.selection) || !p.selection.length || p.selection.length > 50) return null;
      const keys = new Set();
      for (const item of p.selection) {
        const key = p.kind === "comfy" ? item?.key : item;
        if (p.kind === "comfy" ? !registryKey(key) || key.startsWith("output:") || !version(item?.version) : !trainingID(key)) return null;
        if (keys.has(key)) return null;
        keys.add(key);
      }
      return { v: 1, kind: p.kind, id: p.id, phase: p.phase, selection: p.selection.map(item => p.kind === "comfy" ? { key: item.key, version: item.version } : item) };
    } catch (_) { return null; }
  }
  function storagePointer(storage, key) {
    const read = () => {
      const raw = storage.getItem(key);
      if (!raw) return null;
      const p = readPointer(raw);
      if (!p) throw new Error("recovery_invalid");
      return p;
    };
    return {
      read,
      write(pointer, expectedID = null) {
        const valid = readPointer(JSON.stringify(pointer));
        if (!valid) throw new Error("recovery_invalid");
        const previous = read();
        if (previous && previous.id !== expectedID) throw new Error("recovery_conflict");
        if (previous && (previous.kind !== valid.kind || JSON.stringify(previous.selection) !== JSON.stringify(valid.selection) || previous.phase === "confirm" && valid.phase !== "confirm")) throw new Error("recovery_conflict");
        const raw = JSON.stringify(valid);
        storage.setItem(key, raw);
        if (storage.getItem(key) !== raw) throw new Error("recovery_unavailable");
        return valid;
      },
      clear(id) {
        const previous = read();
        if (!previous || previous.id !== id) return;
        storage.removeItem(key);
        if (storage.getItem(key) !== null) throw new Error("recovery_unavailable");
      }
    };
  }
  return { label, tone, active, terminal, operationID, version, registryKey, trainingID, canPrepare, unresolvedInputs, history, readPointer, storagePointer };
});
