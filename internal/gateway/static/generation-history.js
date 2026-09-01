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
  const filterJobs = (items, state) => (Array.isArray(items) ? items : []).filter((job) => (
    (!state?.stateFilter || job.state === state.stateFilter)
    && (!state?.templateFilter || job.template_id === state.templateFilter)
  ));
  return { createState, reduce, filterJobs };
});
