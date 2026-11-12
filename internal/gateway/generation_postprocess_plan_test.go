package gateway

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func postprocessTestSource() postprocessSourceFacts {
	return postprocessSourceFacts{MediaType: "image", Width: 512, Height: 512, Frames: 1, SquarePixels: true, OrientationNormal: true, Opaque: true, DecodeVerified: true}
}

func postprocessTestOptions() postprocessOptions {
	return postprocessOptions{Width: 1024, Height: 1024, Interpolate: 1, RTXQuality: "HIGH"}
}

func postprocessVideoSource() postprocessSourceFacts {
	s := postprocessTestSource()
	s.MediaType, s.Frames, s.FPSNumerator, s.FPSDenominator, s.ConstantFrameRate = "video", 25, 24000, 1001, true
	return s
}

// Contract fixture transcribed from the local read-only object_info on 2026-09-08.
// Asset filenames and display-only metadata are deliberately omitted.
func postprocessTestCatalog() comfySchemaCatalog {
	c := comfySchemaCatalog{Nodes: map[string]comfyNodeSchema{}}
	add := func(class string, outputs []string, fields string) {
		n := comfyNodeSchema{ClassType: class, Outputs: outputs, Required: map[string]comfyInputSchema{}, Optional: map[string]comfyInputSchema{}}
		for _, field := range strings.Fields(fields) {
			name, kind, _ := strings.Cut(field, ":")
			n.Required[name] = comfyInputSchema{Name: name, Type: kind}
		}
		c.Nodes[class] = n
	}
	add("LoadImage", []string{"IMAGE", "MASK"}, "image:COMBO")
	add("SaveImage", []string{"IMAGE"}, "images:IMAGE filename_prefix:STRING")
	add("ImageFromBatch", []string{"IMAGE"}, "image:IMAGE batch_index:INT length:INT")
	add("RepeatImageBatch", []string{"IMAGE"}, "image:IMAGE amount:INT")
	add("ImageBatch", []string{"IMAGE"}, "image1:IMAGE image2:IMAGE")
	add("VHS_LoadVideo", []string{"IMAGE", "INT", "AUDIO", "VHS_VIDEOINFO"}, "video:COMBO force_rate:FLOAT custom_width:INT custom_height:INT frame_load_cap:INT skip_first_frames:INT select_every_nth:INT format:COMBO")
	add("VHS_VideoCombine", []string{"VHS_FILENAMES"}, "images:IMAGE frame_rate:FLOAT loop_count:INT filename_prefix:STRING format:COMBO pingpong:BOOLEAN save_output:BOOLEAN audio:AUDIO")
	add("LCColorMatch", []string{"IMAGE"}, "image:IMAGE reference:IMAGE method:COMBO strength:FLOAT skin_protect:FLOAT")
	add("RIFE VFI", []string{"IMAGE"}, "ckpt_name:COMBO frames:IMAGE clear_cache_after_n_frames:INT multiplier:INT fast_mode:BOOLEAN ensemble:BOOLEAN scale_factor:COMBO dtype:COMBO torch_compile:BOOLEAN batch_size:INT")
	add("RTXVideoSuperResolution", []string{"IMAGE"}, "images:IMAGE resize_type:COMFY_DYNAMICCOMBO_V3 quality:COMBO")
	choices := func(class, name string, values ...any) {
		n := c.Nodes[class]
		field := n.Required[name]
		field.Choices = values
		n.Required[name] = field
	}
	choices("VHS_LoadVideo", "format", "None", "AnimateDiff")
	choices("VHS_VideoCombine", "format", "video/h264-mp4")
	choices("LCColorMatch", "method", "adain", "mean_std")
	choices("RIFE VFI", "ckpt_name", "rife47.pth", "rife49.pth", "rife417.pth", "rife426.pth")
	choices("RIFE VFI", "scale_factor", 1.0)
	choices("RIFE VFI", "dtype", "float32", "float16", "bfloat16")
	choices("RTXVideoSuperResolution", "quality", "LOW", "MEDIUM", "HIGH", "ULTRA")
	n := c.Nodes["RTXVideoSuperResolution"]
	n.Required["resize_type"] = comfyInputSchema{Name: "resize_type", Type: "COMFY_DYNAMICCOMBO_V3", Choices: []any{"target dimensions"}, DynamicOptions: map[string]comfyDynamicOption{
		"target dimensions": {Key: "target dimensions", Required: map[string]comfyInputSchema{"width": {Name: "width", Type: "INT"}, "height": {Name: "height", Type: "INT"}}},
	}}
	n = c.Nodes["VHS_VideoCombine"]
	n.VideoFormats = parseComfyVideoFormats(json.RawMessage(`[["video/h264-mp4"],{"formats":{"video/h264-mp4":[["pix_fmt",["yuv420p","yuv420p10le"]],["crf","INT",{"min":0,"max":100}],["save_metadata","BOOLEAN"],["trim_to_audio","BOOLEAN"]]}}]`))
	c.Nodes["VHS_VideoCombine"] = n
	return c
}

