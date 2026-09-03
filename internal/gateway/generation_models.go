package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const (
	modelFamilyCheckpoint = "checkpoint"
	modelFamilyKrea2      = "krea2"
	modelFamilyFlux2      = "flux2"
	modelFamilyMiniMaxH3  = "minimax_h3"
)

type generationModel struct {
	ID                   string
	Name                 string
	DisplayName          string
	Family               string
	Available            bool
	Reason               string
	TextEncoder          string
	VAE                  string
	AudioVAE             string
	ReferenceModel       string
	Lora                 string
	LoraStrength         float64
	IdentityLora         string
	SupportsImage        bool
	VideoIntegratedTurbo bool
	VideoReferenceOnly   bool
	DefaultSteps         int
	DefaultCFG           float64
	DefaultSampler       string
	DefaultScheduler     string
	DefaultVideoShift    int
	DefaultAudioShift    int
}

type generationModelGroup struct {
	Name   string
	Models []generationModel
}

type generationModelCatalog struct {
	Online            bool
	AvailableCount    int
	Groups            []generationModelGroup
	LoraGroups        []generationLoraGroup
	FluxLoraGroups    []generationLoraGroup
	MiniMaxLoraGroups []generationLoraGroup
	byID              map[string]generationModel
	ObjectInfo        comfyObjectInfoSnapshot
}

type generationLora struct {
	Name            string
	DisplayName     string
	DefaultStrength float64
	Default         bool
}

const miniMaxH3LoraDirectory = "MiniMaxH3\\"

type generationLoraGroup struct {
	Name  string
	Loras []generationLora
}

type generationPreset struct {
	ID               string
	TemplateID       string
	Name             string
	Description      string
	ModelID          string
	ModelName        string
	ModelCount       int
	Family           string
	Available        bool
	Reason           string
	RequiresImage    bool
	AllowsImages     bool
	AdminOnly        bool
	Restricted       bool
	Restriction      string
	MaxInputImages   int
	DefaultSteps     int
	DefaultCFG       float64
	DefaultSampler   string
	DefaultScheduler string
	LoraStrength     float64
}

type comfyNodeInfo struct {
	Input struct {
		Required map[string]json.RawMessage `json:"required"`
		Optional map[string]json.RawMessage `json:"optional"`
		Hidden   map[string]json.RawMessage `json:"hidden"`
	} `json:"input"`
	Output       []json.RawMessage `json:"output"`
	DisplayName  string            `json:"display_name"`
	PythonModule string            `json:"python_module"`
	Category     string            `json:"category"`
}

func (a *App) comfyGenerationModels(ctx context.Context) generationModelCatalog {
	catalog := generationModelCatalog{byID: make(map[string]generationModel)}
	snapshot, err := a.comfyObjectInfo(ctx, false)
	if err != nil {
		return catalog
	}
	catalog = buildGenerationModelCatalog(snapshot.Info)
	catalog.ObjectInfo = snapshot
	catalog.Online = snapshot.Source != comfyObjectInfoLastKnownGood
	return catalog
}

