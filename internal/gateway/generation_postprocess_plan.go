package gateway

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

const (
	postprocessPlanVersion           = 1
	postprocessMinSide               = 64
	postprocessMaxSide               = 4096
	postprocessMaxFramePixels        = 16000000
	postprocessMaxSourceFrames       = 450
	postprocessMaxPixelFrames        = 200000000
	postprocessMaxWorkingTensorBytes = 8 << 30
	postprocessMaxScale              = 2
	postprocessMaxSourceFPS          = 60
	postprocessMaxOutputFPS          = 120
	postprocessMaxSeconds            = 30
)

// These facts must come from a bounded full decode of the retained source bytes,
// not form values or the stream-metadata-only gallery measurement.
type postprocessSourceFacts struct {
	MediaType         string
	Width             int
	Height            int
	Frames            int
	FPSNumerator      int64
	FPSDenominator    int64
	ConstantFrameRate bool
	SquarePixels      bool
	OrientationNormal bool
	Opaque            bool
	DecodeVerified    bool
}

type postprocessOptions struct {
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	Interpolate    int     `json:"interpolate"`
	RTXQuality     string  `json:"rtx_quality,omitempty"`
	RIFECheckpoint string  `json:"rife_checkpoint,omitempty"`
	ColorMethod    string  `json:"color_method,omitempty"`
	ColorStrength  float64 `json:"color_strength,omitempty"`
}

type postprocessPlan struct {
	Version            int                `json:"version"`
	MediaType          string             `json:"media_type"`
	Options            postprocessOptions `json:"options"`
	SourceFrames       int                `json:"source_frames"`
	OutputFrames       int                `json:"output_frames"`
	FPSNumerator       int64              `json:"fps_numerator,omitempty"`
	FPSDenominator     int64              `json:"fps_denominator,omitempty"`
	OutputPixelFrames  int64              `json:"output_pixel_frames"`
	WorkingTensorBytes int64              `json:"working_tensor_bytes"`
	RepeatedTailFrames int                `json:"repeated_tail_frames"`
}

