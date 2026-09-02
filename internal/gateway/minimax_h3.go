package gateway

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

const (
	miniMaxH3FrameMode               = "frames"
	miniMaxH3ReferenceMode           = "references"
	miniMaxH3VideoFPS                = 24
	miniMaxH3MinimumSeconds          = 5
	miniMaxH3DefaultQuality          = 720
	miniMaxH3MaxDimension            = 4096
	miniMaxH3MaxBasePixels           = 4_700_000
	miniMaxH3MaxProcessedFramePixels = 9_000_000
	miniMaxH3MaxRIFEBatchSize        = 4
	miniMaxH3TurboLoraName           = miniMaxH3LoraDirectory + "minimax_h3_turbo_v4_step600_ema.safetensors"
)

// normalizeMiniMaxH3 keeps the public controls intentionally small while
// preserving the model's real constraints: dimensions are divisible by 32 and
// the temporal latent must contain 4 + 17*n frames.
func normalizeMiniMaxH3(input *generationForm) error {
	if input.VideoQuality == 0 {
		input.VideoQuality = miniMaxH3DefaultQuality
	}
	if input.VideoQuality != 480 && input.VideoQuality != 720 && input.VideoQuality != 1080 && input.VideoQuality != 1440 {
		return errors.New("выберите качество видео: 480, 720, 1080 или 1440")
	}
	if input.Width <= 0 || input.Height <= 0 {
		input.Width, input.Height = 704, 1280
	}
	input.VideoResolution = fmt.Sprintf("reference-%dp", input.VideoQuality)
	if input.VideoDurationSeconds == 0 {
		input.VideoDurationSeconds = miniMaxH3MinimumSeconds
	}
	if input.VideoDurationSeconds < miniMaxH3MinimumSeconds || input.VideoDurationSeconds > 60 {
		return errors.New("длительность MiniMax H3 должна быть от 5 до 60 секунд")
	}
	if input.VideoReferenceOnly {
		input.VideoMode = miniMaxH3ReferenceMode
	} else if input.VideoMode == "" {
		input.VideoMode = miniMaxH3FrameMode
	}
	if input.VideoMode != miniMaxH3FrameMode && input.VideoMode != miniMaxH3ReferenceMode {
		return errors.New("выберите корректный режим MiniMax H3")
	}
	if input.VideoIntegratedTurbo {
		input.VideoTurbo = false
	}
	if input.VideoSteps == 0 {
		switch {
		case input.VideoIntegratedTurbo:
			input.VideoSteps = 8
		case input.VideoTurbo:
			input.VideoSteps = 6
		default:
			input.VideoSteps = 25
		}
	}
	if input.VideoIntegratedTurbo && (input.VideoSteps < 6 || input.VideoSteps > 8) {
		return errors.New("для H3 Eros Max beta4 выберите от 6 до 8 шагов")
	}
	if !input.VideoIntegratedTurbo && input.VideoTurbo && (input.VideoSteps < 4 || input.VideoSteps > 8) {
		return errors.New("для MiniMax H3 Turbo выберите от 4 до 8 шагов")
	}
	if !input.VideoIntegratedTurbo && !input.VideoTurbo && (input.VideoSteps < 20 || input.VideoSteps > 25) {
		return errors.New("для MiniMax H3 без Turbo выберите от 20 до 25 шагов")
	}
	if strings.TrimSpace(input.InputAudio) != "" && input.VideoMode != miniMaxH3ReferenceMode {
		return errors.New("аудиореференс MiniMax H3 доступен только в режиме референсов")
	}
	if strings.TrimSpace(input.InputVideo) != "" && input.VideoMode != miniMaxH3ReferenceMode {
		return errors.New("видеореференс MiniMax H3 доступен только в режиме референсов")
	}
	if input.VideoReferenceAudio && strings.TrimSpace(input.InputVideo) == "" {
		return errors.New("сначала добавьте видеореференс, чтобы использовать его звук")
	}
	if input.VideoReferenceDuration == 0 {
		input.VideoReferenceDuration = 5
	}
	if input.VideoReferenceDuration < 1 || input.VideoReferenceDuration > 15 || input.VideoReferenceStart < 0 || input.VideoReferenceStart > 600 {
		return errors.New("выберите фрагмент видеореференса: от 1 до 15 секунд, начало от 0 до 600 секунд")
	}
	if input.VideoAspect == "" {
		input.VideoAspect = "9:16"
	}
	if _, _, err := miniMaxH3AspectDimensions(input.VideoAspect); err != nil {
		return err
	}
	if input.VideoResizeMethod == "" {
		input.VideoResizeMethod = "nearest-exact"
	}
	if input.VideoProportion == "" {
		input.VideoProportion = "crop"
	}
	if input.VideoCropLocation == "" {
		input.VideoCropLocation = "center"
	}
	if input.VideoPadColor == "" {
		input.VideoPadColor = "0, 0, 0"
	}
	if !miniMaxH3Allowed(input.VideoResizeMethod, "nearest-exact", "bicubic", "bilinear", "lanczos", "area", "nvidia_rtx_vsr") ||
		!miniMaxH3Allowed(input.VideoProportion, "crop", "stretch", "resize", "pad", "total_pixels") ||
		!miniMaxH3Allowed(input.VideoCropLocation, "center", "top", "bottom", "left", "right") || len(input.VideoPadColor) > 32 {
		return errors.New("некорректные параметры подготовки кадров MiniMax H3")
	}
	if input.VideoReferenceSize == "" {
		input.VideoReferenceSize = "match"
	}
	if input.VideoReferenceSize != "match" && input.VideoReferenceSize != "max" {
		return errors.New("выберите корректное качество референсов")
	}
	if input.VideoScheduler == "" {
		input.VideoScheduler = "simple"
	}
	if !miniMaxH3Allowed(input.VideoScheduler, "simple", "sgm_uniform", "karras", "exponential", "beta", "normal") {
		return errors.New("выберите корректный планировщик MiniMax H3")
	}
	if input.VideoSampler == "" {
		input.VideoSampler = "euler"
	}
	if input.VideoIntegratedTurbo {
		input.VideoSampler = "euler"
	}
	if !miniMaxH3Allowed(input.VideoSampler, "euler", "res_multistep") {
		return errors.New("выберите корректный сэмплер MiniMax H3")
	}
	if input.VideoShiftVideo == 0 {
		if input.VideoIntegratedTurbo {
			input.VideoShiftVideo = 12
		} else {
			input.VideoShiftVideo = 11
		}
	}
	if input.VideoShiftAudio == 0 {
		if input.VideoIntegratedTurbo {
			input.VideoShiftAudio = 7
		} else {
			input.VideoShiftAudio = 3
		}
	}
	if input.VideoShiftVideo < 1 || input.VideoShiftVideo > 32 || input.VideoShiftAudio < 1 || input.VideoShiftAudio > 32 {
		return errors.New("shift MiniMax H3 должен быть от 1 до 32")
	}
	if input.VideoLowVRAMHeadChunks == 0 {
		input.VideoLowVRAMHeadChunks = 4
	}
	if input.VideoLowVRAMHeadChunks < 1 || input.VideoLowVRAMHeadChunks > 56 {
		return errors.New("число групп Low VRAM Attention должно быть от 1 до 56")
	}
	if input.VideoChunkFFChunks == 0 {
		input.VideoChunkFFChunks = 2
	}
	if input.VideoChunkFFThreshold == 0 {
		input.VideoChunkFFThreshold = 4096
	}
	if input.VideoChunkFFChunks < 1 || input.VideoChunkFFChunks > 64 || input.VideoChunkFFThreshold < 256 || input.VideoChunkFFThreshold > 262144 || input.VideoChunkFFThreshold%256 != 0 {
		return errors.New("некорректные параметры MiniMax H3 Chunk FeedForward")
	}
	if input.VideoMemoryMLP == "" {
		input.VideoMemoryMLP = "auto"
	}
	if input.VideoMemoryPrecision == "" {
		input.VideoMemoryPrecision = "Auto"
	}
	if input.VideoMemoryQKV == "" {
		input.VideoMemoryQKV = "Auto"
	}
	if input.VideoMemoryAttention == "" {
		input.VideoMemoryAttention = "Standard"
	}
	if input.VideoMemoryChunkRows == 0 {
		input.VideoMemoryChunkRows = 4096
	}
	if !miniMaxH3Allowed(input.VideoMemoryMLP, "auto", "off") ||
		!miniMaxH3Allowed(input.VideoMemoryPrecision, "Auto", "BF16", "Preserve native", "Force quant") ||
		!miniMaxH3Allowed(input.VideoMemoryQKV, "Off", "Auto", "Forced") ||
		!miniMaxH3Allowed(input.VideoMemoryAttention, "Standard", "Lower VRAM (slower)") ||
		input.VideoMemoryChunkRows < 256 || input.VideoMemoryChunkRows > 65536 || input.VideoMemoryChunkRows%256 != 0 {
		return errors.New("некорректные параметры H3 Memory Optimization")
	}
	if input.VideoSparseBudget == 0 {
		input.VideoSparseBudget = 0.30
	}
	if input.VideoSparseSchedule == "" {
		input.VideoSparseSchedule = "Hold"
	}
	if input.VideoSparseEarlyKV == 0 {
		input.VideoSparseEarlyKV = 0.5
	}
	if input.VideoSparseLateKV == 0 {
		input.VideoSparseLateKV = 0.5
	}
	if input.VideoSparseBackend == "" {
		input.VideoSparseBackend = "Kitchen INT8"
	}
	if input.VideoAIMDOResidency == "" {
		input.VideoAIMDOResidency = "0 blocks"
	}
	if !miniMaxH3Allowed(input.VideoAIMDOResidency, "stock", "0 blocks", "1 block", "2 blocks", "4 blocks") {
		return errors.New("некорректный лимит резидентности H3 AIMDO")
	}
	if input.VideoSparseBudget < 0.01 || input.VideoSparseBudget > 1 || input.VideoSparseEarlyKV < 0.01 || input.VideoSparseEarlyKV > 1 || input.VideoSparseLateKV < 0.01 || input.VideoSparseLateKV > 1 ||
		input.VideoSparseEarlyStep < 0 || input.VideoSparseEarlyStep > 1000 || input.VideoSparseLateStep < 0 || input.VideoSparseLateStep > 1000 ||
		!miniMaxH3Allowed(input.VideoSparseSchedule, "Hold", "Ramp") ||
		!miniMaxH3Allowed(input.VideoSparseBackend, "Kitchen INT8", "FROST BF16 (SM89)", "Sparse Sage", "BF16 Triton", "FP8 FlexAttention") {
		return errors.New("некорректные параметры H3 Sparse Attention")
	}
	if input.VideoRIFECheckpoint == "" {
		input.VideoRIFECheckpoint = "rife49.pth"
	}
	if !miniMaxH3Allowed(input.VideoRIFECheckpoint, "sudo_rife4_269.662_testV1_scale1.pth", "rife47.pth", "rife49.pth", "rife417.pth", "rife426.pth") {
		return errors.New("выберите доступную модель RIFE")
	}
	if input.VideoRIFEMultiplier == 0 {
		input.VideoRIFEMultiplier = 2
	}
	if input.VideoRIFEMultiplier < 1 || input.VideoRIFEMultiplier > 4 || input.VideoRIFEBatchSize < 0 || input.VideoRIFEBatchSize > miniMaxH3MaxRIFEBatchSize {
		return errors.New("некорректные параметры RIFE")
	}
	if input.VideoRIFEBatchSize == 0 {
		input.VideoRIFEBatchSize = 1
	}
	if input.VideoRIFEDtype == "" {
		input.VideoRIFEDtype = "float32"
	}
	if !miniMaxH3Allowed(input.VideoRIFEDtype, "float32", "float16", "bfloat16") {
		return errors.New("выберите корректную точность RIFE")
	}
	if input.VideoRTXScale == 0 {
		input.VideoRTXScale = 2
	}
	if input.VideoRTXScale < 1 || input.VideoRTXScale > 2 {
		return errors.New("масштаб RTX Super Resolution должен быть от 1 до 2")
	}
	if input.VideoRTXQuality == "" {
		input.VideoRTXQuality = "ULTRA"
	}
	if !miniMaxH3Allowed(input.VideoRTXQuality, "LOW", "MEDIUM", "HIGH", "ULTRA") {
		return errors.New("выберите корректное качество RTX Super Resolution")
	}
	if input.VideoColorMethod == "" {
		input.VideoColorMethod = "adain"
	}
	if !miniMaxH3Allowed(input.VideoColorMethod, "adain", "mean_std") || input.VideoColorStrength < 0 || input.VideoColorStrength > 1 {
		return errors.New("некорректные параметры ColorMatch")
	}
	if input.VideoSharpenMethod == "" {
		input.VideoSharpenMethod = "rcas"
	}
	if input.VideoSharpenStrength == 0 {
		input.VideoSharpenStrength = 0.8
	}
	if input.VideoSharpenRadius == 0 {
		input.VideoSharpenRadius = 1
	}
	if input.VideoSharpenThreshold == 0 {
		input.VideoSharpenThreshold = 0.05
	}
	if input.VideoSharpenIterations == 0 {
		input.VideoSharpenIterations = 10
	}
	if !miniMaxH3Allowed(input.VideoSharpenMethod, "rcas", "adaptive_usm", "high_pass", "deconvolution") || input.VideoSharpenStrength < 0 || input.VideoSharpenStrength > 3 ||
		input.VideoSharpenRadius < 0.5 || input.VideoSharpenRadius > 5 || input.VideoSharpenThreshold < 0 || input.VideoSharpenThreshold > 1 || input.VideoSharpenIterations < 1 || input.VideoSharpenIterations > 100 {
		return errors.New("некорректные параметры повышения резкости видео")
	}
	if (input.VideoSharpenMethod == "rcas" || input.VideoSharpenMethod == "deconvolution") && input.VideoSharpenStrength > 1 {
		return errors.New("для выбранного метода сила резкости должна быть от 0 до 1")
	}
	if input.VideoAudioStart < -60 || input.VideoAudioStart > 60 {
		return errors.New("смещение аудио MiniMax H3 должно быть от -60 до 60 секунд")
	}
	if input.VideoOutputCRF == 0 {
		input.VideoOutputCRF = 19
	}
	if input.VideoOutputCRF < 0 || input.VideoOutputCRF > 51 {
		return errors.New("CRF видео должен быть от 0 до 51")
	}
	if input.VideoMode == miniMaxH3FrameMode && input.imageCount() > 2 {
		return errors.New("MiniMax H3 поддерживает первый и последний кадр: до двух фото")
	}
	if input.VideoMode == miniMaxH3ReferenceMode && input.imageCount() > 4 {
		return errors.New("MiniMax H3 поддерживает до четырёх фото-референсов")
	}
	if input.VideoColorMatch && input.imageCount() == 0 {
		return errors.New("для ColorMatch добавьте хотя бы одно фото")
	}
	input.VideoFilename = miniMaxH3FilenamePrefix(input.VideoFilename)
	if err := validateMiniMaxH3ResourceBudget(*input); err != nil {
		return err
	}
	input.Steps = input.VideoSteps
	input.CFG = 1
	input.Denoise = 1
	input.Sampler = input.VideoSampler
	input.Scheduler = input.VideoScheduler
	return nil
}

