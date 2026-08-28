package gateway

import (
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
)

//go:embed workflows/*.json
var workflowFS embed.FS

const (
	maxGenerationLoraSlots        = 10
	krea2EditMaxBaseMegapixels    = 4.7
	krea2EditMaxLongestSidePixels = 4096
)

type workflowDefinition struct {
	ID             string                    `json:"id"`
	Name           string                    `json:"name"`
	Description    string                    `json:"description"`
	RequiresImage  bool                      `json:"requires_image"`
	AllowsImages   bool                      `json:"allows_images"`
	MaxInputImages int                       `json:"max_input_images"`
	AdminOnly      bool                      `json:"admin_only"`
	Builder        string                    `json:"builder"`
	Nodes          map[string]map[string]any `json:"nodes"`
}

type workflowView struct {
	ID            string
	Name          string
	Description   string
	RequiresImage bool
	AllowsImages  bool
	Restricted    bool
	Restriction   string
}

type generationForm struct {
	TemplateID           string
	PresetID             string
	ModelID              string
	ModelName            string
	ModelFamily          string
	TextEncoder          string
	VAE                  string
	AudioVAE             string
	ReferenceModel       string
	Lora                 string
	LoraStrength         float64
	IdentityLora         string
	LoraNames            [maxGenerationLoraSlots]string
	LoraModel            [maxGenerationLoraSlots]float64
	LoraClip             [maxGenerationLoraSlots]float64
	LorasConfigured      bool
	AspectRatio          string
	OutputMegapixels     float64
	DimensionMultiple    int
	MaxLongestSide       int
	BaseMegapixels       float64
	UpscaleSteps         int
	UpscaleDenoise       float64
	UpscaleAutoDenoise   bool
	UpscaleSampler       string
	DetailSteps          int
	DetailDenoise        float64
	DetailCFG            float64
	DetailSampler        string
	DetailScheduler      string
	ColorTransfer        bool
	ColorMethod          string
	ColorMode            string
	ColorStrength        float64
	SourceMegapixels     float64
	PreserveOriginalSize bool
	FluxGuidance         float64
	FluxDetailerSteps    int
	FluxActiveScale      float64
	FluxTokenWhiten      float64
	FluxNormEqualize     float64
	FluxUpscaleMode      string
	EditUseCustomSize    bool
	EditAspectPreset     string
	EditSwapDimensions   bool
	EditResizeMethod     string
	EditProportion       string
	EditCropLocation     string
	EditPadColor         string
	ReferenceBoost       float64
	GroundingPixels      int
	UpscaleFactor        float64
	UpscaleCFG           float64
	UpscaleScheduler     string
	PostDenoiseBlur      float64
	PostDenoiseEdge      float64
	PostDenoiseRadius    float64
	PostDenoiseStrength  float64
	SkinPreset           string
	SkinStrength         float64
	SkinCoolness         float64
	SkinBrightness       float64
	SkinRosy             float64
	SkinEvenness         float64
	SkinShadowLift       float64
	SkinSmooth           float64
	SkinTexturePreserve  float64
	SkinSaturation       float64
	SkinHighlightProtect float64
	SkinMaskSensitivity  float64
	SkinMaskFeather      float64
	AdjustHue            float64
	AdjustSaturation     float64
	AdjustBrightness     float64
	AdjustContrast       float64
	AdjustSharpness      float64
	LUTName              string
	LUTStrength          float64
	LUTEnabled           bool
	InputImage           string
	ReferenceImages      [3]string
	Positive             string
	Negative             string
	Width                int
	Height               int
	Steps                int
	CFG                  float64
	Denoise              float64
	Sampler              string
	Scheduler            string
	Seed                 int64
	VideoMode            string
	VideoResolution      string
	VideoAspect          string
	VideoQuality         int
	VideoDurationSeconds int
	VideoReferenceSize   string
	VideoSteps           int
	AssistantRequested   bool
	AssistantApplied     bool
	AssistantTemplate    string
	AssistantThink       bool
	AssistantOriginal    string
	AssistantSuggestion  string
}

func (input generationForm) imageCount() int {
	count := 0
	if strings.TrimSpace(input.InputImage) != "" {
		count++
	}
	for _, image := range input.ReferenceImages {
		if strings.TrimSpace(image) != "" {
			count++
		}
	}
	return count
}

func (input generationForm) images() []string {
	images := make([]string, 0, 4)
	if image := strings.TrimSpace(input.InputImage); image != "" {
		images = append(images, image)
	}
	for _, image := range input.ReferenceImages {
		if image = strings.TrimSpace(image); image != "" {
			images = append(images, image)
		}
	}
	return images
}