func buildGenerationModelCatalog(info map[string]comfyNodeInfo) generationModelCatalog {
	catalog := generationModelCatalog{Online: true, byID: make(map[string]generationModel)}
	catalog.ObjectInfo = comfyObjectInfoSnapshot{Info: info, Schema: buildComfySchemaCatalog(info, time.Time{}, ""), Source: comfyObjectInfoLive}
	checkpoints := comfyChoiceStrings(info, "CheckpointLoaderSimple", "ckpt_name")
	diffusion := comfyChoiceStrings(info, "UNETLoader", "unet_name")
	encoders := comfyChoiceStrings(info, "CLIPLoader", "clip_name")
	ggufEncoders := comfyChoiceStrings(info, "ClipLoaderGGUF", "clip_name")
	vaes := comfyChoiceStrings(info, "VAELoader", "vae_name")
	loras := comfyChoiceStrings(info, "LoraLoader", "lora_name")
	catalog.LoraGroups = buildGenerationLoraGroups(loras)
	catalog.FluxLoraGroups = buildFlux2LoraGroups(loras)
	catalog.MiniMaxLoraGroups = buildMiniMaxH3LoraGroups(loras)

	checkpointModels := make([]generationModel, 0, len(checkpoints))
	for _, name := range checkpoints {
		checkpointModels = append(checkpointModels, generationModel{
			ID: generationModelID(modelFamilyCheckpoint, name), Name: name, DisplayName: generationModelDisplayName(name), Family: modelFamilyCheckpoint,
			Available: true, SupportsImage: false, DefaultSteps: 25, DefaultCFG: 7, DefaultSampler: "euler", DefaultScheduler: "normal",
		})
	}
	if len(checkpointModels) > 0 {
		catalog.Groups = append(catalog.Groups, generationModelGroup{Name: "Обычные checkpoint", Models: checkpointModels})
	}

	kreaEncoder := firstMatchingModel(encoders,
		func(name string) bool { return strings.EqualFold(name, "qwen3vl_4b_fp8_scaled.safetensors") },
		func(name string) bool {
			value := strings.ToLower(name)
			return strings.Contains(value, "qwen3vl") && strings.Contains(value, "4b") && strings.Contains(value, "heretic")
		},
		func(name string) bool {
			value := strings.ToLower(name)
			return strings.Contains(value, "qwen3-vl-4b") && strings.Contains(value, "fp8")
		},
		func(name string) bool {
			value := strings.ToLower(name)
			return strings.Contains(value, "qwen3vl") && strings.Contains(value, "4b")
		},
	)
	kreaVAE := exactModel(vaes, "qwen_image_vae.safetensors")
	kreaLora := exactModel(loras, "lenovo_krea2.safetensors")
	// The installed production edit workflow uses the original v1.2 identity
	// LoRA. Keep this exact name so a similarly named user LoRA cannot be
	// selected as the workflow's required identity adapter.
	kreaIdentityLora := exactModel(loras, "krea2_identity_edit_v1_2.safetensors")
	fluxEncoder := firstMatchingModel(append(encoders, ggufEncoders...), func(name string) bool {
		value := strings.ToLower(name)
		return strings.Contains(value, "qwen_3_8b") || strings.Contains(value, "qwen3_8b") || strings.Contains(value, "qwen3-8b") || strings.Contains(value, "qwen3 8b")
	})
	fluxVAE := firstMatchingModel(vaes,
		func(name string) bool { return strings.EqualFold(name, "full_encoder_small_decoder.safetensors") },
		func(name string) bool { return strings.EqualFold(name, "flux2-vae.safetensors") },
	)
	miniMaxEncoder := firstMatchingModel(encoders,
		func(name string) bool {
			return strings.EqualFold(name, "MiniMax\\qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors")
		},
		func(name string) bool {
			value := strings.ToLower(name)
			return strings.Contains(value, "minimax") && strings.Contains(value, "qwen")
		},
	)
	miniMaxVideoVAE := exactModel(vaes, "MiniMax\\minimax_h3_video_vae_fp16.safetensors")
	miniMaxAudioVAE := exactModel(vaes, "MiniMax\\minimax_h3_audio_vae_fp32.safetensors")
	miniMaxReferenceModel := exactModel(diffusion, "MiniMax\\MiniMax_H3_Ref2VA_pruned_int8_convrot.safetensors")

	var kreaModels, fluxModels, miniMaxModels []generationModel
	for _, name := range diffusion {
		lower := strings.ToLower(name)
		switch {
		case strings.Contains(lower, "krea2") || strings.Contains(lower, "krea 2"):
			model := generationModel{
				ID: generationModelID(modelFamilyKrea2, name), Name: name, DisplayName: generationModelDisplayName(name), Family: modelFamilyKrea2,
				TextEncoder: kreaEncoder, VAE: kreaVAE, Lora: kreaLora, LoraStrength: 0.8, IdentityLora: kreaIdentityLora,
				DefaultSteps: 8, DefaultCFG: 1, DefaultSampler: "euler", DefaultScheduler: "simple",
			}
			if strings.Contains(lower, "turbo") {
				model.LoraStrength = 1.2
			}
			model.Available = kreaEncoder != "" && kreaVAE != "" && kreaLora != ""
			if !model.Available {
				model.Reason = missingKreaDependencies(kreaEncoder, kreaVAE, kreaLora)
			}
			model.SupportsImage = model.Available && kreaIdentityLora != "" && hasGenerationNodes(info, "Krea2EditModelPatch", "Krea2EditGroundedEncode", "AspectRatioSimplifier", "UltimateSDUpscale")
			kreaModels = append(kreaModels, model)
		case strings.Contains(lower, "flux2") || strings.Contains(lower, "flux.2"):
			model := generationModel{
				ID: generationModelID(modelFamilyFlux2, name), Name: name, DisplayName: generationModelDisplayName(name), Family: modelFamilyFlux2,
				TextEncoder: fluxEncoder, VAE: fluxVAE, SupportsImage: true, DefaultSteps: 25, DefaultCFG: 1, DefaultSampler: "euler", DefaultScheduler: "normal",
			}
			model.Available = fluxEncoder != "" && fluxVAE != "" && hasGenerationNodes(info, "ClipLoaderGGUF", "LCAspectRatioPipeOut", "LCReferenceLatent", "LCPipeEdit", "LCSamplerConfigureSimplePipeOut", "Power Lora Loader (rgthree)")
			if !model.Available {
				model.Reason = missingGenerationDependencies(fluxEncoder, fluxVAE, "Qwen3 8B text encoder", "Flux 2 VAE")
			}
			fluxModels = append(fluxModels, model)
		case strings.Contains(lower, "minimax_h3_fl2va"):
			model := generationModel{
				ID: generationModelID(modelFamilyMiniMaxH3, name), Name: name, DisplayName: "MiniMax H3 v5 · FL2VA + REF2VA", Family: modelFamilyMiniMaxH3,
				TextEncoder: miniMaxEncoder, VAE: miniMaxVideoVAE, AudioVAE: miniMaxAudioVAE, ReferenceModel: miniMaxReferenceModel,
				DefaultSteps: 25, DefaultCFG: 1, DefaultSampler: "euler", DefaultScheduler: "simple", DefaultVideoShift: 11, DefaultAudioShift: 3,
			}
			model.Available = miniMaxEncoder != "" && miniMaxVideoVAE != "" && miniMaxAudioVAE != "" && miniMaxReferenceModel != "" && hasGenerationNodes(info,
				"MiniMaxH3ImageToVideo", "MiniMaxH3ReferenceToVideo", "MiniMaxH3SigmaShift", "MiniMaxH3MemoryEfficientSageAttentionPatch", "MiniMaxH3TurboLoRA", "MiniMaxH3TurboSampler",
				"H3MemoryOptimization", "H3AIMDOResidencyLimiter", "H3SparseAttentionAdvanced", "LCImageMaskResize", "LCVRAMCacheClear", "ImageSharpenKJ", "CR LoRA Stack", "CR Apply LoRA Stack", "VHS_LoadVideo", "VHS_VideoCombine")
			if !model.Available {
				model.Reason = missingMiniMaxH3Dependencies(miniMaxEncoder, miniMaxVideoVAE, miniMaxAudioVAE, miniMaxReferenceModel)
			}
			miniMaxModels = append(miniMaxModels, model)
		case strings.Contains(lower, "h3erosmax_beta4") || strings.Contains(lower, "h3erosmax-beta4"):
			model := generationModel{
				ID: generationModelID(modelFamilyMiniMaxH3, name), Name: name, DisplayName: "H3 Eros Max beta4 · встроенный Turbo", Family: modelFamilyMiniMaxH3,
				TextEncoder: miniMaxEncoder, VAE: miniMaxVideoVAE, AudioVAE: miniMaxAudioVAE, ReferenceModel: name,
				VideoIntegratedTurbo: true, VideoReferenceOnly: true,
				DefaultSteps: 8, DefaultCFG: 1, DefaultSampler: "euler", DefaultScheduler: "simple", DefaultVideoShift: 12, DefaultAudioShift: 7,
			}
			model.Available = miniMaxEncoder != "" && miniMaxVideoVAE != "" && miniMaxAudioVAE != "" && hasGenerationNodes(info,
				"MiniMaxH3ReferenceToVideo", "MiniMaxH3SigmaShift", "MiniMaxH3MemoryEfficientSageAttentionPatch",
				"H3MemoryOptimization", "H3AIMDOResidencyLimiter", "H3SparseAttentionAdvanced", "LCVRAMCacheClear", "ImageSharpenKJ", "CR LoRA Stack", "CR Apply LoRA Stack", "VHS_LoadVideo", "VHS_VideoCombine")
			if !model.Available {
				model.Reason = missingMiniMaxH3Dependencies(miniMaxEncoder, miniMaxVideoVAE, miniMaxAudioVAE, name)
			}
			miniMaxModels = append(miniMaxModels, model)
		}
	}
	if len(kreaModels) > 0 {
		catalog.Groups = append(catalog.Groups, generationModelGroup{Name: "Krea 2", Models: kreaModels})
	}
	if len(fluxModels) > 0 {
		catalog.Groups = append(catalog.Groups, generationModelGroup{Name: "Flux 2", Models: fluxModels})
	}
	if len(miniMaxModels) > 0 {
		catalog.Groups = append(catalog.Groups, generationModelGroup{Name: "MiniMax H3", Models: miniMaxModels})
	}
	for _, group := range catalog.Groups {
		for _, model := range group.Models {
			catalog.byID[model.ID] = model
			if model.Available {
				catalog.AvailableCount++
			}
		}
	}
	return catalog
}

