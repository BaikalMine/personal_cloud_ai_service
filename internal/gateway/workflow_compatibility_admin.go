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
		textManifest := krea2TextWorkflowManifest()
		textMode, _ := textManifest.mode("text")
		textInput := compatibilityManifestInput(model, textManifest)
		if model.Lora != "" {
			textInput.LoraNames[0] = model.Lora
			textInput.LoraModel[0] = model.LoraStrength
			textInput.LoraClip[0] = 1
		}
		scenarios = append(scenarios, workflowCompatibilityScenario{
			ID: "krea-text:" + modelKey, Name: textMode.Name, Description: textManifest.Description,
			Definition: definitions[textManifest.DefinitionID], Input: textInput,
		})
		if model.SupportsImage {
			editManifest := krea2EditWorkflowManifest()
			editMode, _ := editManifest.mode("edit")
			editInput := compatibilityManifestInput(model, editManifest)
			editInput.InputImage = "gateway-compatibility/source.png"
			editInput.ReferenceImages[0] = "gateway-compatibility/reference.png"
			editInput.IdentityLora = model.IdentityLora
			scenarios = append(scenarios, workflowCompatibilityScenario{
				ID: "krea-edit:" + modelKey, Name: editMode.Name, Description: editManifest.Description,
				Definition: definitions[editManifest.DefinitionID], Input: editInput,
			})
		}
	case modelFamilyFlux2:
		textInput := input
		textInput.FluxGuidance = 3
		scenarios = append(scenarios, workflowCompatibilityScenario{
			ID: "flux-text:" + modelKey, Name: "Текстовый граф", Description: "Базовый Flux2 text-to-image workflow Gateway.",
			Definition: definitions["text-to-image-flux2"], Input: textInput,
		})
		editManifest := flux2EditWorkflowManifest()
		editMode, _ := editManifest.mode("edit")
		editInput := compatibilityManifestInput(model, editManifest)
		editInput.InputImage = "gateway-compatibility/source.png"
		editInput.ReferenceImages = [3]string{"gateway-compatibility/reference-2.png", "gateway-compatibility/reference-3.png", "gateway-compatibility/reference-4.png"}
		scenarios = append(scenarios, workflowCompatibilityScenario{
			ID: "flux-edit:" + modelKey, Name: editMode.Name, Description: editManifest.Description,
			Definition: definitions[editManifest.DefinitionID], Input: editInput,
		})
		postInput := editInput
		postInput.FluxUpscaleMode = "both"
		scenarios = append(scenarios, workflowCompatibilityScenario{
			ID: "flux-post:" + modelKey, Name: "Полная обработка Flux2", Description: "Ultimate SD Upscale, SeedVR2 и финальный LUT.",
			Definition: definitions[editManifest.DefinitionID], Input: postInput,
		})
	case modelFamilyMiniMaxH3:
		manifest := miniMaxH3WorkflowManifest()
		definition := definitions[manifest.DefinitionID]
		if !model.VideoReferenceOnly {
			mode, _ := manifest.mode(miniMaxH3FrameMode)
			frames := compatibilityMiniMaxInput(input)
			frames.VideoMode = miniMaxH3FrameMode
			frames.InputImage = "gateway-compatibility/first.png"
			frames.ReferenceImages[0] = "gateway-compatibility/last.png"
			scenarios = append(scenarios, workflowCompatibilityScenario{
				ID: "minimax-frames:" + modelKey, Name: mode.Name, Description: mode.Description,
				Definition: definition, Input: frames,
			})
		}

		mode, _ := manifest.mode(miniMaxH3ReferenceMode)
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
			ID: "minimax-references:" + modelKey, Name: mode.Name, Description: mode.Description,
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

func compatibilityManifestInput(model generationModel, manifest workflowManifest) generationForm {
	base := compatibilityBaseInput(model)
	input := base
	if err := applyWorkflowManifestDefaults(manifest, &input); err != nil {
		return base
	}
	input.TemplateID = manifest.TemplateID
	input.PresetID = manifest.ID
	input.Positive = base.Positive
	input.Steps = base.Steps
	input.CFG = base.CFG
	input.Sampler = base.Sampler
	input.Scheduler = base.Scheduler
	input.Seed = base.Seed
	return input
}

func compatibilityMiniMaxInput(input generationForm) generationForm {
	manifest := miniMaxH3WorkflowManifest()
	if err := applyWorkflowManifestDefaults(manifest, &input); err != nil {
		return input
	}
	input.Width = 768
	input.Height = 1344
	input.OutputMegapixels = float64(input.Width*input.Height) / (1024 * 1024)
	input.VideoSteps = input.Steps
	input.VideoSampler = input.Sampler
	input.VideoScheduler = input.Scheduler
	if input.VideoIntegratedTurbo {
		input.VideoShiftVideo = 12
		input.VideoShiftAudio = 7
	}
	input.VideoFilename = "AI-Gateway-Compatibility"
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
		return "MiniMax H3 v5"
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
