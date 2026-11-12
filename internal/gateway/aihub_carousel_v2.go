package gateway

import (
	"errors"
	"strconv"
	"strings"
)

// V2 uses one H3 pass and a separate, sequential FLUX reference-edit pass.
// Its RH-specific LoRAs are not silently substituted or applied to the H3 model.
func buildAIHubCarouselV2(input generationForm, graph map[string]any, images []any) (map[string]any, error) {
	qwen := input.AIHubCarouselRefiner == "qwen21"
	if qwen && (input.AIHubQwenModel == "" || input.AIHubQwenEncoder == "" || input.AIHubQwenVAE == "") {
		return nil, errors.New("AIHUB V2: не найдены модель Qwen 2.1, её энкодер или VAE")
	}
	if !qwen && (input.AIHubFluxModel == "" || input.AIHubFluxEncoder == "" || input.AIHubFluxVAE == "") {
		return nil, errors.New("AIHUB V2: не найдены модель FLUX.2, её энкодер или VAE")
	}
	add := func(id, kind string, inputs map[string]any) []any {
		graph[id] = miniMaxH3Node(kind, inputs)
		return []any{id, 0}
	}
	ref := func(id string, slot int) []any { return []any{id, slot} }
	add("300", "GetImageSize", map[string]any{"image": images})
	count := add("301", "SimpleMath+", map[string]any{"a": ref("300", 2), "value": "min(a, " + strconv.Itoa(input.AIHubPhotoCount) + ")"})
	images = add("302", "RandomImageFromBatch", map[string]any{"input": images, "start_index": 0, "end_index": -1, "num_frames": count, "randomness": input.AIHubPhotoRandomness, "min_distance": 0, "max_distance": 0, "seed": input.Seed})
	images = add("303", "ConstrainImage|pysssss", map[string]any{"images": images, "max_width": 2048, "max_height": 2048, "min_width": 0, "min_height": 0, "crop_if_required": "no"})
	images = add("304", "RebatchImages", map[string]any{"images": images, "batch_size": 1})
	var output []any
	if qwen {
		model := add("305", "UNETLoader", map[string]any{"unet_name": input.AIHubQwenModel, "weight_dtype": "default"})
		clip := add("306", "CLIPLoader", map[string]any{"clip_name": input.AIHubQwenEncoder, "type": "qwen_image", "device": "default"})
		vae := add("307", "VAELoader", map[string]any{"vae_name": input.AIHubQwenVAE})
		// Native Qwen conditioning owns the 64-channel latent and reference geometry.
		add("308", "TextEncodeQwenImage21", map[string]any{"clip": clip, "vae": vae, "prompt": aiHubCarouselRefinementPrompt, "negative_prompt": "", "resolution": 0, "images.image_1": images})
		samples := add("315", "KSampler", map[string]any{"model": model, "positive": ref("308", 0), "negative": ref("308", 1), "latent_image": ref("308", 2), "seed": input.Seed, "steps": 25, "cfg": 1.0, "sampler_name": "euler", "scheduler": "simple", "denoise": 1.0})
		output = add("316", "VAEDecode", map[string]any{"samples": samples, "vae": vae})
		add("320", "GetImageSize", map[string]any{"image": images})
		// Qwen rounds to 32px; restore the exact frame size before color matching.
		output = add("321", "ImageScale", map[string]any{"image": output, "upscale_method": "lanczos", "width": ref("320", 0), "height": ref("320", 1), "crop": "disabled"})
	} else {
		model := add("305", "UNETLoader", map[string]any{"unet_name": input.AIHubFluxModel, "weight_dtype": "default"})
		loader := "CLIPLoader"
		if input.AIHubFluxEncoderGGUF {
			loader = "CLIPLoaderGGUF"
		}
		clip := add("306", loader, map[string]any{"clip_name": input.AIHubFluxEncoder, "type": "flux2"})
		vae := add("307", "VAELoader", map[string]any{"vae_name": input.AIHubFluxVAE})
		positive := add("308", "CLIPTextEncode", map[string]any{"clip": clip, "text": aiHubCarouselRefinementPrompt})
		negative := add("309", "ConditioningZeroOut", map[string]any{"conditioning": positive})
		encoded := add("310", "VAEEncode", map[string]any{"pixels": images, "vae": vae})
		positive = add("311", "ReferenceLatent", map[string]any{"conditioning": positive, "latent": encoded})
		negative = add("312", "ReferenceLatent", map[string]any{"conditioning": negative, "latent": encoded})
		add("313", "GetImageSize", map[string]any{"image": images})
		latent := add("314", "EmptyFlux2LatentImage", map[string]any{"width": ref("313", 0), "height": ref("313", 1), "batch_size": 1})
		samples := add("315", "KSampler", map[string]any{"model": model, "positive": positive, "negative": negative, "latent_image": latent, "seed": input.Seed, "steps": 8, "cfg": 1.0, "sampler_name": "euler", "scheduler": "simple", "denoise": 1.0})
		output = add("316", "VAEDecode", map[string]any{"samples": samples, "vae": vae})
	}
	output = add("317", "ColorMatch", map[string]any{"image_ref": images, "image_target": output, "method": "mkl", "strength": 1.0, "multithread": false})
	output = add("318", "FastFilmGrain", map[string]any{"images": output, "grain_intensity": 0.006, "saturation_mix": 0.0, "batch_size": 1})
	add("90", "SaveImage", map[string]any{"images": output, "filename_prefix": "AIHub/CarouselV2"})
	return graph, nil
}

