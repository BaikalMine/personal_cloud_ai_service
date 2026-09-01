(function bootstrapGenerationJob(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.job = api;
})(typeof window !== "undefined" ? window : null, function generationJobFactory() {
  const createState = (overrides = {}) => ({
    items: [],
    revision: 0,
    live: false,
    loading: false,
    activeID: "",
    error: "",
    ...overrides,
  });

  const reduce = (state = createState(), action = {}) => {
    switch (action.type) {
      case "LOAD_START":
        return { ...state, loading: true, error: "" };
      case "SET_JOBS":
        return {
          ...state,
          loading: false,
          items: Array.isArray(action.items) ? action.items : [],
          revision: Math.max(Number(state.revision) || 0, Number(action.revision) || 0),
          error: "",
        };
      case "SET_REVISION":
        return { ...state, revision: Math.max(Number(state.revision) || 0, Number(action.revision) || 0) };
      case "SET_LIVE":
        return { ...state, live: Boolean(action.live) };
      case "LOAD_ERROR":
        return { ...state, loading: false, error: String(action.error || "Load failed") };
      case "SET_ACTIVE":
        return { ...state, activeID: String(action.activeID || "") };
      default:
        return state;
    }
  };

  return { createState, reduce };
});
