package gateway

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	miniMaxH3FrameMode      = "frames"
	miniMaxH3ReferenceMode  = "references"
	miniMaxH3VideoFPS       = 24
	miniMaxH3MinimumSeconds = 5
	miniMaxH3DefaultQuality = 720
	miniMaxH3TurboLoraName  = miniMaxH3LoraDirectory + "minimax_h3_turbo_v4_step600_ema.safetensors"
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
	if input.VideoDurationSeconds != 5 && input.VideoDurationSeconds != 10 && input.VideoDurationSeconds != 15 {
		return errors.New("длительность MiniMax H3 может быть 5, 10 или 15 секунд")
	}
	if input.VideoSteps == 0 {
		if input.VideoTurbo {
			input.VideoSteps = 6
		} else {
			input.VideoSteps = 25
		}
	}
	if input.VideoTurbo && (input.VideoSteps < 4 || input.VideoSteps > 8) {
		return errors.New("для MiniMax H3 Turbo выберите от 4 до 8 шагов")
	}
	if !input.VideoTurbo && (input.VideoSteps < 20 || input.VideoSteps > 25) {
		return errors.New("для MiniMax H3 без Turbo выберите от 20 до 25 шагов")
	}
	if input.VideoMode == "" {
		input.VideoMode = miniMaxH3FrameMode
	}
	if input.VideoMode != miniMaxH3FrameMode && input.VideoMode != miniMaxH3ReferenceMode {
		return errors.New("выберите корректный режим MiniMax H3")
	}
	if strings.TrimSpace(input.InputAudio) != "" && input.VideoMode != miniMaxH3ReferenceMode {
		return errors.New("аудиореференс MiniMax H3 доступен только в режиме референсов")
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
	if input.VideoShiftVideo == 0 {
		input.VideoShiftVideo = 11
	}
	if input.VideoShiftAudio == 0 {
		input.VideoShiftAudio = 3
	}
	if input.VideoShiftVideo < 1 || input.VideoShiftVideo > 32 || input.VideoShiftAudio < 1 || input.VideoShiftAudio > 32 {
		return errors.New("shift MiniMax H3 должен быть от 1 до 32")
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
	if input.VideoRIFEMultiplier < 1 || input.VideoRIFEMultiplier > 4 || input.VideoRIFEBatchSize < 0 || input.VideoRIFEBatchSize > 64 {
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
	if input.VideoRTXScale < 1 || input.VideoRTXScale > 4 {
		return errors.New("масштаб RTX Super Resolution должен быть от 1 до 4")
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
	if input.VideoAudioStart < -60 || input.VideoAudioStart > 60 {
		return errors.New("смещение аудио MiniMax H3 должно быть от -60 до 60 секунд")
	}
	if input.VideoOutputCRF == 0 {
		input.VideoOutputCRF = 19
	}
	if input.VideoOutputCRF < 0 || input.VideoOutputCRF > 100 {
		return errors.New("CRF видео должен быть от 0 до 100")
	}
	if strings.TrimSpace(input.InputImage) == "" {
		return errors.New("для MiniMax H3 добавьте первый кадр")
	}
	if input.VideoMode == miniMaxH3FrameMode && input.imageCount() > 2 {
		return errors.New("MiniMax H3 поддерживает первый и последний кадр: до двух фото")
	}
	if input.VideoMode == miniMaxH3ReferenceMode && input.imageCount() > 4 {
		return errors.New("MiniMax H3 поддерживает до четырёх фото-референсов")
	}
	input.Steps = input.VideoSteps
	input.CFG = 1
	input.Denoise = 1
	input.Sampler = "res_multistep"
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

// miniMaxH3VideoDimensions scales the first reference frame to a requested
// delivery quality while keeping its aspect ratio. Both dimensions are rounded
// to the model's required multiple of 32.
func miniMaxH3VideoDimensions(sourceWidth, sourceHeight, quality int) (int, int, error) {
	if sourceWidth <= 0 || sourceHeight <= 0 {
		return 0, 0, errors.New("не удалось определить размер первого референса")
	}
	if quality != 480 && quality != 720 && quality != 1080 && quality != 1440 {
		return 0, 0, errors.New("выберите качество видео: 480, 720, 1080 или 1440")
	}
	shortSide := min(sourceWidth, sourceHeight)
	scale := float64(quality) / float64(shortSide)
	width := miniMaxH3RoundToMultiple(float64(sourceWidth) * scale)
	height := miniMaxH3RoundToMultiple(float64(sourceHeight) * scale)
	return width, height, nil
}

func miniMaxH3RoundToMultiple(value float64) int {
	rounded := int(math.Round(value/32.0)) * 32
	if rounded < 256 {
		return 256
	}
	return rounded
}

func miniMaxH3FrameCount(seconds int) int {
	frames := int(math.Round(float64(seconds * miniMaxH3VideoFPS)))
	return frames + ((5 - frames%17) % 17)
}

func miniMaxH3Node(classType string, inputs map[string]any) map[string]any {
	return map[string]any{"class_type": classType, "inputs": inputs}
}

// buildMiniMaxH3Prompt uses the core execution chain from the saved
// "Видосы MiniMax H3 (v3)" workflow. Turbo is an optional acceleration pass;
// the ordinary path keeps the original full-step sampler. The H.264 output is
// deliberate so generated MP4 files remain playable on desktop and Android.
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
	if input.VideoSageAttention {
		nodes["5"] = miniMaxH3Node("MiniMaxH3MemoryEfficientSageAttentionPatch", map[string]any{"model": []any{"1", 0}})
		modelInputID = "5"
		nodes["9"].(map[string]any)["inputs"].(map[string]any)["model"] = []any{modelInputID, 0}
	}
	if input.VideoClearVRAM {
		nodes["18"] = miniMaxH3Node("LCVRAMCacheClear", map[string]any{"any": []any{"7", 1}})
		nodes["14"].(map[string]any)["inputs"].(map[string]any)["latent_image"] = []any{"18", 0}
	}
	modelNodeID := modelInputID
	if input.VideoTurbo {
		nodes["6"] = miniMaxH3Node("MiniMaxH3TurboLoRA", map[string]any{"model": []any{modelInputID, 0}, "lora_name": miniMaxH3TurboLoraName, "strength": 1, "low_vram": false})
		nodes["11"] = miniMaxH3Node("MiniMaxH3TurboSampler", map[string]any{})
		modelNodeID = "6"
	} else {
		nodes["11"] = miniMaxH3Node("KSamplerSelect", map[string]any{"sampler_name": input.Sampler})
	}
	miniMaxH3ApplyLoras(nodes, input, modelNodeID)

	images := input.images()
	resizedImages := make([]string, len(images))
	for index, image := range images {
		nodeID := fmt.Sprintf("%d", 30+index)
		nodes[nodeID] = miniMaxH3Node("LoadImage", map[string]any{"image": image})
		resizedID := fmt.Sprintf("%d", 50+index)
		nodes[resizedID] = miniMaxH3Node("ImageResizeKJv2", map[string]any{
			"image":           []any{nodeID, 0},
			"width":           input.Width,
			"height":          input.Height,
			"upscale_method":  "nearest-exact",
			"keep_proportion": "stretch",
			"pad_color":       "0, 0, 0",
			"crop_position":   "center",
			"divisible_by":    2,
			"device":          "cpu",
		})
		resizedImages[index] = resizedID
	}
	if input.VideoMode == miniMaxH3ReferenceMode {
		inputs := map[string]any{
			"clip": []any{"2", 0}, "vae": []any{"3", 0}, "audio_vae": []any{"4", 0},
			"prompt": input.Positive, "width": input.Width, "height": input.Height,
			"length": miniMaxH3FrameCount(input.VideoDurationSeconds), "ref_image_size": input.VideoReferenceSize,
		}
		for index := range images {
			inputs[fmt.Sprintf("ref_images.ref_image_%d", index)] = []any{resizedImages[index], 0}
		}
		if audio := strings.TrimSpace(input.InputAudio); audio != "" {
			nodes["40"] = miniMaxH3Node("LoadAudio", map[string]any{"audio": audio})
			nodes["41"] = miniMaxH3Node("TrimAudioDuration", map[string]any{"audio": []any{"40", 0}, "start_index": input.VideoAudioStart, "duration": float64(input.VideoDurationSeconds)})
			inputs["ref_audios.ref_audio_0"] = []any{"41", 0}
		}
		nodes["7"] = miniMaxH3Node("MiniMaxH3ReferenceToVideo", inputs)
	} else {
		inputs := map[string]any{
			"clip": []any{"2", 0}, "vae": []any{"3", 0}, "prompt": input.Positive,
			"width": input.Width, "height": input.Height, "length": miniMaxH3FrameCount(input.VideoDurationSeconds),
		}
		if len(images) > 0 {
			inputs["first_frame"] = []any{resizedImages[0], 0}
		}
		if len(images) > 1 {
			inputs["last_frame"] = []any{resizedImages[1], 0}
		}
		nodes["7"] = miniMaxH3Node("MiniMaxH3ImageToVideo", inputs)
	}
	videoNodeID := "15"
	if input.VideoRTXEnabled {
		nodes["20"] = miniMaxH3Node("RTXVideoSuperResolution", map[string]any{
			"images":            []any{videoNodeID, 0},
			"resize_type":       "scale by multiplier",
			"resize_type.scale": input.VideoRTXScale,
			"quality":           input.VideoRTXQuality,
		})
		videoNodeID = "20"
	}
	if input.VideoColorMatch {
		nodes["21"] = miniMaxH3Node("LCColorMatch", map[string]any{"image": []any{videoNodeID, 0}, "reference": []any{resizedImages[0], 0}, "method": input.VideoColorMethod, "strength": input.VideoColorStrength})
		videoNodeID = "21"
	}
	frameRate := miniMaxH3VideoFPS
	if input.VideoRIFEEnabled {
		nodes["22"] = miniMaxH3Node("RIFE VFI", map[string]any{"ckpt_name": input.VideoRIFECheckpoint, "frames": []any{videoNodeID, 0}, "clear_cache_after_n_frames": 10, "multiplier": input.VideoRIFEMultiplier, "fast_mode": input.VideoRIFEFastMode, "ensemble": input.VideoRIFEEnsemble, "scale_factor": 1.0, "dtype": input.VideoRIFEDtype, "torch_compile": input.VideoRIFECompile, "batch_size": input.VideoRIFEBatchSize})
		videoNodeID = "22"
		frameRate *= input.VideoRIFEMultiplier
	}
	nodes["17"] = miniMaxH3Node("VHS_VideoCombine", map[string]any{"images": []any{videoNodeID, 0}, "audio": []any{"16", 0}, "frame_rate": frameRate, "loop_count": 0, "filename_prefix": "AI-Gateway-MiniMaxH3", "format": "video/h264-mp4", "pingpong": false, "save_output": true, "pix_fmt": "yuv420p", "crf": input.VideoOutputCRF, "save_metadata": true})
	return nodes, nil
}

func miniMaxH3ApplyLoras(nodes map[string]any, input generationForm, modelNodeID string) {
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
		return
	}
	nodes["8"] = miniMaxH3Node("CR Apply LoRA Stack", map[string]any{"model": []any{modelNodeID, 0}, "clip": []any{"2", 0}, "lora_stack": []any{lastStack, 0}})
}