func buildGenerationLoraGroups(values []string) []generationLoraGroup {
	order := []string{"Базовые Krea2", "Обученные Krea2", "Реализм и детали", "Стили", "Остальные Krea2"}
	grouped := make(map[string][]generationLora)
	for _, name := range values {
		lower := normalizeGenerationModelPath(name)
		if strings.Contains(lower, "ltx2\\") || !isKreaLora(lower) {
			continue
		}
		category := "Остальные Krea2"
		switch {
		case strings.HasPrefix(lower, "trained\\krea2\\"):
			category = "Обученные Krea2"
		case strings.Contains(lower, "lenovo") || strings.Contains(lower, "turbo") || strings.Contains(lower, "projector") || strings.Contains(lower, "textfusion") || strings.Contains(lower, "filterbypass"):
			category = "Базовые Krea2"
		case strings.Contains(lower, "real") || strings.Contains(lower, "detail") || strings.Contains(lower, "skin") || strings.Contains(lower, "onglass") || strings.Contains(lower, "photo"):
			category = "Реализм и детали"
		case strings.Contains(lower, "zidius") || strings.Contains(lower, "altgirl") || strings.Contains(lower, "style") || strings.Contains(lower, "low_resolution"):
			category = "Стили"
		}
		grouped[category] = append(grouped[category], generationLora{
			Name: name, DisplayName: generationModelDisplayName(name), DefaultStrength: generationLoraDefaultStrength(lower),
			Default: strings.EqualFold(name, "lenovo_krea2.safetensors"),
		})
	}
	groups := make([]generationLoraGroup, 0, len(grouped))
	for _, category := range order {
		loras := grouped[category]
		if len(loras) == 0 {
			continue
		}
		sort.Slice(loras, func(i, j int) bool {
			return strings.ToLower(loras[i].DisplayName) < strings.ToLower(loras[j].DisplayName)
		})
		groups = append(groups, generationLoraGroup{Name: category, Loras: loras})
	}
	return groups
}

