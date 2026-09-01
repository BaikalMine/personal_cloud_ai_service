(function bootstrapGenerationRecipes(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.recipes = api;
})(typeof window !== "undefined" ? window : null, function generationRecipesFactory() {
  const createState = (overrides = {}) => ({ items: [], selectedID: "", loading: false, message: "", status: "", ...overrides });
  const reduce = (state = createState(), action = {}) => {
    switch (action.type) {
      case "LOAD_START":
        return { ...state, loading: true };
      case "SET_ITEMS": {
        const items = Array.isArray(action.items) ? action.items : [];
        const selectedID = items.some((item) => String(item.id) === String(state.selectedID)) ? state.selectedID : "";
        return { ...state, items, selectedID, loading: false };
      }
      case "SELECT":
        return { ...state, selectedID: String(action.id || "") };
      case "SET_MESSAGE":
        return { ...state, message: String(action.message || ""), status: String(action.status || "") };
      default:
        return state;
    }
  };
  const selectedRecipe = (state) => state?.items?.find((item) => String(item.id) === String(state.selectedID)) || null;
  return { createState, reduce, selectedRecipe };
});
