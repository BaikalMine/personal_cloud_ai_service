(function bootstrapGenerationHistory(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.history = api;
})(typeof window !== "undefined" ? window : null, function generationHistoryFactory() {
  const createState = (overrides = {}) => ({ variants: [], collapsed: false, stateFilter: "", templateFilter: "", ...overrides });
  const reduce = (state = createState(), action = {}) => {
    switch (action.type) {
      case "SET_VARIANTS":
        return { ...state, variants: Array.isArray(action.variants) ? action.variants : [] };
      case "SET_COLLAPSED":
        return { ...state, collapsed: Boolean(action.collapsed) };
      case "TOGGLE_COLLAPSED":
        return { ...state, collapsed: !state.collapsed };
      case "SET_FILTERS":
        return { ...state, stateFilter: String(action.stateFilter || ""), templateFilter: String(action.templateFilter || "") };
      default:
        return state;
    }
  };
  const normalizedState = value => value === "failed" ? "error" : value === "waiting_resources" ? "waiting_for_resources" : value;
  const filterJobs = (items, state) => (Array.isArray(items) ? items : []).filter((job) => (
    (!state?.stateFilter || normalizedState(job.state) === normalizedState(state.stateFilter))
    && (!state?.templateFilter || job.template_id === state.templateFilter)
  ));
  const validID = value => typeof value === "string" && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
  const projection = (items, batches, filters) => {
    const known = new Map(items.map(job => [String(job.job_id), job]));
    const grouped = new Set();
    batches.forEach(batch => (batch.jobs || []).forEach(job => {
      grouped.add(String(job.job_id)); known.set(String(job.job_id), job);
    }));
    const jobs = filterJobs([...known.values()], filters).filter(job => !grouped.has(String(job.job_id)));
    const groups = batches.map(batch => ({ batch, jobs: filterJobs(batch.jobs || [], filters) }))
      .filter(group => group.jobs.length || (!filters?.stateFilter && !filters?.templateFilter));
    return { known, jobs, groups, total: known.size, shown: jobs.length + groups.reduce((count, group) => count + group.jobs.length, 0) };
  };
  const detailVersion = job => JSON.stringify([job?.state, job?.updated_at, job?.message]);
  const authMessage = "Сеанс завершён или доступ к заданиям изменился. Войдите в аккаунт снова.";
  const collapsedKey = "ai-gateway.generation-history-collapsed";

  /**
   * @typedef {"jobs"|"batches"|"variants"|"detail"|"batchDetail"|"cancel"|"cancelBatch"|"winner"|"branch"} HistoryOperation
   * @param {{csrf:()=>string, fetcher?:Function, timeoutSignal?:Function}} options
   * @returns {(operation:HistoryOperation, fields?:Object, signal?:AbortSignal)=>Promise<Object>}
   * Reads never launch work; mutation calls are explicit and are never retried here.
   */
  const createTransport = ({ csrf, fetcher = fetch, timeoutSignal = ms => AbortSignal.timeout(ms) }) => async (operation, fields = {}, signal) => {
    const paths = { jobs: "/generate/jobs", batches: "/generate/batches", variants: "/generate/variants",
      detail: "/generate/jobs/detail", batchDetail: "/api/tasks/detail", cancel: "/generate/jobs/cancel", cancelBatch: "/generate/batches/cancel",
      winner: "/generate/batches/winner", branch: "/generate/jobs/retry" };
    if (!paths[operation]) throw new Error("Unknown history operation");
    const read = ["jobs", "batches", "variants", "detail", "batchDetail"].includes(operation), body = new URLSearchParams(fields);
    const headers = { Accept: "application/json" };
    if (!read) { body.set("csrf", csrf() || ""); headers["Content-Type"] = "application/x-www-form-urlencoded;charset=UTF-8"; }
    const url = paths[operation] + (read && body.size ? "?" + body : "");
    const response = await fetcher(url, { method: read ? "GET" : "POST", body: read ? undefined : body,
      headers, credentials: "same-origin", cache: "no-store", redirect: "error",
      signal: AbortSignal.any([signal, timeoutSignal(20000)].filter(Boolean)) });
    return { ok: response.ok, status: response.status, redirected: response.redirected,
      payload: await response.json().catch(() => null) };
  };

  /**
   * History owns reads, refresh coalescing, explicit list actions and page lifecycle.
   * The submission controller still owns cancellation of its active launch.
   * @param {Object} options
   * @param {{get:Function, dispatch:Function}} options.job Existing named job slice.
   * @param {{get:Function, dispatch:Function}} options.batch Existing named batch slice.
   * @param {{get:Function, dispatch:Function}} options.history Existing named history slice.
   * @param {Object} options.batchAPI Existing selection/difference helpers.
   * @param {Function} options.transport createTransport-compatible boundary.
   * @param {Object} options.live Existing shared EventSource/poll lifecycle module.
   * @param {Function} options.storage Nonessential collapsed-view preference.
   * @returns {{snapshot:Function, refresh:Function, refreshVariants:Function, details:Function,
   * setFilters:Function, setCollapsed:Function, toggleCompare:Function, comparison:Function,
   * cancelJob:Function, cancelBatch:Function, selectWinner:Function, branch:Function, acceptBatch:Function,
   * repeat:Function, start:Function, suspend:Function, resume:Function, setVisible:Function}}
   */
  const createController = ({ job, batch, history, batchAPI, transport, live, storage, requestedJobID = "", requestedBatchID = "",
    onState = () => {}, onVariants = () => {}, onVariantsError = () => {}, onNotice = () => {},
    onRevoked = () => {}, onRepeat = () => {}, cancelActive = () => false, branchEditor,
    isPostprocess = value => String(value?.template_id || "").startsWith("postprocess-") }) => {
    let active = false, visible = true, started = false, authRequired = false, epoch = 0;
    let stream = null, jobsFlight = null, variantsFlight = null, jobsPending = false, variantsPending = false;
    let batchError = "", variantsError = "", requestedHandled = !requestedJobID && !requestedBatchID, loaded = false;
    const requests = new Set(), actions = new Set(), detailRecords = new Map(), detailFlights = new Map();
    const polls = [];
    const model = () => projection(job.get().items, batch.get().batches || [], history.get());
    const snapshot = () => ({ ...model(), filters: history.get(), batchState: batch.get(), jobState: job.get(),
      active, visible, loaded, authRequired, batchError, variantsError, actions: [...actions],
      details: Object.fromEntries(detailRecords), error: job.get().error || batchError });
    const publish = extra => { if (active) onState({ ...snapshot(), ...extra }); };
    const alive = stamp => active && !authRequired && stamp === epoch;
    const stopReads = () => {
      epoch++;
      requests.forEach(request => request.abort()); requests.clear();
      jobsFlight = variantsFlight = null; jobsPending = variantsPending = false;
      detailFlights.clear();
      for (const [id, value] of detailRecords) if (value.status === "loading") detailRecords.delete(id);
      stream?.close(); polls.forEach(poll => poll.stop());
    };
    const revoke = () => {
      if (authRequired) return;
      authRequired = true; stopReads(); detailRecords.clear();
      job.dispatch({ type: "SET_JOBS", items: [], revision: 0 });
      job.dispatch({ type: "LOAD_ERROR", error: authMessage });
      job.dispatch({ type: "SET_LIVE", live: false });
      batch.dispatch({ type: "SET_BATCHES", batches: [] });
      history.dispatch({ type: "SET_VARIANTS", variants: [] });
      onRevoked(); publish();
    };
    const read = async (operation, fields, stamp, controller) => {
      const response = await transport(operation, fields, controller.signal);
      if (!alive(stamp)) return null;
      if (response.redirected || response.status === 401 || response.status === 403) {
        revoke(); throw new Error(authMessage);
      }
      return response;
    };
    const request = () => { const controller = new AbortController(); requests.add(controller); return controller; };
    const array = (response, key, message) => {
      if (!response?.ok || !Array.isArray(response.payload?.[key])) throw new Error(response?.payload?.error || message);
      return response.payload[key];
    };
    const revealRequested = () => {
      if (requestedHandled) return {};
      requestedHandled = true;
      if (requestedBatchID) {
        if (!(batch.get().batches || []).some(value => value.batch_id === requestedBatchID)) {
          onNotice("Пакет больше не хранится", "Проверьте сохранённые файлы на странице «Результаты».", true);
          return {};
        }
        history.dispatch({ type: "SET_FILTERS", stateFilter: "", templateFilter: "" });
        setCollapsed(false, false);
        return { targetBatch: requestedBatchID };
      }
      const current = model(), target = current.known.get(requestedJobID);
      if (!target) {
        onNotice("Задача больше не хранится", "Проверьте сохранённые результаты на странице «Результаты».", true);
        return {};
      }
      if (!current.jobs.some(item => item.job_id === requestedJobID) && !current.groups.some(group => group.jobs.some(item => item.job_id === requestedJobID))) {
        history.dispatch({ type: "SET_FILTERS", stateFilter: "", templateFilter: "" });
      }
      setCollapsed(false, false);
      return { target: requestedJobID };
    };
    const refresh = () => {
      if (!active || authRequired) return Promise.resolve(false);
      if (jobsFlight) { jobsPending = true; return jobsFlight.promise; }
      const stamp = epoch, controller = request(), flight = { controller, promise: null };
      jobsFlight = flight; jobsPending = false;
      const current = () => alive(stamp) && jobsFlight === flight;
      job.dispatch({ type: "LOAD_START" }); publish();
      flight.promise = (async () => {
        try {
          const [response, batchResponse] = await Promise.all([
            read("jobs", {}, stamp, controller), read("batches", {}, stamp, controller).catch(failure => ({ failure })),
          ]);
          if (!current()) return false;
          const items = [...array(response, "jobs", "Не удалось загрузить задания. Повторите обновление.")];
          let batches = batch.get().batches || [];
          try { batches = array(batchResponse, "batches", "Пакеты пока не обновлены. Повторите обновление."); batchError = ""; }
          catch (failure) { batchError = failure.message; }
          if (validID(requestedBatchID) && !batches.some(value => value.batch_id === requestedBatchID)) {
            const detail = await read("batchDetail", { kind: "generation_batch", id: requestedBatchID }, stamp, controller);
            if (!current()) return false;
            if (detail?.ok && detail.payload?.batch?.batch_id === requestedBatchID && Array.isArray(detail.payload.batch.jobs)) {
              batches = [...batches, detail.payload.batch];
            } else if (detail?.status !== 404) throw new Error("Не удалось загрузить выбранный пакет. Повторите обновление.");
          }
          if (validID(requestedJobID) && !projection(items, batches, {}).known.has(requestedJobID)) {
            const detail = await read("detail", { job_id: requestedJobID }, stamp, controller);
            if (!current()) return false;
            if (detail?.ok && detail.payload?.job?.job_id === requestedJobID) items.push(detail.payload.job);
            else if (detail?.status !== 404) throw new Error("Не удалось загрузить выбранную задачу. Повторите обновление истории.");
          }
          if (!current()) return false;
          job.dispatch({ type: "SET_JOBS", items, revision: response.payload.revision });
          batch.dispatch({ type: "SET_BATCHES", batches });
          const known = model().known;
          for (const id of detailRecords.keys()) {
            if (!known.has(id)) detailRecords.delete(id);
            else if (detailRecords.get(id).version !== detailVersion(known.get(id))) detailRecords.delete(id);
          }
          loaded = true; publish(revealRequested()); return true;
        } catch (failure) {
          if (current()) {
            job.dispatch({ type: "LOAD_ERROR", error: failure.message || "Не удалось загрузить задания." });
            job.dispatch({ type: "SET_LIVE", live: false }); publish();
          }
          return false;
        } finally {
          requests.delete(controller);
          if (jobsFlight === flight) {
            jobsFlight = null;
            if (jobsPending && alive(stamp)) { jobsPending = false; void refresh(); }
          }
        }
      })();
      return flight.promise;
    };
    const refreshVariants = () => {
      if (!active || authRequired) return Promise.resolve(false);
      if (variantsFlight) { variantsPending = true; return variantsFlight.promise; }
      const stamp = epoch, controller = request(), flight = { promise: null };
      variantsFlight = flight; variantsPending = false;
      flight.promise = (async () => {
        try {
          const response = await read("variants", {}, stamp, controller);
          if (!alive(stamp)) return false;
          const variants = array(response, "variants", "Не удалось загрузить историю вариантов.");
          history.dispatch({ type: "SET_VARIANTS", variants }); variantsError = "";
          onVariants(variants); publish(); return true;
        } catch (failure) {
          if (alive(stamp)) { variantsError = failure.message; onVariantsError(failure); publish(); }
          return false;
        } finally {
          requests.delete(controller);
          if (variantsFlight === flight) {
            variantsFlight = null;
            if (variantsPending && alive(stamp)) { variantsPending = false; void refreshVariants(); }
          }
        }
      })();
      return flight.promise;
    };
    const details = (id, force = false) => {
      const current = model().known.get(id);
      if (!active || authRequired || !current || !validID(id)) return Promise.resolve(false);
      if (detailFlights.has(id)) return detailFlights.get(id);
      const version = detailVersion(current), record = detailRecords.get(id);
      if (!force && record?.status === "ready" && record.version === version) return Promise.resolve(true);
      const stamp = epoch, controller = request();
      detailRecords.set(id, { status: "loading", version, transitions: record?.transitions || [] }); publish();
      const pending = (async () => {
        try {
          const response = await read("detail", { job_id: id }, stamp, controller);
          if (!alive(stamp) || !model().known.has(id)) return false;
          if (response?.payload?.job?.job_id && response.payload.job.job_id !== id) throw new Error("Ответ относится к другой задаче.");
          const transitions = array(response, "transitions", "Не удалось загрузить этапы. Повторите запрос.");
          if (detailVersion(model().known.get(id)) !== version) return false;
          detailRecords.set(id, { status: "ready", version, transitions }); publish(); return true;
        } catch (failure) {
          if (alive(stamp) && model().known.has(id) && detailVersion(model().known.get(id)) === version) {
            detailRecords.set(id, { status: "error", version, transitions: [], error: failure.message }); publish();
          }
          return false;
        } finally {
          requests.delete(controller);
          if (detailFlights.get(id) === pending) detailFlights.delete(id);
          if (alive(stamp) && model().known.has(id) && detailVersion(model().known.get(id)) !== version) publish();
        }
      })();
      detailFlights.set(id, pending); return pending;
    };
    const mutate = async (operation, fields, { title, success, apply } = {}) => {
      const key = operation + ":" + (fields.batch_id || fields.job_id);
      if (!active || authRequired || actions.has(key)) return false;
      const stamp = epoch, controller = request(); actions.add(key); publish();
      try {
        const response = await read(operation, fields, stamp, controller);
        if (!alive(stamp)) return false;
        if (!response?.ok || !response.payload) throw new Error(response?.payload?.error || title);
        if (apply) apply(response.payload);
        if (success) onNotice(success[0], response.payload.message || success[1]);
        // A read begun before acknowledgement must not become the final list state.
        const old = jobsFlight;
        if (old) { jobsFlight = null; old.controller.abort(); }
        await refresh(); return true;
      } catch (failure) {
        if (alive(stamp)) onNotice(title, failure.message || "Gateway временно недоступен.", true);
        return false;
      } finally { requests.delete(controller); actions.delete(key); publish(); }
    };
    const findBatch = id => (batch.get().batches || []).find(value => value.batch_id === id);
    const cancelJob = id => {
      const current = model().known.get(id);
      if (!current?.cancellable || !active || authRequired) return Promise.resolve(false);
      if (cancelActive(id)) return Promise.resolve(true);
      return mutate("cancel", { job_id: id }, { title: "Не удалось отменить задание" });
    };
    const cancelBatch = id => !findBatch(id)?.cancellable ? Promise.resolve(false) : mutate("cancelBatch", { batch_id: id }, {
      title: "Не удалось отменить пакет", success: ["Отмена пакета принята", "Новые варианты не запустятся. Активный вариант будет остановлен безопасно."] });
    const selectWinner = (batchID, jobID) => {
      const parent = findBatch(batchID), selected = parent?.jobs?.find(value => value.job_id === jobID);
      if (!selected || selected.state !== "completed" || parent.winner_job_id === jobID) return Promise.resolve(false);
      return mutate("winner", { batch_id: batchID, job_id: jobID }, { title: "Не удалось выбрать вариант",
        success: ["Лучший вариант сохранён", "Его можно открыть, скачать или использовать как основу следующего пакета."] });
    };
    const branch = (batchID, jobID) => {
      const parent = findBatch(batchID), selected = parent?.jobs?.find(value => value.job_id === jobID);
      if (!selected || parent.winner_job_id !== jobID || isPostprocess(selected) || !branchEditor) return Promise.resolve(false);
      const captured = branchEditor.capture();
      return mutate("branch", { job_id: jobID }, { title: "Не удалось создать ветку",
        apply: payload => {
          if (payload.parent_job_id && payload.parent_job_id !== jobID) throw new Error("Ответ относится к другой задаче.");
          branchEditor.apply(payload, parent, selected, captured);
        } });
    };
    const setFilters = fields => { history.dispatch({ type: "SET_FILTERS", ...fields }); publish(); };
    const setCollapsed = (collapsed, notify = true) => {
      history.dispatch({ type: "SET_COLLAPSED", collapsed });
      try { storage().setItem(collapsedKey, collapsed ? "true" : "false"); } catch (_) {}
      if (notify) publish();
    };
    const toggleCompare = (batchID, jobID) => {
      if (findBatch(batchID)?.jobs?.find(value => value.job_id === jobID)?.state !== "completed") return;
      batch.dispatch({ type: "TOGGLE_COMPARE", batchID, jobID }); publish();
    };
    const comparison = id => {
      const parent = findBatch(id), jobs = batchAPI.selectedJobs(batch.get(), parent);
      return parent && jobs.length === 2 ? { batch: parent, jobs, differences: batchAPI.selectedDifferences(batch.get(), parent) } : null;
    };
    const repeat = (id, trigger) => {
      const current = model().known.get(id);
      if (active && !authRequired && current?.retryable && !isPostprocess(current)) onRepeat(id, trigger);
    };
    const acceptBatch = value => {
      if (!active || authRequired || !validID(value?.batch_id)) return;
      if (jobsFlight) { const old = jobsFlight; jobsFlight = null; old.controller.abort(); }
      batch.dispatch({ type: "SET_BATCHES", batches: [value, ...(batch.get().batches || []).filter(item => item.batch_id !== value.batch_id)] });
      setCollapsed(false, false); publish({ targetBatch: value.batch_id });
    };
    const connect = () => {
      if (!active || !visible || authRequired) return;
      stream ||= live.create({ url: () => `/generate/jobs/events?since=${encodeURIComponent(job.get().revision)}`,
        events: { ready: refresh, jobs: refresh }, onError: refresh,
        onState: state => { job.dispatch({ type: "SET_LIVE", live: state === "live" }); publish(); } });
      stream.connect();
    };
    const resume = () => {
      if (active || authRequired) return;
      active = true; epoch++;
      void refresh().then(connect); void refreshVariants(); polls.forEach(poll => poll.start()); publish();
    };
    const start = (isVisible = true) => {
      visible = Boolean(isVisible);
      if (!started) {
        started = true;
        try { history.dispatch({ type: "SET_COLLAPSED", collapsed: storage().getItem(collapsedKey) === "true" }); } catch (_) {}
        if (requestedJobID && !validID(requestedJobID) || requestedBatchID && !validID(requestedBatchID)) {
          requestedHandled = true; onNotice("Не удалось открыть задачу", "Некорректный адрес задачи. Откройте её из списка.", true);
        }
        polls.push(live.poll(refresh, 30000, 120000), live.poll(refreshVariants, 30000, 120000));
      }
      resume();
    };
    const suspend = () => { active = false; stopReads(); job.dispatch({ type: "SET_LIVE", live: false }); };
    const setVisible = value => {
      visible = Boolean(value); if (!active || authRequired) return;
      polls.forEach(poll => { poll.stop(); poll.start(); });
      if (!visible) stream?.close(); else void refresh().then(connect);
    };
    return { snapshot, refresh, refreshVariants, details, setFilters, setCollapsed, toggleCompare, comparison,
      cancelJob, cancelBatch, selectWinner, branch, acceptBatch, repeat, start, suspend, resume, setVisible };
  };

  return { createState, reduce, filterJobs, projection, validID, createTransport, createController };
});
