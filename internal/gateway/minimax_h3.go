package gateway

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	miniMaxH3FrameMode      = "frames"
	miniMaxH3ReferenceMode  = "references"
	miniMaxH3VideoFPS       = 24
	miniMaxH3MinimumSeconds = 5
	miniMaxH3DefaultQuality = 720
)

// normalizeMiniMaxH3 keeps the public controls intentionally small while
// preserving the model's real constraints: dimensions are divisible by 32 and
// the temporal latent must contain 4 + 17*n frames.
func normalizeMiniMaxH3(input *generationForm) error {
	if input.VideoAspect != "" || input.VideoQuality != 0 {
		if input.VideoAspect == "" {
			input.VideoAspect = "portrait"
		}
		if input.VideoQuality == 0 {
			input.VideoQuality = miniMaxH3DefaultQuality
		}
		width, height, err := miniMaxH3VideoDimensions(input.VideoAspect, input.VideoQuality)
		if err != nil {
			return err
		}
		input.Width, input.Height = width, height
		input.VideoResolution = fmt.Sprintf("%s-%dp", input.VideoAspect, input.VideoQuality)
	} else {
		// Preserve existing submitted presets while the public UI migrates to
		// orientation plus one of the standard video-quality profiles.
		switch input.VideoResolution {
		case "", "portrait":
			input.Width, input.Height = 768, 1344
			input.VideoResolution = "portrait"
		case "landscape":
			input.Width, input.Height = 1344, 768
		case "square":
			input.Width, input.Height = 1024, 1024
		default:
			return errors.New("выберите корректный формат видео")
		}
	}
	if input.VideoDurationSeconds == 0 {
		input.VideoDurationSeconds = miniMaxH3MinimumSeconds
	}
	if input.VideoDurationSeconds != 5 && input.VideoDurationSeconds != 10 && input.VideoDurationSeconds != 15 {
		return errors.New("длительность MiniMax H3 может быть 5, 10 или 15 секунд")
	}
	if input.VideoSteps == 0 {
		input.VideoSteps = 25
	}
	if input.VideoSteps < 20 || input.VideoSteps > 25 {
		return errors.New("для MiniMax H3 выберите от 20 до 25 шагов")
	}
	if input.VideoMode == "" {
		input.VideoMode = miniMaxH3FrameMode
	}
	if input.VideoMode != miniMaxH3FrameMode && input.VideoMode != miniMaxH3ReferenceMode {
		return errors.New("выберите корректный режим MiniMax H3")
	}
	if input.VideoReferenceSize == "" {
		input.VideoReferenceSize = "match"
	}
	if input.VideoReferenceSize != "match" && input.VideoReferenceSize != "max" {
		return errors.New("выберите корректное качество референсов")
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
	input.Scheduler = "simple"
	return nil
}

// miniMaxH3VideoDimensions maps common delivery-quality standards to the
// nearest MiniMax-compatible frame. MiniMax requires both dimensions to be
// divisible by 32, so 360p and 2160p use the closest valid dimensions.
func miniMaxH3VideoDimensions(aspect string, quality int) (int, int, error) {
	var shortSide, longSide int
	switch quality {
	case 360:
		shortSide, longSide = 352, 640
	case 480:
		shortSide, longSide = 480, 864
	case 720:
		shortSide, longSide = 704, 1280
	case 1080:
		shortSide, longSide = 1088, 1920
	case 1440:
		shortSide, longSide = 1440, 2560
	case 2160:
		shortSide, longSide = 2176, 3840
	default:
		return 0, 0, errors.New("выберите качество видео: 360, 480, 720, 1080, 1440 или 2160")
	}
	switch aspect {
	case "portrait":
		return shortSide, longSide, nil
	case "landscape":
		return longSide, shortSide, nil
	case "square":
		return shortSide, shortSide, nil
	default:
		return 0, 0, errors.New("выберите ориентацию видео")
	}
}

func miniMaxH3FrameCount(seconds int) int {
	frames := int(math.Round(float64(seconds * miniMaxH3VideoFPS)))
	return frames + ((5 - frames%17) % 17)
}

func miniMaxH3Node(classType string, inputs map[string]any) map[string]any {
	return map[string]any{"class_type": classType, "inputs": inputs}
}

// buildMiniMaxH3Prompt is a compact server-side transcription of the working
// MiniMax H3 workflow. It intentionally omits optional RIFE/RTX branches so a
// normal video request has a single predictable model and output path.
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

	nodes := map[string]any{
		"1":  miniMaxH3Node("UNETLoader", map[string]any{"unet_name": modelName, "weight_dtype": "default"}),
		"2":  miniMaxH3Node("CLIPLoader", map[string]any{"clip_name": input.TextEncoder, "type": "minimax", "device": "default"}),
		"3":  miniMaxH3Node("VAELoader", map[string]any{"vae_name": input.VAE}),
		"4":  miniMaxH3Node("VAELoader", map[string]any{"vae_name": input.AudioVAE}),
		"8":  miniMaxH3Node("MiniMaxH3MemoryEfficientSageAttentionPatch", map[string]any{"model": []any{"1", 0}}),
		"9":  miniMaxH3Node("MiniMaxH3SigmaShift", map[string]any{"model": []any{"8", 0}, "shift_video": 11, "shift_audio": 3}),
		"10": miniMaxH3Node("RandomNoise", map[string]any{"noise_seed": input.Seed}),
		"11": miniMaxH3Node("KSamplerSelect", map[string]any{"sampler_name": "res_multistep"}),
		"12": miniMaxH3Node("BasicScheduler", map[string]any{"model": []any{"9", 0}, "scheduler": "simple", "steps": input.VideoSteps, "denoise": 1}),
		"13": miniMaxH3Node("BasicGuider", map[string]any{"model": []any{"9", 0}, "conditioning": []any{"7", 0}}),
		"14": miniMaxH3Node("SamplerCustomAdvanced", map[string]any{"noise": []any{"10", 0}, "guider": []any{"13", 0}, "sampler": []any{"11", 0}, "sigmas": []any{"12", 0}, "latent_image": []any{"7", 1}}),
		"15": miniMaxH3Node("VAEDecode", map[string]any{"samples": []any{"14", 0}, "vae": []any{"3", 0}}),
		"16": miniMaxH3Node("VAEDecodeAudio", map[string]any{"samples": []any{"14", 0}, "vae": []any{"4", 0}}),
		"17": miniMaxH3Node("VHS_VideoCombine", map[string]any{"images": []any{"15", 0}, "audio": []any{"16", 0}, "frame_rate": miniMaxH3VideoFPS, "loop_count": 0, "filename_prefix": "AI-Gateway-MiniMaxH3", "format": "video/h264-mp4", "pingpong": false, "save_output": true, "pix_fmt": "yuv420p", "crf": 19, "save_metadata": true}),
	}

	images := input.images()
	for index, image := range images {
		nodeID := fmt.Sprintf("%d", 30+index)
		nodes[nodeID] = miniMaxH3Node("LoadImage", map[string]any{"image": image})
	}
	if input.VideoMode == miniMaxH3ReferenceMode {
		inputs := map[string]any{
			"clip": []any{"2", 0}, "vae": []any{"3", 0}, "audio_vae": []any{"4", 0},
			"prompt": input.Positive, "width": input.Width, "height": input.Height,
			"length": miniMaxH3FrameCount(input.VideoDurationSeconds), "ref_image_size": input.VideoReferenceSize,
		}
		for index := range images {
			inputs[fmt.Sprintf("ref_images.ref_image_%d", index)] = []any{fmt.Sprintf("%d", 30+index), 0}
		}
		nodes["7"] = miniMaxH3Node("MiniMaxH3ReferenceToVideo", inputs)
	} else {
		inputs := map[string]any{
			"clip": []any{"2", 0}, "vae": []any{"3", 0}, "prompt": input.Positive,
			"width": input.Width, "height": input.Height, "length": miniMaxH3FrameCount(input.VideoDurationSeconds),
		}
		if len(images) > 0 {
			inputs["first_frame"] = []any{"30", 0}
		}
		if len(images) > 1 {
			inputs["last_frame"] = []any{"31", 0}
		}
		nodes["7"] = miniMaxH3Node("MiniMaxH3ImageToVideo", inputs)
	}
	return nodes, nil
}
