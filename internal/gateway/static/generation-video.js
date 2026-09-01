(function bootstrapGenerationVideo(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (!root) return;
  root.AIGatewayGeneration = root.AIGatewayGeneration || {};
  root.AIGatewayGeneration.video = api;
})(typeof window !== "undefined" ? window : null, function generationVideoFactory() {
  const MODE_FRAMES = "frames";
  const MODE_REFERENCES = "references";
  const aspectPresets = {
    "1:1": [1080, 1080],
    "4:5": [1080, 1350],
    "16:9": [1344, 768],
    "9:16": [1080, 1920],
    "4:1": [1600, 400],
    "2:3": [832, 1248],
    "3:2": [1248, 832],
    "3:4": [896, 1152],
    "4:3": [1152, 896],
    "21:9": [1536, 640],
  };

  const normalizeMode = (mode) => mode === MODE_REFERENCES ? MODE_REFERENCES : MODE_FRAMES;
  const createState = (overrides = {}) => ({ mode: MODE_FRAMES, profileID: "regular", ...overrides });
  const reduce = (state = createState(), action = {}) => {
    switch (action.type) {
      case "SET_MODE":
        return { ...state, mode: normalizeMode(action.mode) };
      case "SET_PROFILE":
        return { ...state, profileID: String(action.profileID || "regular") };
      default:
        return state;
    }
  };

  const activeImageLimit = ({ isMiniMax = false, mode = MODE_FRAMES, maxInputImages = 1 } = {}) => {
    const maximum = Math.max(1, Number(maxInputImages) || 1);
    return isMiniMax && normalizeMode(mode) === MODE_FRAMES ? Math.min(2, maximum) : maximum;
  };

  const referencesAvailable = ({ isMiniMax = false, mode = MODE_FRAMES } = {}) => Boolean(
    isMiniMax && normalizeMode(mode) === MODE_REFERENCES,
  );

  const profileID = ({ integratedTurbo = false, turbo = false } = {}) => (
    integratedTurbo ? "integrated_turbo" : turbo ? "turbo" : "regular"
  );

  const findProfile = (manifest, id) => manifest?.quality_profiles?.find((profile) => profile.id === id) || null;

  const dimensionsForAspect = ({ sourceSize = null, aspect = "9:16", swap = false } = {}) => {
    const dimensions = sourceSize?.width && sourceSize?.height
      ? [Number(sourceSize.width), Number(sourceSize.height)]
      : [...(aspectPresets[aspect] || aspectPresets["9:16"])];
    return swap ? [dimensions[1], dimensions[0]] : dimensions;
  };

  const scaledResolution = ({ sourceSize = null, aspect = "9:16", swap = false, maxResolution = 480, multiple = 32 } = {}) => {
    const [sourceWidth, sourceHeight] = dimensionsForAspect({ sourceSize, aspect, swap });
    const quality = Math.max(multiple, Number(maxResolution) || 480);
    const scale = Math.min(1, quality / Math.max(1, sourceWidth, sourceHeight));
    const round = (value) => Math.max(multiple, Math.floor((value + 1e-6) / multiple) * multiple);
    return { width: round(sourceWidth * scale), height: round(sourceHeight * scale), sourceWidth, sourceHeight };
  };

  return {
    MODE_FRAMES,
    MODE_REFERENCES,
    aspectPresets,
    createState,
    reduce,
    normalizeMode,
    activeImageLimit,
    referencesAvailable,
    profileID,
    findProfile,
    dimensionsForAspect,
    scaledResolution,
  };
});
