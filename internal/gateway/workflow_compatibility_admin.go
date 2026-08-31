package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	workflowCompatibilityOK          = "ok"
	workflowCompatibilityErrorStatus = "error"
	workflowCompatibilityUnavailable = "unavailable"
)

type workflowCompatibilityResult struct {
	ID          string
	Scenario    string
	Description string
	Family      string
	Model       string
	Status      string
	NodeCount   int
	Issues      []workflowCompatibilityIssue
}

type workflowCompatibilityReport struct {
	GeneratedAt time.Time
	Snapshot    comfyObjectInfoSnapshot
	SourceLabel string
	Fingerprint string
	NodeCount   int
	Stale       bool
	Results     []workflowCompatibilityResult
	Compatible  int
	Failed      int
	Unavailable int
	Error       string
}

type workflowCompatibilityScenario struct {
	ID          string
	Name        string
	Description string
	Definition  workflowDefinition
	Input       generationForm
}

func (a *App) currentWorkflowCompatibility(ctx context.Context, force bool) workflowCompatibilityReport {
	report := workflowCompatibilityReport{GeneratedAt: time.Now().UTC()}
	snapshot, err := a.comfyObjectInfo(ctx, force)
	if err != nil {
		report.Error = err.Error()
		report.Snapshot = snapshot
		return report
	}
	report.Snapshot = snapshot
	report.SourceLabel = snapshot.sourceLabel()
	report.Fingerprint = shortFingerprint(snapshot.Fingerprint)
	report.NodeCount = len(snapshot.Schema.Nodes)
	report.Stale = snapshot.Source == comfyObjectInfoLastKnownGood
	catalog := buildGenerationModelCatalog(snapshot.Info)
	catalog.ObjectInfo = snapshot
	report.Results = buildWorkflowCompatibilityResults(catalog)
	for _, result := range report.Results {
		switch result.Status {
		case workflowCompatibilityOK:
			report.Compatible++
		case workflowCompatibilityUnavailable:
			report.Unavailable++
		default:
			report.Failed++
		}
	}
	return report
}

func (a *App) handleAdminWorkflows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	force := false
	if r.Method == http.MethodPost {
		if !a.validCSRF(r) {
			http.Error(w, "доступ запрещён", http.StatusForbidden)
			return
		}
		force = true
	}
	report := a.currentWorkflowCompatibility(r.Context(), force)
	message := ""
	if force {
		message = report.updateSummary()
	}
	a.render(w, r, "admin_workflows", map[string]any{
		"Title": "Совместимость workflow", "Report": report, "Message": message,
	})
}

func buildWorkflowCompatibilityResults(catalog generationModelCatalog) []workflowCompatibilityResult {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		return []workflowCompatibilityResult{{
			ID: "definitions", Scenario: "Встроенные workflow", Status: workflowCompatibilityErrorStatus,
			Issues: []workflowCompatibilityIssue{{Level: "error", Code: "definitions_unavailable", Label: "Шаблоны Gateway", Message: err.Error()}},
		}}
	}
	byID := make(map[string]workflowDefinition, len(definitions))
	for _, definition := range definitions {
		byID[definition.ID] = definition
	}

	results := make([]workflowCompatibilityResult, 0)
	for _, group := range catalog.Groups {
		for _, model := range group.Models {
			if model.Family != modelFamilyKrea2 && model.Family != modelFamilyFlux2 && model.Family != modelFamilyMiniMaxH3 {
				continue
			}
			if !model.Available {
				results = append(results, workflowCompatibilityResult{
					ID: "model:" + model.ID, Scenario: "Готовность модели", Description: model.Reason,
					Family: generationFamilyLabel(model.Family), Model: model.DisplayName, Status: workflowCompatibilityUnavailable,
					Issues: []workflowCompatibilityIssue{{Level: "warning", Code: "model_unavailable", Label: "Зависимости модели", Message: model.Reason}},
				})
				continue
			}
			for _, scenario := range compatibilityScenariosForModel(model, catalog, byID) {
				result := workflowCompatibilityResult{
					ID: scenario.ID, Scenario: scenario.Name, Description: scenario.Description,
					Family: generationFamilyLabel(model.Family), Model: model.DisplayName, Status: workflowCompatibilityOK,
				}
				prompt, buildErr := scenario.Definition.buildPrompt(scenario.Input)
				if buildErr != nil {
					result.Status = workflowCompatibilityErrorStatus
					result.Issues = []workflowCompatibilityIssue{{Level: "error", Code: "build_failed", Label: "Сборка workflow", Message: buildErr.Error()}}
				} else {
					result.NodeCount = len(prompt)
					result.Issues = validateComfyPrompt(catalog.ObjectInfo.Schema, prompt)
					if len(result.Issues) > 0 {
						result.Status = workflowCompatibilityErrorStatus
					}
				}
				results = append(results, result)
			}
		}
	}
	if len(results) == 0 {
		results = append(results, workflowCompatibilityResult{
			ID: "models", Scenario: "Быстрая генерация", Status: workflowCompatibilityUnavailable,
			Issues: []workflowCompatibilityIssue{{Level: "warning", Code: "models_unavailable", Label: "Модели", Message: "В каталоге ComfyUI не найдены поддерживаемые модели быстрой генерации."}},
		})
	}
	sort.SliceStable(results, func(left, right int) bool {
		if results[left].Status != results[right].Status {
			return compatibilityStatusOrder(results[left].Status) < compatibilityStatusOrder(results[right].Status)
		}
		if results[left].Family != results[right].Family {
			return results[left].Family < results[right].Family
		}
		if results[left].Model != results[right].Model {
			return results[left].Model < results[right].Model
		}
		return results[left].Scenario < results[right].Scenario
	})
	return results
}