func TestPostprocessPlanRejectsUnsupported(t *testing.T) {
	tests := []struct {
		name string
		edit func(*postprocessSourceFacts, *postprocessOptions)
	}{
		{"metadata only", func(s *postprocessSourceFacts, o *postprocessOptions) { s.DecodeVerified = false }},
		{"alpha", func(s *postprocessSourceFacts, o *postprocessOptions) { s.Opaque = false }},
		{"rotation", func(s *postprocessSourceFacts, o *postprocessOptions) { s.OrientationNormal = false }},
		{"SAR", func(s *postprocessSourceFacts, o *postprocessOptions) { s.SquarePixels = false }},
		{"unknown type", func(s *postprocessSourceFacts, o *postprocessOptions) { s.MediaType = "audio" }},
		{"source width overflow", func(s *postprocessSourceFacts, o *postprocessOptions) { s.Width = math.MaxInt }},
		{"source height zero", func(s *postprocessSourceFacts, o *postprocessOptions) { s.Height = 0 }},
		{"target overflow", func(s *postprocessSourceFacts, o *postprocessOptions) { o.Width = math.MaxInt }},
		{"target downscale", func(s *postprocessSourceFacts, o *postprocessOptions) { o.Width = 256 }},
		{"target rounding", func(s *postprocessSourceFacts, o *postprocessOptions) { o.Width = 1023 }},
		{"aspect distortion", func(s *postprocessSourceFacts, o *postprocessOptions) { o.Width = 768 }},
		{"RTX quality", func(s *postprocessSourceFacts, o *postprocessOptions) { o.RTXQuality = "AUTO" }},
		{"unused RTX", func(s *postprocessSourceFacts, o *postprocessOptions) { o.Width, o.Height = 512, 512 }},
		{"empty operation", func(s *postprocessSourceFacts, o *postprocessOptions) { o.Width, o.Height, o.RTXQuality = 512, 512, "" }},
		{"animated image", func(s *postprocessSourceFacts, o *postprocessOptions) { s.Frames = 2 }},
		{"image FPS", func(s *postprocessSourceFacts, o *postprocessOptions) { s.FPSNumerator = 24 }},
		{"image interpolation", func(s *postprocessSourceFacts, o *postprocessOptions) {
			o.Interpolate, o.RIFECheckpoint = 2, "rife49.pth"
		}},
		{"zero multiplier", func(s *postprocessSourceFacts, o *postprocessOptions) { o.Interpolate = 0 }},
		{"large multiplier", func(s *postprocessSourceFacts, o *postprocessOptions) { o.Interpolate = math.MaxInt }},
		{"unused checkpoint", func(s *postprocessSourceFacts, o *postprocessOptions) { o.RIFECheckpoint = "rife49.pth" }},
		{"checkpoint path", func(s *postprocessSourceFacts, o *postprocessOptions) {
			*s = postprocessVideoSource()
			o.Interpolate, o.RIFECheckpoint = 2, "../rife49.pth"
		}},
		{"color method", func(s *postprocessSourceFacts, o *postprocessOptions) {
			o.ColorMethod, o.ColorStrength = "unknown", 0.5
		}},
		{"color no strength", func(s *postprocessSourceFacts, o *postprocessOptions) { o.ColorMethod = "adain" }},
		{"unused strength", func(s *postprocessSourceFacts, o *postprocessOptions) { o.ColorStrength = 0.5 }},
		{"color NaN", func(s *postprocessSourceFacts, o *postprocessOptions) {
			o.ColorMethod, o.ColorStrength = "adain", math.NaN()
		}},
		{"color infinity", func(s *postprocessSourceFacts, o *postprocessOptions) {
			o.ColorMethod, o.ColorStrength = "adain", math.Inf(1)
		}},
		{"color excessive", func(s *postprocessSourceFacts, o *postprocessOptions) { o.ColorMethod, o.ColorStrength = "adain", 1.01 }},
		{"variable cadence", func(s *postprocessSourceFacts, o *postprocessOptions) {
			*s = postprocessVideoSource()
			s.ConstantFrameRate = false
		}},
		{"video single frame", func(s *postprocessSourceFacts, o *postprocessOptions) { *s = postprocessVideoSource(); s.Frames = 1 }},
		{"video too many frames", func(s *postprocessSourceFacts, o *postprocessOptions) {
			*s = postprocessVideoSource()
			s.Frames = math.MaxInt
		}},
		{"zero denominator", func(s *postprocessSourceFacts, o *postprocessOptions) {
			*s = postprocessVideoSource()
			s.FPSDenominator = 0
		}},
		{"FPS overflow", func(s *postprocessSourceFacts, o *postprocessOptions) {
			*s = postprocessVideoSource()
			s.FPSNumerator = math.MaxInt64
		}},
		{"sub-one FPS", func(s *postprocessSourceFacts, o *postprocessOptions) {
			*s = postprocessVideoSource()
			s.FPSNumerator = 1
		}},
		{"duration", func(s *postprocessSourceFacts, o *postprocessOptions) {
			*s = postprocessVideoSource()
			s.Frames, s.FPSNumerator, s.FPSDenominator = 450, 10, 1
		}},
		{"high output FPS", func(s *postprocessSourceFacts, o *postprocessOptions) {
			*s = postprocessVideoSource()
			s.FPSNumerator, s.FPSDenominator = 60, 1
			o.Interpolate, o.RIFECheckpoint = 3, "rife49.pth"
		}},
		{"pixel frames", func(s *postprocessSourceFacts, o *postprocessOptions) { *s = postprocessVideoSource(); s.Frames = 300 }},
		{"tensor budget", func(s *postprocessSourceFacts, o *postprocessOptions) {
			*s = postprocessVideoSource()
			s.Frames = 360
			o.Width, o.Height, o.Interpolate, o.RIFECheckpoint = 520, 520, 2, "rife49.pth"
			o.ColorMethod, o.ColorStrength = "adain", 0.5
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, o := postprocessTestSource(), postprocessTestOptions()
			tt.edit(&s, &o)
			if _, err := planPostprocess(s, o); err == nil {
				t.Fatal("unsupported request accepted")
			}
		})
	}
}