func resolveAIHubCarouselV2Models(input *generationForm, info map[string]comfyNodeInfo) error {
	find := func(node, field, filename string) string {
		return firstMatchingModel(comfyChoiceStrings(info, node, field), func(name string) bool {
			normalized := strings.ReplaceAll(name, "\\", "/")
			return normalized == filename || strings.HasSuffix(normalized, "/"+filename)
		})
	}
	for _, name := range []string{"RandomImageFromBatch", "RebatchImages", "FastFilmGrain", "ColorMatch"} {
		if _, ok := info[name]; !ok {
			return errors.New("AIHUB V2: отсутствует нода " + name)
		}
	}
	if input.AIHubCarouselRefiner == "qwen21" {
		input.AIHubQwenModel = find("UNETLoader", "unet_name", "qwen-image-2.1-UC-int8_convrot.safetensors")
		if input.AIHubQwenModel == "" {
			input.AIHubQwenModel = firstMatchingModel(comfyChoiceStrings(info, "UNETLoader", "unet_name"), isQwen21ModelPath)
		}
		input.AIHubQwenEncoder = find("CLIPLoader", "clip_name", "qwen3vl_8b_int8_convrot.safetensors")
		input.AIHubQwenVAE = find("VAELoader", "vae_name", "qwen_image_2.1_vae_bf16.safetensors")
		if input.AIHubQwenModel == "" || input.AIHubQwenEncoder == "" || input.AIHubQwenVAE == "" {
			return errors.New("AIHUB V2: установите Qwen Image 2.1, её Qwen3-VL энкодер и VAE")
		}
		if _, ok := info["TextEncodeQwenImage21"]; !ok {
			return errors.New("AIHUB V2: отсутствует нода TextEncodeQwenImage21")
		}
		return nil
	}
	input.AIHubFluxModel = find("UNETLoader", "unet_name", "flux2Klein_9b.safetensors")
	input.AIHubFluxEncoder = find("CLIPLoader", "clip_name", "qwen_3_8b_fp8mixed.safetensors")
	input.AIHubFluxEncoderGGUF = false
	if input.AIHubFluxEncoder == "" {
		input.AIHubFluxEncoder = find("CLIPLoaderGGUF", "clip_name", "Qwen3-8B-abliterated-bf16.gguf")
		input.AIHubFluxEncoderGGUF = input.AIHubFluxEncoder != ""
	}
	input.AIHubFluxVAE = find("VAELoader", "vae_name", "flux2-vae.safetensors")
	if input.AIHubFluxModel == "" || input.AIHubFluxEncoder == "" || input.AIHubFluxVAE == "" {
		return errors.New("AIHUB V2: установите FLUX.2 Klein 9B, Qwen3 8B энкодер и flux2-vae")
	}
	for _, name := range []string{"RandomImageFromBatch", "RebatchImages", "ReferenceLatent", "EmptyFlux2LatentImage", "FastFilmGrain", "ColorMatch"} {
		if _, ok := info[name]; !ok {
			return errors.New("AIHUB V2: отсутствует нода " + name)
		}
	}
	return nil
}

var aiHubCarouselRefinementPrompt = strings.TrimSpace(`Enhance the supplied photograph with restrained, source-faithful detail refinement. Preserve the exact same person and scene. Keep facial identity, eye shape, nose, lips, jawline, skin tone, age, hairstyle, expression and body proportions unchanged. Preserve the original pose, clothing, framing, camera perspective, background objects, lighting and colors.
Carefully preserve small details already visible in the source. Keep the original manicure and pedicure exactly as shown, including nail shape, length, color, finish and any existing patterns. Gently clarify visible boundaries between nails, skin and highlights without changing the design. If the nails are bare, keep them bare. Preserve finger and toe anatomy, spacing and natural joint creases. Do not invent details that are not visible.
Keep skin realistic and appropriate to the subject's original age. Retain subtle, irregular pores, soft tonal variation and natural creases where the pose creates them. Preserve existing freckles and distinguishing skin marks in their original locations. Do not add wrinkles, exaggerated pores, extra freckles, blemishes or coarse texture. Avoid repetitive skin patterns, etched lines, artificial microdetail, waxy smoothing and a plastic appearance.
Reduce blur gently and clarify existing details in hair, clothing and background surfaces. Keep detail consistent with the original focus and depth of field; do not make naturally out-of-focus areas unnaturally sharp. Where information is unclear, preserve a subtle appearance rather than inventing structures.
Maintain the original photographic character. Avoid glamour retouching, excessive sharpening, halos, artificial gloss and a synthetic HDR look. Make only restrained improvements.`)
