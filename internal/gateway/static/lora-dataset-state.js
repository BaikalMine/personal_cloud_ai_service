(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.AIGatewayLoraDataset = api;
})(typeof window !== "undefined" ? window : null, function () {
  const copy = (value) => JSON.parse(JSON.stringify(value));
  const createID = (random = globalThis.crypto) => {
    if (typeof random.randomUUID === "function") return random.randomUUID();
    // HTTP/IP pages still expose getRandomValues, but not randomUUID.
    return Array.from(random.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join("");
  };
  const createController = ({ request, defaults, onChange = () => {}, schedule = setTimeout, unschedule = clearTimeout, newID = createID }) => {
    const state = { dataset: null, manifest: copy(defaults), assets: {}, datasets: [], status: "loading", error: "", dirty: false, ready: false };
    let epoch = 0;
    let timer = null;
    let saving = null;
    let clientID = newID();
    const publish = (kind = "status") => onChange(state, kind);
    const stopTimer = () => { if (timer !== null) unschedule(timer); timer = null; };
    const setError = (error) => { state.status = error.status === 409 ? "conflict" : "error"; state.error = error.message; publish(); };
    const apply = (view) => {
      state.dataset = view.dataset;
      state.manifest = copy(view.manifest);
      state.manifest.images ||= [];
      state.assets = view.assets || {};
      state.dirty = false;
      state.error = "";
      state.status = "saved";
      epoch += 1;
      state.ready = true;
      publish("load");
    };
    const refreshList = async () => {
      const result = await request("");
      state.datasets = result.datasets || [];
      publish("list");
      return state.datasets;
    };
    const load = async (id) => {
      stopTimer();
      state.ready = false; state.status = "loading"; publish();
      try {
        if (!id) id = (await refreshList())[0]?.id;
        if (id) apply(await request(`/${encodeURIComponent(id)}`));
        else { state.ready = true; state.status = "empty"; publish("load"); }
        return true;
      } catch (error) { setError(error); return false; }
    };
    const touch = (kind = "status") => {
      epoch += 1; state.dirty = true;
      if (state.status !== "conflict") { state.status = "dirty"; state.error = ""; }
      publish(kind); stopTimer();
      if (state.ready && state.status !== "conflict") timer = schedule(() => { timer = null; void flush(); }, 900);
    };
    const flush = async () => {
      stopTimer();
      if (saving) { await saving; return !state.dirty && state.status !== "conflict" && state.status !== "error"; }
      if (!state.ready || state.status === "conflict") return false;
      if (!state.dirty) return true;
      saving = (async () => {
        try {
          state.status = "saving"; publish();
          if (!state.dataset) {
            const created = await request("", "POST", { client_id: clientID, manifest: { ...copy(state.manifest), images: [] } });
            state.dataset = created.dataset;
            state.datasets.unshift(created.dataset);
            publish("list");
          }
          while (state.dirty) {
            const capturedEpoch = epoch;
            const manifest = copy(state.manifest);
            manifest.images = manifest.images.filter((image) => image.asset_id);
            const result = await request(`/${state.dataset.id}/save`, "POST", { revision: state.dataset.revision, manifest });
            state.dataset = result.dataset;
            Object.assign(state.assets, result.assets);
            state.datasets = [result.dataset, ...state.datasets.filter((item) => item.id !== result.dataset.id)];
            state.dirty = epoch !== capturedEpoch;
          }
          state.status = "saved"; state.error = ""; publish("list");
          return true;
        } catch (error) { setError(error); return false; }
        finally { saving = null; }
      })();
      return saving;
    };
    const startNew = ({ preserve = false } = {}) => {
      stopTimer(); state.dataset = null; clientID = newID();
      if (!preserve) { state.manifest = copy(defaults); state.assets = {}; }
      state.status = "empty"; state.error = ""; state.dirty = false; state.ready = true;
      publish(preserve ? "list" : "load");
      if (preserve) touch();
    };
    const ensure = async () => { if (!state.dataset) touch(); return await flush() ? state.dataset?.id : null; };
    return { state, load, apply, touch, flush, ensure, startNew, refreshList, setError, dispose: stopTimer };
  };
  return { createController, createID };
});