func TestPostprocessPlanSupportedOperations(t *testing.T) {
	for _, multiplier := range []int{1, 2, 3, 4} {
		s, o := postprocessVideoSource(), postprocessTestOptions()
		o.Width, o.Height, o.RTXQuality = s.Width, s.Height, ""
		o.Interpolate = multiplier
		if multiplier > 1 {
			o.RIFECheckpoint = "rife49.pth"
		} else {
			o.ColorMethod, o.ColorStrength = "adain", 1
		}
		plan, err := planPostprocess(s, o)
		if err != nil {
			t.Fatal(err)
		}
		if plan.OutputFrames != s.Frames*multiplier || plan.OutputPixelFrames != int64(s.Width*s.Height*plan.OutputFrames) {
			t.Fatalf("wrong output workload: %+v", plan)
		}
	}
	// RTX rounding may introduce a small aspect deviation, but never rounds an
	// already confirmed target behind the user's back.
	s, o := postprocessTestSource(), postprocessTestOptions()
	s.Width, s.Height = 513, 515
	o.Width, o.Height = 1024, 1024
	if _, err := planPostprocess(s, o); err != nil {
		t.Fatal(err)
	}
	s, o = postprocessTestSource(), postprocessTestOptions()
	o.Width, o.Height, o.RTXQuality, o.ColorMethod, o.ColorStrength = s.Width, s.Height, "", "mean_std", 0.5
	if _, err := planPostprocess(s, o); err != nil {
		t.Fatal(err)
	}
}