func compatibilityStatusOrder(status string) int {
	switch status {
	case workflowCompatibilityErrorStatus:
		return 0
	case workflowCompatibilityUnavailable:
		return 1
	default:
		return 2
	}
}

func compatibilityScenariosForModel(model generationModel, catalog generationModelCatalog, definitions map[string]workflowDefinition) []workflowCompatibilityScenario {
	input := compatibilityBaseInput(model)
	modelKey := strings.ReplaceAll(model.ID, "\\", "/")
	scenarios := make([]workflowCompatibilityScenario, 0, 5)
	switch model.Family {
	case modelFamilyKrea2:
		textInput := input
		textInput.BaseMegapixels = 1
		textInput.OutputMegapixels = 1.9
		textInput.UpscaleSteps = 4
		textInput.DetailSteps = 8
		textInput.DetailCFG = 1
		textInput.UpscaleDenoise = 0.15
		textInput.DetailDenoise = 0.1
		textInput.UpscaleSampler = "euler"
		textInput.DetailSampler = "euler"
		textInput.DetailScheduler = "simple"
		textInput.ColorMethod = "reinhard_lab"
		textInput.ColorMode = "per_frame"
		textInput.ColorStrength = 1
		if model.Lora != "" {
			textInput.LoraNames[0] = model.Lora
			textInput.LoraModel[0] = model.LoraStrength
			textInput.LoraClip[0] = 1
		}
		scenarios = append(scenarios, workflowCompatibilityScenario{
			ID: "krea-text:" + modelKey, Name: "Текст в изображение", Description: "PhotoFlow Krea2 с апскейлом и детализацией.",
			Definition: definitions["text-to-image-krea2"], Input: textInput,
		})
		if model.SupportsImage {
			editInput := input
			editInput.InputImage = "gateway-compatibility/source.png"
			editInput.ReferenceImages[0] = "gateway-compatibility/reference.png"
			editInput.ReferenceBoost = 1
			editInput.GroundingPixels = 768
			editInput.UpscaleFactor = 1.5
			editInput.UpscaleSteps = 4
			editInput.UpscaleDenoise = 0.15
			editInput.UpscaleSampler = "deis"
			editInput.UpscaleScheduler = "simple"
			editInput.IdentityLora = model.IdentityLora
			scenarios = append(scenarios, workflowCompatibilityScenario{
				ID: "krea-edit:" + modelKey, Name: "Фото и промт", Description: "Krea2 Identity Edit с двумя изображениями и финишной обработкой.",
				Definition: definitions["image-to-image-krea2"], Input: editInput,
			})
		}
	case modelFamilyFlux2:
		textInput := input
		textInput.FluxGuidance = 3
		scenarios = append(scenarios, workflowCompatibilityScenario{
			ID: "flux-text:" + modelKey, Name: "Текстовый граф", Description: "Базовый Flux2 text-to-image workflow Gateway.",
			Definition: definitions["text-to-image-flux2"], Input: textInput,
		})
		editInput := input
		editInput.InputImage = "gateway-compatibility/source.png"
		editInput.ReferenceImages = [3]string{"gateway-compatibility/reference-2.png", "gateway-compatibility/reference-3.png", "gateway-compatibility/reference-4.png"}
		editInput.SourceMegapixels = 1
		editInput.FluxGuidance = 3
		editInput.FluxDetailerSteps = 8
		editInput.FluxActiveScale = 1
		editInput.FluxTokenWhiten = 0
		editInput.FluxNormEqualize = 0.5
		editInput.FluxUpscaleMode = "none"
		scenarios = append(scenarios, workflowCompatibilityScenario{
			ID: "flux-edit:" + modelKey, Name: "Фото и промт", Description: "Flux2 Edit с четырьмя изображениями и полным conditioning.",
			Definition: definitions["image-to-image-flux2"], Input: editInput,
		})
	case modelFamilyMiniMaxH3:
		definition := definitions["minimax-h3-video"]
		if !model.VideoReferenceOnly {
			frames := compatibilityMiniMaxInput(input)
			frames.VideoMode = miniMaxH3FrameMode
			frames.InputImage = "gateway-compatibility/first.png"
			frames.ReferenceImages[0] = "gateway-compatibility/last.png"
			scenarios = append(scenarios, workflowCompatibilityScenario{
				ID: "minimax-frames:" + modelKey, Name: "Первый и последний кадр", Description: "FL2VA: два ключевых кадра с автоматическим размером референса.",
				Definition: definition, Input: frames,
			})
		}

		references := compatibilityMiniMaxInput(input)
		references.VideoMode = miniMaxH3ReferenceMode
		references.InputImage = "gateway-compatibility/reference-1.png"
		references.ReferenceImages = [3]string{"gateway-compatibility/reference-2.png", "gateway-compatibility/reference-3.png", "gateway-compatibility/reference-4.png"}
		references.InputAudio = "gateway-compatibility/voice.mp3"
		references.InputVideo = "gateway-compatibility/motion.mp4"
		references.VideoReferenceAudio = true
		if lora, ok := firstCompatibilityLora(catalog.MiniMaxLoraGroups); ok {
			references.LoraNames[0] = lora.Name
			references.LoraModel[0] = lora.DefaultStrength
			references.LoraClip[0] = lora.DefaultStrength
		}
		scenarios = append(scenarios, workflowCompatibilityScenario{
			ID: "minimax-references:" + modelKey, Name: "Свободные референсы", Description: "REF2VA: четыре фото, аудио, видео, звук видеореференса и опциональная LoRA.",
			Definition: definition, Input: references,
		})

		post := compatibilityMiniMaxInput(input)
		if model.VideoReferenceOnly {
			post.VideoMode = miniMaxH3ReferenceMode
		} else {
			post.VideoMode = miniMaxH3FrameMode
		}
		post.InputImage = "gateway-compatibility/source.png"
		post.VideoSageAttention = true
		post.VideoClearVRAM = true
		post.VideoMemoryOptimize = true
		post.VideoAIMDOEnabled = true
		post.VideoSparseAttention = true
		post.VideoRIFEEnabled = true
		post.VideoRIFEFastMode = true
		post.VideoRIFEEnsemble = true
		post.VideoRTXEnabled = true
		post.VideoColorMatch = true
		post.VideoColorStrength = 0.8
		post.VideoSharpenEnabled = true
		scenarios = append(scenarios, workflowCompatibilityScenario{
			ID: "minimax-post:" + modelKey, Name: "Полная обработка видео", Description: "SageAttention, Memory/AIMDO/Sparse, RIFE, RTX, ColorMatch и Sharpen.",
			Definition: definition, Input: post,
		})

		if !model.VideoIntegratedTurbo {
			turbo := compatibilityMiniMaxInput(input)
			turbo.VideoMode = miniMaxH3ReferenceMode
			turbo.InputImage = "gateway-compatibility/reference.png"
			turbo.VideoTurbo = true
			turbo.VideoSteps = 6
			scenarios = append(scenarios, workflowCompatibilityScenario{
				ID: "minimax-turbo:" + modelKey, Name: "Turbo LoRA", Description: "Опциональная ветка MiniMax H3 Turbo на шести шагах.",
				Definition: definition, Input: turbo,
			})
		}
	}
	return scenarios
}

