package gateway

import (
	"fmt"
	"math"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const workflowManifestSchemaVersion = 1

const (
	workflowParameterString  = "string"
	workflowParameterInteger = "integer"
	workflowParameterNumber  = "number"
	workflowParameterBoolean = "boolean"
	workflowParameterEnum    = "enum"
)

type workflowManifestCatalog struct {
	SchemaVersion int                `json:"schema_version"`
	Workflows     []workflowManifest `json:"workflows"`
}

// workflowManifest is the product-level contract for one quick-generation
// workflow. The executable ComfyUI graph remains in workflowDefinition; this
// contract describes how the graph is selected, configured and presented.
type workflowManifest struct {
	ID                string                           `json:"id"`
	Version           string                           `json:"version"`
	DefinitionID      string                           `json:"definition_id"`
	TemplateID        string                           `json:"template_id"`
	Family            string                           `json:"family"`
	Name              string                           `json:"name"`
	Description       string                           `json:"description"`
	ModelFamilies     []string                         `json:"model_families"`
	ModelCapabilities []string                         `json:"model_capabilities,omitempty"`
	ModeParameter     string                           `json:"mode_parameter,omitempty"`
	Modes             []workflowModeManifest           `json:"modes"`
	Inputs            []workflowInputManifest          `json:"inputs"`
	Parameters        []workflowParameterManifest      `json:"parameters"`
	Branches          []workflowBranchManifest         `json:"branches"`
	QualityProfiles   []workflowQualityProfileManifest `json:"quality_profiles"`
	PromptAssistant   workflowAssistantManifest        `json:"prompt_assistant"`
	Output            workflowOutputManifest           `json:"output"`
	RequiredClasses   []string                         `json:"required_node_classes"`
}

type workflowModeManifest struct {
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	Description     string                   `json:"description"`
	Default         bool                     `json:"default,omitempty"`
	InputLimits     map[string]workflowLimit `json:"input_limits"`
	RequiredClasses []string                 `json:"required_node_classes"`
	ModelConditions []workflowCondition      `json:"model_conditions,omitempty"`
}

type workflowLimit struct {
	Minimum int `json:"minimum"`
	Maximum int `json:"maximum"`
}

type workflowInputManifest struct {
	ID          string                      `json:"id"`
	Kind        string                      `json:"kind"`
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	FormFields  []string                    `json:"form_fields"`
	Roles       []workflowInputRoleManifest `json:"roles,omitempty"`
	MimeTypes   []string                    `json:"mime_types,omitempty"`
	MaxBytes    int64                       `json:"max_bytes,omitempty"`
}

type workflowInputRoleManifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type workflowParameterManifest struct {
	Name           string              `json:"name"`
	Target         string              `json:"-"`
	Kind           string              `json:"kind"`
	Label          string              `json:"label"`
	Description    string              `json:"description,omitempty"`
	Group          string              `json:"group"`
	Default        any                 `json:"default,omitempty"`
	Minimum        *float64            `json:"minimum,omitempty"`
	Maximum        *float64            `json:"maximum,omitempty"`
	Step           *float64            `json:"step,omitempty"`
	MaxLength      int                 `json:"max_length,omitempty"`
	Options        []workflowOption    `json:"options,omitempty"`
	Conditions     []workflowCondition `json:"conditions,omitempty"`
	InvalidMessage string              `json:"-"`
}

type workflowOption struct {
	Value any    `json:"value"`
	Name  string `json:"name"`
}

type workflowCondition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

type workflowBranchManifest struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Description     string              `json:"description"`
	ToggleParameter string              `json:"toggle_parameter,omitempty"`
	Conditions      []workflowCondition `json:"conditions,omitempty"`
	RequiredClasses []string            `json:"required_node_classes"`
	Order           int                 `json:"order"`
}

type workflowQualityProfileManifest struct {
	ID          string                                      `json:"id"`
	Name        string                                      `json:"name"`
	Description string                                      `json:"description"`
	Default     bool                                        `json:"default,omitempty"`
	Conditions  []workflowCondition                         `json:"conditions,omitempty"`
	Parameters  map[string]workflowProfileParameterManifest `json:"parameters"`
}

type workflowProfileParameterManifest struct {
	Value   any      `json:"value"`
	Minimum *float64 `json:"minimum,omitempty"`
	Maximum *float64 `json:"maximum,omitempty"`
	Locked  bool     `json:"locked,omitempty"`
}

type workflowAssistantManifest struct {
	Profile              string              `json:"profile"`
	Allowed              bool                `json:"allowed"`
	VisionReferences     bool                `json:"vision_references"`
	ReferenceIdentifiers map[string][]string `json:"reference_identifiers"`
	Rules                []string            `json:"rules"`
}

type workflowOutputManifest struct {
	Kinds          []string `json:"kinds"`
	Container      string   `json:"container,omitempty"`
	VideoCodec     string   `json:"video_codec,omitempty"`
	PixelFormat    string   `json:"pixel_format,omitempty"`
	FrameRate      int      `json:"frame_rate,omitempty"`
	GeneratedAudio bool     `json:"generated_audio,omitempty"`
	Postprocessing []string `json:"postprocessing,omitempty"`
}

func workflowManifestCatalogValue() workflowManifestCatalog {
	return workflowManifestCatalog{SchemaVersion: workflowManifestSchemaVersion, Workflows: workflowManifests()}
}

func workflowManifests() []workflowManifest {
	return []workflowManifest{
		miniMaxH3WorkflowManifest(),
		krea2TextWorkflowManifest(),
		krea2EditWorkflowManifest(),
		flux2EditWorkflowManifest(),
	}
}

func workflowManifestByID(id string) (workflowManifest, bool) {
	for _, manifest := range workflowManifests() {
		if manifest.ID == id || manifest.DefinitionID == id {
			return manifest, true
		}
	}
	return workflowManifest{}, false
}