func miniMaxH3Allowed(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

// miniMaxH3VideoDimensions mirrors AspectRatioSimplifier: quality is the
// maximum longer side, small sources are not enlarged, and dimensions are
// aligned down so the selected maximum is never exceeded.
func miniMaxH3VideoDimensions(sourceWidth, sourceHeight, quality int) (int, int, error) {
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return 0, 0, errors.New("не удалось определить размер первого референса")
	}
	if quality != 480 && quality != 720 && quality != 1080 && quality != 1440 {
		return 0, 0, errors.New("выберите качество видео: 480, 720, 1080 или 1440")
	}
	longSide := max(sourceWidth, sourceHeight)
	scaledWidth, scaledHeight := sourceWidth, sourceHeight
	if longSide > quality {
		scale := float64(quality) / float64(longSide)
		// AspectRatioSimplifier rounds the clamped image size first and only
		// then aligns it to divisible_by. Keeping that order also avoids an
		// exact 480 target becoming 479.999... and dropping to 448.
		scaledWidth = max(1, int(math.Round(float64(sourceWidth)*scale)))
		scaledHeight = max(1, int(math.Round(float64(sourceHeight)*scale)))
	}
	width := miniMaxH3FloorToMultiple(float64(scaledWidth))
	height := miniMaxH3FloorToMultiple(float64(scaledHeight))
	fitScale := 1.0
	if longest := max(width, height); longest > miniMaxH3MaxDimension {
		fitScale = min(fitScale, float64(miniMaxH3MaxDimension)/float64(longest))
	}
	if pixels := int64(width) * int64(height); pixels > miniMaxH3MaxBasePixels {
		fitScale = min(fitScale, math.Sqrt(float64(miniMaxH3MaxBasePixels)/float64(pixels)))
	}
	if fitScale < 1 {
		width = miniMaxH3FloorToMultiple(float64(width) * fitScale)
		height = miniMaxH3FloorToMultiple(float64(height) * fitScale)
	}
	if width < 32 || height < 32 {
		return 0, 0, errors.New("кадр слишком вытянут для MiniMax H3")
	}
	return width, height, nil
}