func compatibilityBaseInput(model generationModel) generationForm {
	steps := model.DefaultSteps
	if steps < 1 {
		steps = 25
	}
	cfg := model.DefaultCFG
	if cfg < 1 {
		cfg = 1
	}
	sampler := model.DefaultSampler
	if sampler == "" {
		sampler = "euler"
	}
	scheduler := model.DefaultScheduler
	if scheduler == "" {
		scheduler = "normal"
	}
	return generationForm{
		ModelID: model.ID, ModelName: model.Name, ModelFamily: model.Family, TextEncoder: model.TextEncoder,
		VAE: model.VAE, AudioVAE: model.AudioVAE, ReferenceModel: model.ReferenceModel, Lora: model.Lora,
		LoraStrength: model.LoraStrength, IdentityLora: model.IdentityLora, VideoIntegratedTurbo: model.VideoIntegratedTurbo,
		VideoReferenceOnly: model.VideoReferenceOnly, Positive: "A compatibility test scene with natural motion and clear lighting.",
		Width: 1024, Height: 1024, OutputMegapixels: 1, DimensionMultiple: 16, MaxLongestSide: 4096,
		Steps: steps, CFG: cfg, Denoise: 1, Sampler: sampler, Scheduler: scheduler, Seed: 42,
	}
}