func loadWorkflowDefinitions() ([]workflowDefinition, error) {
	paths, err := fs.Glob(workflowFS, "workflows/*.json")
	if err != nil {
		return nil, err
	}
	definitions := make([]workflowDefinition, 0, len(paths))
	for _, path := range paths {
		body, err := workflowFS.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var definition workflowDefinition
		if err := json.Unmarshal(body, &definition); err != nil {
			return nil, fmt.Errorf("workflow %s: %w", path, err)
		}
		if definition.ID == "" || definition.Name == "" || (len(definition.Nodes) == 0 && definition.Builder == "") {
			return nil, fmt.Errorf("workflow %s is incomplete", path)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func findWorkflow(definitions []workflowDefinition, id string) (workflowDefinition, bool) {
	for _, definition := range definitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return workflowDefinition{}, false
}

func (definition workflowDefinition) buildPrompt(input generationForm) (map[string]any, error) {
	if err := definition.normalizeAndValidate(&input); err != nil {
		return nil, err
	}
	if definition.Builder == "minimax_h3" {
		return buildMiniMaxH3Prompt(input)
	}
	cloned, err := cloneWorkflowNodes(definition.Nodes)
	if err != nil {
		return nil, err
	}
	values := definition.workflowValues(input)
	for nodeID, node := range cloned {
		cloned[nodeID] = replaceWorkflowValues(node, values).(map[string]any)
	}
	pruneOptionalImageNodes(definition.ID, cloned, input)
	applyWorkflowLoras(definition.ID, cloned, input)
	applyWorkflowUpscale(definition.ID, cloned, input)
	prompt := make(map[string]any, len(cloned))
	for nodeID, node := range cloned {
		prompt[nodeID] = node
	}
	return prompt, nil
}

func applyWorkflowLoras(workflowID string, nodes map[string]map[string]any, input generationForm) {
	switch workflowID {
	case "image-to-image-flux2":
		applyFlux2Loras(nodes, input)
	case "text-to-image-krea2":
		appendKrea2TextLoras(nodes, input)
	}
}

// applyWorkflowUpscale adds only the post-processing branches selected in the
// quick-generation form. The base Flux2 workflow remains the compact, stable
// edit path; these nodes reproduce the two optional upscale passes from the
// user's original Flux2 Edit workflow.
func applyWorkflowUpscale(workflowID string, nodes map[string]map[string]any, input generationForm) {
	if workflowID != "image-to-image-flux2" {
		return
	}
	save, ok := nodes["1341"]
	if !ok {
		return
	}
	saveInputs, ok := save["inputs"].(map[string]any)
	if !ok {
		return
	}

	image := []any{"1362", 0}
	if input.FluxUpscaleMode == "ultimate" || input.FluxUpscaleMode == "both" {
		nodes["gateway_flux_ultimate_model"] = map[string]any{
			"class_type": "UpscaleModelLoader",
			"inputs": map[string]any{
				"model_name": "4x_NickelbackFS_72000_G.pth",
			},
		}
		nodes["gateway_flux_ultimate"] = map[string]any{
			"class_type": "UltimateSDUpscale",
			"inputs": map[string]any{
				"image":               image,
				"model":               []any{"1367", 1},
				"positive":            []any{"1367", 14},
				"negative":            []any{"1367", 16},
				"vae":                 []any{"1367", 3},
				"upscale_model":       []any{"gateway_flux_ultimate_model", 0},
				"upscale_by":          1.5,
				"seed":                []any{"1367", 17},
				"steps":               4,
				"cfg":                 []any{"1367", 19},
				"sampler_name":        []any{"1367", 23},
				"scheduler":           []any{"1367", 24},
				"denoise":             0.15,
				"mode_type":           "None",
				"tile_width":          512,
				"tile_height":         512,
				"mask_blur":           0,
				"tile_padding":        32,
				"seam_fix_mode":       "None",
				"seam_fix_denoise":    1.0,
				"seam_fix_width":      64,
				"seam_fix_mask_blur":  8,
				"seam_fix_padding":    16,
				"force_uniform_tiles": true,
				"tiled_decode":        false,
				"batch_size":          1,
			},
		}
		image = []any{"gateway_flux_ultimate", 0}
	}

	if input.FluxUpscaleMode == "seedvr2" || input.FluxUpscaleMode == "both" {
		nodes["gateway_flux_seedvr2_size"] = map[string]any{
			"class_type": "LCGetImage",
			"inputs": map[string]any{
				"image": image,
			},
		}
		nodes["gateway_flux_seedvr2_resolution"] = map[string]any{
			"class_type": "ComfyMathExpression",
			"inputs": map[string]any{
				"values.a":   []any{"gateway_flux_seedvr2_size", 3},
				"values.b":   2,
				"expression": "a*b",
			},
		}
		nodes["gateway_flux_seedvr2_vae"] = map[string]any{
			"class_type": "SeedVR2LoadVAEModel",
			"inputs": map[string]any{
				"model":          "ema_vae_fp16.safetensors",
				"device":         "cuda:0",
				"offload_device": "cpu",
			},
		}
		nodes["gateway_flux_seedvr2_dit"] = map[string]any{
			"class_type": "SeedVR2LoadDiTModel",
			"inputs": map[string]any{
				"model":          "seedvr2_ema_7b_fp16.safetensors",
				"device":         "cuda:0",
				"blocks_to_swap": 8,
				"offload_device": "cpu",
				"attention_mode": "sageattn_2",
			},
		}
		nodes["gateway_flux_seedvr2"] = map[string]any{
			"class_type": "SeedVR2VideoUpscaler",
			"inputs": map[string]any{
				"image":              image,
				"dit":                []any{"gateway_flux_seedvr2_dit", 0},
				"vae":                []any{"gateway_flux_seedvr2_vae", 0},
				"seed":               input.Seed,
				"resolution":         []any{"gateway_flux_seedvr2_resolution", 1},
				"max_resolution":     4098,
				"batch_size":         1,
				"uniform_batch_size": false,
				"color_correction":   "lab",
				"temporal_overlap":   0,
				"prepend_frames":     8,
				"input_noise_scale":  0,
				"latent_noise_scale": 0,
				"offload_device":     "cpu",
				"enable_debug":       true,
			},
		}
		image = []any{"gateway_flux_seedvr2", 0}
	}
	saveInputs["images"] = image
}

func applyFlux2Loras(nodes map[string]map[string]any, input generationForm) {
	node, ok := nodes["539"]
	if !ok {
		return
	}
	inputs, ok := node["inputs"].(map[string]any)
	if !ok {
		return
	}
	for index, name := range input.LoraNames {
		key := "lora_" + strconv.Itoa(index+1)
		delete(inputs, key)
		if strings.TrimSpace(name) == "" {
			continue
		}
		inputs[key] = map[string]any{
			"on":       true,
			"lora":     name,
			"strength": input.LoraModel[index],
		}
	}
}

func appendKrea2TextLoras(nodes map[string]map[string]any, input generationForm) {
	// The saved Krea2 workflow contains the first four loaders. Add a chain only
	// when the user uses slots 5-10, then bind its last outputs to the workflow.
	previousID := "19"
	for index := 4; index < len(input.LoraNames); index++ {
		name := strings.TrimSpace(input.LoraNames[index])
		if name == "" {
			continue
		}
		nodeID := "gateway_lora_" + strconv.Itoa(index+1)
		nodes[nodeID] = map[string]any{
			"class_type": "LoraLoader",
			"inputs": map[string]any{
				"lora_name":      name,
				"strength_model": input.LoraModel[index],
				"strength_clip":  input.LoraClip[index],
				"model":          []any{previousID, 0},
				"clip":           []any{previousID, 1},
			},
		}
		previousID = nodeID
	}
	if previousID == "19" {
		return
	}
	if modelPatch, ok := nodes["5"]["inputs"].(map[string]any); ok {
		modelPatch["model"] = []any{previousID, 0}
	}
	if textEncode, ok := nodes["6"]["inputs"].(map[string]any); ok {
		textEncode["clip"] = []any{previousID, 1}
	}
}

// normalizeAndValidate is the runtime contract for one concrete workflow.
// Shared request fields are normalized once; family-specific options are only
// normalized and validated by the workflow that actually consumes them.
func (definition workflowDefinition) normalizeAndValidate(input *generationForm) error {
	if input.OutputMegapixels == 0 {
		input.OutputMegapixels = float64(input.Width*input.Height) / (1024 * 1024)
	}
	if input.DimensionMultiple == 0 {
		input.DimensionMultiple = 16
	}
	if input.AspectRatio != "" && input.AspectRatio != "custom" {
		var err error
		input.Width, input.Height, err = generationDimensions(input.AspectRatio, input.OutputMegapixels, input.DimensionMultiple, input.MaxLongestSide)
		if err != nil {
			return err
		}
	}
	if definition.RequiresImage && strings.TrimSpace(input.InputImage) == "" {
		if definition.ID == "minimax-h3-video" {
			return errors.New("для MiniMax H3 добавьте первый кадр")
		}
		return errors.New("для этого workflow нужно добавить фото")
	}
	if strings.TrimSpace(input.ModelName) == "" {
		return errors.New("выберите модель генерации")
	}
	if strings.TrimSpace(input.Positive) == "" {
		return errors.New("добавьте позитивный промт")
	}
	if len(input.Positive) > 4000 || len(input.Negative) > 4000 {
		return errors.New("промт слишком длинный")
	}
	if input.Width < 256 || input.Width > 4096 || input.Width%8 != 0 || input.Height < 256 || input.Height > 4096 || input.Height%8 != 0 {
		return errors.New("ширина и высота должны быть от 256 до 4096 и кратны 8")
	}
	if input.OutputMegapixels < 0.1 || input.OutputMegapixels > 16 {
		return errors.New("итоговое разрешение должно быть от 0.1 до 16 мегапикселей")
	}
	if input.MaxLongestSide < 0 || input.MaxLongestSide > 4096 || input.MaxLongestSide%8 != 0 {
		return errors.New("ограничение длинной стороны должно быть от 0 до 4096 и кратно 8")
	}
	if input.Steps < 1 || input.Steps > 100 {
		return errors.New("число шагов должно быть от 1 до 100")
	}
	if input.CFG < 1 || input.CFG > 30 {
		return errors.New("CFG должен быть от 1 до 30")
	}
	if input.Denoise < 0.05 || input.Denoise > 1 {
		return errors.New("сила изменения должна быть от 0.05 до 1")
	}
	if input.Sampler == "" {
		input.Sampler = "euler"
	}
	if input.Scheduler == "" {
		input.Scheduler = "normal"
	}
	// Earlier gateway builds exposed "flux2" as a scheduler. That is a model
	// family, not a scheduler accepted by LCSamplerConfigureSimplePipeOut.
	// Preserve submitted forms from those builds by normalizing the legacy value.
	if definition.ID == "image-to-image-flux2" && input.Scheduler == "flux2" {
		input.Scheduler = "normal"
	}
	if !allowedGenerationSampler(input.Sampler) {
		return errors.New("неподдерживаемый сэмплер")
	}
	if !allowedGenerationScheduler(input.Scheduler) {
		return errors.New("неподдерживаемый планировщик")
	}
	if err := definition.normalizeAndValidateSpecific(input); err != nil {
		return err
	}
	if input.Seed < 0 {
		seed, err := randomSeed()
		if err != nil {
			return err
		}
		input.Seed = seed
	}
	return nil
}

func (definition workflowDefinition) normalizeAndValidateSpecific(input *generationForm) error {
	switch definition.ID {
	case "minimax-h3-video":
		return normalizeMiniMaxH3(input)
	case "image-to-image-flux2":
		normalizeLUT(input)
		// LCAspectRatioPipeOut uses 0 as a no-clamp value in its public UI, but
		// its Flux2 latent path rounds that value down to a zero-sized latent.
		// The original working workflow uses 2160, so keep that safe ceiling when
		// the quick-generation form did not submit an explicit limit.
		if input.MaxLongestSide == 0 {
			input.MaxLongestSide = 2160
		}
		if input.EditAspectPreset == "" {
			input.EditAspectPreset = "custom"
		}
		if input.EditResizeMethod == "" {
			input.EditResizeMethod = "lanczos"
		}
		if input.EditProportion == "" {
			input.EditProportion = "crop"
		}
		if input.EditCropLocation == "" {
			input.EditCropLocation = "center"
		}
		if input.EditPadColor == "" {
			input.EditPadColor = "0, 0, 0"
		}
		if !allowedEditFrame(input.EditAspectPreset, input.EditResizeMethod, input.EditProportion, input.EditCropLocation, input.EditPadColor) {
			return errors.New("некорректные параметры кадра Flux2")
		}
		if !allowedFlux2EditScheduler(input.Scheduler) {
			return errors.New("для Flux2: фото и промт выберите совместимый планировщик")
		}
		if input.FluxGuidance < 0 || input.FluxGuidance > 10 || input.FluxDetailerSteps < 0 || input.FluxDetailerSteps > 100 || input.FluxActiveScale < 0 || input.FluxActiveScale > 10 || input.FluxTokenWhiten < -1 || input.FluxTokenWhiten > 5 || input.FluxNormEqualize < 0 || input.FluxNormEqualize > 1 {
			return errors.New("некорректные параметры conditioning Flux2")
		}
		if input.SourceMegapixels == 0 {
			input.SourceMegapixels = 1
		}
		if input.SourceMegapixels < 0.25 || input.SourceMegapixels > 16 {
			return errors.New("разрешение исходного фото Flux2 должно быть от 0.25 до 16 мегапикселей")
		}
		if input.FluxUpscaleMode == "" {
			input.FluxUpscaleMode = "none"
		}
		if !allowedFlux2UpscaleMode(input.FluxUpscaleMode) {
			return errors.New("некорректный режим апскейла Flux2")
		}
		if !allowedLUT(input.LUTName) || input.LUTStrength < 0 || input.LUTStrength > 1 {
			return errors.New("некорректные параметры LUT для Flux2")
		}
		if input.imageCount() > 4 {
			return errors.New("Flux2: фото и промт поддерживает до четырёх изображений")
		}
	case "image-to-image-krea2":
		if input.GroundingPixels == 0 {
			input.GroundingPixels = 768
		}
		if input.UpscaleFactor == 0 {
			input.UpscaleFactor = 1.5
		}
		if input.UpscaleSteps == 0 {
			input.UpscaleSteps = 4
		}
		if input.UpscaleDenoise == 0 {
			input.UpscaleDenoise = 0.15
		}
		if input.UpscaleSampler == "" {
			input.UpscaleSampler = "deis"
		}
		if input.UpscaleScheduler == "" {
			input.UpscaleScheduler = "simple"
		}
		normalizeKreaEditDefaults(input)
		normalizeLUT(input)
		normalizeKrea2EditResolution(input)
		if input.PreserveOriginalSize {
			// The image is already fitted to the original frame, so the regular
			// final upscale would otherwise change the requested output size.
			input.UpscaleFactor = 1
		}
		if input.ReferenceBoost < 0 || input.ReferenceBoost > 8 {
			return errors.New("сила сохранения исходника Krea2 должна быть от 0 до 8")
		}
		if input.GroundingPixels < 256 || input.GroundingPixels > 2048 || input.GroundingPixels%64 != 0 {
			return errors.New("разрешение анализа фото Krea2 должно быть от 256 до 2048 и кратно 64")
		}
		if input.UpscaleFactor < 1 || input.UpscaleFactor > 2 {
			return errors.New("коэффициент апскейла Krea2 должен быть от 1 до 2")
		}
		if input.UpscaleSteps < 1 || input.UpscaleSteps > 100 || input.UpscaleDenoise < 0.01 || input.UpscaleDenoise > 0.5 || input.UpscaleCFG < 0 || input.UpscaleCFG > 30 || !allowedGenerationSampler(input.UpscaleSampler) || !allowedGenerationScheduler(input.UpscaleScheduler) {
			return errors.New("некорректные параметры апскейла Krea2")
		}
		if input.PostDenoiseBlur < 0.001 || input.PostDenoiseBlur > 8 || input.PostDenoiseEdge < 0.001 || input.PostDenoiseEdge > 0.25 || input.PostDenoiseRadius < 0 || input.PostDenoiseRadius > 3 || input.PostDenoiseStrength < 0 || input.PostDenoiseStrength > 1 {
			return errors.New("некорректные параметры очистки Krea2")
		}
		if !allowedKreaSkinPreset(input.SkinPreset) || input.SkinStrength < 0 || input.SkinStrength > 2 || input.SkinCoolness < 0 || input.SkinCoolness > 1 || input.SkinBrightness < 0 || input.SkinBrightness > 1 || input.SkinRosy < -0.3 || input.SkinRosy > 0.5 || input.SkinEvenness < 0 || input.SkinEvenness > 1 || input.SkinShadowLift < 0 || input.SkinShadowLift > 1 || input.SkinSmooth < 0 || input.SkinSmooth > 1 || input.SkinTexturePreserve < 0 || input.SkinTexturePreserve > 1 || input.SkinSaturation < -0.5 || input.SkinSaturation > 0.5 || input.SkinHighlightProtect < 0 || input.SkinHighlightProtect > 1 || input.SkinMaskSensitivity < 0 || input.SkinMaskSensitivity > 1 || input.SkinMaskFeather < 0 || input.SkinMaskFeather > 1 {
			return errors.New("некорректные параметры обработки кожи Krea2")
		}
		if !allowedLUT(input.LUTName) || input.LUTStrength < 0 || input.LUTStrength > 1 || input.AdjustHue < -1 || input.AdjustHue > 1 || input.AdjustSaturation < -1 || input.AdjustSaturation > 1 || input.AdjustBrightness < -1 || input.AdjustBrightness > 1 || input.AdjustContrast < -1 || input.AdjustContrast > 1 || input.AdjustSharpness < -1 || input.AdjustSharpness > 1 {
			return errors.New("некорректные параметры тона Krea2")
		}
		if input.imageCount() > 2 {
			return errors.New("Krea2: фото и промт поддерживает до двух изображений")
		}
	case "text-to-image-krea2":
		normalizeKreaTextWorkflow(input)
		if err := validateKreaTextWorkflow(*input); err != nil {
			return err
		}
	}
	return nil
}

func allowedFlux2UpscaleMode(value string) bool {
	switch value {
	case "none", "ultimate", "seedvr2", "both":
		return true
	default:
		return false
	}
}

func normalizeKreaEditDefaults(input *generationForm) {
	// Direct workflow callers (including tests and legacy integrations) do not
	// submit the dedicated edit panel. Browser forms always submit SkinPreset,
	// so a deliberate zero in the UI remains a deliberate zero.
	if input.SkinPreset != "" {
		return
	}
	input.SkinPreset = "Natural"
	input.UpscaleCFG = 1
	input.PostDenoiseBlur = 1
	input.PostDenoiseEdge = 0.05
	input.PostDenoiseRadius = 1
	input.PostDenoiseStrength = 0.75
	input.SkinStrength = 1
	input.SkinCoolness = 0.22
	input.SkinBrightness = 0.12
	input.SkinRosy = 0.08
	input.SkinEvenness = 0.18
	input.SkinShadowLift = 0.15
	input.SkinSmooth = 0.06
	input.SkinTexturePreserve = 0.88
	input.SkinSaturation = -0.08
	input.SkinHighlightProtect = 0.75
	input.SkinMaskSensitivity = 0.55
	input.SkinMaskFeather = 0.45
}

// normalizeKrea2EditResolution keeps the source composition while applying a
// hard 4.7 MP limit. Krea2 Edit's pixel path grows very quickly with the source
// frame, so accepting a full-resolution phone image can exhaust 32 GB of VRAM.
func normalizeKrea2EditResolution(input *generationForm) {
	maxPixels := krea2EditMaxBaseMegapixels * 1024 * 1024
	currentPixels := float64(input.Width * input.Height)
	scale := 1.0
	if currentPixels > maxPixels {
		scale = math.Sqrt(maxPixels / currentPixels)
	}
	if longest := max(input.Width, input.Height); longest > krea2EditMaxLongestSidePixels {
		scale = min(scale, float64(krea2EditMaxLongestSidePixels)/float64(longest))
	}
	if scale < 1 {
		input.Width = max(256, int(float64(input.Width)*scale/8)*8)
		input.Height = max(256, int(float64(input.Height)*scale/8)*8)
		// Keep the final frame within the same ceiling instead of scaling the
		// fitted image up again in the optional finishing pass.
		input.PreserveOriginalSize = true
	}
	if input.MaxLongestSide == 0 || input.MaxLongestSide > krea2EditMaxLongestSidePixels {
		input.MaxLongestSide = krea2EditMaxLongestSidePixels
	}
	input.OutputMegapixels = float64(input.Width*input.Height) / (1024 * 1024)
}

// normalizeLUT keeps LCApplyLUT present in the saved workflows while making
// its color transform truly optional. An empty profile means no grading; the
// node receives a known profile and zero strength. Older callers that submit a
// profile and a non-zero strength keep their previous behavior.
func normalizeLUT(input *generationForm) {
	if strings.TrimSpace(input.LUTName) == "" {
		input.LUTName = "LC_Crushed_Blacks.cube"
		input.LUTStrength = 0
		input.LUTEnabled = false
		return
	}
	if !input.LUTEnabled && input.LUTStrength > 0 {
		input.LUTEnabled = true
	}
	if !input.LUTEnabled {
		input.LUTStrength = 0
	}
}

func normalizeKreaTextWorkflow(input *generationForm) {
	if input.BaseMegapixels == 0 {
		input.BaseMegapixels = 1
	}
	if !input.LorasConfigured && input.LoraNames[0] == "" && input.Lora != "" {
		input.LoraNames[0] = input.Lora
		input.LoraModel[0] = input.LoraStrength
		input.LoraClip[0] = input.LoraStrength
	}
	if input.UpscaleSteps == 0 {
		input.UpscaleSteps = 5
	}
	if input.UpscaleAutoDenoise {
		input.UpscaleDenoise = math.Floor((0.14*(math.Sqrt(float64(input.Width*input.Height))/1024)-0.01)*100) / 100
		input.UpscaleDenoise = max(0.01, min(0.5, input.UpscaleDenoise))
	} else if input.UpscaleDenoise == 0 {
		input.UpscaleDenoise = 0.18
	}
	if input.UpscaleSampler == "" {
		input.UpscaleSampler = "euler_ancestral"
	}
	if input.DetailSteps == 0 {
		input.DetailSteps = 2
	}
	if input.DetailDenoise == 0 {
		input.DetailDenoise = 0.03
	}
	if input.DetailCFG == 0 {
		input.DetailCFG = 1
	}
	if input.DetailSampler == "" {
		input.DetailSampler = "euler"
	}
	if input.DetailScheduler == "" {
		input.DetailScheduler = "simple"
	}
	if input.ColorMethod == "" {
		input.ColorMethod = "reinhard_lab"
	}
	if input.ColorMode == "" {
		input.ColorMode = "per_frame"
	}
	if input.ColorStrength == 0 {
		input.ColorStrength = 1
	}
}

func validateKreaTextWorkflow(input generationForm) error {
	if input.BaseMegapixels < 0.5 || input.BaseMegapixels > 2 {
		return errors.New("базовое разрешение Krea2 должно быть от 0.5 до 2 мегапикселей")
	}
	if input.LoraStrength != 0 && (input.LoraStrength < 0.1 || input.LoraStrength > 2) {
		return errors.New("сила Krea LoRA должна быть от 0.1 до 2")
	}
	for index := range input.LoraNames {
		if input.LoraNames[index] != "" && (input.LoraModel[index] < -100 || input.LoraModel[index] > 100 || input.LoraClip[index] < -100 || input.LoraClip[index] > 100) {
			return errors.New("сила LoRA должна быть от -100 до 100")
		}
	}
	if input.UpscaleSteps < 1 || input.UpscaleSteps > 12 || input.UpscaleDenoise < 0.01 || input.UpscaleDenoise > 0.5 || !allowedGenerationSampler(input.UpscaleSampler) {
		return errors.New("некорректные параметры прохода апскейла")
	}
	if input.DetailSteps < 1 || input.DetailSteps > 8 || input.DetailDenoise < 0.01 || input.DetailDenoise > 0.2 || input.DetailCFG < 0 || input.DetailCFG > 30 || !allowedGenerationSampler(input.DetailSampler) || !allowedGenerationScheduler(input.DetailScheduler) {
		return errors.New("некорректные параметры детализации")
	}
	if !allowedColorTransfer(input.ColorMethod, input.ColorMode) || input.ColorStrength < 0 || input.ColorStrength > 10 {
		return errors.New("некорректные параметры переноса цвета")
	}
	return nil
}

func (definition workflowDefinition) workflowValues(input generationForm) map[string]any {
	values := map[string]any{
		"checkpoint":      input.ModelName,
		"diffusion_model": input.ModelName,
		"text_encoder":    input.TextEncoder,
		"vae":             input.VAE,
		"input_image":     input.InputImage,
		"input_image_2":   input.ReferenceImages[0],
		"input_image_3":   input.ReferenceImages[1],
		"input_image_4":   input.ReferenceImages[2],
		"positive_prompt": input.Positive,
		"negative_prompt": input.Negative,
		"width":           input.Width,
		"height":          input.Height,
		"steps":           input.Steps,
		"cfg":             input.CFG,
		"denoise":         input.Denoise,
		"sampler":         input.Sampler,
		"scheduler":       input.Scheduler,
		"seed":            input.Seed,
	}
	switch definition.ID {
	case "image-to-image-flux2":
		values["source_megapixels"] = input.SourceMegapixels
		values["flux_guidance"] = input.FluxGuidance
		values["flux_detailer_steps"] = input.FluxDetailerSteps
		values["flux_active_scale"] = input.FluxActiveScale
		values["flux_token_whiten"] = input.FluxTokenWhiten
		values["flux_norm_equalize"] = input.FluxNormEqualize
		values["edit_use_custom_size"] = input.EditUseCustomSize
		values["max_longest_side"] = input.MaxLongestSide
		values["edit_aspect_preset"] = input.EditAspectPreset
		values["edit_swap_dimensions"] = map[bool]string{true: "On", false: "Off"}[input.EditSwapDimensions]
		values["edit_resize_method"] = input.EditResizeMethod
		values["edit_proportion"] = input.EditProportion
		values["edit_crop_location"] = input.EditCropLocation
		values["edit_pad_color"] = input.EditPadColor
		values["lut_name"] = input.LUTName
		values["lut_strength"] = input.LUTStrength
	case "image-to-image-krea2":
		values["identity_lora"] = input.IdentityLora
		values["reference_boost"] = input.ReferenceBoost
		values["grounding_pixels"] = input.GroundingPixels
		values["upscale_factor"] = input.UpscaleFactor
		values["upscale_steps"] = input.UpscaleSteps
		values["upscale_denoise"] = input.UpscaleDenoise
		values["upscale_sampler"] = input.UpscaleSampler
		values["upscale_cfg"] = input.UpscaleCFG
		values["upscale_scheduler"] = input.UpscaleScheduler
		values["post_denoise_blur"] = input.PostDenoiseBlur
		values["post_denoise_edge"] = input.PostDenoiseEdge
		values["post_denoise_radius"] = input.PostDenoiseRadius
		values["post_denoise_strength"] = input.PostDenoiseStrength
		values["skin_preset"] = input.SkinPreset
		values["skin_strength"] = input.SkinStrength
		values["skin_coolness"] = input.SkinCoolness
		values["skin_brightness"] = input.SkinBrightness
		values["skin_rosy"] = input.SkinRosy
		values["skin_evenness"] = input.SkinEvenness
		values["skin_shadow_lift"] = input.SkinShadowLift
		values["skin_smooth"] = input.SkinSmooth
		values["skin_texture_preserve"] = input.SkinTexturePreserve
		values["skin_saturation"] = input.SkinSaturation
		values["skin_highlight_protect"] = input.SkinHighlightProtect
		values["skin_mask_sensitivity"] = input.SkinMaskSensitivity
		values["skin_mask_feather"] = input.SkinMaskFeather
		values["adjust_hue"] = input.AdjustHue
		values["adjust_saturation"] = input.AdjustSaturation
		values["adjust_brightness"] = input.AdjustBrightness
		values["adjust_contrast"] = input.AdjustContrast
		values["adjust_sharpness"] = input.AdjustSharpness
		values["lut_name"] = input.LUTName
		values["lut_strength"] = input.LUTStrength
		values["edit_use_custom_size"] = input.EditUseCustomSize
		values["max_longest_side"] = input.MaxLongestSide
	case "text-to-image-krea2":
		values["lora"] = input.Lora
		values["lora_strength"] = input.LoraStrength
		values["upscale_steps"] = input.UpscaleSteps
		values["upscale_denoise"] = input.UpscaleDenoise
		values["upscale_sampler"] = input.UpscaleSampler
		values["detail_steps"] = input.DetailSteps
		values["detail_denoise"] = input.DetailDenoise
		values["detail_cfg"] = input.DetailCFG
		values["detail_sampler"] = input.DetailSampler
		values["detail_scheduler"] = input.DetailScheduler
		values["color_method"] = input.ColorMethod
		values["color_mode"] = input.ColorMode
		values["color_strength"] = input.ColorStrength
		if !input.ColorTransfer {
			values["color_strength"] = 0
		}
		for index := range input.LoraNames {
			name := input.LoraNames[index]
			modelStrength := input.LoraModel[index]
			clipStrength := input.LoraClip[index]
			if name == "" {
				name = input.Lora
				modelStrength = 0
				clipStrength = 0
			}
			position := strconv.Itoa(index + 1)
			values["lora_"+position] = name
			values["lora_model_strength_"+position] = modelStrength
			values["lora_clip_strength_"+position] = clipStrength
		}
		values["base_width"], values["base_height"] = baseGenerationDimensions(input.Width, input.Height, input.BaseMegapixels)
	}
	return values
}

func pruneOptionalImageNodes(workflowID string, nodes map[string]map[string]any, input generationForm) {
	removeNodes := func(ids ...string) {
		for _, id := range ids {
			delete(nodes, id)
		}
	}
	removeInput := func(nodeID, name string) {
		if node, ok := nodes[nodeID]; ok {
			if inputs, ok := node["inputs"].(map[string]any); ok {
				delete(inputs, name)
			}
		}
	}
	switch workflowID {
	case "image-to-image-flux2":
		references := []struct {
			image     string
			nodeIDs   []string
			latentKey string
		}{
			{input.ReferenceImages[0], []string{"1076", "1065", "1066"}, "latent_2"},
			{input.ReferenceImages[1], []string{"1077", "1063", "1072"}, "latent_3"},
			{input.ReferenceImages[2], []string{"1316", "1317", "1318"}, "latent_4"},
		}
		for _, reference := range references {
			if strings.TrimSpace(reference.image) == "" {
				removeNodes(reference.nodeIDs...)
				removeInput("1398", reference.latentKey)
			}
		}
	case "image-to-image-krea2":
		if strings.TrimSpace(input.ReferenceImages[0]) == "" {
			removeNodes("20", "21")
			removeInput("8", "source_latent_b")
			removeInput("8", "source_image_b")
			removeInput("9", "image_b")
		}
	}
}

func generationDimensions(aspect string, megapixels float64, multiple, maxLongest int) (int, int, error) {
	parts := strings.Split(aspect, ":")
	if len(parts) != 2 || megapixels < 0.1 || megapixels > 16 || multiple < 8 || multiple > 128 || multiple&(multiple-1) != 0 {
		return 0, 0, errors.New("некорректные параметры разрешения")
	}
	ratioWidth, errWidth := strconv.ParseFloat(parts[0], 64)
	ratioHeight, errHeight := strconv.ParseFloat(parts[1], 64)
	if errWidth != nil || errHeight != nil || ratioWidth <= 0 || ratioHeight <= 0 {
		return 0, 0, errors.New("некорректное соотношение сторон")
	}
	targetPixels := megapixels * 1024 * 1024
	width := int(math.Sqrt(targetPixels*ratioWidth/ratioHeight)/float64(multiple)) * multiple
	height := int(math.Sqrt(targetPixels*ratioHeight/ratioWidth)/float64(multiple)) * multiple
	if maxLongest > 0 && max(width, height) > maxLongest {
		scale := float64(maxLongest) / float64(max(width, height))
		width = int(float64(width)*scale/float64(multiple)) * multiple
		height = int(float64(height)*scale/float64(multiple)) * multiple
	}
	return max(256, width), max(256, height), nil
}

func baseGenerationDimensions(width, height int, megapixels float64) (int, int) {
	targetPixels := int(megapixels * 1024 * 1024)
	if width*height <= targetPixels {
		return width, height
	}
	scale := math.Sqrt(float64(targetPixels) / float64(width*height))
	baseWidth := max(256, int(float64(width)*scale)/64*64)
	baseHeight := max(256, int(float64(height)*scale)/64*64)
	return baseWidth, baseHeight
}

func cloneWorkflowNodes(nodes map[string]map[string]any) (map[string]map[string]any, error) {
	body, err := json.Marshal(nodes)
	if err != nil {
		return nil, err
	}
	var cloned map[string]map[string]any
	if err := json.Unmarshal(body, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func replaceWorkflowValues(value any, values map[string]any) any {
	switch typed := value.(type) {
	case string:
		if strings.HasPrefix(typed, "{{") && strings.HasSuffix(typed, "}}") {
			if replacement, ok := values[strings.TrimSpace(typed[2:len(typed)-2])]; ok {
				return replacement
			}
		}
		result := typed
		for key, replacement := range values {
			result = strings.ReplaceAll(result, "{{"+key+"}}", fmt.Sprint(replacement))
		}
		return result
	case map[string]any:
		for key, item := range typed {
			typed[key] = replaceWorkflowValues(item, values)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = replaceWorkflowValues(item, values)
		}
		return typed
	default:
		return value
	}
}

func randomSeed() (int64, error) {
	// Seed (rgthree), used by the Flux2 workflow, accepts values only through
	// 2^50. Keep random seeds in that shared range for every quick workflow.
	value, err := rand.Int(rand.Reader, big.NewInt((1<<50)+1))
	if err != nil {
		return 0, err
	}
	return value.Int64(), nil
}

// parseGenerationFloat accepts the comma decimal separator commonly used by
// Russian mobile keyboards as well as the dot required by workflow JSON.
func parseGenerationFloat(value string) (float64, error) {
	return strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(value), ",", "."), 64)
}

func parseGenerationForm(r *http.Request) (generationForm, error) {
	if err := r.ParseForm(); err != nil {
		return generationForm{}, err
	}
	parseInt := func(name string, fallback int) (int, error) {
		value := strings.TrimSpace(r.Form.Get(name))
		if value == "" {
			return fallback, nil
		}
		return strconv.Atoi(value)
	}
	parseFloat := func(name string, fallback float64) (float64, error) {
		value := strings.TrimSpace(r.Form.Get(name))
		if value == "" {
			return fallback, nil
		}
		return parseGenerationFloat(value)
	}
	width, err := parseInt("width", 1024)
	if err != nil {
		return generationForm{}, errors.New("некорректная ширина")
	}
	height, err := parseInt("height", 1024)
	if err != nil {
		return generationForm{}, errors.New("некорректная высота")
	}
	steps, err := parseInt("steps", 25)
	if err != nil {
		return generationForm{}, errors.New("некорректное число шагов")
	}
	videoSteps, err := parseInt("video_steps", 25)
	if err != nil {
		return generationForm{}, errors.New("некорректное число шагов MiniMax H3")
	}
	videoDurationSeconds, err := parseInt("video_duration_seconds", miniMaxH3MinimumSeconds)
	if err != nil {
		return generationForm{}, errors.New("некорректная длительность MiniMax H3")
	}
	videoQuality, err := parseInt("video_quality", 0)
	if err != nil {
		return generationForm{}, errors.New("некорректное качество MiniMax H3")
	}
	cfg, err := parseFloat("cfg", 7)
	if err != nil {
		return generationForm{}, errors.New("некорректный CFG")
	}
	denoise, err := parseFloat("denoise", 0.7)
	if err != nil {
		return generationForm{}, errors.New("некорректная сила изменения")
	}
	baseMegapixels, err := parseFloat("base_megapixels", 1)
	if err != nil {
		return generationForm{}, errors.New("некорректное базовое разрешение")
	}
	outputMegapixels, err := parseFloat("output_megapixels", 1.9)
	if err != nil {
		return generationForm{}, errors.New("некорректное итоговое разрешение")
	}
	dimensionMultiple, err := parseInt("dimension_multiple", 16)
	if err != nil {
		return generationForm{}, errors.New("некорректная кратность разрешения")
	}
	maxLongestSide, err := parseInt("max_longest_side", 0)
	if err != nil {
		return generationForm{}, errors.New("некорректное ограничение стороны")
	}
	upscaleSteps, err := parseInt("upscale_steps", 5)
	if err != nil {
		return generationForm{}, errors.New("некорректное число шагов апскейла")
	}
	upscaleDenoise, err := parseFloat("upscale_denoise", 0.18)
	if err != nil {
		return generationForm{}, errors.New("некорректная сила апскейла")
	}
	detailSteps, err := parseInt("detail_steps", 2)
	if err != nil {
		return generationForm{}, errors.New("некорректное число шагов детализации")
	}
	detailDenoise, err := parseFloat("detail_denoise", 0.03)
	if err != nil {
		return generationForm{}, errors.New("некорректная сила детализации")
	}
	detailCFG, err := parseFloat("detail_cfg", 1)
	if err != nil {
		return generationForm{}, errors.New("некорректный CFG детализации")
	}
	colorStrength, err := parseFloat("color_strength", 1)
	if err != nil {
		return generationForm{}, errors.New("некорректная сила переноса цвета")
	}
	sourceMegapixels, err := parseFloat("source_megapixels", 1)
	if err != nil {
		return generationForm{}, errors.New("некорректное разрешение исходного фото")
	}
	referenceBoost, err := parseFloat("reference_boost", 4)
	if err != nil {
		return generationForm{}, errors.New("некорректная сила сохранения исходника")
	}
	groundingPixels, err := parseInt("grounding_pixels", 768)
	if err != nil {
		return generationForm{}, errors.New("некорректное разрешение анализа фото")
	}
	upscaleFactor, err := parseFloat("upscale_factor", 1.5)
	if err != nil {
		return generationForm{}, errors.New("некорректный коэффициент апскейла")
	}
	fluxGuidance, err := parseFloat("flux_guidance", 4)
	if err != nil {
		return generationForm{}, errors.New("некорректный Flux Guidance")
	}
	fluxDetailerSteps, err := parseInt("flux_detailer_steps", 25)
	if err != nil {
		return generationForm{}, errors.New("некорректное число detailer-шагов Flux2")
	}
	fluxActiveScale, err := parseFloat("flux_active_scale", 1)
	if err != nil {
		return generationForm{}, errors.New("некорректный active scale Flux2")
	}
	fluxTokenWhiten, err := parseFloat("flux_token_whiten", 0)
	if err != nil {
		return generationForm{}, errors.New("некорректный token whiten Flux2")
	}
	fluxNormEqualize, err := parseFloat("flux_norm_equalize", 0)
	if err != nil {
		return generationForm{}, errors.New("некорректный norm equalize Flux2")
	}
	upscaleCFG, err := parseFloat("upscale_cfg", 1)
	if err != nil {
		return generationForm{}, errors.New("некорректный CFG апскейла")
	}
	postDenoiseBlur, err := parseFloat("post_denoise_blur", 1)
	if err != nil {
		return generationForm{}, errors.New("некорректный blur постобработки")
	}
	postDenoiseEdge, err := parseFloat("post_denoise_edge", 0.05)
	if err != nil {
		return generationForm{}, errors.New("некорректное сохранение краёв")
	}
	postDenoiseRadius, err := parseFloat("post_denoise_radius", 1)
	if err != nil {
		return generationForm{}, errors.New("некорректный радиус постобработки")
	}
	postDenoiseStrength, err := parseFloat("post_denoise_strength", 0.75)
	if err != nil {
		return generationForm{}, errors.New("некорректная сила постобработки")
	}
	skinStrength, err := parseFloat("skin_strength", 1)
	if err != nil {
		return generationForm{}, errors.New("некорректная сила обработки кожи")
	}
	skinCoolness, err := parseFloat("skin_coolness", 0.22)
	if err != nil {
		return generationForm{}, errors.New("некорректная холодность кожи")
	}
	skinBrightness, err := parseFloat("skin_brightness", 0.12)
	if err != nil {
		return generationForm{}, errors.New("некорректная яркость кожи")
	}
	skinRosy, err := parseFloat("skin_rosy", 0.08)
	if err != nil {
		return generationForm{}, errors.New("некорректный тон кожи")
	}
	skinEvenness, err := parseFloat("skin_evenness", 0.18)
	if err != nil {
		return generationForm{}, errors.New("некорректная ровность кожи")
	}
	skinShadowLift, err := parseFloat("skin_shadow_lift", 0.15)
	if err != nil {
		return generationForm{}, errors.New("некорректное осветление теней")
	}
	skinSmooth, err := parseFloat("skin_smooth", 0.06)
	if err != nil {
		return generationForm{}, errors.New("некорректное сглаживание кожи")
	}
	skinTexture, err := parseFloat("skin_texture_preserve", 0.88)
	if err != nil {
		return generationForm{}, errors.New("некорректное сохранение текстуры")
	}
	skinSaturation, err := parseFloat("skin_saturation", -0.08)
	if err != nil {
		return generationForm{}, errors.New("некорректная насыщенность кожи")
	}
	skinHighlight, err := parseFloat("skin_highlight_protect", 0.75)
	if err != nil {
		return generationForm{}, errors.New("некорректная защита светов")
	}
	skinMaskSensitivity, err := parseFloat("skin_mask_sensitivity", 0.55)
	if err != nil {
		return generationForm{}, errors.New("некорректная чувствительность маски")
	}
	skinMaskFeather, err := parseFloat("skin_mask_feather", 0.45)
	if err != nil {
		return generationForm{}, errors.New("некорректная растушёвка маски")
	}
	adjustHue, err := parseFloat("adjust_hue", 0)
	if err != nil {
		return generationForm{}, errors.New("некорректный оттенок")
	}
	adjustSaturation, err := parseFloat("adjust_saturation", 0)
	if err != nil {
		return generationForm{}, errors.New("некорректная насыщенность")
	}
	adjustBrightness, err := parseFloat("adjust_brightness", 0)
	if err != nil {
		return generationForm{}, errors.New("некорректная яркость")
	}
	adjustContrast, err := parseFloat("adjust_contrast", 0)
	if err != nil {
		return generationForm{}, errors.New("некорректный контраст")
	}
	adjustSharpness, err := parseFloat("adjust_sharpness", 0)
	if err != nil {
		return generationForm{}, errors.New("некорректная резкость")
	}
	lutStrength, err := parseFloat("lut_strength", 0.23)
	if err != nil {
		return generationForm{}, errors.New("некорректная сила LUT")
	}
	var loraNames [maxGenerationLoraSlots]string
	var loraModel, loraClip [maxGenerationLoraSlots]float64
	for index := range loraNames {
		position := strconv.Itoa(index + 1)
		loraNames[index] = strings.TrimSpace(r.Form.Get("lora_" + position))
		loraModel[index], err = parseFloat("lora_model_strength_"+position, 0)
		if err != nil {
			return generationForm{}, errors.New("некорректная сила LoRA для модели")
		}
		loraClip[index], err = parseFloat("lora_clip_strength_"+position, 1)
		if err != nil {
			return generationForm{}, errors.New("некорректная сила LoRA для CLIP")
		}
	}
	seed, err := parseInt64(r.Form.Get("seed"), -1)
	if err != nil {
		return generationForm{}, errors.New("некорректный seed")
	}
	return generationForm{
		TemplateID: strings.TrimSpace(r.Form.Get("template_id")), PresetID: strings.TrimSpace(r.Form.Get("generation_workflow")), ModelID: strings.TrimSpace(r.Form.Get("model")),
		InputImage: strings.TrimSpace(r.Form.Get("input_image")), Positive: strings.TrimSpace(r.Form.Get("positive_prompt")),
		Negative: strings.TrimSpace(r.Form.Get("negative_prompt")), Width: width, Height: height, Steps: steps,
		CFG: cfg, Denoise: denoise, Sampler: strings.TrimSpace(r.Form.Get("sampler")),
		Scheduler: strings.TrimSpace(r.Form.Get("scheduler")), Seed: seed,
		VideoMode: strings.TrimSpace(r.Form.Get("video_mode")), VideoResolution: strings.TrimSpace(r.Form.Get("video_resolution")), VideoAspect: strings.TrimSpace(r.Form.Get("video_aspect")), VideoQuality: videoQuality,
		VideoDurationSeconds: videoDurationSeconds, VideoReferenceSize: strings.TrimSpace(r.Form.Get("video_reference_size")), VideoSteps: videoSteps,
		AssistantRequested: r.Form.Get("assistant_requested") == "true", AssistantApplied: r.Form.Get("assistant_applied") == "true",
		AssistantTemplate: strings.TrimSpace(r.Form.Get("assistant_template_used")), AssistantThink: r.Form.Get("assistant_think_used") == "true",
		AssistantOriginal: strings.TrimSpace(r.Form.Get("assistant_original_prompt")), AssistantSuggestion: strings.TrimSpace(r.Form.Get("assistant_suggestion")),
		AspectRatio: strings.TrimSpace(r.Form.Get("aspect_ratio")), OutputMegapixels: outputMegapixels,
		DimensionMultiple: dimensionMultiple, MaxLongestSide: maxLongestSide,
		BaseMegapixels: baseMegapixels, LoraNames: loraNames, LoraModel: loraModel, LoraClip: loraClip,
		LorasConfigured: r.Form.Get("loras_configured") == "true",
		UpscaleSteps:    upscaleSteps, UpscaleDenoise: upscaleDenoise, UpscaleAutoDenoise: r.Form.Get("upscale_auto_denoise") == "true",
		UpscaleSampler: strings.TrimSpace(r.Form.Get("upscale_sampler")),
		DetailSteps:    detailSteps, DetailDenoise: detailDenoise, DetailCFG: detailCFG,
		DetailSampler: strings.TrimSpace(r.Form.Get("detail_sampler")), DetailScheduler: strings.TrimSpace(r.Form.Get("detail_scheduler")),
		ColorTransfer: r.Form.Get("color_transfer") == "true", ColorMethod: strings.TrimSpace(r.Form.Get("color_method")),
		ColorMode: strings.TrimSpace(r.Form.Get("color_mode")), ColorStrength: colorStrength,
		SourceMegapixels: sourceMegapixels, PreserveOriginalSize: r.Form.Get("preserve_original_size") == "true", EditUseCustomSize: r.Form.Get("edit_use_custom_size") == "true", EditAspectPreset: strings.TrimSpace(r.Form.Get("edit_aspect_preset")), EditSwapDimensions: r.Form.Get("edit_swap_dimensions") == "true", EditResizeMethod: strings.TrimSpace(r.Form.Get("edit_resize_method")), EditProportion: strings.TrimSpace(r.Form.Get("edit_proportion")), EditCropLocation: strings.TrimSpace(r.Form.Get("edit_crop_location")), EditPadColor: strings.TrimSpace(r.Form.Get("edit_pad_color")),
		ReferenceBoost: referenceBoost, GroundingPixels: groundingPixels, UpscaleFactor: upscaleFactor,
		FluxGuidance: fluxGuidance, FluxDetailerSteps: fluxDetailerSteps, FluxActiveScale: fluxActiveScale, FluxTokenWhiten: fluxTokenWhiten, FluxNormEqualize: fluxNormEqualize, FluxUpscaleMode: strings.TrimSpace(r.Form.Get("flux_upscale_mode")),
		UpscaleCFG: upscaleCFG, UpscaleScheduler: strings.TrimSpace(r.Form.Get("upscale_scheduler")),
		PostDenoiseBlur: postDenoiseBlur, PostDenoiseEdge: postDenoiseEdge, PostDenoiseRadius: postDenoiseRadius, PostDenoiseStrength: postDenoiseStrength,
		SkinPreset: strings.TrimSpace(r.Form.Get("skin_preset")), SkinStrength: skinStrength, SkinCoolness: skinCoolness, SkinBrightness: skinBrightness, SkinRosy: skinRosy, SkinEvenness: skinEvenness, SkinShadowLift: skinShadowLift, SkinSmooth: skinSmooth, SkinTexturePreserve: skinTexture, SkinSaturation: skinSaturation, SkinHighlightProtect: skinHighlight, SkinMaskSensitivity: skinMaskSensitivity, SkinMaskFeather: skinMaskFeather,
		AdjustHue: adjustHue, AdjustSaturation: adjustSaturation, AdjustBrightness: adjustBrightness, AdjustContrast: adjustContrast, AdjustSharpness: adjustSharpness, LUTName: strings.TrimSpace(r.Form.Get("lut_name")), LUTStrength: lutStrength, LUTEnabled: r.Form.Get("lut_enabled") == "true",
		ReferenceImages: [3]string{
			strings.TrimSpace(r.Form.Get("input_image_2")),
			strings.TrimSpace(r.Form.Get("input_image_3")),
			strings.TrimSpace(r.Form.Get("input_image_4")),
		},
	}, nil
}

func allowedGenerationSampler(value string) bool {
	switch value {
	case "euler", "euler_ancestral", "dpmpp_2m", "dpmpp_2m_sde", "heun", "deis", "res_multistep", "gradient_estimation", "er_sde":
		return true
	default:
		return false
	}
}

func allowedGenerationScheduler(value string) bool {
	switch value {
	case "normal", "simple", "beta", "karras", "sgm_uniform", "exponential":
		return true
	default:
		return false
	}
}

func allowedFlux2EditScheduler(value string) bool {
	switch value {
	case "normal", "simple", "beta", "karras", "sgm_uniform", "exponential":
		return true
	default:
		return false
	}
}

func allowedKreaSkinPreset(value string) bool {
	switch value {
	case "Natural", "Light", "Fresh", "Porcelain", "Warm keep", "Custom":
		return true
	default:
		return false
	}
}

func allowedLUT(value string) bool {
	switch value {
	case "LC_Crushed_Blacks.cube", "LC Highlights_Protection.cube", "Cool_Natural_Breeze.cube", "street.cube":
		return true
	default:
		return false
	}
}

func allowedEditFrame(aspect, method, proportion, crop, pad string) bool {
	aspectAllowed := aspect == "custom" || aspect == "Instagram Portrait - 1080x1350" || aspect == "Instagram Square - 1080x1080" || aspect == "Instagram Landscape - 1080x608" || aspect == "Instagram Stories/Reels - 1080x1920" || aspect == "3:4 - 896x1152" || aspect == "4:3 - 1152x896" || aspect == "9:16 - 768x1344" || aspect == "16:9 - 1344x768"
	methodAllowed := method == "nearest-exact" || method == "bicubic" || method == "bilinear" || method == "lanczos" || method == "area"
	proportionAllowed := proportion == "crop" || proportion == "stretch" || proportion == "resize" || proportion == "pad" || proportion == "total_pixels"
	cropAllowed := crop == "center" || crop == "top" || crop == "bottom" || crop == "left" || crop == "right"
	return aspectAllowed && methodAllowed && proportionAllowed && cropAllowed && len(pad) <= 32
}

func allowedColorTransfer(method, mode string) bool {
	methodAllowed := method == "reinhard_lab" || method == "mkl_lab" || method == "histogram"
	modeAllowed := mode == "per_frame" || mode == "uniform"
	return methodAllowed && modeAllowed
}

func parseInt64(value string, fallback int64) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseInt(value, 10, 64)
}