// planPostprocess is deliberately independent from generationForm: no prompt,
// seed, model family, sampler, arbitrary graph or filesystem path is accepted.
func planPostprocess(source postprocessSourceFacts, options postprocessOptions) (postprocessPlan, error) {
	invalid := func(reason string) (postprocessPlan, error) {
		return postprocessPlan{}, fmt.Errorf("postprocess: %s", reason)
	}
	if !source.DecodeVerified || !source.SquarePixels || !source.OrientationNormal || !source.Opaque {
		return invalid("source needs verified opaque, unrotated square-pixel frames")
	}
	if source.Width < postprocessMinSide || source.Height < postprocessMinSide || source.Width > postprocessMaxSide || source.Height > postprocessMaxSide || int64(source.Width)*int64(source.Height) > postprocessMaxFramePixels {
		return invalid("source dimensions exceed limits")
	}
	if options.Width < source.Width || options.Height < source.Height || options.Width > postprocessMaxSide || options.Height > postprocessMaxSide || options.Width > source.Width*postprocessMaxScale || options.Height > source.Height*postprocessMaxScale || int64(options.Width)*int64(options.Height) > postprocessMaxFramePixels {
		return invalid("target must be between source size and 2x, at most 4096 per side and 16 MP")
	}
	upscale := options.Width != source.Width || options.Height != source.Height
	if upscale {
		if options.Width%8 != 0 || options.Height%8 != 0 {
			return invalid("RTX target dimensions must be multiples of eight")
		}
		// Permit only the aspect error introduced by rounding each side to 8 px.
		if math.Abs(float64(options.Width)/float64(source.Width)-float64(options.Height)/float64(source.Height)) > 4.0/float64(source.Width)+4.0/float64(source.Height) {
			return invalid("target would change the source aspect ratio")
		}
		switch options.RTXQuality {
		case "LOW", "MEDIUM", "HIGH", "ULTRA":
		default:
			return invalid("unsupported RTX quality")
		}
	} else if options.RTXQuality != "" {
		return invalid("RTX quality supplied without upscale")
	}
	if options.Interpolate < 1 || options.Interpolate > 4 {
		return invalid("interpolation multiplier must be 1 through 4")
	}
	if options.Interpolate > 1 {
		// File availability/hash inspection is a separate pre-dispatch gate. A
		// checkpoint name in object_info alone does not prove it is installed.
		switch options.RIFECheckpoint {
		case "rife47.pth", "rife49.pth", "rife417.pth", "rife426.pth":
		default:
			return invalid("unsupported RIFE checkpoint")
		}
	} else if options.RIFECheckpoint != "" {
		return invalid("RIFE checkpoint supplied without interpolation")
	}
	if math.IsNaN(options.ColorStrength) || math.IsInf(options.ColorStrength, 0) {
		return invalid("color strength must be finite")
	}
	if options.ColorMethod != "" {
		if (options.ColorMethod != "adain" && options.ColorMethod != "mean_std") || options.ColorStrength <= 0 || options.ColorStrength > 1 {
			return invalid("unsupported color match settings")
		}
	} else if options.ColorStrength != 0 {
		return invalid("color strength supplied without color matching")
	}
	if !upscale && options.Interpolate == 1 && options.ColorMethod == "" {
		return invalid("no postprocessing operation selected")
	}
	plan := postprocessPlan{Version: postprocessPlanVersion, MediaType: source.MediaType, Options: options, SourceFrames: source.Frames, OutputFrames: source.Frames}
	switch source.MediaType {
	case "image":
		if source.Frames != 1 || source.FPSNumerator != 0 || source.FPSDenominator != 0 || options.Interpolate != 1 {
			return invalid("only single-frame images without interpolation are supported")
		}
	case "video":
		if !source.ConstantFrameRate || source.Frames < 2 || source.Frames > postprocessMaxSourceFrames || source.FPSNumerator < 1 || source.FPSNumerator > 1000000000 || source.FPSDenominator < 1 || source.FPSDenominator > 1000000000 {
			return invalid("video requires verified constant cadence and 2 through 450 frames")
		}
		fps := float64(source.FPSNumerator) / float64(source.FPSDenominator)
		if fps < 1 || fps > postprocessMaxSourceFPS || float64(source.Frames)/fps > postprocessMaxSeconds || fps*float64(options.Interpolate) > postprocessMaxOutputFPS {
			return invalid("video duration or frame rate exceeds limits")
		}
		if options.Width%2 != 0 || options.Height%2 != 0 {
			return invalid("H.264 output needs even dimensions")
		}
		plan.FPSNumerator, plan.FPSDenominator = source.FPSNumerator*int64(options.Interpolate), source.FPSDenominator
		for a, b := plan.FPSNumerator, plan.FPSDenominator; ; a, b = b, a%b {
			if b == 0 {
				plan.FPSNumerator /= a
				plan.FPSDenominator /= a
				break
			}
		}
		plan.OutputFrames = source.Frames * options.Interpolate
		plan.RepeatedTailFrames = options.Interpolate - 1
	default:
		return invalid("unsupported source media type")
	}
	sourcePixels := int64(source.Width) * int64(source.Height)
	plan.OutputPixelFrames = int64(options.Width) * int64(options.Height) * int64(plan.OutputFrames)
	// RGB float32 tensors retained by the graph, including a second RIFE
	// working buffer. This is a workload guard, not a hardware VRAM guarantee.
	pixels := sourcePixels * int64(source.Frames)
	if options.ColorMethod != "" {
		pixels += sourcePixels*int64(source.Frames) + postprocessMaxFramePixels
	}
	if options.Interpolate > 1 {
		pixels += 3*sourcePixels*int64(plan.OutputFrames) + sourcePixels*int64(plan.RepeatedTailFrames)
	}
	if upscale {
		pixels += plan.OutputPixelFrames
	}
	plan.WorkingTensorBytes = pixels * 12
	if plan.OutputPixelFrames > postprocessMaxPixelFrames || plan.WorkingTensorBytes > postprocessMaxWorkingTensorBytes {
		return invalid("decoded frame workload exceeds postprocessing limits")
	}
	return plan, nil
}