func compatibilityMiniMaxInput(input generationForm) generationForm {
	input.Width = 768
	input.Height = 1344
	input.OutputMegapixels = float64(input.Width*input.Height) / (1024 * 1024)
	input.VideoQuality = 480
	input.VideoDurationSeconds = 5
	input.VideoSteps = input.Steps
	input.VideoSampler = input.Sampler
	input.VideoScheduler = input.Scheduler
	input.VideoShiftVideo = 11
	input.VideoShiftAudio = 3
	input.VideoReferenceDuration = 5
	input.VideoReferenceSize = "match"
	input.VideoAspect = "9:16"
	input.VideoResizeMethod = "nearest-exact"
	input.VideoProportion = "crop"
	input.VideoCropLocation = "center"
	input.VideoPadColor = "0, 0, 0"
	input.VideoMemoryMLP = "auto"
	input.VideoMemoryChunkRows = 4096
	input.VideoMemoryPrecision = "Auto"
	input.VideoMemoryQKV = "Auto"
	input.VideoMemoryAttention = "Standard"
	input.VideoAIMDOResidency = "0 blocks"
	input.VideoSparseBudget = 0.30
	input.VideoSparseSchedule = "Hold"
	input.VideoSparseEarlyKV = 0.5
	input.VideoSparseLateKV = 0.5
	input.VideoSparseBackend = "Kitchen INT8"
	input.VideoRIFECheckpoint = "rife49.pth"
	input.VideoRIFEMultiplier = 2
	input.VideoRIFEDtype = "float32"
	input.VideoRIFEBatchSize = 1
	input.VideoRTXScale = 2
	input.VideoRTXQuality = "ULTRA"
	input.VideoColorMethod = "adain"
	input.VideoSharpenMethod = "rcas"
	input.VideoSharpenStrength = 0.8
	input.VideoSharpenRadius = 1
	input.VideoSharpenThreshold = 0.05
	input.VideoSharpenIterations = 10
	input.VideoFilename = "AI-Gateway-Compatibility"
	input.VideoOutputCRF = 19
	return input
}

func firstCompatibilityLora(groups []generationLoraGroup) (generationLora, bool) {
	for _, group := range groups {
		if len(group.Loras) > 0 {
			return group.Loras[0], true
		}
	}
	return generationLora{}, false
}

func generationFamilyLabel(family string) string {
	switch family {
	case modelFamilyKrea2:
		return "Krea2"
	case modelFamilyFlux2:
		return "Flux2"
	case modelFamilyMiniMaxH3:
		return "MiniMax H3 v4"
	default:
		return family
	}
}

func (report workflowCompatibilityReport) updateSummary() string {
	if report.Error != "" {
		return "Проверка workflow после обновления не выполнена: " + report.Error
	}
	if report.Snapshot.Source == comfyObjectInfoLastKnownGood {
		return "Новый каталог нод пока недоступен; матрица использует последний рабочий снимок."
	}
	if report.Failed > 0 || report.Unavailable > 0 {
		return fmt.Sprintf("Проверка workflow: совместимо %d, ошибок %d, недоступно %d.", report.Compatible, report.Failed, report.Unavailable)
	}
	return fmt.Sprintf("Проверка workflow завершена: все %d сценариев совместимы.", report.Compatible)
}
