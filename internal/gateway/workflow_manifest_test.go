package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestWorkflowManifestCatalogIsValid(t *testing.T) {
	manifests := workflowManifests()
	if err := validateWorkflowManifests(manifests); err != nil {
		t.Fatal(err)
	}
	manifest, ok := workflowManifestByID("minimax-h3-video")
	if !ok {
		t.Fatal("MiniMax H3 manifest is missing")
	}
	if manifest.Version != "5" || manifest.maximumInput("image") != 4 || manifest.requiresInput("image") {
		t.Fatalf("unexpected MiniMax manifest summary: %#v", manifest)
	}
	body, err := json.Marshal(workflowManifestCatalogValue())
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, marker := range []string{`"schema_version":1`, `"id":"minimax-h3-video"`, `"quality_profiles"`, `"prompt_assistant"`, `"postprocessing"`} {
		if !strings.Contains(text, marker) {
			t.Fatalf("manifest JSON is missing %s", marker)
		}
	}
	if strings.Contains(text, "VideoQuality") || strings.Contains(text, "InvalidMessage") {
		t.Fatalf("internal bindings leaked into manifest JSON: %s", text)
	}
}

func TestMiniMaxManifestCoversEveryVideoControl(t *testing.T) {
	body, err := embeddedFS.ReadFile("templates/_generation_sections.html")
	if err != nil {
		t.Fatal(err)
	}
	controlPattern := regexp.MustCompile(`name="(video_[^"]+)"`)
	controls := make(map[string]struct{})
	for _, match := range controlPattern.FindAllStringSubmatch(string(body), -1) {
		controls[match[1]] = struct{}{}
	}
	manifest := miniMaxH3WorkflowManifest()
	parameters := make(map[string]struct{}, len(manifest.Parameters))
	for _, parameter := range manifest.Parameters {
		parameters[parameter.Name] = struct{}{}
		if _, exists := controls[parameter.Name]; !exists {
			t.Errorf("manifest parameter %q has no form control", parameter.Name)
		}
	}
	for control := range controls {
		if _, exists := parameters[control]; !exists {
			t.Errorf("video form control %q is missing from manifest", control)
		}
	}
}

func TestWorkflowManifestParametersHaveFormControls(t *testing.T) {
	body, err := embeddedFS.ReadFile("templates/_generation_sections.html")
	if err != nil {
		t.Fatal(err)
	}
	controlPattern := regexp.MustCompile(`name="([^"]+)"`)
	controls := make(map[string]struct{})
	for _, match := range controlPattern.FindAllStringSubmatch(string(body), -1) {
		controls[match[1]] = struct{}{}
	}
	for _, manifest := range workflowManifests() {
		for _, parameter := range manifest.Parameters {
			if _, exists := controls[parameter.Name]; !exists {
				t.Errorf("%s parameter %q has no form control", manifest.ID, parameter.Name)
			}
		}
	}
}

