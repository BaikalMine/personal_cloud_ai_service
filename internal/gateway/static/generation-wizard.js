(function bootstrapGenerationWizard(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.wizard = api;
})(typeof window !== "undefined" ? window : null, function generationWizardFactory() {
  const createState = (overrides = {}) => ({
    step: 1,
    scenarioID: "",
    workflowID: "",
    requiresImage: false,
    allowsImages: false,
    workflowAvailable: false,
    uploadInFlight: false,
    selectedCount: 0,
    primarySelected: false,
    pendingUploads: 0,
    ...overrides,
  });

  const reduce = (state = createState(), action = {}) => {
    switch (action.type) {
      case "SHOW_STEP":
        return { ...state, step: Math.max(1, Math.min(3, Number(action.step) || 1)) };
      case "SELECT_SCENARIO":
        return {
          ...state,
          step: 2,
          scenarioID: String(action.scenarioID || ""),
          workflowID: "",
          requiresImage: Boolean(action.requiresImage),
          allowsImages: Boolean(action.allowsImages),
          workflowAvailable: false,
        };
      case "SELECT_WORKFLOW":
        return {
          ...state,
          workflowID: String(action.workflowID || ""),
          workflowAvailable: Boolean(action.available),
        };
      case "SET_SELECTIONS":
        return {
          ...state,
          selectedCount: Math.max(0, Number(action.selectedCount) || 0),
          primarySelected: Boolean(action.primarySelected),
          pendingUploads: Math.max(0, Number(action.pendingUploads) || 0),
        };
      case "UPLOAD_START":
        return { ...state, uploadInFlight: true };
      case "UPLOAD_FINISH":
        return { ...state, uploadInFlight: false };
      default:
        return state;
    }
  };

  const canContinue = (state) => Boolean(
    state?.workflowAvailable
    && !state?.uploadInFlight
    && (!state?.requiresImage || state?.primarySelected),
  );

  const nextActionLabel = (state) => {
    if ((Number(state?.pendingUploads) || 0) > 0) return "upload";
    return state?.requiresImage ? "prompt" : "continue";
  };

  return { createState, reduce, canContinue, nextActionLabel };
});
