(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.AIGatewayLiveEvents = api;
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  function create(options, environment = globalThis) {
    let source = null;
    let timer = null;
    let active = false;
    let failures = 0;
    const clear = () => {
      if (timer !== null) environment.clearTimeout(timer);
      timer = null;
      const previous = source;
      source = null;
      previous?.close();
    };
    const open = () => {
      timer = null;
      if (!active || source) return;
      if (typeof environment.EventSource !== "function") {
        options.onState?.("offline");
        return;
      }
      const retry = () => {
        if (!active) return;
        clear();
        failures = Math.min(failures + 1, 5);
        const base = Math.min(30000, 2000 * (2 ** (failures - 1)));
        const delay = Math.min(30000, Math.round(base * (0.8 + environment.Math.random() * 0.4)));
        options.onState?.("offline");
        timer = environment.setTimeout(open, delay);
        options.onError?.();
      };
      let stream;
      try { stream = new environment.EventSource(options.url(), { withCredentials: true }); }
      catch (_) { retry(); return; }
      source = stream;
      options.onState?.("connecting");
      Object.entries(options.events || {}).forEach(([name, receive]) => {
        stream.addEventListener(name, event => {
          if (!active || source !== stream) return;
          failures = 0;
          options.onState?.("live");
          receive(event);
        });
      });
      stream.addEventListener("error", () => { if (source === stream) retry(); });
    };
    return {
      connect() { active = true; if (!source && timer === null) open(); },
      close() { active = false; clear(); },
    };
  }

  // Timer-driven reads are optional; operation recovery remains owned by pages.
  function poll(run, interval, hiddenInterval, environment = globalThis) {
    let timer = null;
    let active = false;
    let running = false;
    const schedule = () => {
      if (!active || timer !== null || running) return;
      timer = environment.setTimeout(tick, environment.document?.hidden ? hiddenInterval : interval);
    };
    const tick = async () => {
      timer = null;
      if (!active) return;
      running = true;
      try { await run(); } catch (_) {} finally { running = false; schedule(); }
    };
    return {
      start() { active = true; schedule(); },
      stop() { active = false; if (timer !== null) environment.clearTimeout(timer); timer = null; },
    };
  }

  return { create, poll };
});
