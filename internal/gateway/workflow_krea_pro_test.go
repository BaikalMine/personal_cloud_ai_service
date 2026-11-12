package gateway

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExportKreaParityFixtures(t *testing.T) {
	path := os.Getenv("KREA_PARITY_FIXTURE")
	if path == "" {
		t.Skip("fixture export is opt-in")
	}
	fixtures := map[string]any{}
	for _, tc := range []struct {
		name, mode      string
		images, upscale int
		upscaler        string
		relayScale      int
	}{
		{"text-native", "text", 0, 1, "", 2}, {"text-vosr", "text", 0, 2, "", 2},
		{"text-relay2", "text", 0, 2, "relay", 2}, {"text-relay4", "text", 0, 2, "relay", 4},
		{"references", "references", 4, 1, "", 2}, {"identity", "identity", 2, 1, "", 2},
		{"control", "control", 1, 1, "", 2}, {"inpaint", "inpaint", 2, 1, "", 2}, {"outpaint", "outpaint", 1, 1, "", 2},
	} {
		input := kreaProTestInput()
		input.KreaProMode, input.KreaProUpscale = tc.mode, tc.upscale
		input.KreaProUpscaler, input.KreaProRelayScale = tc.upscaler, tc.relayScale
		input.Width, input.Height, input.Positive, input.Negative, input.Seed = 512, 512, "A ceramic vase on a table.", "blurry", 42
		if tc.images > 0 {
			input.InputImage = "ref1.png"
		}
		for i := 1; i < tc.images; i++ {
			input.ReferenceImages[i-1] = fmt.Sprintf("ref%d.png", i+1)
		}
		if err := normalizeKreaPro(&input); err != nil {
			t.Fatal(err)
		}
		graph, err := buildKreaProPrompt(input)
		if err != nil {
			t.Fatal(err)
		}
		fixtures[tc.name] = map[string]any{"mode": tc.mode, "images": tc.images, "upscale": tc.upscale, "upscaler": input.KreaProUpscaler, "relay_scale": input.KreaProRelayScale, "graph": graph}
	}
	data, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func kreaProTestInput() generationForm {
	input := generationForm{PresetID: kreaProPresetID, TemplateID: "text-to-image", ModelName: "Krea2\\krea2TurboFP8_krea2TURBO.safetensors", TextEncoder: "qwen3vl_4b_bf16.safetensors", VAE: "krea2RealVae_v10.safetensors"}
	_ = applyWorkflowManifestDefaults(kreaProWorkflowManifest(), &input)
	input.Positive, input.Width, input.Height, input.Seed, input.AspectRatio = "A ceramic vase on a table.", 880, 1168, 42, "custom"
	return input
}

func TestKreaProAllModesValidateFinalOutput(t *testing.T) {
	fixture := loadObjectInfoFixture(t, "krea2-pro.json")
	for _, mode := range []string{"text", "references", "identity", "control", "inpaint", "outpaint"} {
		t.Run(mode, func(t *testing.T) {
			input := kreaProTestInput()
			input.KreaProMode = mode
			if mode != "text" {
				input.InputImage = "gateway-krea-stage-reference.png"
			}
			if mode == "inpaint" {
				input.ReferenceImages[0] = "gateway-krea-stage-reference.png"
			}
			if err := normalizeKreaPro(&input); err != nil {
				t.Fatal(err)
			}
			graph, err := buildKreaProPrompt(input)
			if err != nil {
				t.Fatal(err)
			}
			issues := validateComfyPrompt(buildComfySchemaCatalog(fixture.Nodes, time.Now(), "krea-pro"), graph)
			if len(issues) > 0 {
				t.Fatalf("incompatible final graph: %+v", issues)
			}
			if graph["4071"] == nil {
				t.Fatal("missing final save")
			}
			body, _ := json.Marshal(graph)
			for _, forbidden := range []string{"⬇ Download:", "Logo no background", "Live-action", "LCSmartInpaintPipe", "LCVision"} {
				if strings.Contains(string(body), forbidden) {
					t.Fatalf("disabled/default content leaked: %s", forbidden)
				}
			}
			if mode == "inpaint" {
				if graph["9502"] == nil {
					t.Fatal("uploaded mask was not connected")
				}
			}
		})
	}
}

func TestKreaProOneXBypassesVOSRForEveryMode(t *testing.T) {
	fixture := loadObjectInfoFixture(t, "krea2-pro.json")
	for _, mode := range []string{"text", "references", "identity", "control", "inpaint", "outpaint"} {
		t.Run(mode, func(t *testing.T) {
			input := kreaProTestInput()
			input.KreaProMode, input.KreaProUpscale = mode, 1
			input.KreaProSettings = `{"enabled":{"4046":true}}`
			if mode != "text" {
				input.InputImage = "reference.png"
			}
			if mode == "inpaint" {
				input.ReferenceImages[0] = "mask.png"
			}
			if err := normalizeKreaPro(&input); err != nil {
				t.Fatal(err)
			}
			graph, err := buildKreaProPrompt(input)
			if err != nil {
				t.Fatal(err)
			}
			if node, ok := graph["4046"].(map[string]any); !ok || node["class_type"] != "GatewayKreaProBypass" {
				t.Fatalf("VOSR2 not bypassed: %v", graph["4046"])
			}
			if graph["4047"] != nil {
				t.Fatal("unsupported 1x selector remained reachable")
			}
			if issues := validateComfyPrompt(buildComfySchemaCatalog(fixture.Nodes, time.Now(), "krea-no-upscale"), graph); len(issues) != 0 {
				t.Fatalf("invalid graph: %+v", issues)
			}
		})
	}
}

func TestKreaProOptionalEffectsReachFinalOutput(t *testing.T) {
	fixture := loadObjectInfoFixture(t, "krea2-pro.json")
	for _, option := range kreaProOptions() {
		if !option.Toggle {
			continue
		}
		t.Run(option.ID, func(t *testing.T) {
			input := kreaProTestInput()
			input.KreaProMode = "references"
			input.InputImage = "gateway-krea-stage-reference.png"
			input.ReferenceImages = [3]string{"gateway-krea-stage-reference.png", "gateway-krea-stage-reference.png", "gateway-krea-stage-reference.png"}
			settings, _ := json.Marshal(kreaProSettings{Enabled: map[string]bool{option.ID: true}})
			input.KreaProSettings = string(settings)
			if err := normalizeKreaPro(&input); err != nil {
				t.Fatal(err)
			}
			graph, err := buildKreaProPrompt(input)
			if err != nil {
				t.Fatal(err)
			}
			if graph[option.ID] == nil || graph[option.ID].(map[string]any)["class_type"] == "GatewayKreaProBypass" {
				t.Fatal("effect missing from final branch")
			}
			if issues := validateComfyPrompt(buildComfySchemaCatalog(fixture.Nodes, time.Now(), "effects"), graph); len(issues) > 0 {
				t.Fatalf("effect graph incompatible: %+v", issues)
			}
		})
	}
}

func TestKreaProSettingsRejectUnknownAndUnsafeValues(t *testing.T) {
	for _, raw := range []string{`{"enabled":{"3857":true}}`, `{"nodes":{"4071":{"path":"C:/outside"}}}`, `{"nodes":{"3622":{"font":[]}}}`, `{"nodes":{"3622":{"font_size":-1}}}`, `{"other":true}`, `[]`, `{} {}`} {
		if _, err := parseKreaProSettings(raw); err == nil {
			t.Fatalf("accepted invalid settings: %s", raw)
		}
	}
}

func TestKreaProFractionalSettingsPreservePrecision(t *testing.T) {
	input := kreaProTestInput()
	input.KreaProSettings = `{"enabled":{"4096":true,"3774":true,"3832":true},"nodes":{"4096":{"warmth":-0.22,"brightness":0.12,"rosy":0.08,"even_skin":0.18,"pores":1.2,"saturation":-0.08},"3774":{"angle":314.717570393228,"distance":67.62454593641333},"3832":{"feather":0.35}}}`
	settings, err := parseKreaProSettings(input.KreaProSettings)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := buildKreaProPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	for id, fields := range settings.Nodes {
		actual := graph[id].(map[string]any)["inputs"].(map[string]any)
		for name, value := range fields {
			if actual[name] != value {
				t.Fatalf("%s.%s was rounded: got %v, want %v", id, name, actual[name], value)
			}
		}
	}
}

func TestKreaProLoRAIsOptionalAndBoundToMainModel(t *testing.T) {
	input := kreaProTestInput()
	input.LoraNames[0] = "lenovo_krea2.safetensors"
	input.LoraModel[0] = 0.4
	input.LoraClip[0] = 0.7
	graph, err := buildKreaProPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	loader := graph["9000"].(map[string]any)["inputs"].(map[string]any)
	if loader["strength_model"] != 0.4 || loader["strength_clip"] != 0.7 {
		t.Fatal("user LoRA strengths ignored")
	}
	if graph["3760"].(map[string]any)["inputs"].(map[string]any)["lora_rows"] != "[]" {
		t.Fatal("author LoRAs were forced")
	}
}

func TestKreaProOutpaintGeometryAndLimits(t *testing.T) {
	settings, err := parseKreaProSettings(`{"nodes":{"3870":{"left":10,"right":10}}}`)
	if err != nil {
		t.Fatal(err)
	}
	w, h, _, err := kreaProOutpaintFrame(880, 1168, settings)
	if err != nil || w != 1056 || h != 1168 {
		t.Fatalf("percentage padding: %dx%d %v", w, h, err)
	}
	input := kreaProTestInput()
	input.KreaProMode = "outpaint"
	input.InputImage = "gateway-krea-stage-reference.png"
	input.KreaProSettings = `{"nodes":{"3870":{"left":400,"right":400}}}`
	if normalizeKreaPro(&input) == nil {
		t.Fatal("oversized outpaint admitted")
	}
	outputs := kreaProFinalOutputs(map[string]json.RawMessage{"4071": json.RawMessage(`{"images":[]}`), "preview": json.RawMessage(`{"images":[{}]}`)})
	if len(outputs) != 1 || outputs["preview"] != nil {
		t.Fatal("intermediate previews became final results")
	}
}

func TestKreaProOutputDimensionsMatchPythonMemoryProfile(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		width, height, scale int
		valid                bool
	}{
		{"side-exact", 3072, 256, 4, true}, {"side-over", 3088, 256, 4, false},
		{"pixels-below", 12288, 3279, 1, true}, {"pixels-over", 12288, 3280, 1, false},
		{"relay-four-below", 1584, 1584, 4, true}, {"relay-four-over", 1600, 1584, 4, false},
		{"zero-scale", 1024, 1024, 0, false}, {"overflow-safe", int(^uint(0) >> 1), 1024, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, h, err := kreaProOutputDimensions(tc.width, tc.height, tc.scale)
			if (err == nil) != tc.valid {
				t.Fatalf("%dx%d at %dx: %v", tc.width, tc.height, tc.scale, err)
			}
			if tc.valid && (w != tc.width*tc.scale || h != tc.height*tc.scale) {
				t.Fatalf("incorrect output %dx%d", w, h)
			}
		})
	}
}

