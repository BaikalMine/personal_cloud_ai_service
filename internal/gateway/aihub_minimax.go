package gateway

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"ai-access-gateway/internal/promptassistant"
)

const aiHubWorkflowID = "aihub-minimax-h3"

func aiHubModelProfile(name string) (turbo, supported bool) {
	name = strings.ToLower(strings.ReplaceAll(name, "\\", "/"))
	turbo = miniMaxErosBeta5Turbo(name) || miniMaxErosBeta6(name)
	return turbo, turbo || strings.Contains(name, "minimax_h3_ref2va")
}

func (input generationForm) isAIHub() bool { return input.PresetID == aiHubWorkflowID }

func (input generationForm) isAIHubCarouselV2() bool {
	return input.AIHubMode == "carousel" && input.AIHubCarouselPipeline == "v2"
}

func (input generationForm) aiHubNeedsVideo() bool {
	return input.AIHubMode != "photo" && !(input.isAIHubCarouselV2() && input.AIHubCarouselSource == "prompt")
}

func aiHubImages(input generationForm) []string {
	images := []string{}
	for _, name := range append([]string{input.InputImage}, input.ReferenceImages[:]...) {
		if strings.TrimSpace(name) != "" {
			images = append(images, name)
		}
	}
	for _, name := range input.AIHubExtraImages {
		if strings.TrimSpace(name) != "" {
			images = append(images, name)
		}
	}
	return images
}

