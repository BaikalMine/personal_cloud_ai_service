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
    references: [],
    usage: {},
    draftEdited: false,
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
          references: Array.isArray(action.references) ? action.references : [],
          usage: action.usage && typeof action.usage === "object" ? action.usage : {},
          draftEdited: false,
          error: "",
        };
      case "REQUEST_ERROR":
        return { ...state, status: "error", error: String(action.error || "Request failed") };
		case "DECISION_ERROR":
		  return { ...state, status: "error", approved: false, error: String(action.error || "Decision was not saved") };
      case "APPLY":
		return {
		  ...state,
		  status: "approved",
		  approved: true,
		  action: action.edited || state.draftEdited ? "edited_after_apply" : "applied",
		};
      case "KEEP_ORIGINAL":
        return { ...state, status: "approved", approved: true, action: "kept_original" };
		case "DRAFT_EDITED":
		  return { ...state, draftEdited: true };
      case "PROMPT_EDITED":
		return {
		  ...state,
		  approved: state.action === "applied" || state.action === "edited_after_apply",
		  action: state.action === "applied" || state.action === "edited_after_apply" ? "edited_after_apply" : state.action,
		};
      default:
        return state;
    }
  };

  const tokenize = (value) => String(value || "").match(/\s+|[^\s]+/gu) || [];

  const pushSegment = (segments, type, value) => {
	if (!value) return;
	const last = segments[segments.length - 1];
	if (last?.type === type) last.value += value;
	else segments.push({ type, value });
  };

  const fallbackDiff = (before, after) => ({
	original: before ? [{ type: "removed", value: before }] : [],
	suggestion: after ? [{ type: "added", value: after }] : [],
  });

  const diff = (before, after) => {
	const original = tokenize(before);
	const suggestion = tokenize(after);
	if (original.length * suggestion.length > 1_000_000) return fallbackDiff(String(before || ""), String(after || ""));
	const width = suggestion.length + 1;
	const table = new Uint16Array((original.length + 1) * width);
	for (let i = original.length - 1; i >= 0; i -= 1) {
	  for (let j = suggestion.length - 1; j >= 0; j -= 1) {
		table[i * width + j] = original[i] === suggestion[j]
		  ? table[(i + 1) * width + j + 1] + 1
		  : Math.max(table[(i + 1) * width + j], table[i * width + j + 1]);
	  }
	}
	const result = { original: [], suggestion: [] };
	let i = 0;
	let j = 0;
	while (i < original.length || j < suggestion.length) {
	  if (i < original.length && j < suggestion.length && original[i] === suggestion[j]) {
		pushSegment(result.original, "same", original[i]);
		pushSegment(result.suggestion, "same", suggestion[j]);
		i += 1;
		j += 1;
	  } else if (j < suggestion.length && (i === original.length || table[i * width + j + 1] >= table[(i + 1) * width + j])) {
		pushSegment(result.suggestion, "added", suggestion[j]);
		j += 1;
	  } else {
		pushSegment(result.original, "removed", original[i]);
		i += 1;
	  }
	}
	return result;
  };

  return { createState, reduce, diff };
});