func TestKreaProWebsiteAndDirectBuilderEnforceOutputProfile(t *testing.T) {
	for _, tc := range []struct {
		name, kind           string
		width, height, scale int
		outpaint, valid      bool
	}{
		{"side-exact", "relay", 3072, 256, 4, false, true},
		{"side-over", "relay", 3088, 256, 4, false, false},
		{"pixels-below", "relay", 1584, 1584, 4, false, true},
		{"pixels-over", "relay", 1600, 1584, 4, false, false},
		{"large-relay-two", "relay", 2048, 2048, 2, false, true},
		{"large-relay-four", "relay", 2048, 2048, 4, false, false},
		{"native-unchanged", "none", 2048, 2048, 1, false, true},
		{"vosr-three-limit", "vosr", 4096, 1088, 3, false, true},
		{"outpaint-below", "relay", 1312, 1584, 4, true, true},
		{"outpaint-over", "relay", 1328, 1584, 4, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := kreaProTestInput()
			input.KreaProUpscaler, input.Width, input.Height = tc.kind, tc.width, tc.height
			if tc.kind == "relay" {
				input.KreaProRelayScale = tc.scale
			} else if tc.kind == "vosr" {
				input.KreaProUpscale = tc.scale
			}
			if tc.outpaint {
				input.KreaProMode, input.InputImage = "outpaint", "source.png"
			}
			normalized := input
			if err := normalizeKreaPro(&normalized); (err == nil) != tc.valid {
				t.Fatalf("website output guard: valid=%v err=%v", tc.valid, err)
			}
			if _, err := buildKreaProPrompt(input); (err == nil) != tc.valid {
				t.Fatalf("direct builder output guard: valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestKreaProQueuedDraftAndRecipePreserveSettings(t *testing.T) {
	values := url.Values{"generation_workflow": {"krea2-pro"}, "template_id": {"text-to-image"}, "krea_pro_mode": {"references"}, "krea_pro_upscale": {"3"}, "krea_pro_upscaler": {"relay"}, "krea_pro_relay_scale": {"4"}, "krea_pro_match_source": {"true"}, "krea_pro_settings": {`{"enabled":{"4013":true,"4046":false}}`}, "krea_ultra_step_swap": {"5"}, "krea_ultra_detail_amount": {"0.5"}}
	draft, err := generationDraftValues(values)
	if err != nil {
		t.Fatal(err)
	}
	for name, stored := range map[string]map[string]string{"job": generationJobValues(values, 42), "draft": draft, "recipe": generationRecipeValues(values, 42)} {
		for key, raw := range values {
			if stored[key] != raw[0] {
				t.Fatalf("%s dropped %s", name, key)
			}
		}
	}
}

func TestKreaProReferenceUsesRequestedCanvasAndUniqueFinalName(t *testing.T) {
	input := kreaProTestInput()
	input.KreaProMode = "references"
	input.InputImage = "gateway-krea-stage-reference.png"
	graph, err := buildKreaProPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	resize := graph["9600"].(map[string]any)["inputs"].(map[string]any)
	if resize["width"] != input.Width || resize["height"] != input.Height {
		t.Fatal("reference canvas ignores requested dimensions")
	}
	attachGenerationOutputPrefixes(graph, "test-public-id")
	if !strings.HasSuffix(graph["4071"].(map[string]any)["inputs"].(map[string]any)["filename"].(string), "-test-public-id") {
		t.Fatal("final image name is not job-scoped")
	}
}

func TestKreaProSpeedBoostOffAndManualGrowth(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		input := kreaProTestInput()
		input.KreaUltraSpeedBoost = enabled
		input.KreaUltraGrowStep = 3
		graph, err := buildKreaProPrompt(input)
		if err != nil {
			t.Fatal(err)
		}
		boost := graph["3981"].(map[string]any)["inputs"].(map[string]any)
		want := 0
		if enabled {
			want = 3
		}
		if boost["auto"] != false || boost["grow_at_step"] != want {
			t.Fatalf("enabled=%v: unexpected boost settings %+v", enabled, boost)
		}
	}
}

func TestKreaProReferenceLimitDoesNotChangeOtherKreaWorkflows(t *testing.T) {
	if got := generationInputImageLimit(generationForm{PresetID: kreaProPresetID}, modelFamilyKrea2); got != 4 {
		t.Fatalf("full profile references = %d, want 4", got)
	}
	if got := generationInputImageLimit(generationForm{PresetID: kreaUltraPresetID}, modelFamilyKrea2); got != 2 {
		t.Fatalf("legacy profile reference limit changed to %d", got)
	}
}

func TestKreaProModesExecuteTheirActualConditioningBranches(t *testing.T) {
	for _, mode := range []string{"references", "identity", "control"} {
		input := kreaProTestInput()
		input.KreaProMode = mode
		input.InputImage = "one.png"
		if mode == "references" {
			input.ReferenceImages = [3]string{"two.png", "three.png", "four.png"}
		} else if mode == "identity" {
			input.ReferenceImages[0] = "two.png"
		}
		if err := normalizeKreaPro(&input); err != nil {
			t.Fatal(err)
		}
		graph, err := buildKreaProPrompt(input)
		if err != nil {
			t.Fatal(err)
		}
		expected := map[string][]string{"references": {"3538", "3535", "3556", "3566"}, "identity": {"3513", "3539", "3527", "3512"}, "control": {"3517", "3518", "3543", "3525"}}
		for _, id := range expected[mode] {
			if graph[id] == nil || graph[id].(map[string]any)["class_type"] == "GatewayKreaProBypass" {
				t.Fatalf("%s silently bypassed required node %s", mode, id)
			}
		}
		if mode == "references" && graph["3538"].(map[string]any)["inputs"].(map[string]any)["image4"] == nil {
			t.Fatal("fourth photo not connected to the reference encoder")
		}
	}
}

func TestKreaProConditioningSwitchHasOnlySelectedModeInput(t *testing.T) {
	assertSwitch := func(t *testing.T, graph map[string]any, mode string) {
		t.Helper()
		wantID, wantClass := "3537", "LCPromptToConditioning"
		if mode == "identity" {
			wantID, wantClass = "3539", "Krea2EditGroundedEncode"
		} else if mode == "references" {
			wantID, wantClass = "3538", "Krea2EncodeRebalance"
		}
		switchNode, ok := graph["3541"].(map[string]any)
		if !ok || switchNode["class_type"] != "LCAnySwitch" {
			t.Fatalf("missing conditioning switch: %v", graph["3541"])
		}
		inputs, ok := switchNode["inputs"].(map[string]any)
		if !ok {
			t.Fatal("conditioning switch has invalid inputs")
		}
		connected := map[string]any{}
		for name, value := range inputs {
			if strings.HasPrefix(name, "any_") && value != nil {
				connected[name] = value
			}
		}
		if len(connected) != 1 {
			t.Fatalf("%s must connect only conditioning node %s; first-non-None switch has %v", mode, wantID, connected)
		}
		for name, value := range connected {
			link, ok := value.([]any)
			if !ok || len(link) != 2 || link[0] != wantID || fmt.Sprint(link[1]) != "0" {
				t.Fatalf("%s %s = %v, want [%s 0] (CONDITIONING, never CLIP)", mode, name, value, wantID)
			}
		}
		producer, ok := graph[wantID].(map[string]any)
		if !ok || producer["class_type"] != wantClass {
			t.Fatalf("%s conditioning encoder is missing or bypassed: %v", mode, graph[wantID])
		}
	}
	for _, variant := range []string{"text", "reference", "references", "references-2", "references-3", "identity", "identity-2", "identity-3", "identity-4", "control", "control-2", "control-3", "control-4", "inpaint", "outpaint", "lighting", "detailers"} {
		t.Run("export/"+variant, func(t *testing.T) {
			data, err := kreaProFiles.ReadFile("krea_pro_graphs/" + variant + ".json")
			if err != nil {
				t.Fatal(err)
			}
			graph := map[string]any{}
			if err := json.Unmarshal(data, &graph); err != nil {
				t.Fatal(err)
			}
			mode := "text"
			if strings.HasPrefix(variant, "identity") {
				mode = "identity"
			} else if strings.HasPrefix(variant, "reference") {
				mode = "references"
			}
			assertSwitch(t, graph, mode)
		})
	}
	for _, tc := range []struct {
		mode   string
		images int
	}{{"text", 0}, {"references", 1}, {"references", 2}, {"references", 3}, {"references", 4}, {"identity", 1}, {"identity", 2}, {"control", 1}, {"inpaint", 2}, {"outpaint", 1}} {
		t.Run(fmt.Sprintf("adapter/%s-%d", tc.mode, tc.images), func(t *testing.T) {
			input := kreaProTestInput()
			input.KreaProMode, input.KreaProUpscaler = tc.mode, "none"
			if tc.images > 0 {
				input.InputImage = "one.png"
			}
			for i := 1; i < tc.images; i++ {
				input.ReferenceImages[i-1] = fmt.Sprintf("ref%d.png", i+1)
			}
			if err := normalizeKreaPro(&input); err != nil {
				t.Fatal(err)
			}
			graph, err := buildKreaProPrompt(input)
			if err != nil {
				t.Fatal(err)
			}
			assertSwitch(t, graph, tc.mode)
		})
	}
}

func TestKreaProMoodboardReachesConditioning(t *testing.T) {
	input := kreaProTestInput()
	input.KreaProSettings = `{"nodes":{"2719":{"moodboard_positive":"soft natural window light","moodboard_negative":"oversaturation","style_only":true}}}`
	if err := normalizeKreaPro(&input); err != nil {
		t.Fatal(err)
	}
	graph, err := buildKreaProPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	moodboard := graph["2719"].(map[string]any)["inputs"].(map[string]any)
	if moodboard["moodboard_positive"] != "soft natural window light" || moodboard["moodboard_negative"] != "oversaturation" || moodboard["style_only"] != true {
		t.Fatal("moodboard settings ignored")
	}
}

func TestKreaProUpscalerSelectionAndLegacyCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, kind, want          string
		legacy, relay, scale      int
		vosrEnabled, relayEnabled bool
	}{
		{"legacy-default", "", "vosr", 0, 0, 2, true, false},
		{"legacy-three", "", "vosr", 3, 0, 3, true, false},
		{"legacy-one-wins", "", "none", 1, 4, 1, true, true},
		{"legacy-disabled", "", "none", 2, 0, 1, false, false},
		{"none-overrides-toggles", "none", "none", 3, 4, 1, true, true},
		{"vosr-overrides-toggles", "vosr", "vosr", 3, 4, 3, false, true},
		{"relay-overrides-one", "relay", "relay", 1, 4, 4, true, false},
		{"relay-default-scale", "relay", "relay", 2, 0, 2, true, false},
		{"relay-option-exclusive", "", "relay", 2, 4, 4, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := generationForm{KreaProUpscaler: tc.kind, KreaProUpscale: tc.legacy, KreaProRelayScale: tc.relay}
			settings := kreaProSettings{Enabled: map[string]bool{"4046": tc.vosrEnabled, "4150": tc.relayEnabled}}
			kind, scale, err := kreaProUpscaler(input, settings)
			if err != nil || kind != tc.want || scale != tc.scale {
				t.Fatalf("got %q %dx %v, want %q %dx", kind, scale, err, tc.want, tc.scale)
			}
		})
	}
	for _, input := range []generationForm{{KreaProUpscaler: "other"}, {KreaProUpscale: 4}, {KreaProRelayScale: 3}, {KreaProRelayScale: -2}} {
		if _, _, err := kreaProUpscaler(input, kreaProSettings{}); err == nil {
			t.Fatalf("accepted invalid upscaler: %+v", input)
		}
	}
}