func buildFlux2LoraGroups(values []string) []generationLoraGroup {
	loras := make([]generationLora, 0)
	for _, name := range values {
		lower := normalizeGenerationModelPath(name)
		if !strings.HasPrefix(lower, "flux2\\") && !strings.HasPrefix(lower, "flux.2\\") && !strings.HasPrefix(lower, "trained\\flux2-klein\\") {
			continue
		}
		loras = append(loras, generationLora{
			Name: name, DisplayName: generationModelDisplayName(name), DefaultStrength: 1,
		})
	}
	if len(loras) == 0 {
		return nil
	}
	sort.Slice(loras, func(i, j int) bool {
		return strings.ToLower(loras[i].DisplayName) < strings.ToLower(loras[j].DisplayName)
	})
	return []generationLoraGroup{{Name: "Flux2", Loras: loras}}
}

func buildMiniMaxH3LoraGroups(values []string) []generationLoraGroup {
	loras := make([]generationLora, 0)
	for _, name := range values {
		lower := strings.ToLower(strings.ReplaceAll(name, "/", "\\"))
		if !strings.HasPrefix(lower, strings.ToLower(miniMaxH3LoraDirectory)) {
			continue
		}
		if strings.EqualFold(name, miniMaxH3TurboLoraName) {
			continue
		}
		loras = append(loras, generationLora{
			Name:            name,
			DisplayName:     miniMaxH3LoraDisplayName(name),
			DefaultStrength: miniMaxH3LoraDefaultStrength(lower),
		})
	}
	if len(loras) == 0 {
		return nil
	}
	sort.Slice(loras, func(i, j int) bool {
		return strings.ToLower(loras[i].DisplayName) < strings.ToLower(loras[j].DisplayName)
	})
	return []generationLoraGroup{{Name: "MiniMax H3", Loras: loras}}
}