func miniMaxH3FloorToMultiple(value float64) int {
	return max(32, int(math.Floor(value/32.0))*32)
}

func miniMaxH3AspectDimensions(aspect string) (int, int, error) {
	switch strings.TrimSpace(aspect) {
	case "1:1":
		return 1080, 1080, nil
	case "4:5":
		return 1080, 1350, nil
	case "16:9":
		return 1344, 768, nil
	case "9:16", "":
		return 1080, 1920, nil
	case "4:1":
		return 1600, 400, nil
	case "2:3":
		return 832, 1248, nil
	case "3:2":
		return 1248, 832, nil
	case "3:4":
		return 896, 1152, nil
	case "4:3":
		return 1152, 896, nil
	case "21:9":
		return 1536, 640, nil
	default:
		return 0, 0, errors.New("выберите корректное соотношение сторон MiniMax H3")
	}
}

func miniMaxH3FilenamePrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "AI-Gateway-MiniMaxH3"
	}
	var cleaned strings.Builder
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '-' || char == '_' || char == ' ' {
			cleaned.WriteRune(char)
		}
		if cleaned.Len() >= 80 {
			break
		}
	}
	result := strings.TrimSpace(cleaned.String())
	if result == "" {
		return "AI-Gateway-MiniMaxH3"
	}
	return result
}

