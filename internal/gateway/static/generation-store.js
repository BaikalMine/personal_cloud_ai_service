(function bootstrapGenerationStore(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.store = api;
})(typeof window !== "undefined" ? window : null, function generationStoreFactory() {
  const createStore = (initialState = {}) => {
    let state = { ...initialState };
    const listeners = new Map();

    const emit = (event, payload) => {
      const subscribers = [...(listeners.get(event) || [])];
      subscribers.forEach((listener) => listener(payload, state));
    };

    const subscribe = (event, listener) => {
      if (typeof listener !== "function") return () => {};
      const subscribers = listeners.get(event) || new Set();
      subscribers.add(listener);
      listeners.set(event, subscribers);
      return () => {
        subscribers.delete(listener);
        if (!subscribers.size) listeners.delete(event);
      };
    };

    const setSlice = (name, value, event = `${name}:change`) => {
      const previous = state[name];
      const current = typeof value === "function" ? value(previous) : value;
      if (Object.is(previous, current)) return current;
      state = { ...state, [name]: current };
      const change = { name, previous, current };
      emit(event, change);
      if (event !== "change") emit("change", change);
      return current;
    };

    return {
      getState: () => state,
      getSlice: (name) => state[name],
      setSlice,
      emit,
      subscribe,
      destroy: () => listeners.clear(),
    };
  };

  return { createStore };
});