func miniMaxH3LoraDisplayName(name string) string {
	baseName := strings.ReplaceAll(name, "\\", "/")
	if separator := strings.LastIndex(baseName, "/"); separator >= 0 {
		baseName = baseName[separator+1:]
	}
	baseName = strings.TrimSuffix(baseName, ".safetensors")
	if strings.EqualFold(baseName, "h3_Better_NSFW_Motion_V1") {
		return "Better NSFW Motion (H3 Ref2VA V1)"
	}
	return generationModelDisplayName(name)
}

func miniMaxH3LoraDefaultStrength(lower string) float64 {
	if strings.Contains(lower, "h3_better_nsfw_motion_v1") {
		return 0.9
	}
	return 1
}

func isKreaLora(lower string) bool {
	return strings.Contains(lower, "krea") || strings.Contains(lower, "lenovo")
}

func normalizeGenerationModelPath(name string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), "/", "\\"))
}

func generationLoraDefaultStrength(lower string) float64 {
	switch {
	case strings.Contains(lower, "detailer"):
		return 2
	case strings.Contains(lower, "mons_pubis") || strings.Contains(lower, "filterbypass"):
		return 5
	case strings.Contains(lower, "pubes"):
		return 0.5
	case strings.Contains(lower, "low_resolution"):
		return -2
	case strings.Contains(lower, "bss_"):
		return 1.5
	case strings.Contains(lower, "lenovo"):
		return 0.8
	default:
		return 1
	}
}

func generationLoraAllowed(groups []generationLoraGroup, name string) bool {
	for _, group := range groups {
		for _, lora := range group.Loras {
			if lora.Name == name {
				return true
			}
		}
	}
	return false
}