func TestKreaProUpscalerRequestFields(t *testing.T) {
	for _, kind := range []string{"none", "vosr", "relay"} {
		input := kreaProTestInput()
		values := url.Values{"krea_pro_upscaler": {kind}, "krea_pro_relay_scale": {"4"}}
		if err := applyWorkflowManifestForm(kreaProWorkflowManifest(), values, &input); err != nil {
			t.Fatal(err)
		}
		if input.KreaProUpscaler != kind || input.KreaProRelayScale != 4 {
			t.Fatalf("request fields not parsed: %+v", input)
		}
	}
	for _, values := range []url.Values{{"krea_pro_upscaler": {"other"}}, {"krea_pro_relay_scale": {"3"}}} {
		input := kreaProTestInput()
		if err := applyWorkflowManifestForm(kreaProWorkflowManifest(), values, &input); err == nil {
			t.Fatalf("accepted invalid request: %v", values)
		}
	}
	values := url.Values{"template_id": {"text-to-image"}, "generation_workflow": {kreaProPresetID}, "positive_prompt": {"a ceramic cup"}, "krea_pro_upscaler": {"relay"}, "krea_pro_relay_scale": {"4"}}
	request := httptest.NewRequest("POST", "/generate/run", strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	input, err := parseGenerationForm(request)
	if err != nil || input.KreaProUpscaler != "relay" || input.KreaProRelayScale != 4 {
		t.Fatalf("HTTP form lost upscaler fields: %q %d %v", input.KreaProUpscaler, input.KreaProRelayScale, err)
	}
}

// Project the changed V29 nodes onto a legacy API export while the main agent
// regenerates the full exports. Defaults come from the preserved source file.
func kreaProV29TestGraph(t *testing.T, modes ...string) (map[string]any, []kreaProOption) {
	t.Helper()
	mode := "text"
	if len(modes) > 0 {
		mode = modes[0]
	}
	data, err := kreaProFiles.ReadFile("krea_pro_graphs/" + mode + ".json")
	if err != nil {
		t.Fatal(err)
	}
	graph := map[string]any{}
	if err := json.Unmarshal(data, &graph); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("../../integrations/harness-ultra/reference/Lonecat-v29.0.1.workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Nodes []struct {
			ID      int            `json:"id"`
			Type    string         `json:"type"`
			Widgets map[string]any `json:"widgets_values_named"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	for _, n := range source.Nodes {
		switch n.ID {
		case 4096, 4098, 4106, 4107, 4108, 4109, 4089, 4114, 4150:
			delete(n.Widgets, "control_after_generate")
			delete(n.Widgets, "Undo")
			delete(n.Widgets, "Clear")
			graph[fmt.Sprint(n.ID)] = map[string]any{"class_type": n.Type, "inputs": n.Widgets}
		}
	}
	if graph["1292"] != nil {
		graph["4102"] = graph["1292"]
		delete(graph, "1292")
	}
	if graph["1302"] != nil {
		graph["4105"] = graph["1302"]
		delete(graph, "1302")
	}
	for _, raw := range graph {
		for name, value := range raw.(map[string]any)["inputs"].(map[string]any) {
			v, ok := value.([]any)
			if !ok || len(v) != 2 {
				continue
			}
			switch v[0] {
			case "1292":
				v[0] = "4102"
			case "1302":
				v[0] = "4105"
			case "3657", "3658", "3839":
				v[0] = "4096"
			}
			raw.(map[string]any)["inputs"].(map[string]any)[name] = v
		}
	}
	for _, id := range []string{"3482", "3657", "3658", "3666", "3707", "3839", "4050"} {
		delete(graph, id)
	}
	fields := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
	fields("4096")["image"], fields("4096")["mask"] = []any{"3659", 0}, []any{"3662", 1}
	fields("4150")["image"] = []any{"3621", 0}
	fields("4071")["images"] = []any{"4150", 0}
	fields("3645")["skin_mask"] = []any{"4096", 1}
	fields("3481")["max_resolution"] = 1352
	fields("3481")["aspect_ratio"] = "Instagram Portrait (4:5) - 1080x1350"
	fields("4089")["image"] = []any{"2281", 0}
	fields("4114")["model"], fields("4114")["clip"] = []any{"3760", 0}, []any{"3760", 1}
	fields("3015")["model_1"], fields("3015")["positive_prompt"] = []any{"4114", 0}, []any{"4114", 2}
	options := []kreaProOption{}
	for _, option := range kreaProOptions() {
		switch option.ID {
		case "1292":
			option.ID = "4102"
		case "3657", "3658", "3839", "4096", "4150":
			continue
		}
		options = append(options, option)
	}
	options = append(options, kreaProOption{ID: "4096", Toggle: true, Fields: []kreaProField{
		{Name: "warmth", Type: "FLOAT", Min: workflowNumber(-1), Max: workflowNumber(1)},
		{Name: "pores", Type: "FLOAT", Min: workflowNumber(0), Max: workflowNumber(3)},
	}}, kreaProOption{ID: "4150", Toggle: true})
	return graph, options
}

func TestKreaProV29ExclusiveUpscalerBranches(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		scale int
	}{{"none", 2}, {"vosr", 3}, {"relay", 2}, {"relay", 4}} {
		t.Run(fmt.Sprintf("%s-%d", tc.kind, tc.scale), func(t *testing.T) {
			input := kreaProTestInput()
			input.KreaProUpscaler = tc.kind
			if tc.kind == "relay" {
				input.KreaProRelayScale = tc.scale
			}
			if tc.kind == "vosr" {
				input.KreaProUpscale = tc.scale
			}
			graph, options := kreaProV29TestGraph(t)
			settings, err := parseKreaProSettingsOptions(`{"enabled":{"4046":true,"4150":true}}`, options)
			if err != nil {
				t.Fatal(err)
			}
			result, err := buildKreaProGraph(input, settings, graph, options)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, raw := range result {
				counts[raw.(map[string]any)["class_type"].(string)]++
			}
			wantVOSR, wantRelay := 0, 0
			if tc.kind == "vosr" {
				wantVOSR = 1
			}
			if tc.kind == "relay" {
				wantRelay = 1
			}
			if counts["LCVOSRUpscalePipe"] != wantVOSR || counts["LCRelayVSRUpscale"] != wantRelay {
				t.Fatalf("upscalers cascade or wrong branch selected: %v", counts)
			}
			fields := func(id string) map[string]any { return result[id].(map[string]any)["inputs"].(map[string]any) }
			if fields("4105")["text"] != input.Positive || fields("4114")["text"] != input.Positive {
				t.Fatal("author prompt survived")
			}
			if _, exists := fields("4114")["last_in"]; exists || fields("4114")["block"] != "never" {
				t.Fatal("author prompt cache survived")
			}
			if !reflect.DeepEqual(fields("3760")["model"], []any{"4102", 0}) {
				t.Fatal("removed model shift remained bound")
			}
			canvas := fields("3481")
			if canvas["max_resolution"] != 0 || canvas["aspect_ratio"] != "custom" || canvas["custom_width"] != input.Width || canvas["custom_height"] != input.Height || canvas["resolution_source"] != false {
				t.Fatalf("author resolution overrides service canvas: %v", canvas)
			}
			if tc.kind == "relay" && (fields("4150")["upscale"] != fmt.Sprintf("%dx", tc.scale) || fields("4150")["seed"] != input.Seed || fields("4150")["source"] != "image") {
				t.Fatal("incorrect Relay scale or seed")
			}
			if tc.kind == "vosr" && fields("4047")["value"] != "3x" {
				t.Fatal("incorrect VOSR scale")
			}
		})
	}
}

func TestKreaProV29SkinMasterAndReferencePanels(t *testing.T) {
	input := kreaProTestInput()
	input.KreaProMode, input.InputImage = "references", "one.png"
	input.ReferenceImages[0] = "two.png"
	input.LoraNames[0], input.LoraModel[0], input.LoraClip[0] = "user.safetensors", .4, .7
	graph, options := kreaProV29TestGraph(t, "references-2")
	settings, err := parseKreaProSettingsOptions(`{"enabled":{"4096":true,"3681":true},"nodes":{"4096":{"warmth":-0.22,"pores":1.2},"3774":{"angle":314.717570393228}}}`, options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := buildKreaProGraph(input, settings, graph, options)
	if err != nil {
		t.Fatal(err)
	}
	fields := func(id string) map[string]any { return graph[id].(map[string]any)["inputs"].(map[string]any) }
	if result["4096"].(map[string]any)["class_type"] != "LCSkinMaster" || fields("4096")["warmth"] != -.22 || fields("4096")["pores"] != 1.2 || fields("4096")["seed"] != input.Seed {
		t.Fatal("SkinMaster settings or seed ignored")
	}
	if !reflect.DeepEqual(fields("9000")["model"], []any{"4102", 0}) {
		t.Fatal("LoRA uses removed shift")
	}
	if fields("4089")["selections"] != "[]" {
		t.Fatal("author TouchMask selections survived")
	}
	for id, want := range map[string][]any{"4109": {true, false, false}, "4106": {true, true}, "4107": {false, false}, "4108": {false, false}, "4098": {false, true, true}} {
		var rows []map[string]any
		if err := json.Unmarshal([]byte(fields(id)["rows"].(string)), &rows); err != nil {
			t.Fatal(err)
		}
		for i, value := range want {
			if rows[i]["value"] != value {
				t.Fatalf("panel %s row %d = %v, want %v", id, i, rows[i]["value"], value)
			}
		}
	}
}

func TestKreaProV29AnyPaintUsesUploadedMaskAndOutpaintFrame(t *testing.T) {
	for _, mode := range []string{"inpaint", "outpaint"} {
		t.Run(mode, func(t *testing.T) {
			input := kreaProTestInput()
			input.KreaProMode, input.KreaProUpscaler, input.InputImage = mode, "none", "source.png"
			if mode == "inpaint" {
				input.ReferenceImages[0] = "mask.png"
			}
			graph, options := kreaProV29TestGraph(t, mode)
			settings, err := parseKreaProSettingsOptions("", options)
			if err != nil {
				t.Fatal(err)
			}
			result, err := buildKreaProGraph(input, settings, graph, options)
			if err != nil {
				t.Fatal(err)
			}
			paint := result["3878"].(map[string]any)["inputs"].(map[string]any)
			wantImage, wantMask := []any{"3481", 1}, []any{"9502", 0}
			if mode == "outpaint" {
				wantImage, wantMask = []any{"3870", 0}, []any{"3870", 1}
			}
			if !reflect.DeepEqual(paint["image"], wantImage) || !reflect.DeepEqual(paint["mask"], wantMask) {
				t.Fatalf("author TouchMask still drives paint: %v", paint)
			}
			if result["4089"] != nil {
				t.Fatal("unused interactive TouchMask remains reachable")
			}
			if paint["seed"] != input.Seed || paint["prompt"] != input.Positive {
				t.Fatal("paint ignores service controls")
			}
		})
	}
}

func TestKreaProMissingRelayAndMalformedGraphsReturnErrors(t *testing.T) {
	input := kreaProTestInput()
	input.KreaProUpscaler = "relay"
	graph, options := kreaProV29TestGraph(t)
	delete(graph, "4150")
	settings, _ := parseKreaProSettingsOptions("", options)
	if _, err := buildKreaProGraph(input, settings, graph, options); err == nil || !strings.Contains(err.Error(), "RelayVSR") {
		t.Fatalf("missing Relay silently fell back: %v", err)
	}
	for _, bad := range []any{nil, "invalid", map[string]any{"inputs": nil}} {
		graph, options = kreaProV29TestGraph(t)
		graph["3857"] = bad
		if _, err := buildKreaProGraph(input, settings, graph, options); err == nil {
			t.Fatal("accepted malformed graph")
		}
	}
}