func workflowManifestForInput(input generationForm) (workflowManifest, bool) {
	if manifest, ok := workflowManifestByID(input.PresetID); ok {
		return manifest, true
	}
	candidates := make([]workflowManifest, 0, 1)
	for _, manifest := range workflowManifests() {
		if manifest.TemplateID == input.TemplateID {
			candidates = append(candidates, manifest)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	return workflowManifest{}, false
}

func miniMaxH3WorkflowManifest() workflowManifest {
	return workflowManifest{
		ID:                "minimax-h3-video",
		Version:           "4",
		DefinitionID:      "minimax-h3-video",
		TemplateID:        "minimax-h3-video",
		Family:            modelFamilyMiniMaxH3,
		Name:              "MiniMax H3: видео",
		Description:       "Видео из текста, по ключевым кадрам или по фото-, видео- и аудиореференсам.",
		ModelFamilies:     []string{modelFamilyMiniMaxH3},
		ModelCapabilities: []string{"video_integrated_turbo", "video_reference_only"},
		ModeParameter:     "video_mode",
		RequiredClasses: []string{
			"UNETLoader", "CLIPLoader", "VAELoader", "MiniMaxH3SigmaShift", "RandomNoise",
			"BasicScheduler", "BasicGuider", "SamplerCustomAdvanced", "VAEDecode", "VAEDecodeAudio", "VHS_VideoCombine",
		},
		Modes: []workflowModeManifest{
			{
				ID: "frames", Name: "Промт и точные кадры", Default: true,
				Description:     "Текст создаёт ролик; одно фото фиксирует первый кадр, два фото фиксируют начало и финал.",
				InputLimits:     map[string]workflowLimit{"image": {Minimum: 0, Maximum: 2}, "audio": {Minimum: 0, Maximum: 0}, "video": {Minimum: 0, Maximum: 0}},
				RequiredClasses: []string{"MiniMaxH3ImageToVideo", "LCImageMaskResize"},
				ModelConditions: []workflowCondition{{Field: "video_reference_only", Operator: "equals", Value: false}},
			},
			{
				ID: "references", Name: "Промт и свободные референсы",
				Description:     "До четырёх фото, одно видео и одно аудио помогают задать внешность, объекты, движение и звук.",
				InputLimits:     map[string]workflowLimit{"image": {Minimum: 0, Maximum: 4}, "audio": {Minimum: 0, Maximum: 1}, "video": {Minimum: 0, Maximum: 1}},
				RequiredClasses: []string{"MiniMaxH3ReferenceToVideo", "LCImageMaskResize"},
			},
		},
		Inputs: []workflowInputManifest{
			{
				ID: "image", Kind: "image", Name: "Фото",
				Description: "Точные ключевые кадры в режиме FL2VA или свободные визуальные референсы в режиме REF2VA.",
				FormFields:  []string{"input_image", "input_image_2", "input_image_3", "input_image_4"},
				MimeTypes:   []string{"image/png", "image/jpeg", "image/webp"}, MaxBytes: 32 << 20,
				Roles: standardImageReferenceRoles(),
			},
			{ID: "audio", Kind: "audio", Name: "Аудиореференс", Description: "Голос, музыка или окружение для REF2VA.", FormFields: []string{"input_audio"}, MimeTypes: []string{"audio/wav", "audio/mpeg", "audio/ogg", "audio/mp4", "audio/flac"}, MaxBytes: 32 << 20},
			{ID: "video", Kind: "video", Name: "Видеореференс", Description: "Движение, сцена, камера или тайминг для REF2VA.", FormFields: []string{"input_video"}, MimeTypes: []string{"video/mp4", "video/webm", "video/quicktime", "video/x-matroska"}, MaxBytes: 512 << 20},
		},
		Parameters: miniMaxH3WorkflowParameters(),
		Branches: []workflowBranchManifest{
			{ID: "external_turbo", Name: "Turbo LoRA", Description: "Опциональная внешняя Turbo LoRA для базовой FL2VA.", ToggleParameter: "video_turbo", Conditions: []workflowCondition{{Field: "video_integrated_turbo", Operator: "equals", Value: false}}, RequiredClasses: []string{"MiniMaxH3TurboLoRA", "MiniMaxH3TurboSampler"}, Order: 10},
			{ID: "sage_attention", Name: "SageAttention", Description: "Патч внимания MiniMax H3.", ToggleParameter: "video_sage_attention", RequiredClasses: []string{"MiniMaxH3MemoryEfficientSageAttentionPatch"}, Order: 20},
			{ID: "memory_optimization", Name: "H3 Memory Optimization", Description: "Ограничение пиковых активаций и управление точностью.", ToggleParameter: "video_memory_optimize", RequiredClasses: []string{"H3MemoryOptimization"}, Order: 30},
			{ID: "aimdo", Name: "AIMDO Residency Limiter", Description: "Ограничение резидентных блоков модели.", ToggleParameter: "video_aimdo_enabled", RequiredClasses: []string{"H3AIMDOResidencyLimiter"}, Order: 40},
			{ID: "sparse_attention", Name: "H3 Sparse Attention", Description: "Разреженное внимание MiniMax H3.", ToggleParameter: "video_sparse_attention", RequiredClasses: []string{"H3SparseAttentionAdvanced"}, Order: 50},
			{ID: "clear_vram", Name: "Поэтапная очистка VRAM", Description: "Освобождение кэша между тяжёлыми стадиями.", ToggleParameter: "video_clear_vram", RequiredClasses: []string{"LCVRAMCacheClear"}, Order: 60},
			{ID: "audio_reference", Name: "Аудиореференс", Description: "Обрезка отдельного аудиореференса.", Conditions: []workflowCondition{{Field: "input_audio", Operator: "not_empty", Value: true}}, RequiredClasses: []string{"LoadAudio", "TrimAudioDuration"}, Order: 70},
			{ID: "video_reference", Name: "Видеореференс", Description: "Загрузка фрагмента видео и при необходимости его звука.", Conditions: []workflowCondition{{Field: "input_video", Operator: "not_empty", Value: true}}, RequiredClasses: []string{"VHS_LoadVideo"}, Order: 80},
			{ID: "color_match", Name: "ColorMatch", Description: "Приведение палитры к первому фото.", ToggleParameter: "video_color_match", RequiredClasses: []string{"LCColorMatch"}, Order: 100},
			{ID: "rife", Name: "RIFE", Description: "Интерполяция кадров для повышения FPS.", ToggleParameter: "video_rife_enabled", RequiredClasses: []string{"RIFE VFI"}, Order: 110},
			{ID: "rtx", Name: "RTX Super Resolution", Description: "Финальный NVIDIA-апскейл ролика.", ToggleParameter: "video_rtx_enabled", RequiredClasses: []string{"RTXVideoSuperResolution"}, Order: 120},
			{ID: "sharpen", Name: "Image Sharpen", Description: "Финальная резкость после остальных стадий.", ToggleParameter: "video_sharpen_enabled", RequiredClasses: []string{"ImageSharpenKJ"}, Order: 130},
		},
		QualityProfiles: []workflowQualityProfileManifest{
			{ID: "regular", Name: "Обычная модель", Description: "Полный проход FL2VA без внешнего Turbo.", Default: true, Conditions: []workflowCondition{{Field: "video_integrated_turbo", Operator: "equals", Value: false}, {Field: "video_turbo", Operator: "equals", Value: false}}, Parameters: map[string]workflowProfileParameterManifest{"video_steps": profileParameter(25, 20, 25, false), "video_sampler": profileValue("euler"), "video_scheduler": profileValue("simple"), "video_shift_video": profileValue(11), "video_shift_audio": profileValue(3)}},
			{ID: "turbo", Name: "Внешний Turbo", Description: "Быстрый проход FL2VA с официальной Turbo LoRA.", Conditions: []workflowCondition{{Field: "video_integrated_turbo", Operator: "equals", Value: false}, {Field: "video_turbo", Operator: "equals", Value: true}}, Parameters: map[string]workflowProfileParameterManifest{"video_steps": profileParameter(6, 4, 8, false), "video_sampler": profileLockedValue("euler"), "video_scheduler": profileValue("simple"), "video_shift_video": profileValue(11), "video_shift_audio": profileValue(3)}},
			{ID: "integrated_turbo", Name: "Eros Max", Description: "REF2VA-модель со встроенным Turbo.", Conditions: []workflowCondition{{Field: "video_integrated_turbo", Operator: "equals", Value: true}}, Parameters: map[string]workflowProfileParameterManifest{"video_mode": profileLockedValue("references"), "video_steps": profileParameter(8, 6, 8, false), "video_sampler": profileLockedValue("euler"), "video_scheduler": profileValue("simple"), "video_shift_video": profileValue(12), "video_shift_audio": profileValue(7)}},
		},
		PromptAssistant: workflowAssistantManifest{
			Profile: "minimax-h3", Allowed: true, VisionReferences: true,
			ReferenceIdentifiers: map[string][]string{"frames": {"<Picture 1>", "<Picture 2>"}, "references": {"<Picture 1>", "<Picture 2>", "<Picture 3>", "<Picture 4>", "<Audio 1>", "<Video 1>"}},
			Rules: []string{
				"Сначала проанализировать все переданные изображения и сохранить назначенный порядок.",
				"В режиме frames описывать Picture 1 как точный первый кадр, а Picture 2 как точный последний кадр.",
				"В режиме references явно связывать внешность, одежду, предметы, движение и звук с соответствующими идентификаторами.",
				"Вернуть один готовый к запуску английский Context-IR без отдельного negative prompt.",
			},
		},
		Output: workflowOutputManifest{Kinds: []string{"video", "audio"}, Container: "mp4", VideoCodec: "h264", PixelFormat: "yuv420p", FrameRate: miniMaxH3VideoFPS, GeneratedAudio: true, Postprocessing: []string{"color_match", "rife", "rtx", "sharpen"}},
	}
}

func miniMaxH3WorkflowParameters() []workflowParameterManifest {
	return []workflowParameterManifest{
		enumWorkflowParameter("video_mode", "VideoMode", "Режим", "source", "frames", option("frames", "Промт и точные кадры"), option("references", "Промт и свободные референсы")),
		enumWorkflowParameter("video_quality", "VideoQuality", "Качество", "source", 720, option(480, "480p"), option(720, "720p"), option(1080, "1080p"), option(1440, "1440p")),
		integerWorkflowParameter("video_duration_seconds", "VideoDurationSeconds", "Длительность", "source", 5, 5, 60, 1),
		stringWorkflowParameter("video_filename", "VideoFilename", "Имя файла", "source", "AI-Gateway-MiniMaxH3", 80),
		booleanWorkflowParameter("video_turbo", "VideoTurbo", "Turbo LoRA", "generation", false),
		enumWorkflowParameter("video_reference_size", "VideoReferenceSize", "Размер референсов", "source", "match", option("match", "По размеру кадра"), option("max", "Максимальная детализация")),
		enumWorkflowParameter("video_aspect", "VideoAspect", "Формат без первого фото", "source", "9:16",
			option("1:1", "1:1"), option("4:5", "4:5"), option("16:9", "16:9"), option("9:16", "9:16"), option("4:1", "4:1"), option("2:3", "2:3"), option("3:2", "3:2"), option("3:4", "3:4"), option("4:3", "4:3"), option("21:9", "21:9")),
		booleanWorkflowParameter("video_use_source_aspect", "VideoUseSourceAspect", "Использовать пропорции первого фото", "source", true),
		booleanWorkflowParameter("video_swap_dimensions", "VideoSwapDimensions", "Поменять ширину и высоту", "source", false),
		numberWorkflowParameter("video_reference_start", "VideoReferenceStart", "Начало видеореференса", "source", 0, 0, 600, 0.1),
		integerWorkflowParameter("video_reference_duration", "VideoReferenceDuration", "Длительность видеореференса", "source", 5, 1, 15, 1),
		booleanWorkflowParameter("video_reference_audio", "VideoReferenceAudio", "Использовать звук видеореференса", "source", false),
		integerWorkflowParameter("video_steps", "VideoSteps", "Шаги", "generation", 25, 4, 25, 1),
		enumWorkflowParameter("video_sampler", "VideoSampler", "Сэмплер", "generation", "euler", option("euler", "Euler"), option("res_multistep", "Res Multistep")),
		enumWorkflowParameter("video_scheduler", "VideoScheduler", "Планировщик", "generation", "simple", option("simple", "Simple"), option("sgm_uniform", "SGM Uniform"), option("karras", "Karras"), option("exponential", "Exponential"), option("beta", "Beta"), option("normal", "Normal")),
		integerWorkflowParameter("video_shift_video", "VideoShiftVideo", "Video Sigma", "generation", 11, 1, 32, 1),
		integerWorkflowParameter("video_shift_audio", "VideoShiftAudio", "Audio Sigma", "generation", 3, 1, 32, 1),
		booleanWorkflowParameter("video_sage_attention", "VideoSageAttention", "SageAttention", "optimization", true),
		booleanWorkflowParameter("video_clear_vram", "VideoClearVRAM", "Поэтапно освобождать VRAM", "optimization", true),
		enumWorkflowParameter("video_resize_method", "VideoResizeMethod", "Масштабирование", "source", "nearest-exact", option("nearest-exact", "Nearest exact"), option("lanczos", "Lanczos"), option("bicubic", "Bicubic"), option("bilinear", "Bilinear"), option("area", "Area"), option("nvidia_rtx_vsr", "NVIDIA RTX VSR")),
		enumWorkflowParameter("video_proportion", "VideoProportion", "Вписывание", "source", "crop", option("crop", "Заполнить и обрезать"), option("pad", "Вписать с полями"), option("resize", "Вписать без холста"), option("stretch", "Растянуть"), option("total_pixels", "Сохранить бюджет пикселей")),
		enumWorkflowParameter("video_crop_location", "VideoCropLocation", "Позиция обрезки", "source", "center", option("center", "По центру"), option("top", "Сверху"), option("bottom", "Снизу"), option("left", "Слева"), option("right", "Справа")),
		stringWorkflowParameter("video_pad_color", "VideoPadColor", "Цвет полей", "source", "0, 0, 0", 32),
		booleanWorkflowParameter("video_memory_optimize", "VideoMemoryOptimize", "H3 Memory Optimization", "optimization", true),
		enumWorkflowParameter("video_memory_mlp", "VideoMemoryMLP", "MLP память", "optimization", "auto", option("auto", "Auto"), option("off", "Off")),
		enumWorkflowParameter("video_memory_precision", "VideoMemoryPrecision", "Точность памяти", "optimization", "Auto", option("Auto", "Auto"), option("BF16", "BF16"), option("Preserve native", "Preserve native"), option("Force quant", "Force quant")),
		enumWorkflowParameter("video_memory_qkv", "VideoMemoryQKV", "QKV streaming", "optimization", "Auto", option("Off", "Off"), option("Auto", "Auto"), option("Forced", "Forced")),
		enumWorkflowParameter("video_memory_attention", "VideoMemoryAttention", "Режим внимания", "optimization", "Standard", option("Standard", "Standard"), option("Lower VRAM (slower)", "Lower VRAM")),
		integerWorkflowParameter("video_memory_chunk_rows", "VideoMemoryChunkRows", "Строк в блоке", "optimization", 4096, 256, 65536, 256),
		booleanWorkflowParameter("video_aimdo_enabled", "VideoAIMDOEnabled", "AIMDO Residency Limiter", "optimization", true),
		enumWorkflowParameter("video_aimdo_residency", "VideoAIMDOResidency", "Резидентность модели", "optimization", "0 blocks", option("stock", "Stock"), option("0 blocks", "0 блоков"), option("1 block", "1 блок"), option("2 blocks", "2 блока"), option("4 blocks", "4 блока")),
		booleanWorkflowParameter("video_sparse_attention", "VideoSparseAttention", "H3 Sparse Attention", "optimization", true),
		numberWorkflowParameter("video_sparse_budget", "VideoSparseBudget", "Бюджет видео", "optimization", 0.30, 0.01, 1, 0.01),
		enumWorkflowParameter("video_sparse_backend", "VideoSparseBackend", "Sparse backend", "optimization", "Kitchen INT8", option("Kitchen INT8", "Kitchen INT8"), option("FROST BF16 (SM89)", "FROST BF16 (SM89)"), option("Sparse Sage", "Sparse Sage"), option("BF16 Triton", "BF16 Triton"), option("FP8 FlexAttention", "FP8 FlexAttention")),
		enumWorkflowParameter("video_sparse_early_schedule", "VideoSparseSchedule", "Раннее расписание", "optimization", "Hold", option("Hold", "Hold"), option("Ramp", "Ramp")),
		integerWorkflowParameter("video_sparse_early_steps", "VideoSparseEarlyStep", "Ранние шаги", "optimization", 2, 0, 1000, 1),
		numberWorkflowParameter("video_sparse_early_kv", "VideoSparseEarlyKV", "Ранний KV", "optimization", 0.5, 0.01, 1, 0.01),
		integerWorkflowParameter("video_sparse_late_steps", "VideoSparseLateStep", "Финальные шаги", "optimization", 2, 0, 1000, 1),
		numberWorkflowParameter("video_sparse_late_kv", "VideoSparseLateKV", "Финальный KV", "optimization", 0.5, 0.01, 1, 0.01),
		booleanWorkflowParameter("video_rife_enabled", "VideoRIFEEnabled", "Плавность движения", "postprocessing", false),
		enumWorkflowParameter("video_rife_checkpoint", "VideoRIFECheckpoint", "Модель RIFE", "postprocessing", "rife49.pth", option("rife49.pth", "rife49"), option("rife47.pth", "rife47"), option("rife417.pth", "rife417"), option("rife426.pth", "rife426"), option("sudo_rife4_269.662_testV1_scale1.pth", "sudo_rife4_269")),
		enumWorkflowParameter("video_rife_multiplier", "VideoRIFEMultiplier", "Частота кадров", "postprocessing", 2, option(2, "2x"), option(3, "3x"), option(4, "4x")),
		enumWorkflowParameter("video_rife_dtype", "VideoRIFEDtype", "Точность RIFE", "postprocessing", "float32", option("float32", "float32"), option("float16", "float16"), option("bfloat16", "bfloat16")),
		integerWorkflowParameter("video_rife_batch_size", "VideoRIFEBatchSize", "Размер пакета RIFE", "postprocessing", 1, 1, 4, 1),
		booleanWorkflowParameter("video_rife_fast_mode", "VideoRIFEFastMode", "Быстрый режим RIFE", "postprocessing", true),
		booleanWorkflowParameter("video_rife_ensemble", "VideoRIFEEnsemble", "RIFE Ensemble", "postprocessing", true),
		booleanWorkflowParameter("video_rife_compile", "VideoRIFECompile", "Компиляция RIFE", "postprocessing", true),
		booleanWorkflowParameter("video_rtx_enabled", "VideoRTXEnabled", "Финальный RTX апскейл", "postprocessing", false),
		numberWorkflowParameter("video_rtx_scale", "VideoRTXScale", "Масштаб RTX", "postprocessing", 2, 1, 4, 0.1),
		enumWorkflowParameter("video_rtx_quality", "VideoRTXQuality", "Качество RTX", "postprocessing", "ULTRA", option("LOW", "Low"), option("MEDIUM", "Medium"), option("HIGH", "High"), option("ULTRA", "Ultra")),
		booleanWorkflowParameter("video_color_match", "VideoColorMatch", "Палитра референса", "postprocessing", false),
		enumWorkflowParameter("video_color_method", "VideoColorMethod", "Метод ColorMatch", "postprocessing", "adain", option("adain", "AdaIN"), option("mean_std", "Mean / Std")),
		numberWorkflowParameter("video_color_strength", "VideoColorStrength", "Сила ColorMatch", "postprocessing", 1, 0, 1, 0.05),
		booleanWorkflowParameter("video_sharpen_enabled", "VideoSharpenEnabled", "Финальная резкость", "postprocessing", false),
		enumWorkflowParameter("video_sharpen_method", "VideoSharpenMethod", "Метод резкости", "postprocessing", "rcas", option("rcas", "RCAS"), option("adaptive_usm", "Adaptive USM"), option("high_pass", "High Pass"), option("deconvolution", "Deconvolution")),
		numberWorkflowParameter("video_sharpen_strength", "VideoSharpenStrength", "Сила резкости", "postprocessing", 0.8, 0, 3, 0.01),
		numberWorkflowParameter("video_sharpen_radius", "VideoSharpenRadius", "Радиус резкости", "postprocessing", 1, 0.5, 5, 0.1),
		numberWorkflowParameter("video_sharpen_threshold", "VideoSharpenThreshold", "Порог шума", "postprocessing", 0.05, 0, 1, 0.01),
		integerWorkflowParameter("video_sharpen_iterations", "VideoSharpenIterations", "Итерации резкости", "postprocessing", 10, 1, 100, 1),
		numberWorkflowParameter("video_audio_start", "VideoAudioStart", "Смещение аудио", "output", 0.03, -60, 60, 0.01),
		integerWorkflowParameter("video_output_crf", "VideoOutputCRF", "Качество H.264", "output", 19, 0, 51, 1),
	}
}

func option(value any, name string) workflowOption {
	return workflowOption{Value: value, Name: name}
}

func profileValue(value any) workflowProfileParameterManifest {
	return workflowProfileParameterManifest{Value: value}
}

func profileLockedValue(value any) workflowProfileParameterManifest {
	return workflowProfileParameterManifest{Value: value, Locked: true}
}

func profileParameter(value any, minimum, maximum float64, locked bool) workflowProfileParameterManifest {
	return workflowProfileParameterManifest{Value: value, Minimum: workflowNumber(minimum), Maximum: workflowNumber(maximum), Locked: locked}
}

func enumWorkflowParameter(name, target, label, group string, fallback any, options ...workflowOption) workflowParameterManifest {
	return workflowParameterManifest{Name: name, Target: target, Kind: workflowParameterEnum, Label: label, Group: group, Default: fallback, Options: options, InvalidMessage: "некорректное значение: " + label}
}

func stringWorkflowParameter(name, target, label, group, fallback string, maxLength int) workflowParameterManifest {
	return workflowParameterManifest{Name: name, Target: target, Kind: workflowParameterString, Label: label, Group: group, Default: fallback, MaxLength: maxLength, InvalidMessage: "некорректное значение: " + label}
}

func booleanWorkflowParameter(name, target, label, group string, fallback bool) workflowParameterManifest {
	return workflowParameterManifest{Name: name, Target: target, Kind: workflowParameterBoolean, Label: label, Group: group, Default: fallback, InvalidMessage: "некорректное значение: " + label}
}

func integerWorkflowParameter(name, target, label, group string, fallback int, minimum, maximum, step float64) workflowParameterManifest {
	return workflowParameterManifest{Name: name, Target: target, Kind: workflowParameterInteger, Label: label, Group: group, Default: fallback, Minimum: workflowNumber(minimum), Maximum: workflowNumber(maximum), Step: workflowNumber(step), InvalidMessage: "некорректное значение: " + label}
}

func numberWorkflowParameter(name, target, label, group string, fallback, minimum, maximum, step float64) workflowParameterManifest {
	return workflowParameterManifest{Name: name, Target: target, Kind: workflowParameterNumber, Label: label, Group: group, Default: fallback, Minimum: workflowNumber(minimum), Maximum: workflowNumber(maximum), Step: workflowNumber(step), InvalidMessage: "некорректное значение: " + label}
}

func workflowNumber(value float64) *float64 {
	return &value
}

func validateWorkflowManifests(manifests []workflowManifest) error {
	if len(manifests) == 0 {
		return fmt.Errorf("workflow manifest catalog is empty")
	}
	manifestIDs := make(map[string]struct{}, len(manifests))
	definitionIDs := make(map[string]struct{}, len(manifests))
	for _, manifest := range manifests {
		if strings.TrimSpace(manifest.ID) == "" || strings.TrimSpace(manifest.Version) == "" || strings.TrimSpace(manifest.DefinitionID) == "" || strings.TrimSpace(manifest.TemplateID) == "" || strings.TrimSpace(manifest.Family) == "" {
			return fmt.Errorf("workflow manifest is incomplete: %q", manifest.ID)
		}
		if _, exists := manifestIDs[manifest.ID]; exists {
			return fmt.Errorf("duplicate workflow manifest id %q", manifest.ID)
		}
		manifestIDs[manifest.ID] = struct{}{}
		if _, exists := definitionIDs[manifest.DefinitionID]; exists {
			return fmt.Errorf("duplicate workflow definition id %q", manifest.DefinitionID)
		}
		definitionIDs[manifest.DefinitionID] = struct{}{}
		if err := validateWorkflowManifest(manifest); err != nil {
			return fmt.Errorf("workflow manifest %s: %w", manifest.ID, err)
		}
	}
	return nil
}

func validateWorkflowManifest(manifest workflowManifest) error {
	modeIDs := make(map[string]struct{}, len(manifest.Modes))
	defaultModes := 0
	for _, mode := range manifest.Modes {
		if strings.TrimSpace(mode.ID) == "" || strings.TrimSpace(mode.Name) == "" {
			return fmt.Errorf("mode is incomplete")
		}
		if _, exists := modeIDs[mode.ID]; exists {
			return fmt.Errorf("duplicate mode %q", mode.ID)
		}
		modeIDs[mode.ID] = struct{}{}
		if mode.Default {
			defaultModes++
		}
		for kind, limit := range mode.InputLimits {
			if limit.Minimum < 0 || limit.Maximum < limit.Minimum {
				return fmt.Errorf("mode %q has invalid %s limits", mode.ID, kind)
			}
		}
	}
	if len(manifest.Modes) > 0 && defaultModes != 1 {
		return fmt.Errorf("exactly one default mode is required")
	}
	inputIDs := make(map[string]struct{}, len(manifest.Inputs))
	knownConditions := map[string]struct{}{"lora_count": {}}
	for _, capability := range manifest.ModelCapabilities {
		knownConditions[capability] = struct{}{}
	}
	for _, input := range manifest.Inputs {
		if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Kind) == "" || strings.TrimSpace(input.Name) == "" || len(input.FormFields) == 0 {
			return fmt.Errorf("input is incomplete")
		}
		if _, exists := inputIDs[input.ID]; exists {
			return fmt.Errorf("duplicate input %q", input.ID)
		}
		inputIDs[input.ID] = struct{}{}
		knownConditions[input.ID] = struct{}{}
		for _, field := range input.FormFields {
			knownConditions[field] = struct{}{}
		}
	}
	formType := reflect.TypeOf(generationForm{})
	parameterNames := make(map[string]workflowParameterManifest, len(manifest.Parameters))
	for _, parameter := range manifest.Parameters {
		if strings.TrimSpace(parameter.Name) == "" || strings.TrimSpace(parameter.Target) == "" || strings.TrimSpace(parameter.Label) == "" {
			return fmt.Errorf("parameter is incomplete")
		}
		if _, exists := parameterNames[parameter.Name]; exists {
			return fmt.Errorf("duplicate parameter %q", parameter.Name)
		}
		parameterNames[parameter.Name] = parameter
		knownConditions[parameter.Name] = struct{}{}
		field, exists := formType.FieldByName(parameter.Target)
		if !exists {
			return fmt.Errorf("parameter %q targets unknown generationForm field %q", parameter.Name, parameter.Target)
		}
		if !workflowParameterKindMatchesField(parameter.Kind, field.Type.Kind()) {
			return fmt.Errorf("parameter %q kind %q does not match %s", parameter.Name, parameter.Kind, field.Type)
		}
		if parameter.Minimum != nil && parameter.Maximum != nil && *parameter.Minimum > *parameter.Maximum {
			return fmt.Errorf("parameter %q has inverted range", parameter.Name)
		}
		if parameter.Kind == workflowParameterEnum && len(parameter.Options) == 0 {
			return fmt.Errorf("enum parameter %q has no options", parameter.Name)
		}
	}
	if manifest.ModeParameter != "" {
		parameter, exists := parameterNames[manifest.ModeParameter]
		if !exists || parameter.Kind != workflowParameterEnum {
			return fmt.Errorf("mode selector %q is not an enum parameter", manifest.ModeParameter)
		}
		for modeID := range modeIDs {
			if !workflowManifestValueAllowed(parameter, modeID) {
				return fmt.Errorf("mode selector %q does not allow mode %q", manifest.ModeParameter, modeID)
			}
		}
	}
	for _, parameter := range manifest.Parameters {
		if err := validateWorkflowConditions(parameter.Conditions, knownConditions); err != nil {
			return fmt.Errorf("parameter %q: %w", parameter.Name, err)
		}
	}
	for _, mode := range manifest.Modes {
		if err := validateWorkflowConditions(mode.ModelConditions, knownConditions); err != nil {
			return fmt.Errorf("mode %q: %w", mode.ID, err)
		}
	}
	branchIDs := make(map[string]struct{}, len(manifest.Branches))
	for _, branch := range manifest.Branches {
		if strings.TrimSpace(branch.ID) == "" || strings.TrimSpace(branch.Name) == "" || len(branch.RequiredClasses) == 0 {
			return fmt.Errorf("branch is incomplete")
		}
		if _, exists := branchIDs[branch.ID]; exists {
			return fmt.Errorf("duplicate branch %q", branch.ID)
		}
		branchIDs[branch.ID] = struct{}{}
		if branch.ToggleParameter != "" {
			parameter, exists := parameterNames[branch.ToggleParameter]
			if !exists || parameter.Kind != workflowParameterBoolean {
				return fmt.Errorf("branch %q uses unknown boolean parameter %q", branch.ID, branch.ToggleParameter)
			}
		}
		if err := validateWorkflowConditions(branch.Conditions, knownConditions); err != nil {
			return fmt.Errorf("branch %q: %w", branch.ID, err)
		}
	}
	profileIDs := make(map[string]struct{}, len(manifest.QualityProfiles))
	defaultProfiles := 0
	for _, profile := range manifest.QualityProfiles {
		if strings.TrimSpace(profile.ID) == "" || strings.TrimSpace(profile.Name) == "" || len(profile.Parameters) == 0 {
			return fmt.Errorf("quality profile is incomplete")
		}
		if _, exists := profileIDs[profile.ID]; exists {
			return fmt.Errorf("duplicate quality profile %q", profile.ID)
		}
		profileIDs[profile.ID] = struct{}{}
		if profile.Default {
			defaultProfiles++
		}
		if err := validateWorkflowConditions(profile.Conditions, knownConditions); err != nil {
			return fmt.Errorf("quality profile %q: %w", profile.ID, err)
		}
		for parameterName, rule := range profile.Parameters {
			parameter, exists := parameterNames[parameterName]
			if !exists {
				return fmt.Errorf("quality profile %q uses unknown parameter %q", profile.ID, parameterName)
			}
			if !workflowManifestValueAllowed(parameter, normalizeWorkflowProfileValue(parameter, rule.Value)) {
				return fmt.Errorf("quality profile %q has invalid value for %q", profile.ID, parameterName)
			}
			if rule.Minimum != nil && rule.Maximum != nil && *rule.Minimum > *rule.Maximum {
				return fmt.Errorf("quality profile %q has invalid range for %q", profile.ID, parameterName)
			}
		}
	}
	if len(manifest.QualityProfiles) > 0 && defaultProfiles != 1 {
		return fmt.Errorf("exactly one default quality profile is required")
	}
	return nil
}

func validateWorkflowConditions(conditions []workflowCondition, known map[string]struct{}) error {
	for _, condition := range conditions {
		if _, exists := known[condition.Field]; !exists {
			return fmt.Errorf("condition references unknown field %q", condition.Field)
		}
		switch condition.Operator {
		case "equals", "not_equals", "not_empty", "greater_than", "one_of":
		default:
			return fmt.Errorf("condition for %q uses unknown operator %q", condition.Field, condition.Operator)
		}
	}
	return nil
}

func normalizeWorkflowProfileValue(parameter workflowParameterManifest, value any) any {
	if parameter.Kind == workflowParameterEnum {
		field, ok := reflect.TypeOf(generationForm{}).FieldByName(parameter.Target)
		if ok && field.Type.Kind() == reflect.Int {
			if number, ok := numericValue(value); ok {
				return int64(number)
			}
		}
	}
	if parameter.Kind == workflowParameterInteger {
		if number, ok := numericValue(value); ok {
			return int64(number)
		}
	}
	return value
}

func workflowParameterKindMatchesField(kind string, field reflect.Kind) bool {
	switch kind {
	case workflowParameterString, workflowParameterEnum:
		return field == reflect.String || field == reflect.Int
	case workflowParameterInteger:
		return field == reflect.Int || field == reflect.Int64
	case workflowParameterNumber:
		return field == reflect.Float64
	case workflowParameterBoolean:
		return field == reflect.Bool
	default:
		return false
	}
}

func applyWorkflowManifestForm(manifest workflowManifest, values url.Values, input *generationForm) error {
	if input == nil {
		return fmt.Errorf("generation form is nil")
	}
	target := reflect.ValueOf(input).Elem()
	for _, parameter := range manifest.Parameters {
		field := target.FieldByName(parameter.Target)
		if !field.IsValid() || !field.CanSet() {
			return fmt.Errorf("workflow parameter %s cannot set %s", parameter.Name, parameter.Target)
		}
		rawValues, present := values[parameter.Name]
		raw := ""
		if len(rawValues) > 0 {
			raw = strings.TrimSpace(rawValues[len(rawValues)-1])
		}
		if parameter.Kind == workflowParameterBoolean {
			value, err := parseWorkflowBoolean(raw, present)
			if err != nil {
				return workflowParameterError(parameter)
			}
			field.SetBool(value)
			continue
		}
		if raw == "" {
			if parameter.Default == nil {
				continue
			}
			raw = fmt.Sprint(parameter.Default)
		}
		var value any
		var err error
		switch parameter.Kind {
		case workflowParameterString:
			value = raw
		case workflowParameterInteger:
			value, err = strconv.ParseInt(raw, 10, 64)
		case workflowParameterNumber:
			value, err = parseGenerationFloat(raw)
		case workflowParameterEnum:
			if field.Kind() == reflect.Int {
				value, err = strconv.ParseInt(raw, 10, 64)
			} else {
				value = raw
			}
		default:
			err = fmt.Errorf("unsupported parameter kind %q", parameter.Kind)
		}
		if err != nil || !workflowManifestValueAllowed(parameter, value) {
			return workflowParameterError(parameter)
		}
		switch field.Kind() {
		case reflect.String:
			field.SetString(value.(string))
		case reflect.Int:
			field.SetInt(value.(int64))
		case reflect.Int64:
			field.SetInt(value.(int64))
		case reflect.Float64:
			field.SetFloat(value.(float64))
		default:
			return fmt.Errorf("workflow parameter %s targets unsupported field %s", parameter.Name, field.Type())
		}
	}
	return nil
}

func applyWorkflowManifestDefaults(manifest workflowManifest, input *generationForm) error {
	values := make(url.Values, len(manifest.Parameters))
	for _, parameter := range manifest.Parameters {
		if parameter.Default != nil {
			values.Set(parameter.Name, fmt.Sprint(parameter.Default))
		}
	}
	return applyWorkflowManifestForm(manifest, values, input)
}

func parseWorkflowBoolean(raw string, present bool) (bool, error) {
	if !present {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "on", "yes":
		return true, nil
	case "false", "0", "off", "no", "":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean")
	}
}

func workflowManifestValueAllowed(parameter workflowParameterManifest, value any) bool {
	if parameter.Kind == workflowParameterString {
		text, ok := value.(string)
		return ok && (parameter.MaxLength == 0 || len([]rune(text)) <= parameter.MaxLength)
	}
	if parameter.Kind == workflowParameterEnum {
		for _, option := range parameter.Options {
			if choiceEqual(option.Value, value) {
				return true
			}
		}
		return false
	}
	number, ok := numericValue(value)
	if !ok {
		return false
	}
	if parameter.Minimum != nil && number < *parameter.Minimum {
		return false
	}
	if parameter.Maximum != nil && number > *parameter.Maximum {
		return false
	}
	if parameter.Kind == workflowParameterInteger && parameter.Step != nil && *parameter.Step > 0 {
		origin := 0.0
		if parameter.Minimum != nil {
			origin = *parameter.Minimum
		}
		steps := (number - origin) / *parameter.Step
		if math.Abs(steps-math.Round(steps)) > 1e-7 {
			return false
		}
	}
	return true
}

// validateWorkflowManifestInput is the runtime half of the capability
// contract. Browser requests, preflight checks and compatibility fixtures all
// pass through it after workflow-specific normalization.
func validateWorkflowManifestInput(manifest workflowManifest, input generationForm) error {
	value := reflect.ValueOf(input)
	for _, parameter := range manifest.Parameters {
		if parameter.Kind == workflowParameterBoolean {
			continue
		}
		field := value.FieldByName(parameter.Target)
		if !field.IsValid() {
			return fmt.Errorf("workflow parameter %s cannot read %s", parameter.Name, parameter.Target)
		}
		if !workflowManifestValueAllowed(parameter, field.Interface()) {
			return workflowParameterError(parameter)
		}
	}

	mode, ok := workflowManifestModeForInput(manifest, input)
	if !ok {
		return errorsForWorkflowMode(manifest)
	}
	if !workflowConditionsMatch(manifest, input, mode.ModelConditions) {
		return fmt.Errorf("режим %q недоступен для выбранной модели", mode.Name)
	}
	for kind, limit := range mode.InputLimits {
		count := workflowInputCount(input, kind)
		if count < limit.Minimum || count > limit.Maximum {
			return fmt.Errorf("режим %q принимает %s: от %d до %d", mode.Name, workflowInputKindLabel(kind), limit.Minimum, limit.Maximum)
		}
	}

	for _, profile := range manifest.QualityProfiles {
		if len(profile.Conditions) == 0 || !workflowConditionsMatch(manifest, input, profile.Conditions) {
			continue
		}
		for parameterName, rule := range profile.Parameters {
			if !rule.Locked && rule.Minimum == nil && rule.Maximum == nil {
				continue
			}
			parameter, exists := workflowManifestParameter(manifest, parameterName)
			if !exists {
				continue
			}
			actual := value.FieldByName(parameter.Target).Interface()
			if rule.Locked && !choiceEqual(actual, normalizeWorkflowProfileValue(parameter, rule.Value)) {
				return fmt.Errorf("профиль %q требует значение %q для %s", profile.Name, rule.Value, parameter.Label)
			}
			number, numeric := numericValue(actual)
			if rule.Minimum != nil && (!numeric || number < *rule.Minimum) {
				return fmt.Errorf("профиль %q: %s должен быть не меньше %v", profile.Name, parameter.Label, *rule.Minimum)
			}
			if rule.Maximum != nil && (!numeric || number > *rule.Maximum) {
				return fmt.Errorf("профиль %q: %s должен быть не больше %v", profile.Name, parameter.Label, *rule.Maximum)
			}
		}
	}
	return nil
}

func workflowManifestModeForInput(manifest workflowManifest, input generationForm) (workflowModeManifest, bool) {
	if manifest.ModeParameter != "" {
		parameter, ok := workflowManifestParameter(manifest, manifest.ModeParameter)
		if !ok {
			return workflowModeManifest{}, false
		}
		field := reflect.ValueOf(input).FieldByName(parameter.Target)
		if field.IsValid() {
			return manifest.mode(fmt.Sprint(field.Interface()))
		}
	}
	for _, mode := range manifest.Modes {
		if mode.Default {
			return mode, true
		}
	}
	return workflowModeManifest{}, false
}

func errorsForWorkflowMode(manifest workflowManifest) error {
	return fmt.Errorf("выберите корректный режим %s", manifest.Name)
}

func workflowInputCount(input generationForm, kind string) int {
	switch kind {
	case "image":
		return input.imageCount()
	case "audio":
		if strings.TrimSpace(input.InputAudio) != "" {
			return 1
		}
	case "video":
		if strings.TrimSpace(input.InputVideo) != "" {
			return 1
		}
	}
	return 0
}

func workflowInputKindLabel(kind string) string {
	switch kind {
	case "image":
		return "изображения"
	case "audio":
		return "аудиофайлы"
	case "video":
		return "видеофайлы"
	default:
		return kind
	}
}

func workflowConditionsMatch(manifest workflowManifest, input generationForm, conditions []workflowCondition) bool {
	for _, condition := range conditions {
		actual, ok := workflowConditionValue(manifest, input, condition.Field)
		if !ok {
			return false
		}
		switch condition.Operator {
		case "equals":
			if !choiceEqual(actual, condition.Value) {
				return false
			}
		case "not_equals":
			if choiceEqual(actual, condition.Value) {
				return false
			}
		case "not_empty":
			if strings.TrimSpace(fmt.Sprint(actual)) == "" {
				return false
			}
		case "greater_than":
			left, leftOK := numericValue(actual)
			right, rightOK := numericValue(condition.Value)
			if !leftOK || !rightOK || left <= right {
				return false
			}
		case "one_of":
			choices := reflect.ValueOf(condition.Value)
			matched := false
			if choices.IsValid() && (choices.Kind() == reflect.Slice || choices.Kind() == reflect.Array) {
				for index := 0; index < choices.Len(); index++ {
					if choiceEqual(actual, choices.Index(index).Interface()) {
						matched = true
						break
					}
				}
			}
			if !matched {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func workflowConditionValue(manifest workflowManifest, input generationForm, fieldName string) (any, bool) {
	if parameter, ok := workflowManifestParameter(manifest, fieldName); ok {
		field := reflect.ValueOf(input).FieldByName(parameter.Target)
		if field.IsValid() {
			return field.Interface(), true
		}
	}
	switch fieldName {
	case "video_integrated_turbo":
		return input.VideoIntegratedTurbo, true
	case "video_reference_only":
		return input.VideoReferenceOnly, true
	case "lora_count":
		count := 0
		for _, name := range input.LoraNames {
			if strings.TrimSpace(name) != "" {
				count++
			}
		}
		return count, true
	case "image":
		return workflowInputCount(input, "image"), true
	case "audio", "input_audio":
		return input.InputAudio, true
	case "video", "input_video":
		return input.InputVideo, true
	case "input_image":
		return input.InputImage, true
	case "input_image_2":
		return input.ReferenceImages[0], true
	case "input_image_3":
		return input.ReferenceImages[1], true
	case "input_image_4":
		return input.ReferenceImages[2], true
	default:
		return nil, false
	}
}

func workflowParameterError(parameter workflowParameterManifest) error {
	if parameter.InvalidMessage != "" {
		return fmt.Errorf("%s", parameter.InvalidMessage)
	}
	return fmt.Errorf("некорректное значение: %s", parameter.Label)
}

func workflowManifestParameter(manifest workflowManifest, name string) (workflowParameterManifest, bool) {
	for _, parameter := range manifest.Parameters {
		if parameter.Name == name {
			return parameter, true
		}
	}
	return workflowParameterManifest{}, false
}

func (manifest workflowManifest) maximumInput(kind string) int {
	maximum := 0
	for _, mode := range manifest.Modes {
		if limit, ok := mode.InputLimits[kind]; ok && limit.Maximum > maximum {
			maximum = limit.Maximum
		}
	}
	return maximum
}

func (manifest workflowManifest) requiresInput(kind string) bool {
	if len(manifest.Modes) == 0 {
		return false
	}
	for _, mode := range manifest.Modes {
		limit, ok := mode.InputLimits[kind]
		if !ok || limit.Minimum == 0 {
			return false
		}
	}
	return true
}

func (manifest workflowManifest) mode(id string) (workflowModeManifest, bool) {
	for _, mode := range manifest.Modes {
		if mode.ID == id {
			return mode, true
		}
	}
	return workflowModeManifest{}, false
}

func sortedWorkflowManifests(manifests []workflowManifest) []workflowManifest {
	result := append([]workflowManifest(nil), manifests...)
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}
