(function bootstrapGenerationAssistant(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.assistant = api;
})(typeof window !== "undefined" ? window : null, function generationAssistantFactory() {
  const createState = (overrides = {}) => ({
    status: "idle",
    approved: false,
    original: "",
    suggestion: "",
    action: "",
    correlationID: "",
    error: "",
    ...overrides,
  });

  const reduce = (state = createState(), action = {}) => {
    switch (action.type) {
      case "RESET":
        return createState();
      case "REQUEST_START":
        return { ...createState(), status: "loading", original: String(action.original || "") };
      case "REQUEST_SUCCESS":
        return {
          ...state,
          status: "review",
          suggestion: String(action.suggestion || ""),
          correlationID: String(action.correlationID || ""),
          error: "",
        };
      case "REQUEST_ERROR":
        return { ...state, status: "error", error: String(action.error || "Request failed") };
      case "APPLY":
        return { ...state, status: "approved", approved: true, action: "applied", suggestion: String(action.suggestion ?? state.suggestion) };
      case "KEEP_ORIGINAL":
        return { ...state, status: "approved", approved: true, action: "kept_original" };
      case "PROMPT_EDITED":
        return { ...state, approved: false, action: state.action === "applied" ? "applied_edited" : state.action };
      default:
        return state;
    }
  };

  return { createState, reduce };
});