func buildGenerationPresets(catalog generationModelCatalog) []generationPreset {
	var krea, kreaEdit, flux *generationModel
	kreaCount, fluxCount := 0, 0
	for _, group := range catalog.Groups {
		for index := range group.Models {
			model := group.Models[index]
			switch model.Family {
			case modelFamilyKrea2:
				if model.Available {
					kreaCount++
				}
				if krea == nil || generationModelPreference(model) > generationModelPreference(*krea) {
					copy := model
					krea = &copy
				}
				if model.SupportsImage && (kreaEdit == nil || kreaEditModelPreference(model) > kreaEditModelPreference(*kreaEdit)) {
					copy := model
					kreaEdit = &copy
				}
			case modelFamilyFlux2:
				if model.Available {
					fluxCount++
				}
				if flux == nil || generationModelPreference(model) > generationModelPreference(*flux) {
					copy := model
					flux = &copy
				}
			}
		}
	}
	presets := make([]generationPreset, 0, 4)
	if krea != nil {
		manifest := krea2TextWorkflowManifest()
		preset := presetFromModel(manifest.ID, manifest.TemplateID, manifest.Name, manifest.Description, *krea, manifest.requiresInput("image"))
		preset.ModelCount = kreaCount
		presets = append(presets, preset)
		if kreaEdit != nil {
			editManifest := krea2EditWorkflowManifest()
			edit := presetFromModel(editManifest.ID, editManifest.TemplateID, editManifest.Name, editManifest.Description, *kreaEdit, editManifest.requiresInput("image"))
			// The original identity-edit recipe is trained for Krea2 Turbo at
			// 8-12 steps and CFG 1.0; arbitrary Krea fine-tunes visibly diverge.
			edit.DefaultSteps = 8
			edit.DefaultCFG = 1
			edit.DefaultSampler = "euler"
			edit.DefaultScheduler = "simple"
			edit.ModelCount = kreaCount
			edit.MaxInputImages = editManifest.maximumInput("image")
			edit.AllowsImages = edit.MaxInputImages > 0
			presets = append(presets, edit)
		}
	}
	if flux != nil {
		manifest := flux2EditWorkflowManifest()
		preset := presetFromModel(manifest.ID, manifest.TemplateID, manifest.Name, manifest.Description, *flux, manifest.requiresInput("image"))
		preset.ModelCount = fluxCount
		preset.MaxInputImages = manifest.maximumInput("image")
		preset.AllowsImages = preset.MaxInputImages > 0
		presets = append(presets, preset)
	}
	var miniMax *generationModel
	miniMaxCount := 0
	for _, group := range catalog.Groups {
		for index := range group.Models {
			model := group.Models[index]
			if model.Family != modelFamilyMiniMaxH3 {
				continue
			}
			if model.Available {
				miniMaxCount++
			}
			if miniMax == nil || (!miniMax.Available && model.Available) {
				copy := model
				miniMax = &copy
			}
		}
	}
	if miniMax != nil {
		manifest := miniMaxH3WorkflowManifest()
		preset := presetFromModel(manifest.ID, manifest.TemplateID, manifest.Name, manifest.Description, *miniMax, manifest.requiresInput("image"))
		preset.ModelCount = miniMaxCount
		preset.MaxInputImages = manifest.maximumInput("image")
		preset.AllowsImages = preset.MaxInputImages > 0
		presets = append(presets, preset)
	}
	return presets
}

func hasGenerationNodes(info map[string]comfyNodeInfo, names ...string) bool {
	for _, name := range names {
		if _, ok := info[name]; !ok {
			return false
		}
	}
	return true
}

func quickGenerationModels(catalog generationModelCatalog) []generationModel {
	var models []generationModel
	for _, group := range catalog.Groups {
		for _, model := range group.Models {
			if model.Available && (model.Family == modelFamilyKrea2 || model.Family == modelFamilyFlux2 || model.Family == modelFamilyMiniMaxH3) {
				models = append(models, model)
			}
		}
	}
	return models
}

func presetFromModel(id, templateID, name, description string, model generationModel, requiresImage bool) generationPreset {
	return generationPreset{
		ID: id, TemplateID: templateID, Name: name, Description: description,
		ModelID: model.ID, ModelName: model.DisplayName, Family: model.Family,
		Available: model.Available, Reason: model.Reason, RequiresImage: requiresImage,
		DefaultSteps: model.DefaultSteps, DefaultCFG: model.DefaultCFG,
		DefaultSampler: model.DefaultSampler, DefaultScheduler: model.DefaultScheduler,
		LoraStrength: model.LoraStrength,
	}
}