func TestMiniMaxManifestDrivesFormDefaultsAndValidation(t *testing.T) {
	form := url.Values{
		"template_id":                        {"minimax-h3-video"},
		"video_sparse_budget":                {"0,35"},
		"video_sage_attention":               {"true"},
		"video_low_vram_attention":           {"true"},
		"video_low_vram_head_chunks":         {"8"},
		"video_chunk_feed_forward":           {"true"},
		"video_chunk_feed_forward_chunks":    {"3"},
		"video_chunk_feed_forward_threshold": {"8192"},
		"video_memory_optimize":              {"true"},
		"video_sparse_attention":             {"true"},
		"video_sparse_early_steps":           {"3"},
		"video_sparse_late_steps":            {"4"},
		"video_use_source_aspect":            {"true"},
		"video_rife_checkpoint":              {"rife49.pth"},
		"video_rife_multiplier":              {"2"},
		"video_rife_dtype":                   {"float32"},
		"video_rife_batch_size":              {"1"},
		"video_rtx_quality":                  {"ULTRA"},
		"video_color_method":                 {"adain"},
		"video_sharpen_method":               {"rcas"},
		"video_memory_mlp":                   {"auto"},
		"video_memory_precision":             {"Auto"},
		"video_memory_qkv":                   {"Auto"},
		"video_memory_attention":             {"Standard"},
		"video_aimdo_residency":              {"0 blocks"},
		"video_sparse_backend":               {"Kitchen INT8"},
		"video_sparse_early_schedule":        {"Hold"},
	}
	request := httptest.NewRequest(http.MethodPost, "/generate/run", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	input, err := parseGenerationForm(request)
	if err != nil {
		t.Fatal(err)
	}
	if input.VideoQuality != 720 || input.VideoDurationSeconds != 5 || input.VideoSteps != 25 || input.VideoRTXScale != 2 {
		t.Fatalf("manifest defaults were not applied: %#v", input)
	}
	if input.VideoSparseBudget != 0.35 || input.VideoSparseEarlyStep != 3 || input.VideoSparseLateStep != 4 {
		t.Fatalf("manifest values were not parsed: %#v", input)
	}
	if !input.VideoSageAttention || !input.VideoLowVRAMAttention || input.VideoLowVRAMHeadChunks != 8 || !input.VideoChunkFeedForward || input.VideoChunkFFChunks != 3 || input.VideoChunkFFThreshold != 8192 || !input.VideoMemoryOptimize || !input.VideoSparseAttention || !input.VideoUseSourceAspect {
		t.Fatalf("manifest booleans were not parsed: %#v", input)
	}

	form.Set("video_quality", "900")
	request = httptest.NewRequest(http.MethodPost, "/generate/run", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := parseGenerationForm(request); err == nil {
		t.Fatal("unsupported MiniMax quality was accepted")
	}

	form.Set("video_quality", "720")
	form.Set("video_rtx_scale", "2.1")
	request = httptest.NewRequest(http.MethodPost, "/generate/run", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := parseGenerationForm(request); err == nil {
		t.Fatal("RTX scale above the workflow maximum was accepted")
	}
}

func TestImageWorkflowManifestsDriveFamilyDefaults(t *testing.T) {
	tests := []struct {
		name     string
		template string
		preset   string
		check    func(t *testing.T, input generationForm)
	}{
		{
			name: "Krea2 text", template: "text-to-image", preset: "photoflow-krea2",
			check: func(t *testing.T, input generationForm) {
				if input.Steps != 25 || input.CFG != 7 || input.BaseMegapixels != 1 || input.OutputMegapixels != 1.9 || input.UpscaleSteps != 5 || input.DetailSteps != 2 || input.ColorMethod != "reinhard_lab" {
					t.Fatalf("Krea2 text defaults = %#v", input)
				}
			},
		},
		{
			name: "Krea2 edit", template: "image-to-image", preset: "photoflow-krea2-edit",
			check: func(t *testing.T, input generationForm) {
				if input.Steps != 8 || input.CFG != 1 || input.Denoise != 1 || input.ReferenceBoost != 4 || input.GroundingPixels != 768 || input.UpscaleFactor != 1.5 || input.SkinPreset != "Natural" {
					t.Fatalf("Krea2 edit defaults = %#v", input)
				}
			},
		},
		{
			name: "Flux2 edit", template: "image-to-image", preset: "photoflow-flux2-edit",
			check: func(t *testing.T, input generationForm) {
				if input.Steps != 25 || input.CFG != 1 || input.Denoise != 0.9 || input.SourceMegapixels != 1 || input.FluxGuidance != 4 || input.FluxUpscaleMode != "none" {
					t.Fatalf("Flux2 edit defaults = %#v", input)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			form := url.Values{"template_id": {test.template}, "generation_workflow": {test.preset}}
			request := httptest.NewRequest(http.MethodPost, "/generate/run", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			input, err := parseGenerationForm(request)
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, input)
		})
	}
}

func TestKrea2TextManifestDefinesPhotoFlowBranchDefaults(t *testing.T) {
	manifest := krea2TextWorkflowManifest()
	var input generationForm
	if err := applyWorkflowManifestDefaults(manifest, &input); err != nil {
		t.Fatal(err)
	}
	if input.KreaSageEnabled || !input.DetailEnabled || !input.ColorTransfer || input.ImageFilterEnabled {
		t.Fatalf("PhotoFlow branch defaults = %#v", input)
	}
	if input.KreaSageMode != "auto" || !input.KreaSageAllowCompile || !input.KreaFP16Accumulation {
		t.Fatalf("SageAttention defaults = %#v", input)
	}
	if input.ImageFilterContrast != 1 || input.ImageFilterSaturation != 1 || input.ImageFilterSharpness != 1 || input.ImageLevelMid != 127.5 || input.ImageLevelWhite != 255 {
		t.Fatalf("image filter defaults = %#v", input)
	}
}

func TestWorkflowManifestResolverDoesNotGuessBetweenEditFamilies(t *testing.T) {
	if _, ok := workflowManifestForInput(generationForm{TemplateID: "image-to-image"}); ok {
		t.Fatal("ambiguous image-to-image workflow was resolved without a preset")
	}
	manifest, ok := workflowManifestForInput(generationForm{TemplateID: "image-to-image", PresetID: "photoflow-flux2-edit"})
	if !ok || manifest.Family != modelFamilyFlux2 {
		t.Fatalf("Flux2 manifest resolution = %#v, %v", manifest, ok)
	}
}

func TestWorkflowManifestRequiredClassesExistInCompatibilityFixture(t *testing.T) {
	info := compatibilityFixtureObjectInfo(t)
	for _, manifest := range workflowManifests() {
		classes := append([]string(nil), manifest.RequiredClasses...)
		for _, mode := range manifest.Modes {
			classes = append(classes, mode.RequiredClasses...)
		}
		for _, branch := range manifest.Branches {
			classes = append(classes, branch.RequiredClasses...)
		}
		for _, classType := range classes {
			if _, exists := info[classType]; !exists {
				t.Errorf("%s compatibility fixture is missing manifest class %q", manifest.ID, classType)
			}
		}
	}
}

func TestWorkflowManifestsAreFilteredByGenerationAccess(t *testing.T) {
	manifests := workflowManifests()
	if got := workflowManifestsForUser(manifests, nil); len(got) != 0 {
		t.Fatalf("anonymous manifests = %d, want 0", len(got))
	}
	imageOnly := &User{CanUseQuickGeneration: true, CanGenerateImageToImage: true}
	if got := workflowManifestsForUser(manifests, imageOnly); len(got) != 2 {
		t.Fatalf("image-only manifests = %d, want 2", len(got))
	}
	video := &User{CanUseQuickGeneration: true, CanGenerateVideo: true}
	if got := workflowManifestsForUser(manifests, video); len(got) != 1 || got[0].ID != "minimax-h3-video" {
		t.Fatalf("video manifests = %#v", got)
	}
}

func TestGenerationCapabilitiesHandlerReturnsOnlyAccessibleManifests(t *testing.T) {
	app := &App{}
	user := &User{CanUseQuickGeneration: true, CanGenerateImageToImage: true}
	request := httptest.NewRequest(http.MethodGet, "/generate/capabilities", nil)
	request = request.WithContext(context.WithValue(request.Context(), userCtxKey, user))
	response := httptest.NewRecorder()

	app.handleGenerationCapabilities(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "private, max-age=60" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var catalog workflowManifestCatalog
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != workflowManifestSchemaVersion || len(catalog.Workflows) != 2 {
		t.Fatalf("capability catalog = %#v", catalog)
	}
	for _, manifest := range catalog.Workflows {
		if manifest.TemplateID != "image-to-image" {
			t.Fatalf("unexpected manifest %q for image-only user", manifest.ID)
		}
	}
}

func TestWorkflowManifestRuntimeValidationUsesModeAndProfile(t *testing.T) {
	manifest := miniMaxH3WorkflowManifest()
	input := generationForm{VideoMode: miniMaxH3FrameMode, VideoIntegratedTurbo: true}
	if err := applyWorkflowManifestDefaults(manifest, &input); err != nil {
		t.Fatal(err)
	}
	input.VideoMode = miniMaxH3ReferenceMode
	input.VideoSteps = 8
	input.VideoSampler = "res_multistep"
	if err := validateWorkflowManifestInput(manifest, input); err == nil {
		t.Fatal("integrated Turbo accepted an unlocked sampler")
	}
	input.VideoSampler = "euler"
	input.VideoSteps = 9
	if err := validateWorkflowManifestInput(manifest, input); err == nil {
		t.Fatal("integrated Turbo accepted steps outside its profile")
	}
	input.VideoSteps = 8
	if err := validateWorkflowManifestInput(manifest, input); err != nil {
		t.Fatal(err)
	}
	input.VideoMode = miniMaxH3FrameMode
	if err := validateWorkflowManifestInput(manifest, input); err == nil {
		t.Fatal("integrated reference-only model accepted frames mode")
	}
}

func TestMiniMaxH3ManifestSeparatesAssistantProfilesByMode(t *testing.T) {
	assistant := miniMaxH3WorkflowManifest().PromptAssistant
	if got, want := assistant.ModeProfiles[miniMaxH3FrameMode], "minimax-h3-fl2va"; got != want {
		t.Fatalf("FL2VA assistant profile = %q, want %q", got, want)
	}
	if got, want := assistant.ModeProfiles[miniMaxH3ReferenceMode], "minimax-h3-ref2va"; got != want {
		t.Fatalf("REF2VA assistant profile = %q, want %q", got, want)
	}
	if len(assistant.ModeRules[miniMaxH3FrameMode]) < 2 || len(assistant.ModeRules[miniMaxH3ReferenceMode]) < 2 {
		t.Fatalf("branch-specific assistant rules are incomplete: %#v", assistant.ModeRules)
	}
}
