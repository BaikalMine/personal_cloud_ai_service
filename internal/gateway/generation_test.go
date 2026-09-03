package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGenerateTemplateRenders(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = templates.ExecuteTemplate(&output, "generate", map[string]any{
		"Title": "Быстрая генерация", "CSRF": "csrf", "AssetVersion": "asset",
		"Retention":         newRetentionPolicyView(Config{}.Retention),
		"Workflows":         []workflowView{{ID: "text-to-image", Name: "Текст в изображение", Description: "Описание"}},
		"GenerationPresets": []generationPreset{{ID: "photoflow-krea2-edit", TemplateID: "image-to-image", Name: "Krea 2: фото и промт", Family: modelFamilyKrea2, Available: true, ModelID: "krea2:test", ModelCount: 2, RequiresImage: true, MaxInputImages: 2}},
		"QuickModels":       []generationModel{{ID: "krea2:test", Name: "krea.safetensors", DisplayName: "Krea2 / krea", Family: modelFamilyKrea2, Available: true, DefaultSteps: 8, DefaultCFG: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "data-comfy-generation") || !strings.Contains(output.String(), "/static/generate.js") ||
		!strings.Contains(output.String(), "/static/generation-store.js") || !strings.Contains(output.String(), "/static/generation-media.js") ||
		!strings.Contains(output.String(), "/static/generation-summary.js") || !strings.Contains(output.String(), "Krea 2: фото и промт") ||
		!strings.Contains(output.String(), "Выберите workflow и модель") || !strings.Contains(output.String(), "Что будет создавать результат") ||
		!strings.Contains(output.String(), "generation-editor-profile") || !strings.Contains(output.String(), "prompt-assistant-template") ||
		!strings.Contains(output.String(), "Перенос внешности и редактирование") || !strings.Contains(output.String(), "prompt-assistant-think") ||
		!strings.Contains(output.String(), "Максимум деталей · 4,7 Мп") || !strings.Contains(output.String(), "data-gallery-image-picker-open") ||
		!strings.Contains(output.String(), "Из моих генераций") || !strings.Contains(output.String(), "generation-image-picker-refresh") ||
		!strings.Contains(output.String(), "generation-image-picker-grid") || !strings.Contains(output.String(), `data-requires-advanced-settings="true"`) {
		t.Fatal("generation template did not render the wizard")
	}
}

func TestGenerationJavaScriptModulesAreServed(t *testing.T) {
	app := &App{}
	for requestPath, assetPath := range staticJavaScriptAssets {
		requestPath, assetPath := requestPath, assetPath
		t.Run(strings.TrimPrefix(requestPath, "/static/"), func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, requestPath, nil)
			response := httptest.NewRecorder()
			app.handleStaticJS(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d", requestPath, response.Code, http.StatusOK)
			}
			want, err := embeddedFS.ReadFile(assetPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(response.Body.Bytes(), want) {
				t.Fatalf("GET %s returned the wrong embedded asset", requestPath)
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/static/generation-unknown.js", nil)
	response := httptest.NewRecorder()
	app.handleStaticJS(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown JavaScript status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestQuickGenerationTelemetryPathUsesClientRequestID(t *testing.T) {
	const requestID = "generation-0123456789abcdef"
	if got, want := quickGenerationTelemetryPath("  "+requestID+"  "), "/generate/run/"+requestID; got != want {
		t.Fatalf("quick generation telemetry path = %q, want %q", got, want)
	}
}

func TestGenerationImageSourcesAreAvailableToEveryImageInputScenario(t *testing.T) {
	app := &App{}
	handler := app.requireQuickGenerationTypes(quickGenerationImageInputTemplateIDs(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name       string
		user       *User
		wantStatus int
	}{
		{
			name:       "Krea2 and Flux2 image edit",
			user:       &User{CanUseQuickGeneration: true, CanGenerateImageToImage: true},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "MiniMax video",
			user:       &User{CanUseQuickGeneration: true, CanGenerateVideo: true},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "text only",
			user:       &User{CanUseQuickGeneration: true, CanGenerateTextToImage: true},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/generate/library/images", nil)
			request = request.WithContext(context.WithValue(request.Context(), userCtxKey, test.user))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestGenerationClientStopsRecoveryAfterConfirmedRunFailure(t *testing.T) {
	script, err := embeddedFS.ReadFile("static/generate.js")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(script), `const response = await fetch("/generate/run"`)
	if start < 0 {
		t.Fatal("generation submit response block was not found")
	}
	end := strings.Index(string(script[start:]), "updateGenerationQuota(payload.quota)")
	if end < 0 {
		t.Fatal("generation submit response block was not found")
	}
	block := string(script[start : start+end])
	for _, marker := range []string{"if (!response.ok)", "clearActiveGeneration()", "setGenerationActions({ retry: true })", "runProgress.hidden = true", "return;"} {
		if !strings.Contains(block, marker) {
			t.Fatalf("confirmed failure block is missing %q", marker)
		}
	}
	if strings.Contains(block, `throw new Error(payload.error`) {
		t.Fatal("confirmed HTTP failure still enters ambiguous request recovery")
	}
}

func TestQuickGenerationUploadForwardsNamespacedImageToComfyUI(t *testing.T) {
	var receivedSubfolder, receivedName string
	imagePayload := testPNG(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/upload/image" {
			t.Fatalf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Fatal(err)
		}
		receivedSubfolder = r.FormValue("subfolder")
		file, header, err := r.FormFile("image")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		payload, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(payload, imagePayload) {
			t.Fatalf("uploaded image = %q, err = %v", payload, err)
		}
		receivedName = header.Filename
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"sample.png","subfolder":"` + receivedSubfolder + `"}`))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstreamURL, SessionSecret: "01234567890123456789012345678901"}, proxyCounts: make(map[string]int64)}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", "sample.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(imagePayload); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("type", "input"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	user := &User{ID: 7}
	request := httptest.NewRequest(http.MethodPost, "/generate/upload/image", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request = request.WithContext(context.WithValue(request.Context(), userCtxKey, user))
	response := httptest.NewRecorder()
	app.quickGenerationUploadHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.HasPrefix(receivedName, "gateway-") || !strings.HasSuffix(receivedName, ".png") || receivedSubfolder != comfyUploadNamespace(app.comfyClientID(user.ID)) {
		t.Fatalf("upstream image = %q, subfolder = %q", receivedName, receivedSubfolder)
	}
}

func TestStreamGenerationOutputUsesOriginalVideoRouteAndRange(t *testing.T) {
	var requestPath, requestRange string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		requestRange = r.Header.Get("Range")
		if got := r.URL.Query().Get("filename"); got != "clip.mp4" {
			t.Fatalf("filename = %q", got)
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-4/5")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("video"))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstreamURL}}
	request := httptest.NewRequest(http.MethodGet, "/generate/output", nil)
	request.Header.Set("Range", "bytes=0-4")
	response := httptest.NewRecorder()
	streamed, err := app.streamGenerationOutput(response, request, generationOutput{
		Filename: "clip.mp4", Type: "output", MediaType: "video",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !streamed || requestPath != "/view" || requestRange != "bytes=0-4" {
		t.Fatalf("video output streamed=%v path=%q range=%q", streamed, requestPath, requestRange)
	}
	if response.Code != http.StatusPartialContent || response.Header().Get("Content-Type") != "video/mp4" || response.Header().Get("Content-Range") != "bytes 0-4/5" || response.Body.String() != "video" {
		t.Fatalf("output = (%d, %q, %q, %q)", response.Code, response.Header().Get("Content-Type"), response.Header().Get("Content-Range"), response.Body.String())
	}
}

func TestReadGenerationOutputArchiveFingerprintsBeyondArchiveLimit(t *testing.T) {
	payload := bytes.Repeat([]byte("video"), 32)
	body, sizeBytes, contentHash, err := readGenerationOutputArchive(bytes.NewReader(payload), 16, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil || sizeBytes != int64(len(payload)) || len(contentHash) != 64 {
		t.Fatalf("archive = body:%d size:%d hash:%q", len(body), sizeBytes, contentHash)
	}
	if _, _, _, err := readGenerationOutputArchive(bytes.NewReader(payload), 16, int64(len(payload)-1)); err == nil {
		t.Fatal("output above the absolute fingerprint limit was accepted")
	}
}

func TestSpoolGenerationOutputArchiveBoundsDiskAndCleansUp(t *testing.T) {
	directory := t.TempDir()
	payload := bytes.Repeat([]byte("video"), 32)
	file, spoolPath, sizeBytes, contentHash, err := spoolGenerationOutputArchive(
		bytes.NewReader(payload), directory, int64(len(payload)), 1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	if file == nil || spoolPath == "" || sizeBytes != int64(len(payload)) || len(contentHash) != 64 {
		t.Fatalf("spool = file:%v path:%q size:%d hash:%q", file, spoolPath, sizeBytes, contentHash)
	}
	stored, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(stored, payload) {
		t.Fatalf("stored payload=%q err=%v", stored, err)
	}
	archive := generationOutputArchive{File: file, path: spoolPath}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spoolPath); !os.IsNotExist(err) {
		t.Fatalf("archive spool remains after close: %v", err)
	}

	file, spoolPath, sizeBytes, contentHash, err = spoolGenerationOutputArchive(
		bytes.NewReader(payload), directory, 16, 1024,
	)
	if err != nil {
		t.Fatal(err)
	}
	if file != nil || spoolPath != "" || sizeBytes != int64(len(payload)) || len(contentHash) != 64 {
		t.Fatalf("oversized spool = file:%v path:%q size:%d hash:%q", file, spoolPath, sizeBytes, contentHash)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("oversized archive left spool files=%v err=%v", entries, err)
	}
	if _, _, _, _, err := spoolGenerationOutputArchive(bytes.NewReader(payload), directory, 16, 31); err == nil {
		t.Fatal("output above fingerprint limit was accepted")
	}
	entries, err = os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed archive left spool files=%v err=%v", entries, err)
	}
}

func TestGenerationDownloadDisposition(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/generate/output?download=1", nil)
	response := httptest.NewRecorder()
	setGenerationDownloadDisposition(response, request, `folder\\portrait.png`)
	if got := response.Header().Get("Content-Disposition"); !strings.Contains(got, `attachment; filename=portrait.png`) {
		t.Fatalf("Content-Disposition = %q", got)
	}

	previewRequest := httptest.NewRequest(http.MethodGet, "/generate/output", nil)
	previewResponse := httptest.NewRecorder()
	setGenerationDownloadDisposition(previewResponse, previewRequest, "portrait.png")
	if got := previewResponse.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("preview Content-Disposition = %q, want empty", got)
	}
}

func TestParseGenerationFloatAcceptsRussianDecimalSeparator(t *testing.T) {
	for raw, want := range map[string]float64{"0,22": 0.22, "1.5": 1.5, " 4,7 ": 4.7} {
		got, err := parseGenerationFloat(raw)
		if err != nil || got != want {
			t.Fatalf("parseGenerationFloat(%q) = %v, %v; want %v, nil", raw, got, err, want)
		}
	}
}

func TestEnforceGenerationSettingsAccessRemovesUnpermittedLoras(t *testing.T) {
	input := generationForm{LorasConfigured: true}
	input.LoraNames[0] = "MiniMaxH3/example.safetensors"
	input.LoraModel[0] = 0.75
	input.LoraClip[0] = 0.9

	enforceGenerationSettingsAccess(&User{Role: "user"}, &input)
	if input.LorasConfigured || input.LoraNames[0] != "" || input.LoraModel[0] != 0 || input.LoraClip[0] != 0 {
		t.Fatalf("restricted user kept LoRA controls: %#v", input)
	}

	allowed := generationForm{LorasConfigured: true}
	allowed.LoraNames[0] = "MiniMaxH3/example.safetensors"
	enforceGenerationSettingsAccess(&User{Role: "user", CanUseAdvancedGenerationSettings: true}, &allowed)
	if !allowed.LorasConfigured || allowed.LoraNames[0] == "" {
		t.Fatalf("allowed user lost LoRA controls: %#v", allowed)
	}
}

func TestValidateVideoGenerationQualityLimitsBaseOnly(t *testing.T) {
	user := &User{Role: "user", MaxVideoGenerationQuality: 720}
	if err := validateVideoGenerationQuality(user, generationForm{VideoQuality: 1080}); err == nil {
		t.Fatal("base video quality above the account limit was accepted")
	}
	for name, input := range map[string]generationForm{
		"base at limit":       {VideoQuality: 720},
		"two times RTX":       {VideoQuality: 720, VideoRTXEnabled: true, VideoRTXScale: 2},
		"default RTX scaling": {VideoQuality: 720, VideoRTXEnabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateVideoGenerationQuality(user, input); err != nil {
				t.Fatalf("allowed base quality with optional RTX upscale was rejected: %v", err)
			}
		})
	}
	if err := validateVideoGenerationQuality(user, generationForm{VideoQuality: 720, VideoRTXEnabled: true, VideoRTXScale: 2.1}); err == nil {
		t.Fatal("RTX scale above the workflow maximum was accepted")
	}
}

func TestParseGenerationFormAcceptsCommasInPhotoAndPromptControls(t *testing.T) {
	form := url.Values{
		"width":                 {"1024"},
		"height":                {"1024"},
		"steps":                 {"20"},
		"cfg":                   {"1,5"},
		"denoise":               {"0,75"},
		"reference_boost":       {"4,25"},
		"upscale_factor":        {"1,5"},
		"upscale_denoise":       {"0,15"},
		"skin_coolness":         {"0,22"},
		"skin_brightness":       {"0,12"},
		"skin_texture_preserve": {"0,88"},
		"post_denoise_edge":     {"0,05"},
		"lora_model_strength_1": {"0,8"},
		"lora_clip_strength_1":  {"1,0"},
		"output_megapixels":     {"1,9"},
		"source_megapixels":     {"1,5"},
		"flux_guidance":         {"3,5"},
		"flux_active_scale":     {"0,8"},
		"flux_token_whiten":     {"0,25"},
		"flux_norm_equalize":    {"0,5"},
		"color_strength":        {"0,6"},
		"template_id":           {"image-to-image"},
		"generation_workflow":   {"photoflow-krea2-edit"},
		"positive_prompt":       {"portrait"},
		"input_image":           {"gateway/input.png"},
		"model":                 {"krea2:test"},
		"skin_preset":           {"Natural"},
		"lut_name":              {"LC_Crushed_Blacks.cube"},
		"upscale_sampler":       {"deis"},
		"upscale_scheduler":     {"simple"},
		"sampler":               {"euler"},
		"scheduler":             {"simple"},
		"seed":                  {"42"},
	}
	request := httptest.NewRequest(http.MethodPost, "/generate/run", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	input, err := parseGenerationForm(request)
	if err != nil {
		t.Fatalf("parseGenerationForm() error = %v", err)
	}
	if input.CFG != 1.5 || input.Denoise != 0.75 || input.ReferenceBoost != 4.25 || input.UpscaleFactor != 1.5 || input.SkinCoolness != 0.22 || input.LoraModel[0] != 0.8 || input.FluxGuidance != 3.5 || input.FluxActiveScale != 0.8 || input.FluxTokenWhiten != 0.25 || input.FluxNormEqualize != 0.5 || input.ColorStrength != 0.6 {
		t.Fatalf("comma controls were not preserved: %#v", input)
	}
}

func TestParseGenerationFormKeepsReferenceRolesAndSources(t *testing.T) {
	form := url.Values{
		"template_id":         {"image-to-image"},
		"generation_workflow": {"photoflow-flux2-edit"},
		"model":               {"flux2:test"},
		"positive_prompt":     {"replace the jacket"},
		"input_image":         {"gateway/base.png"},
		"input_image_2":       {"gateway/jacket.png"},
		"image_role_1":        {"identity"},
		"image_source_1":      {"gallery"},
		"image_source_id_1":   {"17"},
		"image_source_name_1": {"base.png"},
		"image_role_2":        {"wardrobe_object"},
		"image_source_2":      {"device"},
		"image_source_name_2": {"jacket.png"},
		"width":               {"1024"},
		"height":              {"1024"},
		"steps":               {"20"},
		"cfg":                 {"1"},
		"denoise":             {"0.75"},
		"sampler":             {"euler"},
		"scheduler":           {"simple"},
		"seed":                {"42"},
	}
	request := httptest.NewRequest(http.MethodPost, "/generate/run", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	input, err := parseGenerationForm(request)
	if err != nil {
		t.Fatalf("parseGenerationForm() error = %v", err)
	}
	references := input.references()
	if len(references) != 2 {
		t.Fatalf("references = %#v", references)
	}
	if references[0].Number != 1 || references[0].Role != "base_scene" || references[0].Source != "gallery" || references[0].SourceID != "17" || references[0].SourceName != "base.png" {
		t.Fatalf("primary reference = %#v", references[0])
	}
	if references[1].Number != 2 || references[1].Role != "wardrobe_object" || references[1].Source != "device" || references[1].SourceName != "jacket.png" {
		t.Fatalf("secondary reference = %#v", references[1])
	}

	video := generationForm{
		TemplateID:      "minimax-h3-video",
		VideoMode:       miniMaxH3FrameMode,
		InputImage:      "gateway/first.png",
		ReferenceImages: [3]string{"gateway/last.png"},
		ReferenceMetadata: [maxGenerationReferenceSlots]generationReferenceMetadata{
			{Role: "style", Source: "gallery"},
			{Role: "identity", Source: "device"},
		},
	}.references()
	if video[0].Role != "first_frame" || video[1].Role != "last_frame" {
		t.Fatalf("MiniMax frame roles = %#v", video)
	}
}

func TestWorkflowDefinitionsBuildTypedPrompt(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image")
	if !ok {
		t.Fatal("image-to-image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		TemplateID: "image-to-image", ModelName: "model.safetensors", ModelFamily: modelFamilyCheckpoint, InputImage: "gateway/gateway-0123456789abcdef01234567/input.png",
		Positive: "portrait", Negative: "blurry", Width: 1024, Height: 768, Steps: 20, CFG: 7, Denoise: 0.65, Seed: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	node, ok := prompt["3"].(map[string]any)
	if !ok {
		t.Fatalf("sampler node has type %T", prompt["3"])
	}
	inputs := node["inputs"].(map[string]any)
	if got, want := inputs["seed"], int64(42); got != want {
		t.Fatalf("seed = %v, want %v", got, want)
	}
	if got, want := inputs["denoise"], 0.65; got != want {
		t.Fatalf("denoise = %v, want %v", got, want)
	}
	imageNode := prompt["10"].(map[string]any)
	imageInputs := imageNode["inputs"].(map[string]any)
	if got, want := imageInputs["image"], "gateway/gateway-0123456789abcdef01234567/input.png"; got != want {
		t.Fatalf("image = %v, want %v", got, want)
	}
	if _, err := json.Marshal(prompt); err != nil {
		t.Fatal(err)
	}
}

func TestKrea2WorkflowUsesDiffusionDependencies(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "text-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "qwen3vl_4b_fp8_scaled.safetensors", VAE: "qwen_image_vae.safetensors",
		Positive: "portrait", Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Seed: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	loader := prompt["1"].(map[string]any)
	if loader["class_type"] != "UNETLoader" || loader["inputs"].(map[string]any)["unet_name"] != "Krea2/model.safetensors" {
		t.Fatalf("unexpected diffusion loader: %#v", loader)
	}
	clip := prompt["2"].(map[string]any)["inputs"].(map[string]any)
	if clip["type"] != "krea2" || clip["clip_name"] != "qwen3vl_4b_fp8_scaled.safetensors" {
		t.Fatalf("unexpected Krea 2 text encoder: %#v", clip)
	}
}

func TestKrea2WorkflowKeepsFullHDPortraitDimensions(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "text-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", TextEncoder: "encoder.safetensors",
		VAE: "qwen_image_vae.safetensors", Lora: "lenovo_krea2.safetensors", LoraStrength: 0.8,
		Positive: "portrait", Width: 1080, Height: 1920, Steps: 8, CFG: 1,
		Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		BaseMegapixels: 1.5, UpscaleSteps: 8, UpscaleDenoise: 0.22,
		DetailEnabled: true, DetailSteps: 3, DetailDenoise: 0.035,
	})
	if err != nil {
		t.Fatal(err)
	}
	upscale := prompt["11"].(map[string]any)["inputs"].(map[string]any)
	if upscale["width"] != 1080 || upscale["height"] != 1920 {
		t.Fatalf("final dimensions = %vx%v, want 1080x1920", upscale["width"], upscale["height"])
	}
	upscaleSampler := prompt["13"].(map[string]any)["inputs"].(map[string]any)
	if upscaleSampler["steps"] != 8 || upscaleSampler["denoise"] != 0.22 {
		t.Fatalf("unexpected upscale pass: %#v", upscaleSampler)
	}
	detailSampler := prompt["14"].(map[string]any)["inputs"].(map[string]any)
	if detailSampler["steps"] != 3 || detailSampler["denoise"] != 0.035 {
		t.Fatalf("unexpected detail pass: %#v", detailSampler)
	}
}

func TestGenerationDimensionsFollowAspectAndMegapixels(t *testing.T) {
	width, height, err := generationDimensions("3:4", 1.9, 16, 0)
	if err != nil {
		t.Fatal(err)
	}
	if width%16 != 0 || height%16 != 0 || math.Abs(float64(width)/float64(height)-0.75) > 0.02 {
		t.Fatalf("dimensions = %dx%d, want a 3:4 result aligned to 16 pixels", width, height)
	}
	megapixels := float64(width*height) / (1024 * 1024)
	if math.Abs(megapixels-1.9) > 0.08 {
		t.Fatalf("megapixels = %.2f, want approximately 1.9", megapixels)
	}
}

func TestKrea2WorkflowAppliesLoraChainAndColorTransfer(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "text-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", TextEncoder: "encoder.safetensors", VAE: "vae.safetensors", Lora: "fallback.safetensors",
		LorasConfigured: true, LoraNames: [maxGenerationLoraSlots]string{"lenovo_krea2.safetensors", "Krea2/detailer.safetensors", "", "", "Krea2/final-style.safetensors"},
		LoraModel: [maxGenerationLoraSlots]float64{0.8, 2, 0, 0, 0.45}, LoraClip: [maxGenerationLoraSlots]float64{1, 0.7, 0, 0, 0.55},
		Positive: "portrait", AspectRatio: "3:4", OutputMegapixels: 1.9, DimensionMultiple: 16,
		Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Seed: 42,
		KreaSageEnabled: true, KreaSageMode: "auto", KreaSageAllowCompile: true, KreaFP16Accumulation: true,
		ColorTransfer: true, ColorMethod: "mkl_lab", ColorMode: "uniform", ColorStrength: 0.75,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondLora := prompt["17"].(map[string]any)["inputs"].(map[string]any)
	if secondLora["lora_name"] != "Krea2/detailer.safetensors" || secondLora["strength_model"] != 2.0 || secondLora["strength_clip"] != 0.7 {
		t.Fatalf("unexpected second LoRA: %#v", secondLora)
	}
	fifthLora := prompt["gateway_lora_5"].(map[string]any)["inputs"].(map[string]any)
	if fifthLora["lora_name"] != "Krea2/final-style.safetensors" || fifthLora["model"].([]any)[0] != "19" {
		t.Fatalf("unexpected fifth LoRA: %#v", fifthLora)
	}
	sage := prompt["gateway_krea_sage"].(map[string]any)["inputs"].(map[string]any)
	if got := sage["model"].([]any)[0]; got != "gateway_lora_5" {
		t.Fatalf("Krea2 SageAttention is not connected to the fifth LoRA: %v", got)
	}
	if got := prompt["5"].(map[string]any)["inputs"].(map[string]any)["model"].([]any)[0]; got != "gateway_krea_sage" {
		t.Fatalf("Krea2 torch patch is not connected to SageAttention: %v", got)
	}
	color := prompt["20"].(map[string]any)["inputs"].(map[string]any)
	if color["method"] != "mkl_lab" || color["source_stats"] != "uniform" || color["strength"] != 0.75 {
		t.Fatalf("unexpected color transfer: %#v", color)
	}
}

func TestKrea2TextWorkflowMirrorsOptionalPhotoFlowBranches(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "text-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 workflow is missing")
	}
	base := generationForm{
		ModelName: "Krea2/model.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "encoder.safetensors", VAE: "vae.safetensors", Lora: "lenovo_krea2.safetensors",
		Positive: "portrait", Width: 1024, Height: 1024, OutputMegapixels: 1.9,
		Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
	}

	t.Run("all optional branches disabled", func(t *testing.T) {
		prompt, err := definition.buildPrompt(base)
		if err != nil {
			t.Fatal(err)
		}
		for _, nodeID := range []string{"5", "14", "20", "gateway_krea_sage", "gateway_krea_image_filter", "gateway_krea_image_levels"} {
			if _, exists := prompt[nodeID]; exists {
				t.Fatalf("disabled branch left node %s in prompt", nodeID)
			}
		}
		if got := prompt["9"].(map[string]any)["inputs"].(map[string]any)["model"]; !reflect.DeepEqual(got, []any{"19", 0}) {
			t.Fatalf("base model source = %#v", got)
		}
		if got := prompt["15"].(map[string]any)["inputs"].(map[string]any)["samples"]; !reflect.DeepEqual(got, []any{"13", 0}) {
			t.Fatalf("refinement bypass = %#v", got)
		}
		if got := prompt["16"].(map[string]any)["inputs"].(map[string]any)["images"]; !reflect.DeepEqual(got, []any{"15", 0}) {
			t.Fatalf("base output = %#v", got)
		}
	})

	t.Run("all optional branches enabled", func(t *testing.T) {
		input := base
		input.KreaSageEnabled = true
		input.KreaSageMode = "sageattn_qk_int8_pv_fp16_triton"
		input.KreaSageAllowCompile = true
		input.KreaFP16Accumulation = true
		input.DetailEnabled = true
		input.DetailSteps = 3
		input.DetailDenoise = 0.04
		input.ColorTransfer = true
		input.ColorMethod = "reinhard_lab"
		input.ColorMode = "per_frame"
		input.ColorStrength = 0.8
		input.ImageFilterEnabled = true
		input.ImageFilterBrightness = 0.1
		input.ImageFilterContrast = 1.2
		input.ImageFilterSaturation = 0.9
		input.ImageFilterSharpness = 1.3
		input.ImageFilterBlur = 1
		input.ImageFilterGaussian = 0.5
		input.ImageFilterEdge = 0.2
		input.ImageFilterDetail = true
		input.ImageLevelBlack = 2
		input.ImageLevelMid = 126
		input.ImageLevelWhite = 250

		prompt, err := definition.buildPrompt(input)
		if err != nil {
			t.Fatal(err)
		}
		sage := prompt["gateway_krea_sage"].(map[string]any)
		if sage["class_type"] != "PathchSageAttentionKJ" || sage["inputs"].(map[string]any)["sage_attention"] != input.KreaSageMode {
			t.Fatalf("SageAttention node = %#v", sage)
		}
		if got := prompt["13"].(map[string]any)["inputs"].(map[string]any)["model"]; !reflect.DeepEqual(got, []any{"5", 0}) {
			t.Fatalf("patched model source = %#v", got)
		}
		if got := prompt["15"].(map[string]any)["inputs"].(map[string]any)["samples"]; !reflect.DeepEqual(got, []any{"14", float64(0)}) {
			t.Fatalf("refinement source = %#v", got)
		}
		filter := prompt["gateway_krea_image_filter"].(map[string]any)["inputs"].(map[string]any)
		if got := filter["image"]; !reflect.DeepEqual(got, []any{"20", 0}) || filter["detail_enhance"] != "true" || filter["contrast"] != 1.2 {
			t.Fatalf("image filter = %#v", filter)
		}
		levels := prompt["gateway_krea_image_levels"].(map[string]any)["inputs"].(map[string]any)
		if levels["mid_level"] != 126.0 || levels["white_level"] != 250.0 {
			t.Fatalf("image levels = %#v", levels)
		}
		if got := prompt["16"].(map[string]any)["inputs"].(map[string]any)["images"]; !reflect.DeepEqual(got, []any{"gateway_krea_image_levels", 0}) {
			t.Fatalf("filtered output = %#v", got)
		}
	})
}

func TestFlux2ImageWorkflowUsesReferenceLatent(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", ModelFamily: modelFamilyFlux2,
		TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/gateway-0123456789abcdef01234567/input.png",
		Positive:   "replace the background", Negative: "artifacts", Width: 1024, Height: 768,
		Steps: 20, CFG: 5, Denoise: 0.7, Seed: 42,
		LUTName: "street.cube", LUTStrength: 0.28, LUTEnabled: true,
		LoraNames: [maxGenerationLoraSlots]string{"Flux2\\klein_snofs_v1_4.safetensors", "", "", "", "", "", "", "", "", "Flux2\\final-style.safetensors"}, LoraModel: [maxGenerationLoraSlots]float64{0.75, 0, 0, 0, 0, 0, 0, 0, 0, 0.4},
	})
	if err != nil {
		t.Fatal(err)
	}
	loadImage := prompt["1075"].(map[string]any)["inputs"].(map[string]any)
	if loadImage["image"] != "gateway/gateway-0123456789abcdef01234567/input.png" {
		t.Fatalf("unexpected input image: %#v", loadImage)
	}
	reference := prompt["1398"].(map[string]any)
	if reference["class_type"] != "LCReferenceLatent" {
		t.Fatalf("reference conditioning is missing: %#v", reference)
	}
	scaler := prompt["1061"].(map[string]any)["inputs"].(map[string]any)
	if got, want := scaler["megapixels"], float64(1); got != want {
		t.Fatalf("megapixels = %v, want %v", got, want)
	}
	aspect := prompt["1305"].(map[string]any)["inputs"].(map[string]any)
	if got, want := aspect["max_resolution"], 2160; got != want {
		t.Fatalf("max resolution = %v, want %v", got, want)
	}
	sampler := prompt["1328"].(map[string]any)["inputs"].(map[string]any)
	if got, want := sampler["scheduler"], "normal"; got != want {
		t.Fatalf("Flux2 scheduler = %v, want %q", got, want)
	}
	lut := prompt["1430"].(map[string]any)["inputs"].(map[string]any)
	if lut["image"].([]any)[0] != "1362" || lut["lut_name"] != "street.cube" || lut["strength"] != 0.28 {
		t.Fatalf("Flux2 LUT is not connected to final image: %#v", lut)
	}
	lora := prompt["539"].(map[string]any)["inputs"].(map[string]any)["lora_1"].(map[string]any)
	if got, want := lora["lora"], "Flux2\\klein_snofs_v1_4.safetensors"; got != want {
		t.Fatalf("Flux2 LoRA = %v, want %q", got, want)
	}
	if got, want := lora["strength"], 0.75; got != want {
		t.Fatalf("Flux2 LoRA strength = %v, want %v", got, want)
	}
	tenthLora := prompt["539"].(map[string]any)["inputs"].(map[string]any)["lora_10"].(map[string]any)
	if got, want := tenthLora["lora"], "Flux2\\final-style.safetensors"; got != want {
		t.Fatalf("Flux2 tenth LoRA = %v, want %q", got, want)
	}
	encoded, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "{{") {
		t.Fatalf("Flux2 prompt contains an unresolved workflow placeholder: %s", encoded)
	}
	for _, nodeID := range []string{"1076", "1065", "1066", "1077", "1063", "1072", "1316", "1317", "1318"} {
		if _, exists := prompt[nodeID]; exists {
			t.Fatalf("unused optional Flux reference node %s was not removed", nodeID)
		}
	}
}

func TestFlux2ImageWorkflowPreservesOriginalResolution(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", ModelFamily: modelFamilyFlux2,
		TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input.png", Positive: "edit the portrait", Width: 4032, Height: 3024,
		OutputMegapixels: 11.63, DimensionMultiple: 16, MaxLongestSide: 4096,
		Steps: 25, CFG: 1, Denoise: 0.9, Sampler: "euler", Scheduler: "normal", Seed: 42,
		EditUseCustomSize: true, PreserveOriginalSize: true, SourceMegapixels: 11.63,
	})
	if err != nil {
		t.Fatal(err)
	}
	inputScale := prompt["1061"].(map[string]any)["inputs"].(map[string]any)
	if got, want := inputScale["megapixels"], 11.63; got != want {
		t.Fatalf("Flux2 input megapixels = %v, want %v", got, want)
	}
	frame := prompt["1305"].(map[string]any)["inputs"].(map[string]any)
	if frame["custom_width"] != 4032 || frame["custom_height"] != 3024 || frame["resolution_source"] != true {
		t.Fatalf("Flux2 original frame is not preserved: %#v", frame)
	}
}

func TestKrea2ImageWorkflowFitsLargeOriginalToSafeBaseResolution(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "encoder.safetensors", VAE: "vae.safetensors", IdentityLora: "identity.safetensors",
		InputImage: "gateway/input.png", Positive: "change the light", Width: 3016, Height: 3168,
		OutputMegapixels: 9.1, DimensionMultiple: 16, MaxLongestSide: 4096,
		Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		EditUseCustomSize: true, PreserveOriginalSize: true, ReferenceBoost: 4, GroundingPixels: 768,
		UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "deis", UpscaleScheduler: "simple",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := prompt["5"].(map[string]any)["inputs"].(map[string]any)
	width, height := frame["custom_width"].(int), frame["custom_height"].(int)
	if width > krea2EditMaxLongestSidePixels || height > krea2EditMaxLongestSidePixels || float64(width*height) > krea2EditMaxBaseMegapixels*1024*1024 || frame["max_resolution"] != krea2EditMaxLongestSidePixels || frame["resolution_source"] != true {
		t.Fatalf("Krea2 frame was not fitted to the safe edit budget: %#v", frame)
	}
	if got, want := prompt["14"].(map[string]any)["inputs"].(map[string]any)["upscale_by"], 1.0; got != want {
		t.Fatalf("Krea2 upscale factor = %v, want %v", got, want)
	}
}

func TestKrea2ImageWorkflowPreservesShortPanorama(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "encoder.safetensors", VAE: "vae.safetensors", IdentityLora: "identity.safetensors",
		InputImage: "gateway/input.png", Positive: "preserve the panorama", Width: 1056, Height: 192,
		OutputMegapixels: 0.19, DimensionMultiple: 16, MaxLongestSide: 4096,
		Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		EditUseCustomSize: true, PreserveOriginalSize: true, ReferenceBoost: 4, GroundingPixels: 768,
		UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "deis", UpscaleScheduler: "simple",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := prompt["5"].(map[string]any)["inputs"].(map[string]any)
	if frame["custom_width"] != 1056 || frame["custom_height"] != 192 {
		t.Fatalf("Krea2 short panorama was distorted: %#v", frame)
	}
}

func TestFlux2ImageWorkflowNormalizesLegacySchedulerAndBindsMaxResolution(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input.png", Positive: "edit", Width: 1080, Height: 1352, Steps: 25, CFG: 1, Denoise: 0.9,
		Sampler: "euler", Scheduler: "flux2", Seed: -1, SourceMegapixels: 1, MaxLongestSide: 2160,
	})
	if err != nil {
		t.Fatal(err)
	}
	aspect := prompt["1305"].(map[string]any)["inputs"].(map[string]any)
	if got, want := aspect["max_resolution"], 2160; got != want {
		t.Fatalf("max resolution = %v, want %v", got, want)
	}
	sampler := prompt["1328"].(map[string]any)["inputs"].(map[string]any)
	if got, want := sampler["scheduler"], "normal"; got != want {
		t.Fatalf("legacy Flux2 scheduler = %v, want %q", got, want)
	}
	seed := prompt["404"].(map[string]any)["inputs"].(map[string]any)["seed"].(int64)
	if seed < 0 || seed > 1<<50 {
		t.Fatalf("Flux2 seed %d is outside rgthree range", seed)
	}
}

func TestFlux2ImageWorkflowBindsDedicatedConditioningControls(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input.png", Positive: "edit", Width: 1024, Height: 1024, Steps: 31, CFG: 1.4, Denoise: 0.82,
		Sampler: "euler", Scheduler: "normal", Seed: 42, SourceMegapixels: 1.5, FluxGuidance: 3.2, FluxDetailerSteps: 12,
		FluxActiveScale: 1.25, FluxTokenWhiten: 0.4, FluxNormEqualize: 0.2,
	})
	if err != nil {
		t.Fatal(err)
	}
	guidance := prompt["286"].(map[string]any)["inputs"].(map[string]any)
	if got, want := guidance["Xi"], 3.2; got != want {
		t.Fatalf("Flux guidance = %v, want %v", got, want)
	}
	enhancer := prompt["851"].(map[string]any)["inputs"].(map[string]any)
	if got, want := enhancer["active_scale"], 1.25; got != want {
		t.Fatalf("Flux active scale = %v, want %v", got, want)
	}
	if got, want := enhancer["per_token_whiten"], 0.4; got != want {
		t.Fatalf("Flux token whiten = %v, want %v", got, want)
	}
	sampler := prompt["1328"].(map[string]any)["inputs"].(map[string]any)
	if got, want := sampler["detailer_steps"], 12; got != want {
		t.Fatalf("Flux detailer steps = %v, want %v", got, want)
	}
}

func TestFlux2ImageWorkflowAppliesSelectedUpscaleBranches(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	base := generationForm{
		ModelName: "Flux2/model.safetensors", TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input.png", Positive: "edit", Width: 1024, Height: 1024, Steps: 25, CFG: 1, Denoise: 0.9,
		Sampler: "euler", Scheduler: "normal", Seed: 42, SourceMegapixels: 1,
	}
	for _, test := range []struct {
		mode       string
		wantOutput string
		wantFirst  string
		wantSecond bool
	}{
		{mode: "none", wantOutput: "1362"},
		{mode: "ultimate", wantOutput: "gateway_flux_ultimate", wantFirst: "1362"},
		{mode: "seedvr2", wantOutput: "gateway_flux_seedvr2", wantFirst: "1362", wantSecond: true},
		{mode: "both", wantOutput: "gateway_flux_seedvr2", wantFirst: "gateway_flux_ultimate", wantSecond: true},
	} {
		t.Run(test.mode, func(t *testing.T) {
			input := base
			input.FluxUpscaleMode = test.mode
			prompt, err := definition.buildPrompt(input)
			if err != nil {
				t.Fatal(err)
			}
			saved := prompt["1341"].(map[string]any)["inputs"].(map[string]any)["images"].([]any)
			if got := saved[0]; got != test.wantOutput {
				t.Fatalf("saved image = %v, want %q", got, test.wantOutput)
			}
			ultimate, hasUltimate := prompt["gateway_flux_ultimate"]
			if (test.mode == "ultimate" || test.mode == "both") != hasUltimate {
				t.Fatalf("Ultimate branch present = %t for mode %q", hasUltimate, test.mode)
			}
			if hasUltimate {
				if got := ultimate.(map[string]any)["inputs"].(map[string]any)["image"].([]any)[0]; got != "1362" {
					t.Fatalf("Ultimate input = %v, want core Flux2 output", got)
				}
			}
			seed, hasSeed := prompt["gateway_flux_seedvr2"]
			if test.wantSecond != hasSeed {
				t.Fatalf("SeedVR2 branch present = %t for mode %q", hasSeed, test.mode)
			}
			if hasSeed {
				if got := seed.(map[string]any)["inputs"].(map[string]any)["image"].([]any)[0]; got != test.wantFirst {
					t.Fatalf("SeedVR2 input = %v, want %q", got, test.wantFirst)
				}
				if got := seed.(map[string]any)["inputs"].(map[string]any)["resolution"].([]any)[0]; got != "gateway_flux_seedvr2_resolution" {
					t.Fatalf("SeedVR2 resolution is not derived from the current image: %v", got)
				}
			}
		})
	}
}

func TestFlux2ImageWorkflowRejectsUnknownUpscaleMode(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	_, err = definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", TextEncoder: "encoder.safetensors", VAE: "vae.safetensors", InputImage: "gateway/input.png",
		Positive: "edit", Width: 1024, Height: 1024, Steps: 25, CFG: 1, Denoise: 0.9, Sampler: "euler", Scheduler: "normal", Seed: 42,
		FluxUpscaleMode: "everything",
	})
	if err == nil || !strings.Contains(err.Error(), "режим апскейла Flux2") {
		t.Fatalf("error = %v, want Flux2 upscale mode validation error", err)
	}
}

func TestRandomSeedFitsRGThreeRange(t *testing.T) {
	for range 32 {
		seed, err := randomSeed()
		if err != nil {
			t.Fatal(err)
		}
		if seed < 0 || seed > 1<<50 {
			t.Fatalf("seed %d is outside rgthree range", seed)
		}
	}
}

func TestFlux2ImageWorkflowAcceptsFourImages(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", ModelFamily: modelFamilyFlux2,
		TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input-1.png", ReferenceImages: [3]string{"gateway/input-2.png", "gateway/input-3.png", "gateway/input-4.png"},
		Positive: "combine the reference photos", Width: 1024, Height: 768, Steps: 20, CFG: 5, Denoise: 0.7, Seed: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	for nodeID, image := range map[string]string{"1076": "gateway/input-2.png", "1077": "gateway/input-3.png", "1316": "gateway/input-4.png"} {
		inputs := prompt[nodeID].(map[string]any)["inputs"].(map[string]any)
		if got := inputs["image"]; got != image {
			t.Fatalf("%s image = %v, want %q", nodeID, got, image)
		}
	}
	referenceInputs := prompt["1398"].(map[string]any)["inputs"].(map[string]any)
	for _, name := range []string{"latent_1", "latent_2", "latent_3", "latent_4"} {
		if _, ok := referenceInputs[name]; !ok {
			t.Fatalf("reference latent %s is missing: %#v", name, referenceInputs)
		}
	}
}

func TestKrea2ImageWorkflowUsesIdentityEditAndQualityUpscale(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		TemplateID: "image-to-image", ModelName: "Krea2/gonzalomoKrea2_v40.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "qwen3VLInstruct4bHeretic_v10.safetensors", VAE: "qwen_image_vae.safetensors",
		IdentityLora: "krea2_identity_edit_v1_2.safetensors", InputImage: "gateway/input.png", Positive: "change the jacket to red",
		Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		ReferenceBoost: 4, GroundingPixels: 768, UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "euler_ancestral",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := prompt["7"].(map[string]any)["inputs"].(map[string]any)
	if got, want := identity["lora_name"], "krea2_identity_edit_v1_2.safetensors"; got != want {
		t.Fatalf("identity LoRA = %v, want %v", got, want)
	}
	patch := prompt["8"].(map[string]any)["inputs"].(map[string]any)
	if got, want := patch["ref_boost"], 4.0; got != want {
		t.Fatalf("reference boost = %v, want %v", got, want)
	}
	upscale := prompt["14"].(map[string]any)["inputs"].(map[string]any)
	if got, want := upscale["upscale_by"], 1.5; got != want {
		t.Fatalf("upscale factor = %v, want %v", got, want)
	}
	if prompt["19"].(map[string]any)["class_type"] != "SaveImage" {
		t.Fatalf("Krea 2 workflow does not save the final image: %#v", prompt["19"])
	}
	if _, exists := prompt["20"]; exists {
		t.Fatal("optional Krea reference image loader was not removed")
	}
}

func TestKrea2ImageWorkflowBindsDedicatedUpscaleAndPostProcessingControls(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", TextEncoder: "encoder.safetensors", VAE: "vae.safetensors", IdentityLora: "identity.safetensors",
		InputImage: "gateway/input.png", Positive: "edit", Width: 1024, Height: 1024, Steps: 20, CFG: 8, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		ReferenceBoost: 4, GroundingPixels: 768, UpscaleFactor: 1.75, UpscaleSteps: 6, UpscaleCFG: 1.5, UpscaleDenoise: 0.21, UpscaleSampler: "deis", UpscaleScheduler: "normal",
		PostDenoiseBlur: 1.5, PostDenoiseEdge: 0.1, PostDenoiseRadius: 1.2, PostDenoiseStrength: 0.6,
		SkinPreset: "Fresh", SkinStrength: 1.1, SkinCoolness: 0.3, SkinBrightness: 0.15, SkinRosy: 0.1, SkinEvenness: 0.2, SkinShadowLift: 0.2, SkinSmooth: 0.1, SkinTexturePreserve: 0.8, SkinSaturation: -0.05, SkinHighlightProtect: 0.7, SkinMaskSensitivity: 0.6, SkinMaskFeather: 0.4,
		AdjustHue: 0.1, AdjustSaturation: 0.2, AdjustBrightness: -0.1, AdjustContrast: 0.15, AdjustSharpness: 0.25, LUTName: "street.cube", LUTStrength: 0.4,
	})
	if err != nil {
		t.Fatal(err)
	}
	upscale := prompt["14"].(map[string]any)["inputs"].(map[string]any)
	if got, want := upscale["upscale_by"], 1.75; got != want {
		t.Fatalf("upscale factor = %v, want %v", got, want)
	}
	if got, want := upscale["scheduler"], "normal"; got != want {
		t.Fatalf("upscale scheduler = %v, want %q", got, want)
	}
	denoise := prompt["15"].(map[string]any)["inputs"].(map[string]any)
	if got, want := denoise["strength"], 0.6; got != want {
		t.Fatalf("post denoise strength = %v, want %v", got, want)
	}
	skin := prompt["16"].(map[string]any)["inputs"].(map[string]any)
	if got, want := skin["preset"], "Fresh"; got != want {
		t.Fatalf("skin preset = %v, want %q", got, want)
	}
	adjust := prompt["17"].(map[string]any)["inputs"].(map[string]any)
	if got, want := adjust["sharpness"], 0.25; got != want {
		t.Fatalf("sharpness = %v, want %v", got, want)
	}
	lut := prompt["18"].(map[string]any)["inputs"].(map[string]any)
	if got, want := lut["lut_name"], "street.cube"; got != want {
		t.Fatalf("LUT = %v, want %q", got, want)
	}
}

func TestNormalizeLUTMakesColorGradingOptional(t *testing.T) {
	disabled := generationForm{}
	normalizeLUT(&disabled)
	if disabled.LUTEnabled || disabled.LUTStrength != 0 || disabled.LUTName != "LC_Crushed_Blacks.cube" {
		t.Fatalf("disabled LUT = %#v", disabled)
	}
	legacy := generationForm{LUTName: "street.cube", LUTStrength: 0.4}
	normalizeLUT(&legacy)
	if !legacy.LUTEnabled || legacy.LUTStrength != 0.4 {
		t.Fatalf("legacy LUT = %#v", legacy)
	}
}

func TestKreaEditDefaultsDoNotEnableLUT(t *testing.T) {
	input := generationForm{}
	normalizeKreaEditDefaults(&input)
	normalizeLUT(&input)
	if input.LUTEnabled || input.LUTStrength != 0 || input.LUTName != "LC_Crushed_Blacks.cube" {
		t.Fatalf("Krea2 defaults enabled LUT: %#v", input)
	}
}

func TestKrea2ImageWorkflowAcceptsSecondImage(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		TemplateID: "image-to-image", ModelName: "Krea2/gonzalomoKrea2_v40.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "qwen3VLInstruct4bHeretic_v10.safetensors", VAE: "qwen_image_vae.safetensors", IdentityLora: "krea2_identity_edit_v1_2.safetensors",
		InputImage: "gateway/input-1.png", ReferenceImages: [3]string{"gateway/input-2.png"}, Positive: "use the second image as a style reference",
		Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		ReferenceBoost: 4, GroundingPixels: 768, UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "euler_ancestral",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := prompt["20"].(map[string]any)["inputs"].(map[string]any)["image"]; got != "gateway/input-2.png" {
		t.Fatalf("second Krea image = %v", got)
	}
	patchInputs := prompt["8"].(map[string]any)["inputs"].(map[string]any)
	for _, name := range []string{"source_latent_b", "source_image_b"} {
		if _, ok := patchInputs[name]; !ok {
			t.Fatalf("Krea optional input %s is missing: %#v", name, patchInputs)
		}
	}
}

func TestFlux2ImageWorkflowDoesNotValidateKrea2OnlySettings(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-flux2")
	if !ok {
		t.Fatal("Flux 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", ModelFamily: modelFamilyFlux2,
		TextEncoder: "qwen_3_8b_fp8mixed.safetensors", VAE: "flux2-vae.safetensors",
		InputImage: "gateway/input.png", Positive: "replace the background", Width: 1024, Height: 768,
		Steps: 20, CFG: 1, Denoise: 0.7, Seed: 42, SourceMegapixels: 1,
		// These are Krea2-specific fields and must not reject or alter Flux2 execution.
		ReferenceBoost: 99, GroundingPixels: 1, UpscaleFactor: 9,
	})
	if err != nil {
		t.Fatalf("Flux2 rejected Krea2-only values: %v", err)
	}
	encoded, err := json.Marshal(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "99") {
		t.Fatalf("Krea2 reference boost leaked into Flux2 prompt: %s", encoded)
	}
}

func TestKrea2ImageWorkflowDoesNotValidateFlux2OnlySettings(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "image-to-image-krea2")
	if !ok {
		t.Fatal("Krea 2 image workflow is missing")
	}
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "Krea2/gonzalomoKrea2_v40.safetensors", ModelFamily: modelFamilyKrea2,
		TextEncoder: "qwen3VLInstruct4bHeretic_v10.safetensors", VAE: "qwen_image_vae.safetensors",
		IdentityLora: "krea2_identity_edit_v1_2.safetensors", InputImage: "gateway/input.png", Positive: "change the jacket",
		Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple", Seed: 42,
		ReferenceBoost: 4, GroundingPixels: 768, UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "euler_ancestral",
		// Source megapixels are a Flux2-only control and must not affect Krea2.
		SourceMegapixels: 99,
	})
	if err != nil {
		t.Fatalf("Krea2 rejected Flux2-only values: %v", err)
	}
	if got := prompt["8"].(map[string]any)["inputs"].(map[string]any)["ref_boost"]; got != 4.0 {
		t.Fatalf("unexpected Krea2 reference boost: %v", got)
	}
}

func TestImageEditWorkflowsEnforceTheirOwnReferenceLimits(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	krea, _ := findWorkflow(definitions, "image-to-image-krea2")
	_, err = krea.buildPrompt(generationForm{
		ModelName: "Krea2/model.safetensors", InputImage: "gateway/one.png", ReferenceImages: [3]string{"gateway/two.png", "gateway/three.png"},
		Positive: "edit", Width: 1024, Height: 1024, Steps: 8, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple",
		ReferenceBoost: 4, GroundingPixels: 768, UpscaleFactor: 1.5, UpscaleSteps: 4, UpscaleDenoise: 0.15, UpscaleSampler: "euler_ancestral",
	})
	if err == nil || !strings.Contains(err.Error(), "до двух") {
		t.Fatalf("Krea2 reference limit error = %v", err)
	}

	flux, _ := findWorkflow(definitions, "image-to-image-flux2")
	_, err = flux.buildPrompt(generationForm{
		ModelName: "Flux2/model.safetensors", InputImage: "gateway/one.png", ReferenceImages: [3]string{"gateway/two.png", "gateway/three.png", "gateway/four.png"},
		Positive: "edit", Width: 1024, Height: 1024, Steps: 20, CFG: 1, Denoise: 0.7, SourceMegapixels: 1,
	})
	if err != nil {
		t.Fatalf("Flux2 must accept four images: %v", err)
	}
}

func TestGenerationModelCatalogGroupsDiffusionModels(t *testing.T) {
	var info map[string]comfyNodeInfo
	fixture := `{
		"CheckpointLoaderSimple":{"input":{"required":{"ckpt_name":[["regular.safetensors"]]}}},
		"UNETLoader":{"input":{"required":{"unet_name":[["Krea2/krea2Turbo.safetensors","Flux2/flux2Klein_9b.safetensors"]]}}},
		"CLIPLoader":{"input":{"required":{"clip_name":[["Huihui-Qwen3-VL-4B-Instruct-abliterated-fp8_scaled.safetensors"]]}}},
		"VAELoader":{"input":{"required":{"vae_name":[["qwen_image_vae.safetensors"]]}}},
		"LoraLoader":{"input":{"required":{"lora_name":[["lenovo_krea2.safetensors"]]}}}
	}`
	if err := json.Unmarshal([]byte(fixture), &info); err != nil {
		t.Fatal(err)
	}
	catalog := buildGenerationModelCatalog(info)
	if got, want := catalog.AvailableCount, 2; got != want {
		t.Fatalf("available models = %d, want %d", got, want)
	}
	var krea, flux generationModel
	for _, group := range catalog.Groups {
		for _, model := range group.Models {
			switch model.Family {
			case modelFamilyKrea2:
				krea = model
			case modelFamilyFlux2:
				flux = model
			}
		}
	}
	if !krea.Available || krea.DefaultSteps != 8 || krea.TextEncoder == "" || krea.VAE == "" {
		t.Fatalf("unexpected Krea 2 model: %#v", krea)
	}
	if krea.SupportsImage {
		t.Fatalf("Krea 2 must not be offered for image editing: %#v", krea)
	}
	if flux.Available || !strings.Contains(flux.Reason, "Qwen3 8B") || !strings.Contains(flux.Reason, "Flux 2 VAE") {
		t.Fatalf("unexpected Flux 2 dependency state: %#v", flux)
	}
	if !flux.SupportsImage {
		t.Fatalf("Flux 2 must be offered for image editing when dependencies are installed: %#v", flux)
	}
}

func TestGenerationModelCatalogEnablesInstalledEditWorkflows(t *testing.T) {
	var info map[string]comfyNodeInfo
	fixture := `{
		"UNETLoader":{"input":{"required":{"unet_name":[["Flux.2 Klein 9B/base.safetensors","Krea2/gonzalomoKrea2_v40.safetensors","Krea2/krea2TurboOfficialComfy_krea2TurboBf16.safetensors"]]}}},
		"CLIPLoader":{"input":{"required":{"clip_name":[["qwen3VLInstruct4bHeretic_v10.safetensors"]]}}},
		"ClipLoaderGGUF":{"input":{"required":{"clip_name":[["Qwen3-8B-abliterated-bf16.gguf"]]}}},
		"VAELoader":{"input":{"required":{"vae_name":[["qwen_image_vae.safetensors","flux2-vae.safetensors"]]}}},
		"LoraLoader":{"input":{"required":{"lora_name":[["lenovo_krea2.safetensors","krea2_identity_edit_v1_2.safetensors"]]}}},
		"Krea2EditModelPatch":{},"Krea2EditGroundedEncode":{},"AspectRatioSimplifier":{},"UltimateSDUpscale":{},
		"LCAspectRatioPipeOut":{},"LCReferenceLatent":{},"LCPipeEdit":{},"LCSamplerConfigureSimplePipeOut":{},"Power Lora Loader (rgthree)":{}
	}`
	if err := json.Unmarshal([]byte(fixture), &info); err != nil {
		t.Fatal(err)
	}
	catalog := buildGenerationModelCatalog(info)
	presets := buildGenerationPresets(catalog)
	if preset, ok := findGenerationPreset(presets, "photoflow-krea2-edit", "image-to-image"); !ok || preset.ModelName != "Krea2/krea2TurboOfficialComfy_krea2TurboBf16" || preset.DefaultSteps != 8 || preset.DefaultCFG != 1 || preset.MaxInputImages != 2 {
		t.Fatalf("unexpected Krea 2 edit preset: %#v", preset)
	}
	if preset, ok := findGenerationPreset(presets, "photoflow-flux2-edit", "image-to-image"); !ok || !preset.Available || !preset.AllowsImages || preset.MaxInputImages != 4 {
		t.Fatalf("Flux 2 edit preset is unavailable: %#v", preset)
	}
}

func TestGenerationModelCatalogDiscoversMiniMaxH3V5AndErosMax(t *testing.T) {
	var info map[string]comfyNodeInfo
	fixture := `{
		"UNETLoader":{"input":{"required":{"unet_name":[["MiniMax\\MiniMax_H3_FL2VA_pruned_int8_convrot.safetensors","MiniMax\\MiniMax_H3_Ref2VA_pruned_int8_convrot.safetensors","MiniMax\\h3ErosMax_beta4.safetensors"]]}}},
		"CLIPLoader":{"input":{"required":{"clip_name":[["MiniMax\\qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors"]]}}},
		"VAELoader":{"input":{"required":{"vae_name":[["MiniMax\\minimax_h3_video_vae_fp16.safetensors","MiniMax\\minimax_h3_audio_vae_fp32.safetensors"]]}}},
		"LoraLoader":{"input":{"required":{"lora_name":[["MiniMaxH3\\motion.safetensors","MiniMaxH3\\minimax_h3_turbo_v4_step600_ema.safetensors"]]}}},
		"MiniMaxH3ImageToVideo":{},"MiniMaxH3ReferenceToVideo":{},"MiniMaxH3SigmaShift":{},"MiniMaxH3MemoryEfficientSageAttentionPatch":{},
		"MiniMaxH3TurboLoRA":{},"MiniMaxH3TurboSampler":{},"H3MemoryOptimization":{},"H3AIMDOResidencyLimiter":{},"H3SparseAttentionAdvanced":{},
		"LCImageMaskResize":{},"LCVRAMCacheClear":{},"ImageSharpenKJ":{},"CR LoRA Stack":{},"CR Apply LoRA Stack":{},"VHS_LoadVideo":{},"VHS_VideoCombine":{}
	}`
	if err := json.Unmarshal([]byte(fixture), &info); err != nil {
		t.Fatal(err)
	}
	catalog := buildGenerationModelCatalog(info)
	var base, eros generationModel
	for _, group := range catalog.Groups {
		for _, model := range group.Models {
			if model.Family != modelFamilyMiniMaxH3 {
				continue
			}
			if model.VideoIntegratedTurbo {
				eros = model
			} else {
				base = model
			}
		}
	}
	if !base.Available || base.DefaultSampler != "euler" || base.DefaultVideoShift != 11 || base.DefaultAudioShift != 3 {
		t.Fatalf("unexpected MiniMax H3 v5 base model: %#v", base)
	}
	if !eros.Available || !eros.VideoIntegratedTurbo || !eros.VideoReferenceOnly || eros.ReferenceModel != eros.Name || eros.DefaultSteps != 8 || eros.DefaultVideoShift != 12 || eros.DefaultAudioShift != 7 {
		t.Fatalf("unexpected H3 Eros Max model: %#v", eros)
	}
	preset, ok := findGenerationPreset(buildGenerationPresets(catalog), "minimax-h3-video", "minimax-h3-video")
	if !ok || preset.ModelCount != 2 || !preset.AllowsImages || preset.MaxInputImages != 4 {
		t.Fatalf("MiniMax H3 preset = %#v", preset)
	}
}

func TestFlux2LoraGroupsExcludeOtherFamilies(t *testing.T) {
	groups := buildFlux2LoraGroups([]string{
		"Krea2/detailer.safetensors",
		"Flux2/klein_style.safetensors",
		"Flux.2/character.safetensors",
		"Trained/Flux2-Klein/alice/portrait.safetensors",
		"LTX2/video.safetensors",
	})
	if len(groups) != 1 || groups[0].Name != "Flux2" || len(groups[0].Loras) != 3 {
		t.Fatalf("unexpected Flux2 LoRA groups: %#v", groups)
	}
	if groups[0].Loras[0].Name != "Flux.2/character.safetensors" || groups[0].Loras[1].Name != "Flux2/klein_style.safetensors" || groups[0].Loras[2].Name != "Trained/Flux2-Klein/alice/portrait.safetensors" {
		t.Fatalf("unexpected Flux2 LoRA entries: %#v", groups[0].Loras)
	}
}

func TestKrea2LoraGroupsExposeTrainedAdaptersSeparately(t *testing.T) {
	groups := buildGenerationLoraGroups([]string{
		"lenovo_krea2.safetensors",
		"Trained/Krea2/alice/portrait.safetensors",
		"Flux2/style.safetensors",
	})
	if len(groups) != 2 || groups[0].Name != "Базовые Krea2" || groups[1].Name != "Обученные Krea2" {
		t.Fatalf("unexpected Krea2 LoRA groups: %#v", groups)
	}
	if len(groups[1].Loras) != 1 || groups[1].Loras[0].Name != "Trained/Krea2/alice/portrait.safetensors" {
		t.Fatalf("trained Krea2 LoRA is missing: %#v", groups[1].Loras)
	}
}

func TestMiniMaxH3LoraGroupsUseDedicatedDirectory(t *testing.T) {
	groups := buildMiniMaxH3LoraGroups([]string{
		"HMNSFW-AIO-V2.5.safetensors",
		"MiniMaxH3/h3_Better_NSFW_Motion_V1.safetensors",
		"MiniMaxH3/HMNSFW-AIO-V2.5.safetensors",
		"MiniMaxH3/SexGod-NaughtyTimes-v2-rank256.safetensors",
		miniMaxH3TurboLoraName,
		"Krea2/SynthPussy_H3_closeups_v1-step00008300.safetensors",
	})
	if len(groups) != 1 || groups[0].Name != "MiniMax H3" || len(groups[0].Loras) != 3 {
		t.Fatalf("unexpected MiniMax H3 LoRA groups: %#v", groups)
	}
	foundBetterMotion := false
	for _, lora := range groups[0].Loras {
		name := strings.ReplaceAll(lora.Name, "/", "\\")
		if !strings.HasPrefix(name, miniMaxH3LoraDirectory) || name == miniMaxH3TurboLoraName {
			t.Fatalf("MiniMax H3 group leaked an invalid LoRA: %#v", lora)
		}
		if name == "MiniMaxH3\\h3_Better_NSFW_Motion_V1.safetensors" {
			foundBetterMotion = true
			if lora.DisplayName != "Better NSFW Motion (H3 Ref2VA V1)" {
				t.Fatalf("unexpected Better NSFW Motion display name: %q", lora.DisplayName)
			}
			if lora.DefaultStrength != 0.9 {
				t.Fatalf("unexpected Better NSFW Motion default strength: %v", lora.DefaultStrength)
			}
		}
	}
	if !foundBetterMotion {
		t.Fatal("MiniMax H3 Better NSFW Motion LoRA is missing from the dedicated catalog")
	}
}

func TestFlux2PreserveOriginalAllowsPanoramaBelow256(t *testing.T) {
	definition := workflowDefinition{ID: "image-to-image-flux2", RequiresImage: true}
	input := generationForm{
		ModelName: "Flux2/model.safetensors", ModelFamily: modelFamilyFlux2,
		InputImage: "gateway/input.png", Positive: "preserve the panorama", Width: 1056, Height: 192,
		OutputMegapixels: 0.19, MaxLongestSide: 4096, Steps: 25, CFG: 1, Denoise: 0.9, Seed: 42,
		PreserveOriginalSize: true, SourceMegapixels: 0.25,
	}
	if err := definition.normalizeAndValidate(&input); err != nil {
		t.Fatalf("Flux2 preserve-original panorama was rejected: %v", err)
	}
}

func TestTextGenerationStillRejectsDimensionsBelow256(t *testing.T) {
	definition := workflowDefinition{ID: "text-to-image"}
	input := generationForm{
		ModelName: "model.safetensors", Positive: "panorama", Width: 1056, Height: 192,
		OutputMegapixels: 0.19, Steps: 25, CFG: 7, Denoise: 1, Seed: 42,
	}
	if err := definition.normalizeAndValidate(&input); err == nil {
		t.Fatal("text generation accepted a dimension below 256")
	}
}

func TestQuickGenerationPresetLimitsPhotoFlowToKreaFamily(t *testing.T) {
	catalog := generationModelCatalog{byID: map[string]generationModel{}}
	krea := generationModel{ID: "krea", Name: "Krea2/model-v40.safetensors", DisplayName: "Krea2 / model-v40", Family: modelFamilyKrea2, Available: true, DefaultSteps: 8, DefaultCFG: 1}
	flux := generationModel{ID: "flux", Name: "Flux2/model.safetensors", DisplayName: "Flux2 / model", Family: modelFamilyFlux2, Available: true}
	checkpoint := generationModel{ID: "checkpoint", Name: "regular.safetensors", Family: modelFamilyCheckpoint, Available: true}
	catalog.Groups = []generationModelGroup{{Name: "models", Models: []generationModel{krea, flux, checkpoint}}}
	for _, model := range catalog.Groups[0].Models {
		catalog.byID[model.ID] = model
	}
	presets := buildGenerationPresets(catalog)
	preset, ok := findGenerationPreset(presets, "photoflow-krea2", "text-to-image")
	if !ok || preset.Family != modelFamilyKrea2 || preset.ModelID != krea.ID {
		t.Fatalf("unexpected PhotoFlow preset: %#v", preset)
	}
	quickModels := quickGenerationModels(catalog)
	if len(quickModels) != 2 {
		t.Fatalf("quick model count = %d, want only Krea2 and Flux2", len(quickModels))
	}
	for _, model := range quickModels {
		if model.Family == modelFamilyCheckpoint {
			t.Fatalf("checkpoint leaked into the quick generation allowlist: %#v", model)
		}
	}
}

func TestValidateGenerationImageNamespace(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	own := comfyUploadNamespace(app.comfyClientID(7))
	if err := app.validateGenerationImage(own+"/input.png", 7); err != nil {
		t.Fatal(err)
	}
	if err := app.validateGenerationImage("gateway/gateway-aaaaaaaaaaaaaaaaaaaaaaaa/input.png", 7); err == nil {
		t.Fatal("foreign image namespace was accepted")
	}
}

func TestParseGenerationOutputs(t *testing.T) {
	outputs, err := parseGenerationOutputs(map[string]json.RawMessage{
		"9":  json.RawMessage(`{"images":[{"filename":"AI-Gateway_00001.png","subfolder":"","type":"output"}]}`),
		"10": json.RawMessage(`{"videos":[{"filename":"clip.mp4","subfolder":"","type":"output","format":"video/mp4"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 || outputs[0].URL == "" || outputs[1].URL == "" {
		t.Fatalf("unexpected outputs: %#v", outputs)
	}
	if !strings.Contains(outputs[0].URL, "/generate/output?") || !strings.Contains(outputs[1].URL, "/generate/output?") {
		t.Fatalf("missing image view URL: %#v", outputs)
	}
}

func TestSubmitComfyPromptInjectsUserClientID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/prompt" {
			t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var document struct {
			ClientID  string         `json:"client_id"`
			Prompt    map[string]any `json:"prompt"`
			ExtraData map[string]any `json:"extra_data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&document); err != nil {
			t.Fatal(err)
		}
		if document.ClientID == "" || len(document.Prompt) != 1 {
			t.Fatalf("gateway identity or prompt missing: %#v", document)
		}
		extra, ok := document.ExtraData["extra_pnginfo"].(map[string]any)
		if !ok {
			t.Fatalf("extra_pnginfo is missing: %#v", document.ExtraData)
		}
		if _, ok := extra["workflow"].(map[string]any); !ok {
			t.Fatalf("workflow metadata is missing: %#v", extra)
		}
		if document.ExtraData["gateway_job_id"] != "job_test_abcdef012345" {
			t.Fatalf("gateway job correlation is missing: %#v", document.ExtraData)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"prompt_id":"abcdef0123456789"}`))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, SessionSecret: "01234567890123456789012345678901"}}
	promptID, err := app.submitComfyPrompt(context.Background(), 17, "job_test_abcdef012345", map[string]any{"1": map[string]any{"class_type": "Test"}})
	if err != nil {
		t.Fatal(err)
	}
	if promptID != "abcdef0123456789" {
		t.Fatalf("prompt ID = %q", promptID)
	}
}

func TestFetchGenerationStatusParsesHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history/abcdef0123456789" {
			t.Fatalf("unexpected history path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"abcdef0123456789":{"status":{"status_str":"success","completed":true},"outputs":{"9":{"images":[{"filename":"result.png","subfolder":"","type":"output"}]}}}}`))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, SessionSecret: "01234567890123456789012345678901"}}
	status, err := app.fetchGenerationStatus(context.Background(), "abcdef0123456789", 17)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Known || status.State != "completed" || len(status.Outputs) != 1 || status.Outputs[0].Filename != "result.png" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestFetchGenerationStatusReportsQueuePosition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/history/abcdef0123456789":
			_, _ = w.Write([]byte(`{}`))
		case "/queue":
			_, _ = w.Write([]byte(`{"queue_running":[],"queue_pending":[[1,"other",{},{}],[2,"abcdef0123456789",{},{}]]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, SessionSecret: "01234567890123456789012345678901"}}
	status, err := app.fetchGenerationStatus(context.Background(), "abcdef0123456789", 17)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Known || status.State != "queued" || status.QueuePosition != 2 || status.QueueTotal != 2 {
		t.Fatalf("unexpected queue status: %#v", status)
	}
}

func TestFetchGenerationStatusDistinguishesUnknownPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/history/abcdef0123456789":
			_, _ = w.Write([]byte(`{}`))
		case "/queue":
			_, _ = w.Write([]byte(`{"queue_running":[],"queue_pending":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream}}
	status, err := app.fetchGenerationStatus(context.Background(), "abcdef0123456789", 17)
	if err != nil {
		t.Fatal(err)
	}
	if status.Known || status.State != "queued" {
		t.Fatalf("unknown prompt status = %#v", status)
	}
}

func TestFindComfyPromptByGenerationJobInQueue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/queue" {
			t.Fatalf("history must not be queried after a queue match: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"queue_running":[],"queue_pending":[[7,"abcdef0123456789",{}, {"gateway_job_id":"job_test_abcdef012345"}]]}`))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream}}
	promptID, found, err := app.findComfyPromptByGenerationJob(context.Background(), "job_test_abcdef012345")
	if err != nil || !found || promptID != "abcdef0123456789" {
		t.Fatalf("queue recovery prompt=%q found=%v err=%v", promptID, found, err)
	}
}

func TestFindComfyPromptByGenerationJobInHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			_, _ = w.Write([]byte(`{"queue_running":[],"queue_pending":[]}`))
		case "/history":
			_, _ = w.Write([]byte(`{"fedcba9876543210":{"prompt":[8,"fedcba9876543210",{}, {"gateway_job_id":"job_history_abcdef0123"}]}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream}}
	promptID, found, err := app.findComfyPromptByGenerationJob(context.Background(), "job_history_abcdef0123")
	if err != nil || !found || promptID != "fedcba9876543210" {
		t.Fatalf("history recovery prompt=%q found=%v err=%v", promptID, found, err)
	}
}

func TestGenerationJobValuesPreserveMiniMaxH3V5Controls(t *testing.T) {
	form := url.Values{
		"csrf":                               {"omit"},
		"video_low_vram_attention":           {"true"},
		"video_low_vram_head_chunks":         {"4"},
		"video_chunk_feed_forward":           {"true"},
		"video_chunk_feed_forward_chunks":    {"2"},
		"video_chunk_feed_forward_threshold": {"4096"},
		"video_memory_optimize":              {"true"},
		"video_memory_chunk_rows":            {"4096"},
		"video_sparse_attention":             {"true"},
		"video_sparse_early_schedule":        {"Ramp"},
		"video_rife_enabled":                 {"true"},
		"video_rife_checkpoint":              {"rife49.pth"},
		"video_rtx_enabled":                  {"true"},
		"video_rtx_scale":                    {"2"},
		"video_color_match":                  {"true"},
		"video_sharpen_enabled":              {"true"},
		"video_output_crf":                   {"19"},
		"input_image":                        {"gateway/image.png"},
		"image_role_1":                       {"first_frame"},
		"image_source_1":                     {"gallery"},
		"image_source_id_1":                  {"17"},
		"image_source_name_1":                {"saved.png"},
		"input_audio":                        {"gateway/voice.mp3"},
		"assistant_original_prompt":          {"original"},
		"assistant_suggestion":               {"enhanced"},
		"lora_1":                             {"MiniMaxH3\\VBVR_H3_attn_only.safetensors"},
		"lora_model_strength_1":              {"0.85"},
		"lora_clip_strength_1":               {"0.95"},
		"lora_2":                             {"MiniMaxH3\\h3_Better_NSFW_Motion_V1.safetensors"},
		"lora_model_strength_2":              {"1.15"},
		"lora_clip_strength_2":               {"0.8"},
		"loras_configured":                   {"true"},
		"untrusted_unrelated_parameter":      {"omit"},
	}
	values := generationJobValues(form, 42)
	for _, name := range []string{"video_low_vram_attention", "video_low_vram_head_chunks", "video_chunk_feed_forward", "video_chunk_feed_forward_chunks", "video_chunk_feed_forward_threshold", "video_memory_optimize", "video_memory_chunk_rows", "video_sparse_attention", "video_sparse_early_schedule", "video_rife_enabled", "video_rife_checkpoint", "video_rtx_enabled", "video_rtx_scale", "video_color_match", "video_sharpen_enabled", "video_output_crf", "input_image", "image_role_1", "image_source_1", "image_source_id_1", "image_source_name_1", "input_audio", "assistant_original_prompt", "assistant_suggestion", "lora_1", "lora_model_strength_1", "lora_clip_strength_1", "lora_2", "lora_model_strength_2", "lora_clip_strength_2", "loras_configured", "seed"} {
		if values[name] == "" {
			t.Fatalf("durable generation payload dropped %q: %#v", name, values)
		}
	}
	if _, exists := values["csrf"]; exists {
		t.Fatal("durable generation payload retained CSRF")
	}
	if _, exists := values["untrusted_unrelated_parameter"]; exists {
		t.Fatal("durable generation payload retained unrelated input")
	}
}

func TestGenerationJobValuesPreserveKrea2PhotoFlowBranches(t *testing.T) {
	form := url.Values{
		"krea_sage_enabled":       {"true"},
		"krea_sage_mode":          {"sageattn_qk_int8_pv_fp16_triton"},
		"krea_sage_allow_compile": {"true"},
		"krea_fp16_accumulation":  {"true"},
		"detail_enabled":          {"false"},
		"color_transfer":          {"false"},
		"image_filter_enabled":    {"true"},
		"image_filter_contrast":   {"1.2"},
		"image_filter_detail":     {"true"},
		"image_level_mid":         {"126"},
	}
	values := generationJobValues(form, 42)
	for name, want := range map[string]string{
		"krea_sage_enabled": "true", "krea_sage_mode": "sageattn_qk_int8_pv_fp16_triton",
		"detail_enabled": "false", "color_transfer": "false", "image_filter_enabled": "true",
		"image_filter_contrast": "1.2", "image_filter_detail": "true", "image_level_mid": "126",
	} {
		if got := values[name]; got != want {
			t.Fatalf("saved Krea2 value %s = %q, want %q: %#v", name, got, want, values)
		}
	}
}

func TestGenerationRecipeKeepsReferenceRolesWithoutUserMediaSources(t *testing.T) {
	values := generationRecipeValues(url.Values{
		"template_id":         {"image-to-image"},
		"image_role_1":        {"base_scene"},
		"image_role_2":        {"wardrobe_object"},
		"image_source_1":      {"gallery"},
		"image_source_id_1":   {"17"},
		"image_source_name_1": {"saved.png"},
		"input_image":         {"gateway/saved.png"},
	}, 42)
	if values["image_role_1"] != "base_scene" || values["image_role_2"] != "wardrobe_object" {
		t.Fatalf("recipe reference roles = %#v", values)
	}
	for _, name := range []string{"input_image", "image_source_1", "image_source_id_1", "image_source_name_1"} {
		if _, exists := values[name]; exists {
			t.Fatalf("recipe retained user-specific media field %q: %#v", name, values)
		}
	}
}

func TestGenerationRecipeKeepsLongMiniMaxVideoPrompt(t *testing.T) {
	prompt := strings.Repeat("v", 6500)
	values := generationRecipeValues(url.Values{
		"template_id":     {"minimax-h3-video"},
		"positive_prompt": {prompt},
	}, 42)
	if values["positive_prompt"] != prompt {
		t.Fatalf("long MiniMax prompt was not preserved in the recipe: %d chars", len(values["positive_prompt"]))
	}
	imageValues := generationRecipeValues(url.Values{
		"template_id":     {"text-to-image"},
		"positive_prompt": {prompt},
	}, 42)
	if _, exists := imageValues["positive_prompt"]; exists {
		t.Fatal("image recipe accepted a prompt above its 4000-character limit")
	}
}

func TestGenerationQueueOverviewCountsRunningAndPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/queue" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"queue_running":[[1,"running-one",{},{}]],"queue_pending":[[2,"pending-one",{},{}],[3,"pending-two",{},{}]]}`))
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, SessionSecret: "01234567890123456789012345678901"}}
	overview, err := app.generationQueueOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if overview.Running != 1 || overview.Pending != 2 {
		t.Fatalf("unexpected queue overview: %#v", overview)
	}
}

func TestReleaseComfyMemoryWhenQueueIsIdle(t *testing.T) {
	var freeRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			_, _ = w.Write([]byte(`{"queue_running":[],"queue_pending":[]}`))
		case "/free":
			freeRequests++
			if r.Method != http.MethodPost {
				t.Fatalf("free method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Basic test-token" {
				t.Fatalf("authorization = %q", got)
			}
			var payload map[string]bool
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if !payload["unload_models"] || !payload["free_memory"] {
				t.Fatalf("unexpected free payload: %#v", payload)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream, ComfyUIUpstreamAuthHeader: "Basic test-token"}}
	app.releaseComfyMemoryIfIdle(context.Background())
	if freeRequests != 1 {
		t.Fatalf("free requests = %d, want 1", freeRequests)
	}
}

func TestReleaseComfyMemoryKeepsQueuedGenerationLoaded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			_, _ = w.Write([]byte(`{"queue_running":[[1,"running",{},{}]],"queue_pending":[]}`))
		case "/free":
			t.Fatal("memory release must not run while a generation is active")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream}}
	app.releaseComfyMemoryIfIdle(context.Background())
}

func TestMiniMaxH3WorkflowBuildsFrameAndReferenceGraphs(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "minimax-h3-video")
	if !ok || definition.Builder != "minimax_h3" || definition.AdminOnly {
		t.Fatalf("MiniMax H3 definition is missing or unexpectedly restricted: %#v", definition)
	}
	base := generationForm{
		ModelName:      "MiniMax\\MiniMax_H3_FL2VA_pruned_int8_convrot.safetensors",
		ReferenceModel: "MiniMax\\MiniMax_H3_Ref2VA_pruned_int8_convrot.safetensors",
		TextEncoder:    "MiniMax\\qwen3vl_32b_minimax_h3_nvfp4_awq.safetensors",
		VAE:            "MiniMax\\minimax_h3_video_vae_fp16.safetensors",
		AudioVAE:       "MiniMax\\minimax_h3_audio_vae_fp32.safetensors",
		Positive:       "A dancer turns toward the camera in warm evening light.", InputImage: "gateway/input-1.png", Width: 768, Height: 1344,
		Steps: 25, CFG: 1, Denoise: 1, VideoSampler: "euler", Sampler: "euler", Scheduler: "simple", Seed: 42,
		VideoMode: miniMaxH3FrameMode, VideoResolution: "portrait", VideoDurationSeconds: 5, VideoSteps: 25,
		VideoSageAttention: true, VideoClearVRAM: true,
	}
	prompt, err := definition.buildPrompt(base)
	if err != nil {
		t.Fatal(err)
	}
	frame := prompt["7"].(map[string]any)
	if got, want := frame["class_type"], "MiniMaxH3ImageToVideo"; got != want {
		t.Fatalf("frame node type = %v, want %v", got, want)
	}
	frameInputs := frame["inputs"].(map[string]any)
	if got, want := frameInputs["length"], 124; got != want {
		t.Fatalf("five second frame count = %v, want %v", got, want)
	}
	if _, ok := frameInputs["first_frame"]; !ok {
		t.Fatal("first frame is absent")
	}
	if _, ok := prompt["8"]; ok {
		t.Fatal("LoRA stack must not be created when no optional LoRA is selected")
	}
	if got, want := prompt["9"].(map[string]any)["inputs"].(map[string]any)["model"], []any{"5", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("no-LoRA model path = %#v, want %#v", got, want)
	}
	if _, ok := prompt["6"]; ok {
		t.Fatal("Turbo LoRA must remain optional")
	}
	if got, want := prompt["11"].(map[string]any)["class_type"], "KSamplerSelect"; got != want {
		t.Fatalf("regular sampler = %v, want %q", got, want)
	}
	video := prompt["17"].(map[string]any)["inputs"].(map[string]any)
	if got, want := video["format"], "video/h264-mp4"; got != want {
		t.Fatalf("browser video format = %v, want %q", got, want)
	}
	if got, want := prompt["18"].(map[string]any)["class_type"], "LCVRAMCacheClear"; got != want {
		t.Fatalf("cache-clear node = %v, want %q", got, want)
	}
	for _, nodeID := range []string{"19", "23"} {
		if got, want := prompt[nodeID].(map[string]any)["class_type"], "LCVRAMCacheClear"; got != want {
			t.Fatalf("cache-clear node %s = %v, want %q", nodeID, got, want)
		}
	}
	if got, want := prompt["50"].(map[string]any)["class_type"], "LCImageMaskResize"; got != want {
		t.Fatalf("frame resize = %v, want %q", got, want)
	}

	base.VideoTurbo = true
	base.VideoSteps = 6
	base.LoraNames = [maxGenerationLoraSlots]string{"MiniMax/style.safetensors", "", "", "MiniMax/motion.safetensors"}
	base.LoraModel = [maxGenerationLoraSlots]float64{0.7, 0, 0, 0.5}
	base.LoraClip = [maxGenerationLoraSlots]float64{0.7, 0, 0, 0.5}
	prompt, err = definition.buildPrompt(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, stackID := range []string{"lora_stack_1", "lora_stack_2"} {
		inputs := prompt[stackID].(map[string]any)["inputs"].(map[string]any)
		for _, key := range []string{"switch_1", "switch_2", "switch_3"} {
			if _, ok := inputs[key]; !ok {
				t.Fatalf("%s is missing %s: %#v", stackID, key, inputs)
			}
		}
	}
	if got, want := prompt["6"].(map[string]any)["inputs"].(map[string]any)["lora_name"], miniMaxH3TurboLoraName; got != want {
		t.Fatalf("Turbo LoRA = %v, want %q", got, want)
	}
	if got, want := prompt["11"].(map[string]any)["class_type"], "MiniMaxH3TurboSampler"; got != want {
		t.Fatalf("Turbo sampler = %v, want %q", got, want)
	}
	if got, want := prompt["9"].(map[string]any)["inputs"].(map[string]any)["model"], []any{"8", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("optional LoRA model path = %#v, want %#v", got, want)
	}
	if got, want := prompt["7"].(map[string]any)["inputs"].(map[string]any)["clip"], []any{"8", 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("optional LoRA CLIP path = %#v, want %#v", got, want)
	}

	base.VideoMode = miniMaxH3ReferenceMode
	base.InputImage = "gateway/input-1.png"
	base.ReferenceImages = [3]string{"gateway/input-2.png", "gateway/input-3.png"}
	prompt, err = definition.buildPrompt(base)
	if err != nil {
		t.Fatal(err)
	}
	references := prompt["7"].(map[string]any)
	if got, want := references["class_type"], "MiniMaxH3ReferenceToVideo"; got != want {
		t.Fatalf("reference node type = %v, want %v", got, want)
	}
	referenceInputs := references["inputs"].(map[string]any)
	if _, ok := referenceInputs["ref_images.ref_image_2"]; !ok {
		t.Fatalf("third reference image is absent: %#v", referenceInputs)
	}
	if got, want := prompt["1"].(map[string]any)["inputs"].(map[string]any)["unet_name"], base.ReferenceModel; got != want {
		t.Fatalf("reference model = %v, want %q", got, want)
	}
	if got, want := referenceInputs["ref_images.ref_image_0"], []any{"30", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("native reference source = %#v, want %#v", got, want)
	}
	if _, ok := prompt["50"]; ok {
		t.Fatal("reference mode must not stretch source images")
	}
	base.InputAudio = "gateway/reference-audio.mp3"
	prompt, err = definition.buildPrompt(base)
	if err != nil {
		t.Fatal(err)
	}
	referenceInputs = prompt["7"].(map[string]any)["inputs"].(map[string]any)
	if got, want := referenceInputs["ref_audios.ref_audio_0"], []any{"41", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audio reference = %#v, want %#v", got, want)
	}
	if got, want := prompt["40"].(map[string]any)["class_type"], "LoadAudio"; got != want {
		t.Fatalf("audio loader = %v, want %q", got, want)
	}
	if got, want := prompt["41"].(map[string]any)["class_type"], "TrimAudioDuration"; got != want {
		t.Fatalf("audio trim = %v, want %q", got, want)
	}
	base.InputVideo = "gateway/reference-video.mp4"
	base.VideoReferenceStart = 1.5
	base.VideoReferenceDuration = 4
	base.VideoReferenceAudio = true
	prompt, err = definition.buildPrompt(base)
	if err != nil {
		t.Fatal(err)
	}
	referenceInputs = prompt["7"].(map[string]any)["inputs"].(map[string]any)
	if got, want := referenceInputs["ref_videos.ref_video_0"], []any{"42", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("video reference = %#v, want %#v", got, want)
	}
	if got, want := referenceInputs["ref_video_audios.ref_video_audio_0"], []any{"42", 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("video audio reference = %#v, want %#v", got, want)
	}
	loader := prompt["42"].(map[string]any)["inputs"].(map[string]any)
	if got := loader["custom_width"]; got != 0 {
		t.Fatalf("video reference width = %v, want native size", got)
	}
	if got := loader["custom_height"]; got != 0 {
		t.Fatalf("video reference height = %v, want native size", got)
	}
	if got, want := loader["frame_load_cap"], 96; got != want {
		t.Fatalf("video reference frame cap = %v, want %d", got, want)
	}
	if got, want := loader["skip_first_frames"], 36; got != want {
		t.Fatalf("video reference skip = %v, want %d", got, want)
	}
}

func TestMiniMaxH3ErosMaxUsesIntegratedTurboReferencePath(t *testing.T) {
	input := generationForm{
		ModelName: "MiniMax\\h3ErosMax_beta4.safetensors", ReferenceModel: "MiniMax\\h3ErosMax_beta4.safetensors",
		TextEncoder: "MiniMax\\text.safetensors", VAE: "MiniMax\\video.safetensors", AudioVAE: "MiniMax\\audio.safetensors",
		Positive: "A single continuous camera move.", InputImage: "gateway/input.png", Width: 768, Height: 1024, Seed: 42,
		VideoMode: miniMaxH3FrameMode, VideoReferenceOnly: true, VideoIntegratedTurbo: true, VideoTurbo: true,
		VideoQuality: 480, VideoDurationSeconds: 5, VideoSteps: 8, VideoSampler: "euler", VideoScheduler: "simple", VideoShiftVideo: 12, VideoShiftAudio: 7,
		VideoSageAttention: true, VideoMemoryOptimize: true, VideoMemoryChunkRows: 4096, VideoMemoryMLP: "auto", VideoMemoryPrecision: "Auto", VideoMemoryQKV: "Auto", VideoMemoryAttention: "Standard",
		VideoAIMDOEnabled: true, VideoAIMDOResidency: "0 blocks",
		VideoSparseAttention: true, VideoSparseBudget: 0.15, VideoSparseSchedule: "Hold", VideoSparseEarlyStep: 4, VideoSparseEarlyKV: 0.5, VideoSparseLateStep: 0, VideoSparseLateKV: 0.5, VideoSparseBackend: "Kitchen INT8",
	}
	if err := normalizeMiniMaxH3(&input); err != nil {
		t.Fatal(err)
	}
	if input.VideoMode != miniMaxH3ReferenceMode || input.VideoTurbo || input.Sampler != "euler" {
		t.Fatalf("normalized Eros profile = %#v", input)
	}
	prompt, err := buildMiniMaxH3Prompt(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := prompt["7"].(map[string]any)["class_type"], "MiniMaxH3ReferenceToVideo"; got != want {
		t.Fatalf("Eros conditioning = %v, want %q", got, want)
	}
	if _, ok := prompt["6"]; ok {
		t.Fatal("integrated Eros Turbo must not add the external Turbo LoRA")
	}
	if got, want := prompt["11"].(map[string]any)["class_type"], "KSamplerSelect"; got != want {
		t.Fatalf("Eros sampler node = %v, want %q", got, want)
	}
	if got, want := prompt["24"].(map[string]any)["class_type"], "H3MemoryOptimization"; got != want {
		t.Fatalf("memory node = %v, want %q", got, want)
	}
	if got, want := prompt["26"].(map[string]any)["class_type"], "H3AIMDOResidencyLimiter"; got != want {
		t.Fatalf("AIMDO node = %v, want %q", got, want)
	}
	if got, want := prompt["25"].(map[string]any)["class_type"], "H3SparseAttentionAdvanced"; got != want {
		t.Fatalf("sparse node = %v, want %q", got, want)
	}
	if got, want := prompt["25"].(map[string]any)["inputs"].(map[string]any)["early_schedule"], "Hold"; got != want {
		t.Fatalf("sparse early schedule = %v, want %q", got, want)
	}
	if got, want := prompt["9"].(map[string]any)["inputs"].(map[string]any)["model"], []any{"25", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Eros model chain = %#v, want %#v", got, want)
	}
}

func TestMiniMaxH3V5BuildsOptionalMemoryNodesInWorkflowOrder(t *testing.T) {
	input := generationForm{
		ModelName: "MiniMax/model.safetensors", TextEncoder: "MiniMax/text.safetensors", VAE: "MiniMax/video.safetensors", AudioVAE: "MiniMax/audio.safetensors",
		Positive: "A single continuous camera move.", Width: 768, Height: 1024, Seed: 42,
		VideoMode: miniMaxH3FrameMode, VideoDurationSeconds: 5, VideoSteps: 25, VideoSampler: "euler", VideoScheduler: "simple", VideoShiftVideo: 11, VideoShiftAudio: 3,
		VideoLowVRAMAttention: true, VideoLowVRAMHeadChunks: 4,
		VideoChunkFeedForward: true, VideoChunkFFChunks: 2, VideoChunkFFThreshold: 4096,
		VideoSageAttention: true, VideoMemoryOptimize: true, VideoMemoryChunkRows: 4096, VideoMemoryMLP: "auto", VideoMemoryPrecision: "Auto", VideoMemoryQKV: "Auto", VideoMemoryAttention: "Standard",
		VideoAIMDOEnabled: true, VideoAIMDOResidency: "0 blocks",
		VideoSparseAttention: true, VideoSparseBudget: 0.30, VideoSparseSchedule: "Hold", VideoSparseEarlyStep: 2, VideoSparseEarlyKV: 0.5, VideoSparseLateStep: 2, VideoSparseLateKV: 0.5, VideoSparseBackend: "Kitchen INT8",
	}
	prompt, err := buildMiniMaxH3Prompt(input)
	if err != nil {
		t.Fatal(err)
	}
	for nodeID, classType := range map[string]string{"28": "MiniMaxLowVRAMAttention", "29": "MiniMaxChunkFeedForward", "5": "MiniMaxH3MemoryEfficientSageAttentionPatch"} {
		if got := prompt[nodeID].(map[string]any)["class_type"]; got != classType {
			t.Fatalf("node %s = %v, want %q", nodeID, got, classType)
		}
	}
	if got, want := prompt["28"].(map[string]any)["inputs"].(map[string]any)["model"], []any{"1", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Low VRAM input = %#v, want %#v", got, want)
	}
	if got, want := prompt["29"].(map[string]any)["inputs"].(map[string]any)["model"], []any{"28", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Chunk FeedForward input = %#v, want %#v", got, want)
	}
	if got, want := prompt["5"].(map[string]any)["inputs"].(map[string]any)["model"], []any{"29", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SageAttention input = %#v, want %#v", got, want)
	}
	if got, want := prompt["9"].(map[string]any)["inputs"].(map[string]any)["model"], []any{"25", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("final model chain = %#v, want %#v", got, want)
	}
}

func TestMiniMaxH3WorkflowBuildsOptionalPostProcessing(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := findWorkflow(definitions, "minimax-h3-video")
	if !ok {
		t.Fatal("MiniMax H3 definition is missing")
	}
	input := generationForm{
		ModelName: "MiniMax/model.safetensors", TextEncoder: "MiniMax/text.safetensors", VAE: "MiniMax/video.safetensors", AudioVAE: "MiniMax/audio.safetensors",
		Positive: "A calm cinematic scene.", InputImage: "gateway/input.png", Width: 768, Height: 1344, Steps: 25, CFG: 1, Denoise: 1, Seed: 42,
		VideoMode: miniMaxH3FrameMode, VideoQuality: 720, VideoDurationSeconds: 5, VideoSteps: 25,
		VideoSageAttention: true, VideoClearVRAM: true, VideoMemoryOptimize: true, VideoMemoryChunkRows: 4096, VideoMemoryMLP: "auto", VideoMemoryPrecision: "Auto", VideoMemoryQKV: "Auto", VideoMemoryAttention: "Standard",
		VideoAIMDOEnabled: true, VideoAIMDOResidency: "0 blocks",
		VideoSparseAttention: true, VideoSparseBudget: 0.15, VideoSparseSchedule: "Hold", VideoSparseEarlyStep: 4, VideoSparseEarlyKV: 0.5, VideoSparseLateStep: 0, VideoSparseLateKV: 0.5, VideoSparseBackend: "Kitchen INT8",
		VideoRIFEEnabled: true, VideoRIFECheckpoint: "rife49.pth", VideoRIFEMultiplier: 2,
		VideoRIFEFastMode: true, VideoRIFEEnsemble: true, VideoRIFEDtype: "float32", VideoRIFEBatchSize: 1,
		VideoRTXEnabled: true, VideoRTXScale: 2, VideoRTXQuality: "ULTRA", VideoColorMatch: true, VideoColorMethod: "adain", VideoColorStrength: 0.8,
		VideoSharpenEnabled: true, VideoSharpenMethod: "rcas", VideoSharpenStrength: 0.8, VideoSharpenRadius: 1, VideoSharpenThreshold: 0.05, VideoSharpenIterations: 10,
		VideoOutputCRF: 19,
	}
	prompt, err := definition.buildPrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	for nodeID, classType := range map[string]string{"20": "RTXVideoSuperResolution", "21": "LCColorMatch", "22": "RIFE VFI", "24": "H3MemoryOptimization", "25": "H3SparseAttentionAdvanced", "26": "H3AIMDOResidencyLimiter", "27": "ImageSharpenKJ"} {
		if got := prompt[nodeID].(map[string]any)["class_type"]; got != classType {
			t.Fatalf("node %s = %v, want %q", nodeID, got, classType)
		}
	}
	rtx := prompt["20"].(map[string]any)["inputs"].(map[string]any)
	if got, want := rtx["resize_type"], "scale by multiplier"; got != want {
		t.Fatalf("RTX resize type = %v, want %q", got, want)
	}
	if got, want := rtx["resize_type.scale"], 2.0; got != want {
		t.Fatalf("RTX scale = %v, want %v", got, want)
	}
	if _, exists := rtx["scale"]; exists {
		t.Fatal("RTX scale must use the DynamicCombo key path")
	}
	video := prompt["17"].(map[string]any)["inputs"].(map[string]any)
	if got, want := video["images"], []any{"27", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("video source = %#v, want %#v", got, want)
	}
	if got, want := video["frame_rate"], 48; got != want {
		t.Fatalf("frame rate = %v, want %d", got, want)
	}
	if got, want := prompt["20"].(map[string]any)["inputs"].(map[string]any)["images"], []any{"22", 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RTX input = %#v, want %#v", got, want)
	}
	if got, want := prompt["27"].(map[string]any)["inputs"].(map[string]any)["method.strength"], 0.8; got != want {
		t.Fatalf("sharpen strength = %#v, want %#v", got, want)
	}
}

func TestMiniMaxH3SupportsTextToVideoWithoutFirstFrame(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := findWorkflow(definitions, "minimax-h3-video")
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "model", ReferenceModel: "reference", TextEncoder: "clip", VAE: "video-vae", AudioVAE: "audio-vae",
		Positive: "animate", Width: 768, Height: 1344, Steps: 25, CFG: 1, Denoise: 1, Sampler: "res_multistep", Scheduler: "simple",
		VideoMode: miniMaxH3FrameMode, VideoResolution: "portrait", VideoDurationSeconds: 5, VideoSteps: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	inputs := prompt["7"].(map[string]any)["inputs"].(map[string]any)
	if _, exists := inputs["first_frame"]; exists {
		t.Fatalf("text-to-video unexpectedly contains a first frame: %#v", inputs)
	}
}

func TestMiniMaxH3SupportsPromptOnlyReferenceMode(t *testing.T) {
	definitions, err := loadWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := findWorkflow(definitions, "minimax-h3-video")
	prompt, err := definition.buildPrompt(generationForm{
		ModelName: "model", ReferenceModel: "reference", TextEncoder: "clip", VAE: "video-vae", AudioVAE: "audio-vae",
		Positive: "a prompt-driven reference video", Width: 768, Height: 1344, Steps: 25, CFG: 1, Denoise: 1, Sampler: "euler", Scheduler: "simple",
		VideoMode: miniMaxH3ReferenceMode, VideoResolution: "portrait", VideoDurationSeconds: 5, VideoSteps: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	node := prompt["7"].(map[string]any)
	if got, want := node["class_type"], "MiniMaxH3ReferenceToVideo"; got != want {
		t.Fatalf("prompt-only reference node = %v, want %q", got, want)
	}
	for key := range node["inputs"].(map[string]any) {
		if strings.HasPrefix(key, "ref_images.") || strings.HasPrefix(key, "ref_videos.") || strings.HasPrefix(key, "ref_audios.") {
			t.Fatalf("prompt-only reference workflow unexpectedly contains %q", key)
		}
	}
}

func TestRequiresImageEditingSupport(t *testing.T) {
	if !requiresImageEditingSupport(workflowDefinition{RequiresImage: true, Builder: "flux2_edit"}) {
		t.Fatal("image-edit workflow must require dedicated image-edit support")
	}
	if requiresImageEditingSupport(workflowDefinition{RequiresImage: true, Builder: "minimax_h3"}) {
		t.Fatal("MiniMax H3 must not be treated as an image-edit workflow")
	}
	if requiresImageEditingSupport(workflowDefinition{RequiresImage: false, Builder: "checkpoint"}) {
		t.Fatal("text workflow must not require dedicated image-edit support")
	}
}

func TestMiniMaxH3RejectsAudioOutsideReferenceMode(t *testing.T) {
	input := generationForm{VideoMode: miniMaxH3FrameMode, VideoResolution: "portrait", VideoDurationSeconds: 5, VideoSteps: 25, InputImage: "gateway/first.png", InputAudio: "gateway/reference.mp3"}
	if err := normalizeMiniMaxH3(&input); err == nil || !strings.Contains(err.Error(), "аудиореференс") {
		t.Fatalf("frame-mode audio error = %v", err)
	}
}

func TestMiniMaxH3V5RejectsUnsupportedMemoryAndRTXRanges(t *testing.T) {
	base := generationForm{VideoQuality: 480, VideoDurationSeconds: 5, VideoSteps: 25, VideoMode: miniMaxH3FrameMode, Width: 480, Height: 640}

	input := base
	input.VideoLowVRAMHeadChunks = 57
	if err := normalizeMiniMaxH3(&input); err == nil || !strings.Contains(err.Error(), "Low VRAM Attention") {
		t.Fatalf("head chunk error = %v", err)
	}

	input = base
	input.VideoChunkFFThreshold = 500
	if err := normalizeMiniMaxH3(&input); err == nil || !strings.Contains(err.Error(), "Chunk FeedForward") {
		t.Fatalf("feed-forward threshold error = %v", err)
	}

	input = base
	input.VideoRTXScale = 2.1
	if err := normalizeMiniMaxH3(&input); err == nil || !strings.Contains(err.Error(), "от 1 до 2") {
		t.Fatalf("RTX range error = %v", err)
	}
}

func TestMiniMaxH3VideoQualityPreservesReferenceAspect(t *testing.T) {
	for _, quality := range []int{480, 720, 1080, 1440} {
		width, height, err := miniMaxH3VideoDimensions(1200, 1600, quality)
		if err != nil {
			t.Fatalf("dimensions for %dp: %v", quality, err)
		}
		if width%32 != 0 || height%32 != 0 {
			t.Fatalf("%dp produced non-compatible dimensions %dx%d", quality, width, height)
		}
		if got := float64(width) / float64(height); math.Abs(got-0.75) > 0.03 {
			t.Fatalf("%dp changed reference aspect ratio: %.3f", quality, got)
		}
		input := generationForm{VideoQuality: quality, VideoDurationSeconds: 5, VideoSteps: 25, VideoMode: miniMaxH3FrameMode, InputImage: "gateway/first-frame.png", Width: width, Height: height}
		if err := normalizeMiniMaxH3(&input); err != nil {
			t.Fatalf("normalize %dp: %v", quality, err)
		}
		if input.VideoResolution != "reference-"+strconv.Itoa(quality)+"p" {
			t.Fatalf("video resolution = %q", input.VideoResolution)
		}
	}
}

func TestMiniMaxH3AlwaysUsesFirstPictureAspect(t *testing.T) {
	var body bytes.Buffer
	if err := png.Encode(&body, image.NewGray(image.Rect(0, 0, 352, 480))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/view" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(body.Bytes())
	}))
	defer server.Close()
	upstream, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{ComfyUIUpstream: upstream}}
	input := generationForm{InputImage: "gateway/first.png", VideoAspect: "16:9", VideoQuality: 480}
	if err := app.resolveMiniMaxH3ReferenceDimensions(context.Background(), &input); err != nil {
		t.Fatal(err)
	}
	if input.Width != 352 || input.Height != 480 {
		t.Fatalf("first picture aspect was replaced by the manual preset: %dx%d", input.Width, input.Height)
	}
}

func TestMiniMaxH3VideoDimensionsBoundExtremeReferences(t *testing.T) {
	width, height, err := miniMaxH3VideoDimensions(6000, 2000, 1440)
	if err != nil {
		t.Fatal(err)
	}
	if max(width, height) > miniMaxH3MaxDimension || int64(width)*int64(height) > miniMaxH3MaxBasePixels {
		t.Fatalf("unsafe dimensions: %dx%d", width, height)
	}
	if got := float64(width) / float64(height); math.Abs(got-3) > 0.04 {
		t.Fatalf("aspect ratio changed: %.3f", got)
	}
}

func TestMiniMaxH3VideoQualityClampsLongSide(t *testing.T) {
	width, height, err := miniMaxH3VideoDimensions(1408, 1872, 480)
	if err != nil {
		t.Fatal(err)
	}
	if width != 352 || height != 480 {
		t.Fatalf("480 max-resolution dimensions = %dx%d, want 352x480", width, height)
	}
	width, height, err = miniMaxH3VideoDimensions(352, 480, 1440)
	if err != nil {
		t.Fatal(err)
	}
	if width != 352 || height != 480 {
		t.Fatalf("small source was unexpectedly enlarged to %dx%d", width, height)
	}
}

func TestMiniMaxH3ResourceBudgetRejectsCombinedHeavyPostProcessing(t *testing.T) {
	input := generationForm{
		Width: 1440, Height: 2560, InputImage: "gateway/first.png",
		VideoMode: miniMaxH3FrameMode, VideoQuality: 1440, VideoDurationSeconds: 15, VideoSteps: 25,
		VideoRTXEnabled: true, VideoRTXScale: 2, VideoRTXQuality: "ULTRA",
		VideoRIFEEnabled: true, VideoRIFEMultiplier: 4, VideoRIFEBatchSize: 1, VideoRIFECheckpoint: "rife49.pth", VideoRIFEDtype: "float32",
	}
	if err := normalizeMiniMaxH3(&input); err == nil || !strings.Contains(err.Error(), "RTX") {
		t.Fatalf("heavy post-processing error = %v", err)
	}
}

func TestMiniMaxH3ResourceBudgetAllowsPracticalPostProcessing(t *testing.T) {
	input := generationForm{
		Width: 720, Height: 1280, InputImage: "gateway/first.png",
		VideoMode: miniMaxH3FrameMode, VideoQuality: 720, VideoDurationSeconds: 15, VideoSteps: 25,
		VideoRTXEnabled: true, VideoRTXScale: 2, VideoRTXQuality: "ULTRA",
		VideoRIFEEnabled: true, VideoRIFEMultiplier: 2, VideoRIFEBatchSize: 1, VideoRIFECheckpoint: "rife49.pth", VideoRIFEDtype: "float32",
	}
	if err := normalizeMiniMaxH3(&input); err != nil {
		t.Fatalf("practical post-processing was rejected: %v", err)
	}
}

func TestMiniMaxH3ResourceBudgetAllows2KWithRTXAndRIFE(t *testing.T) {
	input := generationForm{
		Width: 1080, Height: 1440, InputImage: "gateway/first.png",
		VideoMode: miniMaxH3FrameMode, VideoQuality: 1440, VideoDurationSeconds: 15, VideoSteps: 25,
		VideoRTXEnabled: true, VideoRTXScale: 2, VideoRTXQuality: "ULTRA",
		VideoRIFEEnabled: true, VideoRIFEMultiplier: 4, VideoRIFEBatchSize: 1, VideoRIFECheckpoint: "rife49.pth", VideoRIFEDtype: "float32",
	}
	if err := normalizeMiniMaxH3(&input); err != nil {
		t.Fatalf("2K video with RTX and RIFE was rejected: %v", err)
	}
}

func TestGenerationAuditMetadataCapturesMiniMaxSettingsAndLoras(t *testing.T) {
	input := generationForm{
		TemplateID: "minimax-h3-video", ModelName: "MiniMax/model.safetensors", ModelFamily: modelFamilyMiniMaxH3,
		InputImage: "gateway/first.png", ReferenceImages: [3]string{"gateway/second.png"},
		ReferenceMetadata: [maxGenerationReferenceSlots]generationReferenceMetadata{
			{Role: "identity", Source: "gallery", SourceID: "17", SourceName: "first.png"},
			{Role: "style", Source: "device", SourceName: "second.png"},
		},
		Width: 480, Height: 640, Steps: 20, CFG: 1, Denoise: 1, Sampler: "res_multistep", Scheduler: "simple", Seed: 42,
		VideoMode: miniMaxH3ReferenceMode, VideoResolution: "reference-480p", VideoQuality: 480, VideoDurationSeconds: 5,
		VideoReferenceSize: "match", VideoSteps: 20, VideoScheduler: "simple", VideoShiftVideo: 11, VideoShiftAudio: 3,
		VideoSampler: "euler", VideoSageAttention: true, VideoLowVRAMAttention: true, VideoLowVRAMHeadChunks: 4, VideoChunkFeedForward: true, VideoChunkFFChunks: 2, VideoChunkFFThreshold: 4096, VideoClearVRAM: true,
		VideoMemoryOptimize: true, VideoMemoryMLP: "auto", VideoMemoryChunkRows: 4096, VideoMemoryPrecision: "Auto", VideoMemoryQKV: "Auto", VideoMemoryAttention: "Standard",
		VideoAIMDOEnabled: true, VideoAIMDOResidency: "0 blocks",
		VideoSparseAttention: true, VideoSparseBudget: 0.15, VideoSparseSchedule: "Hold", VideoSparseEarlyStep: 4, VideoSparseEarlyKV: 0.5, VideoSparseLateStep: 0, VideoSparseLateKV: 0.5, VideoSparseBackend: "Kitchen INT8",
		VideoRIFEEnabled: true, VideoRIFECheckpoint: "rife49.pth", VideoRIFEMultiplier: 2, VideoRIFEFastMode: true, VideoRIFEEnsemble: true, VideoRIFEDtype: "float32", VideoRIFEBatchSize: 1,
		VideoRTXEnabled: true, VideoRTXScale: 2, VideoRTXQuality: "ULTRA",
		VideoColorMatch: true, VideoColorMethod: "adain", VideoColorStrength: 1, VideoOutputCRF: 19,
		VideoSharpenEnabled: true, VideoSharpenMethod: "rcas", VideoSharpenStrength: 0.8, VideoSharpenRadius: 1, VideoSharpenThreshold: 0.05, VideoSharpenIterations: 10,
		LoraNames: [maxGenerationLoraSlots]string{"MiniMaxH3/motion.safetensors"}, LoraModel: [maxGenerationLoraSlots]float64{0.9}, LoraClip: [maxGenerationLoraSlots]float64{1},
	}
	metadata := generationAuditMetadata(workflowDefinition{ID: "minimax-h3-video"}, input)
	video, ok := metadata["minimax_h3"].(map[string]any)
	if !ok || video["mode"] != miniMaxH3ReferenceMode || video["quality"] != 480 || video["sage_attention"] != true || video["clear_vram"] != true {
		t.Fatalf("MiniMax audit settings = %#v", video)
	}
	if video["rife"].(map[string]any)["enabled"] != true || video["rtx"].(map[string]any)["scale"] != float64(2) || video["color_match"].(map[string]any)["enabled"] != true {
		t.Fatalf("MiniMax post-processing audit settings = %#v", video)
	}
	if video["low_vram_attention"].(map[string]any)["head_chunks"] != 4 || video["chunk_feed_forward"].(map[string]any)["threshold"] != 4096 || video["memory_optimization"].(map[string]any)["enabled"] != true || video["aimdo_residency"].(map[string]any)["residency"] != "0 blocks" || video["sparse_attention"].(map[string]any)["early_schedule"] != "Hold" || video["sparse_attention"].(map[string]any)["backend"] != "Kitchen INT8" || video["sharpen"].(map[string]any)["method"] != "rcas" {
		t.Fatalf("MiniMax v5 audit settings = %#v", video)
	}
	loras, ok := metadata["loras"].([]map[string]any)
	if !ok || len(loras) != 1 || loras[0]["name"] != "MiniMaxH3/motion.safetensors" || loras[0]["model_strength"] != 0.9 {
		t.Fatalf("MiniMax audit LoRAs = %#v", metadata["loras"])
	}
	references, ok := metadata["references"].([]map[string]any)
	if !ok || len(references) != 2 || references[0]["number"] != 1 || references[0]["role"] != "identity" || references[0]["source"] != "gallery" || references[0]["source_id"] != "17" || references[1]["role"] != "style" || references[1]["source"] != "device" {
		t.Fatalf("MiniMax audit references = %#v", metadata["references"])
	}
}