// buildPostprocessPrompt accepts only namespaced references. Quotes may reserve
// deterministic names; dispatch must prove their uploaded bytes and ownership.
func buildPostprocessPrompt(source postprocessSourceFacts, options postprocessOptions, sourceName, colorReference, namespace string, catalog comfySchemaCatalog) (map[string]any, postprocessPlan, error) {
	plan, err := planPostprocess(source, options)
	if err != nil {
		return nil, postprocessPlan{}, err
	}
	if err := validateComfyInputReference(sourceName, namespace); err != nil {
		return nil, postprocessPlan{}, err
	}
	if strings.ContainsAny(sourceName, "[]") || strings.TrimSpace(sourceName) != sourceName {
		return nil, postprocessPlan{}, errors.New("postprocess: annotated source paths are not allowed")
	}
	if options.ColorMethod != "" {
		if err := validateComfyInputReference(colorReference, namespace); err != nil {
			return nil, postprocessPlan{}, err
		}
		if strings.ContainsAny(colorReference, "[]") || strings.TrimSpace(colorReference) != colorReference || colorReference == sourceName {
			return nil, postprocessPlan{}, errors.New("postprocess: a separate color reference is required")
		}
	} else if colorReference != "" {
		return nil, postprocessPlan{}, errors.New("postprocess: unused color reference")
	}
	graph := map[string]any{}
	add := func(id, class string, inputs map[string]any) { graph[id] = miniMaxH3Node(class, inputs) }
	link := func(id string) []any { return []any{id, 0} }
	if source.MediaType == "image" {
		add("1", "LoadImage", map[string]any{"image": sourceName})
	} else {
		add("1", "VHS_LoadVideo", map[string]any{"video": sourceName, "force_rate": 0.0, "custom_width": 0, "custom_height": 0, "frame_load_cap": 0, "skip_first_frames": 0, "select_every_nth": 1, "format": "None"})
	}
	last := "1"
	if options.ColorMethod != "" {
		add("2", "LoadImage", map[string]any{"image": colorReference})
		add("3", "LCColorMatch", map[string]any{"image": link(last), "reference": link("2"), "method": options.ColorMethod, "strength": options.ColorStrength, "skin_protect": 0.5})
		last = "3"
	}
	if options.Interpolate > 1 {
		add("4", "RIFE VFI", map[string]any{"frames": link(last), "ckpt_name": options.RIFECheckpoint, "multiplier": options.Interpolate, "clear_cache_after_n_frames": 10, "fast_mode": true, "ensemble": false, "scale_factor": 1.0, "dtype": "float32", "torch_compile": false, "batch_size": 1})
		// RIFE includes the last original frame once. Hold that sample for the
		// remainder of its original frame interval to retain source duration.
		add("7", "ImageFromBatch", map[string]any{"image": link("4"), "batch_index": (source.Frames - 1) * options.Interpolate, "length": 1})
		add("8", "RepeatImageBatch", map[string]any{"image": link("7"), "amount": plan.RepeatedTailFrames})
		add("9", "ImageBatch", map[string]any{"image1": link("4"), "image2": link("8")})
		last = "9"
	}
	if options.Width != source.Width || options.Height != source.Height {
		add("5", "RTXVideoSuperResolution", map[string]any{"images": link(last), "resize_type": "target dimensions", "resize_type.width": options.Width, "resize_type.height": options.Height, "quality": options.RTXQuality})
		last = "5"
	}
	if source.MediaType == "image" {
		add("6", "SaveImage", map[string]any{"images": link(last), "filename_prefix": "postprocess"})
	} else {
		add("6", "VHS_VideoCombine", map[string]any{"images": link(last), "audio": []any{"1", 2}, "frame_rate": float64(plan.FPSNumerator) / float64(plan.FPSDenominator), "loop_count": 0, "filename_prefix": "postprocess", "format": "video/h264-mp4", "pingpong": false, "save_output": true, "pix_fmt": "yuv420p", "crf": 19, "save_metadata": true, "trim_to_audio": false})
	}
	if issues := validateComfyPrompt(catalog, graph); len(issues) > 0 {
		return nil, postprocessPlan{}, fmt.Errorf("postprocess: installed node schema is incompatible: %v", issues)
	}
	for _, raw := range graph {
		node := raw.(map[string]any)
		class := node["class_type"].(string)
		schema := catalog.Nodes[class]
		for field, value := range node["inputs"].(map[string]any) {
			input, known := schema.Required[field]
			if !known {
				input, known = schema.Optional[field]
			}
			if known {
				if (input.Type == "COMBO" || input.Type == "COMFY_DYNAMICCOMBO_V3") && !isRuntimeAssetChoice(class, field) && !choiceContains(input.Choices, value) {
					return nil, postprocessPlan{}, fmt.Errorf("postprocess: installed schema does not offer %s.%s", class, field)
				}
				continue
			}
			if class == "RTXVideoSuperResolution" && (field == "resize_type.width" || field == "resize_type.height") {
				option := schema.Required["resize_type"].DynamicOptions["target dimensions"]
				if _, ok := option.Required[strings.TrimPrefix(field, "resize_type.")]; ok {
					continue
				}
			}
			// VHS exposes encoder widgets in format metadata, not input.required.
			if class == "VHS_VideoCombine" && (field == "pix_fmt" || field == "crf" || field == "save_metadata" || field == "trim_to_audio") {
				if input, ok := schema.VideoFormats["video/h264-mp4"][field]; ok {
					if issues := validateComfyInput(catalog, graph, "6", schema, input, field, value); len(issues) == 0 && (input.Type != "COMBO" || choiceContains(input.Choices, value)) {
						continue
					}
				}
			}
			return nil, postprocessPlan{}, fmt.Errorf("postprocess: installed schema no longer declares %s.%s", class, field)
		}
	}
	return graph, plan, nil
}