func TestPostprocessGraphAndCadence(t *testing.T) {
	for _, video := range []bool{false, true} {
		for _, color := range []bool{false, true} {
			s, o := postprocessTestSource(), postprocessTestOptions()
			if video {
				s = postprocessVideoSource()
				o.Interpolate, o.RIFECheckpoint = 3, "rife49.pth"
			}
			ref := ""
			if color {
				o.ColorMethod, o.ColorStrength, ref = "mean_std", 0.5, "gateway/gateway-owner/ref.png"
			}
			graph, plan, err := buildPostprocessPrompt(s, o, "gateway/gateway-owner/source.png", ref, "gateway/gateway-owner", postprocessTestCatalog())
			if err != nil {
				t.Fatal(err)
			}
			if plan.Version != 1 || plan.Options != o {
				t.Fatalf("lost requested settings: %+v", plan)
			}
			allowed := map[string]bool{"LoadImage": true, "SaveImage": true, "VHS_LoadVideo": true, "VHS_VideoCombine": true, "RTXVideoSuperResolution": true, "LCColorMatch": true, "RIFE VFI": true, "ImageFromBatch": true, "RepeatImageBatch": true, "ImageBatch": true}
			for _, raw := range graph {
				if !allowed[raw.(map[string]any)["class_type"].(string)] {
					t.Fatal("non-allowlisted node")
				}
			}
			inputs := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
			if inputs("5")["resize_type"] != "target dimensions" || inputs("5")["resize_type.width"] != 1024 {
				t.Fatal("not using exact dimensions")
			}
			if video {
				if plan.OutputFrames != 75 || plan.FPSNumerator != 72000 || plan.FPSDenominator != 1001 || plan.RepeatedTailFrames != 2 {
					t.Fatalf("wrong RIFE cadence: %+v", plan)
				}
				if inputs("1")["format"] != "None" || inputs("1")["force_rate"] != 0.0 || inputs("1")["frame_load_cap"] != 0 {
					t.Fatal("loader changes source")
				}
				if !reflect.DeepEqual(inputs("6")["audio"], []any{"1", 2}) || inputs("6")["trim_to_audio"] != false {
					t.Fatal("audio not retained")
				}
				if inputs("4")["torch_compile"] != false || inputs("4")["batch_size"] != 1 {
					t.Fatal("unexpected RIFE workload")
				}
				if inputs("7")["batch_index"] != 72 || inputs("8")["amount"] != 2 || !reflect.DeepEqual(inputs("5")["images"], []any{"9", 0}) {
					t.Fatal("last original frame interval not retained")
				}
			} else if plan.OutputFrames != 1 || plan.FPSNumerator != 0 || plan.RepeatedTailFrames != 0 {
				t.Fatal("image has temporal settings")
			}
			encoded, _ := json.Marshal(graph)
			if err := validateComfyPromptAssetInputs(encoded, "gateway/gateway-owner"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPostprocessGraphOmitsUnselectedOperations(t *testing.T) {
	for _, kind := range []string{"image", "video"} {
		for mask := 1; mask < 8; mask++ {
			upscale, interpolate, color := mask&1 != 0, mask&2 != 0, mask&4 != 0
			if kind == "image" && interpolate {
				continue
			}
			s, o, ref := postprocessTestSource(), postprocessTestOptions(), ""
			if kind == "video" {
				s = postprocessVideoSource()
			}
			if !upscale {
				o.Width, o.Height, o.RTXQuality = s.Width, s.Height, ""
			}
			if interpolate {
				o.Interpolate, o.RIFECheckpoint = 2, "rife49.pth"
			}
			if color {
				o.ColorMethod, o.ColorStrength, ref = "adain", 0.5, "gateway/gateway-owner/ref.png"
			}
			graph, _, err := buildPostprocessPrompt(s, o, "gateway/gateway-owner/source", ref, "gateway/gateway-owner", postprocessTestCatalog())
			if err != nil {
				t.Fatalf("%s mask=%d: %v", kind, mask, err)
			}
			for id, want := range map[string]bool{"2": color, "3": color, "4": interpolate, "5": upscale, "7": interpolate, "8": interpolate, "9": interpolate} {
				if _, found := graph[id]; found != want {
					t.Fatalf("%s mask=%d unexpected node %s", kind, mask, id)
				}
			}
		}
	}
}

func TestPostprocessGraphRejectsForeignOrAnnotatedSources(t *testing.T) {
	for _, name := range []string{"../source.png", "https://example.com/source.png", "C:/source.png", "source.png", "gateway/gateway-other/source.png", "gateway/gateway-owner/source.png [output]", "gateway/gateway-owner", " gateway/gateway-owner/source.png"} {
		_, _, err := buildPostprocessPrompt(postprocessTestSource(), postprocessTestOptions(), name, "", "gateway/gateway-owner", postprocessTestCatalog())
		if err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	for _, ref := range []string{"", "gateway/gateway-other/ref.png", "gateway/gateway-owner/source.png", "gateway/gateway-owner/ref.png [output]"} {
		o := postprocessTestOptions()
		o.ColorMethod, o.ColorStrength = "adain", 0.5
		if _, _, err := buildPostprocessPrompt(postprocessTestSource(), o, "gateway/gateway-owner/source.png", ref, "gateway/gateway-owner", postprocessTestCatalog()); err == nil {
			t.Fatalf("accepted color reference %q", ref)
		}
	}
	if _, _, err := buildPostprocessPrompt(postprocessTestSource(), postprocessTestOptions(), "gateway/gateway-owner/source.png", "gateway/gateway-owner/ref.png", "gateway/gateway-owner", postprocessTestCatalog()); err == nil {
		t.Fatal("accepted unused color reference")
	}
}

func TestPostprocessGraphFailsClosedOnSchemaDrift(t *testing.T) {
	s, o := postprocessVideoSource(), postprocessTestOptions()
	o.Interpolate, o.RIFECheckpoint, o.ColorMethod, o.ColorStrength = 2, "rife49.pth", "adain", 0.5
	build := func(c comfySchemaCatalog) error {
		_, _, err := buildPostprocessPrompt(s, o, "gateway/gateway-owner/source.mp4", "gateway/gateway-owner/ref.png", "gateway/gateway-owner", c)
		return err
	}
	for _, class := range []string{"LoadImage", "VHS_LoadVideo", "VHS_VideoCombine", "LCColorMatch", "RIFE VFI", "RTXVideoSuperResolution", "ImageFromBatch", "RepeatImageBatch", "ImageBatch"} {
		c := postprocessTestCatalog()
		delete(c.Nodes, class)
		if err := build(c); err == nil {
			t.Fatalf("accepted missing %s", class)
		}
	}
	for _, field := range []struct{ class, name string }{{"VHS_LoadVideo", "format"}, {"RIFE VFI", "batch_size"}, {"LCColorMatch", "reference"}, {"VHS_VideoCombine", "audio"}} {
		c := postprocessTestCatalog()
		delete(c.Nodes[field.class].Required, field.name)
		if err := build(c); err == nil {
			t.Fatalf("accepted removed %s.%s", field.class, field.name)
		}
	}
	c := postprocessTestCatalog()
	f := c.Nodes["RIFE VFI"].Required["ckpt_name"]
	f.Choices = []any{"rife47.pth"}
	c.Nodes["RIFE VFI"].Required["ckpt_name"] = f
	if err := build(c); err == nil {
		t.Fatal("accepted missing checkpoint choice")
	}
	c = postprocessTestCatalog()
	f = c.Nodes["RTXVideoSuperResolution"].Required["resize_type"]
	f.DynamicOptions = nil
	c.Nodes["RTXVideoSuperResolution"].Required["resize_type"] = f
	if err := build(c); err == nil {
		t.Fatal("accepted missing exact-size selector")
	}
	c = postprocessTestCatalog()
	delete(c.Nodes["VHS_VideoCombine"].VideoFormats["video/h264-mp4"], "trim_to_audio")
	if err := build(c); err == nil {
		t.Fatal("accepted missing trim setting")
	}
	c = postprocessTestCatalog()
	f = c.Nodes["VHS_VideoCombine"].VideoFormats["video/h264-mp4"]["pix_fmt"]
	f.Choices = []any{"yuv420p10le"}
	c.Nodes["VHS_VideoCombine"].VideoFormats["video/h264-mp4"]["pix_fmt"] = f
	if err := build(c); err == nil {
		t.Fatal("accepted incompatible encoder pixel format")
	}
	for _, field := range []struct{ class, name string }{{"RIFE VFI", "ckpt_name"}, {"RTXVideoSuperResolution", "quality"}, {"VHS_LoadVideo", "format"}, {"VHS_VideoCombine", "format"}} {
		c = postprocessTestCatalog()
		f = c.Nodes[field.class].Required[field.name]
		f.Choices = nil
		c.Nodes[field.class].Required[field.name] = f
		if err := build(c); err == nil {
			t.Fatalf("accepted empty choices for %s.%s", field.class, field.name)
		}
	}
}