func normalizeAIHub(input *generationForm) error {
	turbo, supported := aiHubModelProfile(input.ModelName)
	beta6 := miniMaxErosBeta6(input.ModelName)
	if input.AIHubCarouselRefiner == "" {
		input.AIHubCarouselRefiner = "flux"
	}
	if input.isAIHubCarouselV2() && input.AIHubCarouselRefiner != "flux" && input.AIHubCarouselRefiner != "qwen21" {
		return errors.New("AIHUB V2: неизвестная модель доработки")
	}
	if input.AIHubTask == "" {
		input.AIHubTask = "reference"
	}
	if input.AIHubAudioReference && (input.AIHubMode == "carousel" || input.AIHubAudioSource != "original" && input.AIHubAudioSource != "ltx") {
		return errors.New("аудиореференс H3 требует оригинальный звук видео или озвучку LTX")
	}
	if input.AIHubMode == "" {
		input.AIHubMode = "video"
	}
	if input.AIHubAudioSource == "" {
		input.AIHubAudioSource = "native"
	}
	if input.AIHubMask == "" {
		input.AIHubMask = "none"
	}
	if input.AIHubSteps == 0 {
		input.AIHubSteps = 25
		if turbo {
			input.AIHubSteps = 6
		}
		if beta6 {
			input.AIHubSteps = 10
		}
	}
	if input.AIHubPhotoFrames == 0 {
		input.AIHubPhotoFrames = 50
	}
	if input.AIHubPhotoStride == 0 {
		input.AIHubPhotoStride = 8
	}
	if input.AIHubLatentScale == 0 {
		input.AIHubLatentScale = 2
	}
	if input.VideoDurationSeconds == 0 {
		input.VideoDurationSeconds = 5
	}
	if input.VideoOutputCRF == 0 {
		input.VideoOutputCRF = 19
	}
	if input.VideoReferenceSize == "" {
		input.VideoReferenceSize = "match"
	}
	if input.VideoRTXScale == 0 {
		input.VideoRTXScale = 2
	}
	if input.VideoRTXQuality == "" {
		input.VideoRTXQuality = "ULTRA"
	}
	input.VideoMode = miniMaxH3ReferenceMode
	if input.AIHubTask != "reference" {
		if input.AIHubMode != "video" || len(aiHubImages(*input)) == 0 {
			return errors.New("для замены добавьте видеореференс и фото с нужной внешностью или одеждой")
		}
		role := "identity"
		if input.AIHubTask == "replace_clothing" {
			role = "wardrobe_object"
		}
		found := false
		for _, reference := range input.references() {
			found = found || reference.Role == role
		}
		if !found {
			if role == "identity" {
				return errors.New("для замены персонажа назначьте нужному фото роль «Внешность и лицо»")
			}
			return errors.New("для замены одежды назначьте нужному фото роль «Одежда, предмет или материал»")
		}
	}
	if input.AIHubMatchDuration {
		if input.AIHubMode != "video" {
			return errors.New("длина видеореференса доступна только в режиме видео по видео")
		}
		input.VideoReferenceStart = 0
		input.VideoDurationSeconds = max(5, (input.AIHubOutputFrames+23)/24)
	}
	if !miniMaxH3Allowed(input.AIHubMode, "video", "photo", "carousel") || !miniMaxH3Allowed(input.AIHubAudioSource, "native", "original", "ltx") || !miniMaxH3Allowed(input.AIHubMask, "none", "sam3", "sapiens") {
		return errors.New("AIHUB: некорректный режим")
	}
	if err := validateWorkflowManifestInput(aiHubWorkflowManifest(), *input); err != nil {
		return err
	}
	if !supported {
		return errors.New("AIHUB: выберите H3 Eros Max beta6, 10Eros Turbo beta5 или обычную MiniMax H3 Ref2VA")
	}
	if beta6 && (input.AIHubSteps < 8 || input.AIHubSteps > 12) {
		return errors.New("AIHUB: для H3 Eros Max beta6 нужно 8–12 шагов")
	}
	if turbo && !beta6 && (input.AIHubSteps < 6 || input.AIHubSteps > 8) || !turbo && (input.AIHubSteps < 20 || input.AIHubSteps > 25) {
		return errors.New("AIHUB: для Turbo нужно 6–8 шагов, для обычной модели 20–25")
	}
	if input.VAE == "" || input.AudioVAE == "" || input.TextEncoder == "" {
		return errors.New("AIHUB: отсутствуют H3 VAE или текстовая модель")
	}
	if err := validateGenerationPrompt(input.Positive, input.AssistantOriginal, input.AssistantSuggestion); err != nil {
		return err
	}
	if strings.TrimSpace(input.Positive) == "" {
		return errors.New("добавьте описание сцены")
	}
	if input.Width < 32 || input.Height < 32 || input.Width%32 != 0 || input.Height%32 != 0 || input.Width > 4096 || input.Height > 4096 || input.Width*input.Height > miniMaxH3MaxBasePixels {
		return errors.New("AIHUB: стороны кадра должны быть кратны 32, размер базы не больше 4,7 МП")
	}
	if input.VideoDurationSeconds < 5 || input.VideoDurationSeconds > 30 || !input.AIHubMatchDuration && input.VideoDurationSeconds > 15 {
		return errors.New("AIHUB: длительность должна быть от 5 до 15 секунд")
	}
	if input.VideoReferenceStart < 0 || input.VideoReferenceStart > 600 || math.IsNaN(input.VideoReferenceStart) || math.IsInf(input.VideoReferenceStart, 0) {
		return errors.New("AIHUB: некорректное начало видеореференса")
	}
	if !miniMaxH3Allowed(input.VideoReferenceSize, "match", "max") || input.VideoOutputCRF < 14 || input.VideoOutputCRF > 32 {
		return errors.New("AIHUB: некорректные параметры экспорта или референсов")
	}
	if input.aiHubNeedsVideo() && input.InputVideo == "" {
		return errors.New("AIHUB: добавьте видеореференс")
	}
	if input.AIHubMode == "photo" {
		if input.InputImage == "" {
			return errors.New("для видео по фото добавьте первое фото")
		}
		if input.AIHubAudioSource == "original" || input.AIHubMask != "none" {
			return errors.New("звук видеореференса и маски доступны только с видеореференсом")
		}
	}
	if input.AIHubMode == "carousel" && input.InputImage == "" {
		return errors.New("для фотокарусели добавьте первое фото")
	}
	if input.isAIHubCarouselV2() {
		if input.AIHubPhotoCount < 1 || input.AIHubPhotoCount > 24 || math.IsNaN(input.AIHubPhotoRandomness) || math.IsInf(input.AIHubPhotoRandomness, 0) || input.AIHubPhotoRandomness < 0 || input.AIHubPhotoRandomness > 1 {
			return errors.New("AIHUB V2: выберите от 1 до 24 фото и случайность от 0 до 1")
		}
		if input.AIHubDLSSEnabled || input.AIHubFGEnabled || input.VideoRTXEnabled {
			return errors.New("AIHUB V2: DLSS и RTX относятся к видео; отключите их для фотокарусели")
		}
	}
	if input.AIHubMode != "carousel" && input.AIHubAudioSource == "ltx" && strings.TrimSpace(input.AIHubAudioPrompt) == "" {
		return errors.New("добавьте текст для озвучки LTX")
	}
	if input.AIHubMode != "carousel" && input.AIHubVoiceConversion && input.InputAudio == "" {
		return errors.New("для замены голоса добавьте аудиообразец")
	}
	if input.AIHubMask != "none" && input.AIHubMode == "video" && strings.TrimSpace(input.AIHubMaskPrompt) == "" && input.AIHubMask == "sam3" {
		return errors.New("укажите объект для маски SAM3")
	}
	if input.AIHubMode != "carousel" && input.AIHubRemoveBackground && input.InputImage == "" {
		return errors.New("для удаления фона добавьте первое фото")
	}
	if input.AIHubT8 && input.AIHubAudioSource == "native" {
		return errors.New("T8 требует внешнюю озвучку: LTX или оригинальную дорожку")
	}
	if input.AIHubMode == "carousel" && (input.AIHubT8 || input.AIHubVoiceConversion || input.AIHubAudioSource != "native") {
		return errors.New("озвучка доступна только для видео AIHUB")
	}
	if input.VideoRTXEnabled && (input.VideoRTXScale < 1 || input.VideoRTXScale > 2 || math.IsNaN(input.VideoRTXScale) || math.IsInf(input.VideoRTXScale, 0) || !miniMaxH3Allowed(input.VideoRTXQuality, "LOW", "MEDIUM", "HIGH", "ULTRA")) {
		return errors.New("AIHUB: некорректные настройки RTX")
	}
	w, h := aiHubBaseDimensions(*input)
	scale := 1.0
	if input.AIHubMode == "carousel" && !input.isAIHubCarouselV2() {
		scale = input.AIHubLatentScale
	}
	if input.VideoRTXEnabled {
		scale *= input.VideoRTXScale
	}
	if float64(w*h)*scale*scale > miniMaxH3MaxProcessedFramePixels {
		return errors.New("AIHUB: итоговый кадр после апскейла превышает 9 МП; уменьшите базу или масштаб")
	}
	if err := validateAIHubDLSS(*input); err != nil {
		return err
	}
	for i, name := range input.LoraNames {
		if name != "" && (math.IsNaN(input.LoraModel[i]) || math.IsInf(input.LoraModel[i], 0) || input.LoraModel[i] < -10 || input.LoraModel[i] > 10) {
			return errors.New("вес LoRA должен быть от -10 до 10")
		}
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

func aiHubBaseDimensions(input generationForm) (int, int) {
	w, h := input.Width, input.Height
	if input.AIHubMode != "carousel" && input.AIHubHalfResolution {
		w = max(32, int(math.RoundToEven(float64(w)/64))*32)
		h = max(32, int(math.RoundToEven(float64(h)/64))*32)
	}
	return w, h
}

// Only selected branches are materialized. UI switches and RunningHub outputs
// never enter the server prompt, including their unused validation dependencies.
func buildAIHubPrompt(input generationForm) (map[string]any, error) {
	if err := validateAIHubDLSS(input); err != nil {
		return nil, err
	}
	n := map[string]any{}
	add := func(id, kind string, inputs map[string]any) []any {
		n[id] = miniMaxH3Node(kind, inputs)
		return []any{id, 0}
	}
	ref := func(id string, slot int) []any { return []any{id, slot} }
	model := add("1", "UNETLoader", map[string]any{"unet_name": input.ModelName, "weight_dtype": "default"})
	clip := add("2", "CLIPLoader", map[string]any{"clip_name": input.TextEncoder, "type": "minimax", "device": "default"})
	vae := add("3", "VAELoader", map[string]any{"vae_name": input.VAE})
	ava := add("4", "VAELoader", map[string]any{"vae_name": input.AudioVAE})
	model = add("5", "ModelAttentionBackend", map[string]any{"model": model, "attention": "comfy kitchen attention"})
	for i, name := range input.LoraNames {
		if name != "" && input.LoraModel[i] != 0 {
			model = add(fmt.Sprint(200+i), "LoraLoaderModelOnly", map[string]any{"model": model, "lora_name": name, "strength_model": input.LoraModel[i]})
		}
	}
	w, h := aiHubBaseDimensions(input)
	frames := 5 + int(math.Ceil(float64(input.VideoDurationSeconds*24-5)/17))*17
	cap := frames
	if input.AIHubMatchDuration {
		if input.AIHubMode != "video" || input.AIHubOutputFrames < 5 || input.AIHubOutputFrames > 720 {
			return nil, errors.New("AIHUB: длительность видеореференса не определена; повторите проверку")
		}
		frames = 5 + (max(120, input.AIHubOutputFrames)-5+16)/17*17
		cap = input.AIHubOutputFrames
	}
	if input.AIHubMode == "carousel" && !input.isAIHubCarouselV2() {
		cap = input.AIHubPhotoFrames
	}
	var video []any
	if input.aiHubNeedsVideo() {
		video = add("20", "VHS_LoadVideo", map[string]any{"video": input.InputVideo, "force_rate": 24, "custom_width": 0, "custom_height": 0, "frame_load_cap": cap, "skip_first_frames": int(math.Round(input.VideoReferenceStart * 24)), "select_every_nth": 1, "format": "None"})
	}
	if input.AIHubMode == "video" && input.AIHubMask != "none" {
		inverted := add("21", "ImageInvert", map[string]any{"image": video})
		var mask []any
		if input.AIHubMask == "sam3" {
			if input.AIHubMaskModel == "" {
				return nil, errors.New("SAM3: модель не найдена")
			}
			add("22", "CheckpointLoaderSimple", map[string]any{"ckpt_name": input.AIHubMaskModel})
			text := add("23", "CLIPTextEncode", map[string]any{"clip": ref("22", 1), "text": input.AIHubMaskPrompt})
			first := add("24", "ImageFromBatch", map[string]any{"image": video, "batch_index": 0, "length": 1})
			detect := add("25", "SAM3_Detect", map[string]any{"model": ref("22", 0), "image": first, "conditioning": text, "threshold": 0.35, "refine_iterations": 4, "individual_masks": false})
			track := add("26", "SAM3_VideoTrack", map[string]any{"images": video, "model": ref("22", 0), "initial_mask": detect, "conditioning": text, "detection_threshold": 0.35, "detect_interval": 1, "max_objects": 1})
			mask = add("27", "SAM3_TrackToMask", map[string]any{"track_data": track, "object_indices": "0"})
		} else {
			if input.AIHubMaskModel == "" {
				return nil, errors.New("Sapiens2: модель не найдена")
			}
			m := add("22", "Sapiens2Loader", map[string]any{"checkpoint": input.AIHubMaskModel})
			s := add("23", "Sapiens2Seg", map[string]any{"image": inverted, "sapiens2_model": m, "frames_per_batch": 1})
			mask = add("27", "Sapiens2SegExtract", map[string]any{"class_id_mask": s, "class_name": "Background", "invert": true, "num_classes": 29})
		}
		expand, blur := 18, 4
		if input.AIHubMask == "sapiens" {
			expand, blur = 28, 8
		}
		mask = add("28", "GrowMaskWithBlur", map[string]any{"mask": mask, "expand": expand, "incremental_expandrate": 0, "tapered_corners": true, "flip_input": false, "blur_radius": blur, "lerp_alpha": 0.9, "decay_factor": 1, "fill_holes": false})
		video = add("29", "ImageCompositeMasked", map[string]any{"destination": video, "source": inverted, "mask": mask, "x": 0, "y": 0, "resize_source": false})
	}
	if input.AIHubMode == "video" {
		video = add("30", "ResizeImageMaskNode", map[string]any{"input": video, "resize_type": "scale total pixels", "resize_type.megapixels": 1.05, "scale_method": "bicubic"})
	}
	if video != nil {
		video = add("31", "GatewayAIHubReferenceFrames", map[string]any{"images": video})
	}
	prompt := input.Positive
	if task := promptassistant.AIHubTaskInstruction(input.AIHubTask); task != "" {
		roles := ""
		for i, reference := range input.references() {
			roles += fmt.Sprintf("\n<Picture %d>: %s.", i+1, reference.Role)
		}
		prompt = task + "\nDeclared picture roles (do not transfer other attributes):" + roles + "\n\n" + prompt
	}
	conditioning := map[string]any{"clip": clip, "vae": vae, "audio_vae": ava, "prompt": prompt, "width": w, "height": h, "length": frames, "ref_image_size": input.VideoReferenceSize}
	if video != nil {
		conditioning["ref_videos.ref_video_0"] = video
	}
	if input.AIHubMode == "carousel" && video != nil {
		conditioning["length"] = ref("31", 1)
	}
	for i, name := range aiHubImages(input) {
		im := add(fmt.Sprint(40+i), "LoadImage", map[string]any{"image": name})
		if i == 0 && input.AIHubMode != "carousel" && input.AIHubRemoveBackground {
			im = add("49", "Image Rembg (Remove Background)", map[string]any{"images": im, "transparency": true, "model": "u2net", "post_processing": false, "only_mask": false, "alpha_matting": false, "alpha_matting_foreground_threshold": 240, "alpha_matting_background_threshold": 10, "alpha_matting_erode_size": 10, "background_color": "none"})
		}
		conditioning[fmt.Sprintf("ref_images.ref_image_%d", i)] = im
	}
	add("7", "MiniMaxH3ReferenceToVideo", conditioning)
	latent := ref("7", 1)
	var audio []any
	if input.AIHubMode != "carousel" && input.AIHubAudioSource == "ltx" {
		if input.AIHubLTXModel == "" || input.AIHubGemma == "" || input.AIHubAudioVAE == "" {
			return nil, errors.New("LTX: отсутствует модель, Gemma или аудиодекодер")
		}
		lm := add("100", "UNETLoader", map[string]any{"unet_name": input.AIHubLTXModel, "weight_dtype": "default"})
		lm = add("101", "LTXVAudioOnlyModel", map[string]any{"model": lm})
		lc := add("102", "CLIPLoader", map[string]any{"clip_name": input.AIHubGemma, "type": "ltxv", "device": "default"})
		lav := add("103", "VAELoader", map[string]any{"vae_name": input.AIHubAudioVAE})
		var prompt any = input.AIHubAudioPrompt
		if input.AIHubAudioEnhance {
			prompt = add("104", "TextGenerateLTX2Prompt", map[string]any{"clip": lc, "prompt": prompt, "max_length": 600, "sampling_mode": "on", "sampling_mode.temperature": 0.01, "sampling_mode.top_k": 0, "sampling_mode.top_p": 1.0, "sampling_mode.min_p": 0.0, "sampling_mode.repetition_penalty": 1.15, "sampling_mode.seed": input.Seed, "thinking": false, "use_default_template": true})
		}
		positive := add("105", "CLIPTextEncode", map[string]any{"clip": lc, "text": prompt})
		negative := add("106", "CLIPTextEncode", map[string]any{"clip": lc, "text": input.AIHubAudioNegative})
		add("107", "LTXVConditioning", map[string]any{"positive": positive, "negative": negative, "frame_rate": 24})
		guider := add("108", "CFGGuider", map[string]any{"model": lm, "positive": ref("107", 0), "negative": ref("107", 1), "cfg": 1.0})
		vl := add("109", "LTXVAudioOnlyEmptyVideoLatent", map[string]any{})
		al := add("110", "LTXVEmptyLatentAudio", map[string]any{"audio_vae": lav, "frames_number": 1 + int(math.Ceil(float64(frames-1)/8))*8, "frame_rate": 24, "batch_size": 1})
		av := add("111", "LTXVConcatAVLatent", map[string]any{"video_latent": vl, "audio_latent": al})
		noise := add("112", "RandomNoise", map[string]any{"noise_seed": input.Seed})
		sampler := add("113", "KSamplerSelect", map[string]any{"sampler_name": "euler_ancestral"})
		sigmas := add("114", "ManualSigmas", map[string]any{"sigmas": "1.0, 0.99375, 0.9875, 0.98125, 0.975, 0.909375, 0.725, 0.421875, 0.0"})
		samples := add("115", "SamplerCustomAdvanced", map[string]any{"noise": noise, "guider": guider, "sampler": sampler, "sigmas": sigmas, "latent_image": av})
		add("116", "LTXVSeparateAVLatent", map[string]any{"av_latent": samples})
		audio = add("117", "LTXVAudioVAEDecode", map[string]any{"samples": ref("116", 1), "audio_vae": lav})
		audio = add("118", "TrimAudioDuration", map[string]any{"audio": audio, "start_index": 0, "duration": float64(frames) / 24})
	} else if input.AIHubMode == "video" && input.AIHubAudioSource == "original" {
		audio = ref("20", 2)
	}
	if input.AIHubAudioReference {
		if audio == nil || input.AIHubMode == "carousel" {
			return nil, errors.New("AIHUB: отсутствует источник аудиореференса H3")
		}
		if input.AIHubAudioSource == "original" {
			conditioning["ref_video_audios.ref_video_audio_0"] = audio
		} else {
			conditioning["ref_audios.ref_audio_0"] = audio
		}
	}
	if input.AIHubMode != "carousel" && input.AIHubT8 {
		latent = add("120", "MiniMaxH3AudioLatentControlT8", map[string]any{"av_latent": latent, "source_audio": audio, "audio_vae": ava, "mode": "lock", "strength": 0.0})
	}
	noise := add("10", "RandomNoise", map[string]any{"noise_seed": input.Seed})
	turbo, supported := aiHubModelProfile(input.ModelName)
	if !supported {
		return nil, errors.New("AIHUB: несовместимая модель")
	}
	samplerName, scheduler := "er_sde", "beta57"
	if miniMaxErosBeta6(input.ModelName) {
		samplerName, scheduler = "euler", "simple"
		model = add("9", "MiniMaxH3SigmaShift", map[string]any{"model": model, "shift_video": 10.0, "shift_audio": 3.0})
	} else if !turbo {
		samplerName, scheduler = "euler", "simple"
		model = add("9", "MiniMaxH3SigmaShift", map[string]any{"model": model, "shift_video": 11.0, "shift_audio": 3.0})
	}
	sampler := add("11", "KSamplerSelect", map[string]any{"sampler_name": samplerName})
	steps := input.AIHubSteps
	if input.AIHubMode == "carousel" && turbo && !miniMaxErosBeta6(input.ModelName) {
		steps = 8
	}
	sigmas := add("12", "BasicScheduler", map[string]any{"model": model, "scheduler": scheduler, "steps": steps, "denoise": 1.0})
	guider := add("13", "BasicGuider", map[string]any{"model": model, "conditioning": ref("7", 0)})
	add("14", "SamplerCustomAdvanced", map[string]any{"noise": noise, "guider": guider, "sampler": sampler, "sigmas": sigmas, "latent_image": latent})
	samples := ref("14", 1)
	if input.AIHubMode == "carousel" && !input.isAIHubCarouselV2() {
		if input.AIHubLatentModel == "" {
			return nil, errors.New("не найдена модель H3 latent-upscaler")
		}
		add("60", "LTXVSeparateAVLatent", map[string]any{"av_latent": samples})
		up := add("61", "MinimaxH3LatentUpscaler3D", map[string]any{"latent": ref("60", 0), "model_name": input.AIHubLatentModel, "mode": "scale by multiplier", "mode.scale": input.AIHubLatentScale, "align": 32, "enable_temporal_chunking": true, "force_unload": true, "device": "cuda", "precision": "bf16"})
		av := add("62", "LTXVConcatAVLatent", map[string]any{"video_latent": up, "audio_latent": ref("60", 1)})
		sig := add("63", "ManualSigmas", map[string]any{"sigmas": "0.72, 0.55, 0.30, 0.10, 0.00"})
		samples = add("64", "SamplerCustomAdvanced", map[string]any{"noise": noise, "guider": guider, "sampler": sampler, "sigmas": sig, "latent_image": av})
	}
	images := add("15", "VAEDecode", map[string]any{"samples": samples, "vae": vae})
	if input.AIHubMatchDuration {
		images = add("17", "ImageFromBatch", map[string]any{"image": images, "batch_index": 0, "length": input.AIHubOutputFrames})
	}
	if input.AIHubMode == "carousel" && !input.isAIHubCarouselV2() {
		images = add("66", "ImageFromBatch", map[string]any{"image": images, "batch_index": 0, "length": ref("31", 2)})
		images = add("65", "VHS_SelectEveryNthImage", map[string]any{"images": images, "select_every_nth": input.AIHubPhotoStride, "skip_first_images": input.AIHubPhotoSkip})
	}
	if input.isAIHubCarouselV2() {
		if video != nil {
			images = add("319", "ImageFromBatch", map[string]any{"image": images, "batch_index": 0, "length": ref("31", 2)})
		}
		return buildAIHubCarouselV2(input, n, images)
	}
	frameRate := 24
	streamVideo := input.AIHubMode != "carousel" && (input.AIHubFGEnabled || input.AIHubDLSSEnabled)
	if input.VideoRTXEnabled && !streamVideo {
		images = add("70", "RTXVideoSuperResolution", map[string]any{"images": images, "resize_type": "scale by multiplier", "resize_type.scale": input.VideoRTXScale, "quality": input.VideoRTXQuality})
	}
	if input.AIHubDLSSEnabled && !streamVideo {
		settings := add("71", "DLSS5Settings", aiHubDLSSSettings(input.AIHubDLSSStrength, true))
		images = add("72", "GatewayAIHubEnhanceImages", map[string]any{"images": images, "settings": settings})
	}
	if input.AIHubMode == "carousel" {
		add("90", "SaveImage", map[string]any{"images": images, "filename_prefix": "AIHub/Carousel"})
		return n, nil
	}
	if audio == nil {
		audio = add("16", "VAEDecodeAudio", map[string]any{"samples": samples, "vae": ava})
	}
	if input.AIHubVoiceConversion {
		voice := add("80", "LoadAudio", map[string]any{"audio": input.InputAudio})
		audio = add("81", "SeedVCRun", map[string]any{"source_audio": audio, "ref_audio": voice, "steps": 30, "speed": 1.0, "inference_cfg_rate": 0.7, "f0_condition": false, "auto_f0_adjust": true, "pitch_shift": 0, "unload_model": true})
	}
	if input.AIHubMatchDuration {
		audio = add("18", "TrimAudioDuration", map[string]any{"audio": audio, "start_index": 0, "duration": float64(input.AIHubOutputFrames) / 24})
	}
	if streamVideo {
		multiplier := 1
		if input.AIHubFGEnabled {
			multiplier = input.AIHubFGMultiplier
		}
		streamInputs := map[string]any{"images": images, "audio": audio, "fg_multiplier": multiplier,
			"nr_enabled": input.AIHubDLSSEnabled, "rtx_enabled": input.VideoRTXEnabled,
			"rtx_scale": input.VideoRTXScale, "rtx_quality": input.VideoRTXQuality,
			"crf": input.VideoOutputCRF, "filename_prefix": "AIHub/Video"}
		if !input.VideoRTXEnabled {
			streamInputs["rtx_scale"] = 1.0
			streamInputs["rtx_quality"] = "HIGH"
		}
		if input.AIHubDLSSEnabled {
			streamInputs["settings"] = add("71", "DLSS5Settings", aiHubDLSSSettings(input.AIHubDLSSStrength, false))
		}
		add("90", "GatewayAIHubVideoStream", streamInputs)
	} else {
		add("90", "VHS_VideoCombine", map[string]any{"images": images, "audio": audio, "frame_rate": frameRate, "loop_count": 0, "filename_prefix": "AIHub/Video", "format": "video/h264-mp4", "pix_fmt": "yuv420p", "crf": input.VideoOutputCRF, "save_metadata": true, "trim_to_audio": false, "pingpong": false, "save_output": true})
	}
	return n, nil
}

func resolveAIHubModels(input *generationForm, info map[string]comfyNodeInfo) error {
	resolve := func(node, field, filename string) (string, error) {
		for _, name := range comfyChoiceStrings(info, node, field) {
			normalized := strings.ReplaceAll(name, "\\", "/")
			if normalized == filename || strings.HasSuffix(normalized, "/"+filename) {
				return name, nil
			}
		}
		return "", fmt.Errorf("AIHUB: не установлен %s", filename)
	}
	var err error
	if input.isAIHubCarouselV2() {
		return resolveAIHubCarouselV2Models(input, info)
	}
	if input.AIHubMode == "carousel" {
		input.AIHubLatentModel, err = resolve("MinimaxH3LatentUpscaler3D", "model_name", "minimax_h3_latent_upscaler_3d_bf16.safetensors")
		return err
	}
	if input.AIHubAudioSource == "ltx" {
		for _, item := range []struct {
			node, field, file string
			target            *string
		}{
			{"UNETLoader", "unet_name", "ltx-2.5-22b-distilled-transformer-comfy-int8-convrot.safetensors", &input.AIHubLTXModel},
			{"CLIPLoader", "clip_name", "gemma4-12b-with-proj-ltx-2.5-bf16.safetensors", &input.AIHubGemma},
			{"VAELoader", "vae_name", "ltx-2.5-audio-vae-bf16.safetensors", &input.AIHubAudioVAE},
		} {
			*item.target, err = resolve(item.node, item.field, item.file)
			if err != nil {
				return err
			}
		}
	}
	if input.AIHubMask == "sam3" {
		input.AIHubMaskModel, err = resolve("CheckpointLoaderSimple", "ckpt_name", "sam3.1_multiplex_fp16.safetensors")
	}
	if input.AIHubMask == "sapiens" {
		input.AIHubMaskModel, err = resolve("Sapiens2Loader", "checkpoint", "sapiens2_1b_seg.safetensors")
	}
	return err
}