func validateMiniMaxH3ResourceBudget(input generationForm) error {
	basePixels := float64(input.Width) * float64(input.Height)
	if basePixels > miniMaxH3MaxBasePixels {
		return errors.New("кадр MiniMax H3 превышает безопасные 4,7 Мп: уменьшите качество видео")
	}
	scale := 1.0
	if input.VideoRTXEnabled {
		scale = input.VideoRTXScale
	}
	processedWidth := float64(input.Width) * scale
	processedHeight := float64(input.Height) * scale
	processedPixels := basePixels * scale * scale
	if processedWidth > miniMaxH3MaxDimension || processedHeight > miniMaxH3MaxDimension || processedPixels > miniMaxH3MaxProcessedFramePixels {
		return errors.New("RTX Super Resolution создаст слишком большой кадр: уменьшите качество видео или масштаб RTX")
	}
	return nil
}

func miniMaxH3FrameCount(seconds int) int {
	frames := int(math.Round(float64(seconds * miniMaxH3VideoFPS)))
	return frames + ((5 - frames%17) % 17)
}

func miniMaxH3Node(classType string, inputs map[string]any) map[string]any {
	return map[string]any{"class_type": classType, "inputs": inputs}
}

// buildMiniMaxH3Prompt mirrors the executable path of the saved MiniMax H3 v5
// workflow. The H.264 output is deliberate so generated MP4 files remain
// playable on desktop and Android.
func buildMiniMaxH3Prompt(input generationForm) (map[string]any, error) {
	if strings.TrimSpace(input.AudioVAE) == "" || strings.TrimSpace(input.TextEncoder) == "" || strings.TrimSpace(input.VAE) == "" {
		return nil, errors.New("MiniMax H3: не найдены обязательные модели")
	}
	modelName := input.ModelName
	if input.VideoMode == miniMaxH3ReferenceMode {
		modelName = input.ReferenceModel
		if strings.TrimSpace(modelName) == "" {
			return nil, errors.New("MiniMax H3: не найдена модель для режима референсов")
		}
	}

	modelInputID := "1"
	nodes := map[string]any{
		"1":  miniMaxH3Node("UNETLoader", map[string]any{"unet_name": modelName, "weight_dtype": "default"}),
		"2":  miniMaxH3Node("CLIPLoader", map[string]any{"clip_name": input.TextEncoder, "type": "minimax", "device": "default"}),
		"3":  miniMaxH3Node("VAELoader", map[string]any{"vae_name": input.VAE}),
		"4":  miniMaxH3Node("VAELoader", map[string]any{"vae_name": input.AudioVAE}),
		"9":  miniMaxH3Node("MiniMaxH3SigmaShift", map[string]any{"model": []any{modelInputID, 0}, "shift_video": input.VideoShiftVideo, "shift_audio": input.VideoShiftAudio}),
		"10": miniMaxH3Node("RandomNoise", map[string]any{"noise_seed": input.Seed}),
		"12": miniMaxH3Node("BasicScheduler", map[string]any{"model": []any{"9", 0}, "scheduler": input.VideoScheduler, "steps": input.VideoSteps, "denoise": 1}),
		"13": miniMaxH3Node("BasicGuider", map[string]any{"model": []any{"9", 0}, "conditioning": []any{"7", 0}}),
		"14": miniMaxH3Node("SamplerCustomAdvanced", map[string]any{"noise": []any{"10", 0}, "guider": []any{"13", 0}, "sampler": []any{"11", 0}, "sigmas": []any{"12", 0}, "latent_image": []any{"7", 1}}),
		"15": miniMaxH3Node("VAEDecode", map[string]any{"samples": []any{"14", 0}, "vae": []any{"3", 0}}),
		"16": miniMaxH3Node("VAEDecodeAudio", map[string]any{"samples": []any{"14", 0}, "vae": []any{"4", 0}}),
	}
	if input.VideoLowVRAMAttention {
		nodes["28"] = miniMaxH3Node("MiniMaxLowVRAMAttention", map[string]any{
			"model": []any{modelInputID, 0}, "head_chunks": input.VideoLowVRAMHeadChunks,
		})
		modelInputID = "28"
	}
	if input.VideoChunkFeedForward {
		nodes["29"] = miniMaxH3Node("MiniMaxChunkFeedForward", map[string]any{
			"model": []any{modelInputID, 0}, "chunks": input.VideoChunkFFChunks, "seq_threshold": input.VideoChunkFFThreshold,
		})
		modelInputID = "29"
	}
	if input.VideoSageAttention {
		nodes["5"] = miniMaxH3Node("MiniMaxH3MemoryEfficientSageAttentionPatch", map[string]any{"model": []any{modelInputID, 0}})
		modelInputID = "5"
	}
	if input.VideoMemoryOptimize {
		nodes["24"] = miniMaxH3Node("H3MemoryOptimization", map[string]any{
			"model": []any{modelInputID, 0}, "fused_qkv": "auto", "mlp_memory": input.VideoMemoryMLP,
			"chunk_rows": input.VideoMemoryChunkRows, "preserve_precision": true, "precision_mode": input.VideoMemoryPrecision,
			"qkv_streaming_mode": input.VideoMemoryQKV, "embedding_memory_mode": "Auto", "kitchen_v_memory_mode": input.VideoMemoryAttention,
		})
		modelInputID = "24"
	}
	if input.VideoAIMDOEnabled {
		nodes["26"] = miniMaxH3Node("H3AIMDOResidencyLimiter", map[string]any{
			"model": []any{modelInputID, 0}, "residency": input.VideoAIMDOResidency,
		})
		modelInputID = "26"
	}
	if input.VideoSparseAttention {
		nodes["25"] = miniMaxH3Node("H3SparseAttentionAdvanced", map[string]any{
			"model": []any{modelInputID, 0}, "video_budget": input.VideoSparseBudget,
			"early_steps": input.VideoSparseEarlyStep, "early_kv": input.VideoSparseEarlyKV,
			"late_steps": input.VideoSparseLateStep, "late_kv": input.VideoSparseLateKV, "backend": input.VideoSparseBackend,
			"early_schedule": input.VideoSparseSchedule,
		})
		modelInputID = "25"
	}
	modelNodeID := modelInputID
	if input.VideoTurbo && !input.VideoIntegratedTurbo {
		nodes["6"] = miniMaxH3Node("MiniMaxH3TurboLoRA", map[string]any{"model": []any{modelInputID, 0}, "lora_name": miniMaxH3TurboLoraName, "strength": 1, "low_vram": false})
		nodes["11"] = miniMaxH3Node("MiniMaxH3TurboSampler", map[string]any{})
		modelNodeID = "6"
	} else {
		nodes["11"] = miniMaxH3Node("KSamplerSelect", map[string]any{"sampler_name": input.Sampler})
	}
	clipInput := miniMaxH3ApplyLoras(nodes, input, modelNodeID)

	images := input.images()
	rawImages := make([]string, len(images))
	preparedFrames := make([]string, len(images))
	for index, image := range images {
		nodeID := fmt.Sprintf("%d", 30+index)
		nodes[nodeID] = miniMaxH3Node("LoadImage", map[string]any{"image": image})
		rawImages[index] = nodeID
		preparedFrames[index] = nodeID
		if input.VideoMode != miniMaxH3FrameMode {
			continue
		}
		resizeID := fmt.Sprintf("%d", 50+index)
		nodes[resizeID] = miniMaxH3Node("LCImageMaskResize", map[string]any{
			"image": []any{nodeID, 0}, "match_aspect_ratio": false, "aspect_ratio": "custom",
			"custom_width": input.Width, "custom_height": input.Height, "upscale_by": "none",
			"multiplier": 1.0, "megapixels": 1.0, "upscale_method": input.VideoResizeMethod,
			"proportion": input.VideoProportion, "crop_location": input.VideoCropLocation, "pad_color": input.VideoPadColor, "divisible_by": 32,
		})
		preparedFrames[index] = resizeID
	}
	if input.VideoMode == miniMaxH3ReferenceMode {
		inputs := map[string]any{
			"clip": clipInput, "vae": []any{"3", 0}, "audio_vae": []any{"4", 0},
			"prompt": input.Positive, "width": input.Width, "height": input.Height,
			"length": miniMaxH3FrameCount(input.VideoDurationSeconds), "ref_image_size": input.VideoReferenceSize,
		}
		for index := range images {
			// MiniMax performs its own downscale-only reference preparation. Keep
			// every source image at its native aspect ratio and resolution here.
			inputs[fmt.Sprintf("ref_images.ref_image_%d", index)] = []any{rawImages[index], 0}
		}
		if audio := strings.TrimSpace(input.InputAudio); audio != "" {
			nodes["40"] = miniMaxH3Node("LoadAudio", map[string]any{"audio": audio})
			nodes["41"] = miniMaxH3Node("TrimAudioDuration", map[string]any{"audio": []any{"40", 0}, "start_index": input.VideoAudioStart, "duration": float64(input.VideoDurationSeconds)})
			inputs["ref_audios.ref_audio_0"] = []any{"41", 0}
		}
		if video := strings.TrimSpace(input.InputVideo); video != "" {
			nodes["42"] = miniMaxH3Node("VHS_LoadVideo", map[string]any{
				"video": video, "force_rate": miniMaxH3VideoFPS, "custom_width": 0, "custom_height": 0,
				"frame_load_cap":    input.VideoReferenceDuration * miniMaxH3VideoFPS,
				"skip_first_frames": int(math.Round(input.VideoReferenceStart * miniMaxH3VideoFPS)), "select_every_nth": 1, "format": "None",
			})
			inputs["ref_videos.ref_video_0"] = []any{"42", 0}
			if input.VideoReferenceAudio {
				inputs["ref_video_audios.ref_video_audio_0"] = []any{"42", 2}
			}
		}
		nodes["7"] = miniMaxH3Node("MiniMaxH3ReferenceToVideo", inputs)
	} else {
		inputs := map[string]any{
			"clip": clipInput, "vae": []any{"3", 0}, "prompt": input.Positive,
			"width": input.Width, "height": input.Height, "length": miniMaxH3FrameCount(input.VideoDurationSeconds),
		}
		if len(images) > 0 {
			inputs["first_frame"] = []any{preparedFrames[0], 0}
		}
		if len(images) > 1 {
			inputs["last_frame"] = []any{preparedFrames[1], 0}
		}
		nodes["7"] = miniMaxH3Node("MiniMaxH3ImageToVideo", inputs)
	}
	if input.VideoClearVRAM {
		nodes["18"] = miniMaxH3Node("LCVRAMCacheClear", map[string]any{"any": []any{"7", 1}})
		nodes["14"].(map[string]any)["inputs"].(map[string]any)["latent_image"] = []any{"18", 0}
		nodes["19"] = miniMaxH3Node("LCVRAMCacheClear", map[string]any{"any": []any{"14", 0}})
		nodes["15"].(map[string]any)["inputs"].(map[string]any)["samples"] = []any{"19", 0}
		nodes["16"].(map[string]any)["inputs"].(map[string]any)["samples"] = []any{"19", 0}
	}
	videoNodeID := "15"
	if input.VideoClearVRAM {
		nodes["23"] = miniMaxH3Node("LCVRAMCacheClear", map[string]any{"any": []any{"15", 0}})
		videoNodeID = "23"
	}
	if input.VideoColorMatch {
		referenceID := rawImages[0]
		if input.VideoMode == miniMaxH3FrameMode {
			referenceID = preparedFrames[0]
		}
		nodes["21"] = miniMaxH3Node("LCColorMatch", map[string]any{"image": []any{videoNodeID, 0}, "reference": []any{referenceID, 0}, "method": input.VideoColorMethod, "strength": input.VideoColorStrength})
		videoNodeID = "21"
	}
	frameRate := miniMaxH3VideoFPS
	if input.VideoRIFEEnabled {
		nodes["22"] = miniMaxH3Node("RIFE VFI", map[string]any{"ckpt_name": input.VideoRIFECheckpoint, "frames": []any{videoNodeID, 0}, "clear_cache_after_n_frames": 10, "multiplier": input.VideoRIFEMultiplier, "fast_mode": input.VideoRIFEFastMode, "ensemble": input.VideoRIFEEnsemble, "scale_factor": 1.0, "dtype": input.VideoRIFEDtype, "torch_compile": input.VideoRIFECompile, "batch_size": input.VideoRIFEBatchSize})
		videoNodeID = "22"
		frameRate *= input.VideoRIFEMultiplier
	}
	if input.VideoRTXEnabled {
		nodes["20"] = miniMaxH3Node("RTXVideoSuperResolution", map[string]any{
			"images":            []any{videoNodeID, 0},
			"resize_type":       "scale by multiplier",
			"resize_type.scale": input.VideoRTXScale,
			"quality":           input.VideoRTXQuality,
		})
		videoNodeID = "20"
	}
	if input.VideoSharpenEnabled {
		sharpenInputs := map[string]any{
			"image": []any{videoNodeID, 0}, "method": input.VideoSharpenMethod, "method.strength": input.VideoSharpenStrength,
		}
		switch input.VideoSharpenMethod {
		case "adaptive_usm":
			sharpenInputs["method.radius"] = input.VideoSharpenRadius
			sharpenInputs["method.threshold"] = input.VideoSharpenThreshold
		case "high_pass":
			sharpenInputs["method.radius"] = input.VideoSharpenRadius
		case "deconvolution":
			sharpenInputs["method.radius"] = input.VideoSharpenRadius
			sharpenInputs["method.iterations"] = input.VideoSharpenIterations
		}
		nodes["27"] = miniMaxH3Node("ImageSharpenKJ", sharpenInputs)
		videoNodeID = "27"
	}
	nodes["17"] = miniMaxH3Node("VHS_VideoCombine", map[string]any{"images": []any{videoNodeID, 0}, "audio": []any{"16", 0}, "frame_rate": frameRate, "loop_count": 0, "filename_prefix": input.VideoFilename, "format": "video/h264-mp4", "pingpong": false, "save_output": true, "pix_fmt": "yuv420p", "crf": input.VideoOutputCRF, "save_metadata": true})
	return nodes, nil
}