func generationModelPreference(model generationModel) int {
	value := strings.ToLower(model.Name)
	score := 0
	if model.Available {
		score += 1000
	}
	if strings.Contains(value, "v40") || strings.Contains(value, "v4.0") {
		score += 100
	}
	if !strings.Contains(value, "turbo") {
		score += 20
	}
	return score
}

func kreaEditModelPreference(model generationModel) int {
	if !model.Available || !model.SupportsImage {
		return -1
	}
	value := strings.ToLower(model.Name)
	score := 1000
	if strings.EqualFold(model.Name, "Krea2\\krea2TurboOfficialComfy_krea2TurboBf16.safetensors") {
		return score + 10000
	}
	if strings.Contains(value, "krea2_turbo_fp8_scaled") {
		return score + 9000
	}
	if strings.Contains(value, "turbo") {
		return score + 100
	}
	return score
}

func findGenerationPreset(presets []generationPreset, id, templateID string) (generationPreset, bool) {
	for _, preset := range presets {
		if preset.ID == id && preset.TemplateID == templateID {
			return preset, true
		}
	}
	return generationPreset{}, false
}

func missingKreaDependencies(encoder, vae, lora string) string {
	missing := make([]string, 0, 3)
	if encoder == "" {
		missing = append(missing, "Qwen3-VL 4B text encoder")
	}
	if vae == "" {
		missing = append(missing, "qwen_image_vae.safetensors")
	}
	if lora == "" {
		missing = append(missing, "lenovo_krea2.safetensors")
	}
	return "Не установлено: " + strings.Join(missing, ", ")
}

func comfyChoiceStrings(info map[string]comfyNodeInfo, nodeName, inputName string) []string {
	node, ok := info[nodeName]
	if !ok {
		return nil
	}
	raw, ok := node.Input.Required[inputName]
	if !ok {
		return nil
	}
	var choices []json.RawMessage
	if json.Unmarshal(raw, &choices) != nil || len(choices) == 0 {
		return nil
	}
	var values []string
	if json.Unmarshal(choices[0], &values) != nil {
		return nil
	}
	values = uniqueNonEmptyStrings(values)
	sort.Strings(values)
	return values
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func generationModelID(family, name string) string {
	return family + ":" + base64.RawURLEncoding.EncodeToString([]byte(name))
}

func generationModelDisplayName(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".safetensors")
	return strings.ReplaceAll(name, "\\", " / ")
}

func firstMatchingModel(values []string, matches ...func(string) bool) string {
	for _, match := range matches {
		for _, value := range values {
			if match(value) {
				return value
			}
		}
	}
	return ""
}

func exactModel(values []string, wanted string) string {
	return firstMatchingModel(values, func(value string) bool { return strings.EqualFold(value, wanted) })
}

func missingGenerationDependencies(encoder, vae, encoderName, vaeName string) string {
	missing := make([]string, 0, 2)
	if encoder == "" {
		missing = append(missing, encoderName)
	}
	if vae == "" {
		missing = append(missing, vaeName)
	}
	return "Не установлено: " + strings.Join(missing, ", ")
}

func missingMiniMaxH3Dependencies(encoder, videoVAE, audioVAE, referenceModel string) string {
	missing := make([]string, 0, 4)
	if encoder == "" {
		missing = append(missing, "MiniMax H3 Qwen3-VL encoder")
	}
	if videoVAE == "" {
		missing = append(missing, "MiniMax H3 video VAE")
	}
	if audioVAE == "" {
		missing = append(missing, "MiniMax H3 audio VAE")
	}
	if referenceModel == "" {
		missing = append(missing, "MiniMax H3 reference model")
	}
	if len(missing) == 0 {
		return "Не установлены обязательные ноды MiniMax H3 v5"
	}
	return "Не установлено: " + strings.Join(missing, ", ")
}