func miniMaxH3ApplyLoras(nodes map[string]any, input generationForm, modelNodeID string) []any {
	lastStack := ""
	for offset := 0; offset < len(input.LoraNames); offset += 3 {
		hasLora := false
		for slot := 0; slot < 3 && offset+slot < len(input.LoraNames); slot++ {
			if strings.TrimSpace(input.LoraNames[offset+slot]) != "" {
				hasLora = true
				break
			}
		}
		if !hasLora {
			continue
		}
		stackInputs := map[string]any{}
		if lastStack != "" {
			stackInputs["lora_stack"] = []any{lastStack, 0}
		}
		for slot := 0; slot < 3; slot++ {
			position := strconv.Itoa(slot + 1)
			stackInputs["switch_"+position] = "Off"
			stackInputs["lora_name_"+position] = "None"
			stackInputs["model_weight_"+position] = 0.0
			stackInputs["clip_weight_"+position] = 0.0
			if offset+slot >= len(input.LoraNames) {
				continue
			}
			name := strings.TrimSpace(input.LoraNames[offset+slot])
			if name == "" {
				continue
			}
			stackInputs["switch_"+position] = "On"
			stackInputs["lora_name_"+position] = name
			stackInputs["model_weight_"+position] = input.LoraModel[offset+slot]
			stackInputs["clip_weight_"+position] = input.LoraClip[offset+slot]
		}
		stackID := fmt.Sprintf("lora_stack_%d", offset/3+1)
		nodes[stackID] = miniMaxH3Node("CR LoRA Stack", stackInputs)
		lastStack = stackID
	}
	if lastStack == "" {
		delete(nodes, "8")
		nodes["9"].(map[string]any)["inputs"].(map[string]any)["model"] = []any{modelNodeID, 0}
		return []any{"2", 0}
	}
	nodes["8"] = miniMaxH3Node("CR Apply LoRA Stack", map[string]any{"model": []any{modelNodeID, 0}, "clip": []any{"2", 0}, "lora_stack": []any{lastStack, 0}})
	nodes["9"].(map[string]any)["inputs"].(map[string]any)["model"] = []any{"8", 0}
	return []any{"8", 1}
}
