package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/promptassistant"
)

const harnessUltraMount = "/internal/harness-ultra/"

var harnessUltraID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

// This broker owns host credentials and physical admission, never the agent's
// prompts, queue or results. The editable microservice cannot control mining,
// submit other workflows, enumerate another user's history or read host files.
func (a *App) harnessUltraBroker() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !a.cfg.Harness.Enabled() || len(a.cfg.Harness.UltraKey) < 32 || a.cfg.RestoreDrillMode ||
			len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.cfg.Harness.UltraKey)) != 1 {
			http.NotFound(w, r)
			return
		}
		if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Content-Encoding") != "" || a.gpuCoordinator == nil || !a.cfg.MiningFencedCommands {
			http.Error(w, "broker unavailable or invalid path", 400)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, harnessUltraMount), "/")
		if len(parts) == 2 && parts[0] == "moss" {
			switch {
			case parts[1] == "catalog" && r.Method == "GET":
				a.harnessMossCatalog(w, r)
			case parts[1] == "loras" && r.Method == "POST":
				a.harnessImageLoras(w, r, "mossimage")
			case parts[1] == "prompt-guide" && r.Method == "POST":
				a.harnessImagePromptGuide(w, r, modelFamilyMoss)
			default:
				http.NotFound(w, r)
			}
			return
		}
		if len(parts) == 1 && parts[0] == "catalog" && r.Method == "GET" {
			a.harnessUltraCatalog(w, r)
			return
		}
		if len(parts) == 1 && parts[0] == "loras" && r.Method == "POST" {
			a.harnessUltraLoras(w, r)
			return
		}
		if len(parts) == 1 && parts[0] == "prompt-guide" && r.Method == "POST" {
			a.harnessUltraPromptGuide(w, r)
			return
		}
		if len(parts) == 1 && parts[0] == "validate" && r.Method == "POST" {
			a.harnessUltraValidate(w, r)
			return
		}
		if len(parts) == 2 && parts[0] == "inputs" && harnessUltraID.MatchString(parts[1]) && r.Method == "POST" {
			a.harnessUltraUpload(w, r, parts[1])
			return
		}
		if len(parts) < 2 || parts[0] != "jobs" || !harnessUltraID.MatchString(parts[1]) {
			http.NotFound(w, r)
			return
		}
		id := parts[1]
		if len(parts) == 2 && r.Method == "GET" {
			a.harnessUltraStatus(w, r, id)
			return
		}
		if len(parts) == 4 && parts[2] == "outputs" && r.Method == "GET" {
			a.harnessUltraOutput(w, r, id, parts[3])
			return
		}
		if len(parts) != 3 || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		switch parts[2] {
		case "dispatch":
			a.harnessUltraDispatch(w, r, id)
		case "cancel":
			a.harnessUltraCancel(w, r, id)
		default:
			http.NotFound(w, r)
		}
	})
}

// Ultra needs the cached executor schema, not the unrelated website model groups.
func (a *App) harnessUltraExecutorCatalog(ctx context.Context) generationModelCatalog {
	snapshot, err := a.comfyObjectInfo(ctx, false)
	return generationModelCatalog{ObjectInfo: snapshot, Online: err == nil && snapshot.Source != comfyObjectInfoLastKnownGood}
}

func (a *App) harnessUltraCatalog(w http.ResponseWriter, r *http.Request) {
	catalog := a.harnessUltraExecutorCatalog(r.Context())
	models := harnessUltraModels(catalog)
	if !catalog.Online || len(models) == 0 || !hasGenerationNodes(catalog.ObjectInfo.Info, kreaProWorkflowManifest().RequiredClasses...) {
		http.Error(w, "Krea2 Ultra executor unavailable", 503)
		return
	}
	writeJSON(w, 200, map[string]any{"workflow": "krea2-pro", "version": kreaProWorkflowManifest().Version, "models": models,
		"loras": comfyChoiceStrings(catalog.ObjectInfo.Info, "LoraLoader", "lora_name"), "options": kreaProOptions(), "companions": harnessUltraCompanions(catalog),
		"capabilities": map[string]any{"relay_vsr": harnessUltraRelayCapability(catalog)}})
}

type harnessUltraRelayVSRCapability struct {
	Available   bool  `json:"available"`
	ReadyScales []int `json:"ready_scales"`
}

func harnessUltraRelayCapability(catalog generationModelCatalog) harnessUltraRelayVSRCapability {
	capability := harnessUltraRelayVSRCapability{ReadyScales: []int{}}
	info := catalog.ObjectInfo.Info
	if !catalog.Online || catalog.ObjectInfo.Source == comfyObjectInfoLastKnownGood || !hasGenerationNodes(info, "LCRelayVSRUpscale", "RelayVSRConditioning", "VSRDecode") {
		return capability
	}
	hasFile := func(choices []string, filename string) bool {
		for _, choice := range choices {
			if strings.EqualFold(path.Base(strings.ReplaceAll(choice, "\\", "/")), filename) {
				return true
			}
		}
		return false
	}
	models := comfyChoiceStrings(info, "UNETLoader", "unet_name")
	vaes := comfyChoiceStrings(info, "VAELoader", "vae_name")
	for _, asset := range []struct {
		scale      int
		model, vae string
	}{
		{2, "relayvsr_x2_lightx2v_int8_convrot.safetensors", "relayvsr_flash_vae_x2_fp32.safetensors"},
		{4, "relayvsr_x4_int8_convrot.safetensors", "relayvsr_flash_vae_x4_fp32.safetensors"},
	} {
		if hasFile(models, asset.model) && hasFile(vaes, asset.vae) {
			capability.ReadyScales = append(capability.ReadyScales, asset.scale)
		}
	}
	capability.Available = len(capability.ReadyScales) > 0
	return capability
}

func harnessUltraModels(catalog generationModelCatalog) []map[string]any {
	models := []map[string]any{}
	companions := harnessUltraCompanions(catalog)
	encoder, vae := kreaCompanionAssets(companions["text_encoders"], companions["vaes"])
	if vae == "" {
		vae = exactModel(companions["vaes"], "krea2RealVae_v10.safetensors")
	}
	if encoder == "" || vae == "" {
		return models
	}
	for _, name := range comfyChoiceStrings(catalog.ObjectInfo.Info, "GatewayKreaProOptimizer", "model_file") {
		lower := strings.ToLower(strings.ReplaceAll(name, "\\", "/"))
		if (strings.HasPrefix(lower, "krea2/") || strings.HasPrefix(lower, "krea 2/")) && !strings.Contains(lower, "raw") {
			models = append(models, map[string]any{"name": name, "text_encoder": encoder, "vae": vae})
		}
	}
	return models
}

func (a *App) harnessUltraValidate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID    string         `json:"id"`
		Graph map[string]any `json:"graph"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !harnessUltraID.MatchString(input.ID) {
		http.Error(w, "invalid workflow plan", 400)
		return
	}
	catalog := a.harnessUltraExecutorCatalog(r.Context())
	if !catalog.Online {
		http.Error(w, "executor unavailable", 503)
		return
	}
	if err := validateHarnessUltraGraph(input.Graph, input.ID, catalog); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, 200, map[string]any{"valid": true, "gpu_acquired": false, "reachable_nodes": len(input.Graph)})
}

func (a *App) ultraComfy(ctx context.Context, method, endpoint string, query url.Values, body io.Reader, contentType string, work *domain.GPUWork) ([]byte, int, error) {
	u := *a.cfg.ComfyUIUpstream
	u.Path, u.RawQuery = singleJoiningSlash(u.Path, endpoint), query.Encode()
	r, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		r.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	if work != nil && a.cfg.ComfyGPUGuardRequired {
		r.Header.Set(comfyGPUKeyHeader, a.cfg.ComfyGPUGuardToken)
		r.Header.Set(comfyGPUWorkHeader, work.ID)
		r.Header.Set(comfyGPULeaseHeader, work.LeaseToken)
	}
	response, err := (&http.Client{Timeout: 40 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxGenerationHistory+1))
	if len(data) > maxGenerationHistory {
		return nil, response.StatusCode, errors.New("executor response too large")
	}
	return data, response.StatusCode, err
}

func (a *App) harnessUltraUpload(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "image too large", 413)
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	ext := map[string]string{"png": "png", "jpeg": "jpg", "webp": "webp"}[format]
	if err != nil || ext == "" || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 16384 || cfg.Height > 16384 || int64(cfg.Width)*int64(cfg.Height) > 64_000_000 {
		http.Error(w, "invalid image", 400)
		return
	}
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	part, _ := form.CreateFormFile("image", id+"."+ext)
	_, _ = part.Write(data)
	_ = form.WriteField("subfolder", "harness-ultra")
	_ = form.WriteField("type", "input")
	_ = form.WriteField("overwrite", "true")
	_ = form.Close()
	body, status, err := a.ultraComfy(r.Context(), "POST", "/upload/image", nil, &buf, form.FormDataContentType(), nil)
	var reply struct{ Name, Subfolder, Type string }
	if err != nil || status != 200 || json.Unmarshal(body, &reply) != nil || reply.Name != id+"."+ext || reply.Subfolder != "harness-ultra" || reply.Type != "input" {
		http.Error(w, "image upload unconfirmed", 502)
		return
	}
	writeJSON(w, 200, map[string]any{"name": "harness-ultra/" + reply.Name, "width": cfg.Width, "height": cfg.Height})
}

func validateHarnessUltraGraph(graph map[string]any, id string, catalog generationModelCatalog) error {
	if loader, ok := graph["32"].(map[string]any); ok && loader["class_type"] == "UNETLoader" && graph["3857"] == nil {
		return validateHarnessMossGraph(graph, id, catalog)
	}
	if len(graph) < 5 || len(graph) > 400 {
		return errors.New("invalid workflow size")
	}
	allowed := map[string]bool{}
	for _, kind := range kreaProWorkflowManifest().RequiredClasses {
		allowed[kind] = true
	}
	allowed["LCRelayVSRUpscale"] = true
	relayCapability := harnessUltraRelayCapability(catalog)
	for nodeID, raw := range graph {
		n, ok := raw.(map[string]any)
		if !ok {
			return errors.New("invalid node")
		}
		kind, _ := n["class_type"].(string)
		inputs, ok := n["inputs"].(map[string]any)
		if !ok || !allowed[kind] {
			return errors.New("node is outside Krea2 Ultra")
		}
		if kind == "LCRelayVSRUpscale" {
			ready := false
			for _, scale := range relayCapability.ReadyScales {
				if inputs["upscale"] == fmt.Sprintf("%dx", scale) {
					ready = true
				}
			}
			if !relayCapability.Available || !ready {
				return errors.New("RelayVSR core nodes and local model/VAE are not ready for the selected scale")
			}
		}
		if kind == "LCSaveImage" {
			if nodeID != "4071" || inputs["path"] != "harness-ultra/"+id || inputs["filename"] != "result" || inputs["filename_prefix"] != "Harness-Ultra" || inputs["format"] != "png" {
				return errors.New("output namespace mismatch")
			}
		}
		if kind == "LoadImage" {
			name, _ := inputs["image"].(string)
			base := strings.TrimPrefix(name, "harness-ultra/")
			if !strings.HasPrefix(name, "harness-ultra/") || !harnessUltraID.MatchString(strings.TrimSuffix(base, path.Ext(base))) || (path.Ext(base) != ".png" && path.Ext(base) != ".jpg" && path.Ext(base) != ".webp") {
				return errors.New("input namespace mismatch")
			}
		}
		if kind == "LCLoraLoader" && inputs["lora_rows"] != "[]" {
			return errors.New("use explicit LoRA nodes")
		}
		if kind == "LCVRAMCacheClear" {
			inputs["free_memory"] = false
		}
		if kind == "LCWeightedPrompt" {
			inputs["block"] = "never"
			delete(inputs, "last_in")
		}
		if kind == "LCTouchMask" {
			inputs["block"] = false
		}
	}
	loader, _ := graph["3857"].(map[string]any)
	if loader == nil || loader["class_type"] != "GatewayKreaProOptimizer" {
		return errors.New("missing Krea2 loader")
	}
	inputs := loader["inputs"].(map[string]any)
	validModel := false
	for _, model := range harnessUltraModels(catalog) {
		companions := harnessUltraCompanions(catalog)
		encoder, _ := inputs["text_encoder"].(string)
		vae, _ := inputs["vae"].(string)
		if inputs["model_file"] == model["name"] && exactModel(companions["text_encoders"], encoder) != "" && exactModel(companions["vaes"], vae) != "" {
			validModel = true
		}
	}
	if !validModel {
		return errors.New("Krea2 model or companion assets unavailable")
	}
	inputs["torch_compile"], inputs["speed_ups"] = "off", "Manual"
	if save, ok := graph["4071"].(map[string]any); !ok || save["class_type"] != "LCSaveImage" {
		return errors.New("missing final output")
	}
	// Editable source does not make the shared GPU an unbounded allocator.
	dimensions, ok := graph["3481"].(map[string]any)
	if !ok || dimensions["class_type"] != "LCAspectRatioPipeOut" {
		return errors.New("missing image dimensions")
	}
	dims, ok := dimensions["inputs"].(map[string]any)
	if !ok {
		return errors.New("invalid image dimensions")
	}
	width, wok := dims["custom_width"].(float64)
	height, hok := dims["custom_height"].(float64)
	if !wok || !hok || math.IsNaN(width) || math.IsNaN(height) || width < 256 || height < 256 || width > 4096 || height > 4096 || width*height > qwen21MaxOutputPixels || int(width)%16 != 0 || int(height)%16 != 0 || width != float64(int(width)) || height != float64(int(height)) || dims["resolution_source"] != false || dims["max_resolution"] != float64(0) || dims["aspect_ratio"] != "custom" || dims["batch_size"] != float64(1) {
		return errors.New("image exceeds the shared GPU memory profile")
	}
	if issues := validateComfyPrompt(catalog.ObjectInfo.Schema, graph); len(issues) > 0 {
		return fmt.Errorf("workflow incompatible: %s", issues[0].Message)
	}
	if err := validateHarnessUltraUpscaling(graph, int(width), int(height)); err != nil {
		return err
	}
	return nil
}

func validateHarnessUltraUpscaling(graph map[string]any, width, height int) error {
	active := 0
	outputWidth, outputHeight, outputScale := width, height, 1
	for id, raw := range graph {
		n := raw.(map[string]any)
		inputs := n["inputs"].(map[string]any)
		switch n["class_type"] {
		case "ImageScale":
			if (id != "9600" && id != "9501") || inputs["width"] != float64(width) || inputs["height"] != float64(height) {
				return errors.New("image resize is outside the base canvas")
			}
		case "LCImageRotatePad":
			if id != "3870" {
				return errors.New("unexpected outpaint allocator")
			}
			frame := map[string]any{}
			for _, name := range []string{"left", "right", "top", "bottom", "angle", "fit", "aspect", "snap_to"} {
				if value, present := inputs[name]; present {
					if name == "left" || name == "right" || name == "top" || name == "bottom" || name == "angle" {
						if _, ok := value.(float64); !ok {
							return errors.New("outpaint geometry must be literal")
						}
					} else if _, ok := value.(string); !ok {
						return errors.New("outpaint geometry must be literal")
					}
					frame[name] = value
				}
			}
			w, h, _, err := kreaProOutpaintFrame(width, height, kreaProSettings{Nodes: map[string]map[string]any{"3870": frame}})
			if err != nil || w > 4096 || h > 4096 || w*h > qwen21MaxOutputPixels {
				return errors.New("outpaint exceeds the shared GPU memory profile")
			}
			outputWidth, outputHeight = w, h
		case "LCRelayVSRUpscale":
			active++
			if id != "4150" || inputs["source"] != "image" || (inputs["upscale"] != "2x" && inputs["upscale"] != "4x") {
				return errors.New("RelayVSR requires image mode at 2x or 4x")
			}
			image, ok := inputs["image"].([]any)
			if !ok || len(image) != 2 || image[0] != "3621" || image[1] != float64(0) {
				return errors.New("RelayVSR must use the final postprocessed image")
			}
			outputScale = 2
			if inputs["upscale"] == "4x" {
				outputScale = 4
			}
		case "LCVOSRUpscalePipe":
			active++
			if id != "4046" {
				return errors.New("unexpected VOSR allocator")
			}
			value := inputs["upscale_by"]
			if linked, ok := value.([]any); ok {
				if len(linked) != 2 || linked[0] != "4047" || linked[1] != float64(0) {
					return errors.New("VOSR scale must use its size selector")
				}
				selector, ok := graph["4047"].(map[string]any)
				if !ok || selector["class_type"] != "LCComboSelector" {
					return errors.New("missing VOSR size selector")
				}
				value = selector["inputs"].(map[string]any)["value"]
			}
			if value != "2x" && value != "3x" {
				return errors.New("VOSR requires 2x or 3x")
			}
			outputScale = 2
			if value == "3x" {
				outputScale = 3
			}
		}
	}
	if active > 1 {
		return errors.New("upscalers cannot be combined")
	}
	_, _, err := kreaProOutputDimensions(outputWidth, outputHeight, outputScale)
	return err
}

func (a *App) harnessUltraDispatch(w http.ResponseWriter, r *http.Request, id string) {
	var input struct {
		Lease string         `json:"lease"`
		Graph map[string]any `json:"graph"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !harnessUltraID.MatchString(input.Lease) {
		http.Error(w, "invalid dispatch", 400)
		return
	}
	catalog := a.harnessUltraExecutorCatalog(r.Context())
	if !catalog.Online {
		http.Error(w, "executor unavailable", 503)
		return
	}
	if err := validateHarnessUltraGraph(input.Graph, id, catalog); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	user, err := a.store.UserByID(r.Context(), a.cfg.Harness.MiningUserID)
	if err != nil || user.Role != "admin" || user.Disabled || user.AccountExpiresAt.Valid && !user.AccountExpiresAt.Time.After(time.Now()) {
		http.Error(w, "operator unavailable", 503)
		return
	}
	user.QueuePriority = false
	user.PauseMiningForQuickGeneration = a.cfg.Harness.PauseMining
	work, err := a.gpuCoordinator.admit(r.Context(), domain.GPUWorkHarnessUltra, id, &user, input.Lease, time.Now())
	var wait *gpuAdmissionWait
	if errors.As(err, &wait) {
		writeJSON(w, 409, map[string]any{"state": "waiting_gpu", "message": wait.Error()})
		return
	}
	if err != nil {
		http.Error(w, "admission unconfirmed", 503)
		return
	}
	if work.LeaseToken != input.Lease || work.Phase != "reserved" {
		writeJSON(w, 409, map[string]any{"state": "already_dispatched"})
		return
	}
	ctx, stop := a.gpuCoordinator.maintain(r.Context(), work)
	defer stop()
	err = a.store.SetGPUInferencePreparation(ctx, work.ID, work.LeaseToken, false)
	var lease *domain.QuickGenerationMiningLease
	if err == nil && user.PauseMiningForQuickGeneration {
		lease, _, err = a.reserveMiningForGPUWork(ctx, &user, 0, 0, &work, nil)
	}
	if err == nil {
		err = a.store.SetGPUInferencePreparation(ctx, work.ID, work.LeaseToken, true)
	}
	if err != nil {
		a.gpuCoordinator.finishInference(work.Kind, id, promptassistant.ExecutionNotDispatched)
		if lease != nil {
			a.releaseMiningPause(context.Background(), lease.ID)
		}
		http.Error(w, "mining pause unconfirmed", 503)
		return
	}
	work, err = a.store.BeginHarnessUltraDispatch(ctx, work.ID, input.Lease, id)
	if err != nil {
		http.Error(w, "dispatch fenced", 409)
		return
	}
	payload, _ := json.Marshal(map[string]any{"prompt_id": id, "client_id": "harness-ultra-" + id, "prompt": input.Graph, "extra_data": map[string]any{"harness_ultra_job": id}})
	body, status, err := a.ultraComfy(ctx, "POST", "/prompt", nil, bytes.NewReader(payload), "application/json", &work)
	// No retry after the durable dispatch boundary, even on a transport error.
	if err != nil {
		writeJSON(w, 202, map[string]any{"state": "submission_unknown", "id": id})
		return
	}
	if status != 200 {
		var rejection map[string]json.RawMessage
		if status == 400 && json.Unmarshal(body, &rejection) == nil && len(rejection["error"]) > 0 || a.cfg.ComfyGPUGuardRequired && status == 409 && strings.TrimSpace(string(body)) == "GPU admission deferred" {
			_ = a.store.RecordGPUInferenceCompletion(context.Background(), work.Kind, id, "executor_terminal")
		}
		writeJSON(w, 202, map[string]any{"state": "reconciling", "id": id, "executor_status": status, "error": string(body[:min(len(body), 4000)])})
		return
	}
	var reply struct {
		PromptID string `json:"prompt_id"`
	}
	if json.Unmarshal(body, &reply) != nil || reply.PromptID != id {
		writeJSON(w, 202, map[string]any{"state": "submission_unknown", "id": id})
		return
	}
	_ = a.store.AcknowledgeHarnessUltraDispatch(ctx, work.ID, work.LeaseToken, id)
	writeJSON(w, 202, map[string]any{"state": "running", "id": id})
}

func (a *App) reconcileHarnessUltraWork(ctx context.Context, work domain.GPUWork) error {
	settled, err := a.store.HarnessUltraPermitSettled(ctx, work)
	if err != nil {
		return err
	}
	if settled {
		return a.gpuCoordinator.release(ctx, work.Kind, work.JobKey, "executor_terminal")
	}
	if !a.cfg.ComfyGPUGuardRequired && (work.Phase == "dispatching" || work.Phase == "running") {
		settled, err = a.harnessUltraLegacyCompletion(ctx, work)
		if err != nil {
			return err
		}
		if settled {
			return a.gpuCoordinator.release(ctx, work.Kind, work.JobKey, "executor_terminal")
		}
	}
	if work.Phase == "reserved" || work.Phase == "resources_ready" {
		fenced, changed, err := a.store.FenceExpiredGPUInferenceReservation(ctx, work.ID)
		if err != nil {
			return err
		}
		if changed {
			return a.gpuCoordinator.release(ctx, work.Kind, work.JobKey, fenced.InferenceCompletion)
		}
	}
	if work.State == domain.GPUWorkHeld && (work.Phase == "dispatching" || work.Phase == "running") {
		_, err = a.store.HeartbeatGPUWork(ctx, work.ID, work.LeaseToken, managedGPULeaseDuration)
	}
	return err
}

// The installed production ComfyUI has no native permit hooks. Use its terminal
// history (or an acknowledged, cancelled dispatch), empty queue and measured
// bounded idle allocator cache together. Absence/history loss alone is never proof.
func (a *App) harnessUltraLegacyCompletion(ctx context.Context, work domain.GPUWork) (bool, error) {
	entry, known, err := a.ultraHistory(ctx, work.JobKey)
	if err != nil {
		return false, err
	}
	terminal := known && (entry.Status.Completed || entry.Status.Status == "error")
	terminal = terminal || work.InferenceCompletion == "executor_terminal" || work.CancellationRequested && work.Phase == "running"
	if !terminal {
		return false, nil
	}
	queue, err := a.fetchGenerationQueue(ctx)
	if err != nil {
		return false, err
	}
	if queue.Running == nil || queue.Pending == nil || len(queue.Running)+len(queue.Pending) != 0 {
		return false, nil
	}
	if !a.releaseComfyMemoryIfIdle(ctx) {
		return false, nil
	}
	body, status, err := a.ultraComfy(ctx, "GET", "/system_stats", nil, nil, "", nil)
	if err != nil {
		return false, err
	}
	var stats struct {
		Devices []struct {
			Type     string `json:"type"`
			Reserved *int64 `json:"torch_vram_total"`
			Total    int64  `json:"vram_total"`
			Free     int64  `json:"vram_free"`
		} `json:"devices"`
	}
	if status != 200 || json.Unmarshal(body, &stats) != nil || len(stats.Devices) == 0 {
		return false, errors.New("GPU memory observation unavailable")
	}
	for _, device := range stats.Devices {
		if device.Type != "cuda" || device.Reserved == nil || *device.Reserved < 0 || *device.Reserved > a.cfg.Harness.UltraIdleCacheBytes || device.Total <= 0 || device.Free < device.Total*3/4 {
			return false, nil
		}
	}
	queue, err = a.fetchGenerationQueue(ctx)
	return err == nil && queue.Running != nil && queue.Pending != nil && len(queue.Running)+len(queue.Pending) == 0, err
}

type harnessUltraHistory struct {
	Outputs map[string]struct {
		Images []generationOutput `json:"images"`
	} `json:"outputs"`
	Status struct {
		Status    string            `json:"status_str"`
		Completed bool              `json:"completed"`
		Messages  []json.RawMessage `json:"messages"`
	} `json:"status"`
}

func (a *App) ultraHistory(ctx context.Context, id string) (harnessUltraHistory, bool, error) {
	var history map[string]harnessUltraHistory
	body, status, err := a.ultraComfy(ctx, "GET", "/history/"+id, nil, nil, "", nil)
	if err != nil {
		return harnessUltraHistory{}, false, err
	}
	if status != 200 || json.Unmarshal(body, &history) != nil {
		return harnessUltraHistory{}, false, errors.New("history unavailable")
	}
	entry, ok := history[id]
	return entry, ok, nil
}

func (a *App) harnessUltraStatus(w http.ResponseWriter, r *http.Request, id string) {
	work, err := a.store.GPUWorkBySource(r.Context(), domain.GPUWorkHarnessUltra, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, 200, map[string]any{"state": "not_submitted", "settled": true})
		return
	}
	if err != nil {
		http.Error(w, "status unavailable", 503)
		return
	}
	_ = a.reconcileHarnessUltraWork(r.Context(), work)
	work, err = a.store.GPUWorkBySource(r.Context(), domain.GPUWorkHarnessUltra, id)
	if err != nil {
		http.Error(w, "status unavailable", 503)
		return
	}
	settled := work.State == domain.GPUWorkReleased || work.State == domain.GPUWorkCancelled
	state := "waiting_gpu"
	if work.Phase == "dispatching" || work.Phase == "running" {
		state = "running"
	}
	entry, known, historyErr := a.ultraHistory(r.Context(), id)
	outputs := []generationOutput{}
	if known && (entry.Status.Completed || entry.Status.Status == "error") {
		state = "completed"
		if entry.Status.Status != "success" {
			state = "failed"
		}
		for _, output := range entry.Outputs["4071"].Images {
			if validUltraOutput(output, id) {
				output.URL = ""
				outputs = append(outputs, output)
			}
		}
		if state == "completed" && len(outputs) == 0 {
			state = "failed"
		}
	} else if settled {
		state = "failed"
	}
	if work.CancellationRequested {
		state = "cancelling"
		if settled {
			state = "cancelled"
		}
	}
	message := ""
	if known && entry.Status.Status == "error" {
		for _, raw := range entry.Status.Messages {
			var parts []json.RawMessage
			if json.Unmarshal(raw, &parts) != nil || len(parts) != 2 {
				continue
			}
			var kind string
			_ = json.Unmarshal(parts[0], &kind)
			if kind != "execution_error" && kind != "execution_interrupted" {
				continue
			}
			var detail struct {
				NodeType         string `json:"node_type"`
				ExceptionType    string `json:"exception_type"`
				ExceptionMessage string `json:"exception_message"`
			}
			if json.Unmarshal(parts[1], &detail) == nil {
				message = strings.TrimSpace(detail.NodeType + ": " + detail.ExceptionType + ": " + detail.ExceptionMessage)
				message = message[:min(len(message), 1200)]
			}
		}
	}
	if historyErr != nil {
		message = "executor temporarily unavailable"
	}
	if settled && !known && work.ExternalID != "" {
		message = "submission ended without an output; inspect service diagnostics"
	}
	writeJSON(w, 200, map[string]any{"id": id, "state": state, "settled": settled, "outputs": outputs, "message": message, "executor_available": historyErr == nil})
}

func validUltraOutput(output generationOutput, id string) bool {
	return output.Type == "output" && strings.ReplaceAll(output.Subfolder, "\\", "/") == "harness-ultra/"+id && path.Base(output.Filename) == output.Filename && !strings.ContainsAny(output.Filename, "\\\x00") && strings.HasSuffix(strings.ToLower(output.Filename), ".png")
}

func (a *App) harnessUltraOutput(w http.ResponseWriter, r *http.Request, id, rawIndex string) {
	index, err := strconv.Atoi(rawIndex)
	if err != nil || index < 0 || index > 63 {
		http.NotFound(w, r)
		return
	}
	if _, err = a.store.GPUWorkBySource(r.Context(), domain.GPUWorkHarnessUltra, id); err != nil {
		http.NotFound(w, r)
		return
	}
	entry, known, err := a.ultraHistory(r.Context(), id)
	outputs := entry.Outputs["4071"].Images
	if err != nil || !known || !entry.Status.Completed || entry.Status.Status != "success" || index >= len(outputs) || !validUltraOutput(outputs[index], id) {
		http.NotFound(w, r)
		return
	}
	archive, err := a.fetchGenerationOutputArchive(r.Context(), outputs[index])
	if err != nil {
		http.Error(w, "output unavailable", 502)
		return
	}
	defer archive.Close()
	w.Header().Set("Content-Type", archive.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(archive.SizeBytes, 10))
	_, _ = io.Copy(w, archive.File)
}

func (a *App) harnessUltraCancel(w http.ResponseWriter, r *http.Request, id string) {
	work, err := a.store.GPUWorkBySource(r.Context(), domain.GPUWorkHarnessUltra, id)
	if errors.Is(err, sql.ErrNoRows) {
		// A cancelled unknown ID is a tombstone for a delayed dispatch request.
		work, err = a.store.EnqueueGPUWork(r.Context(), domain.GPUWorkRequest{ID: newRequestID(), ResourceID: domain.GPUPrimaryResource, Kind: domain.GPUWorkHarnessUltra, JobKey: id, UserID: a.cfg.Harness.MiningUserID})
	}
	if err != nil {
		http.Error(w, "cancellation unavailable", 503)
		return
	}
	work, err = a.store.RequestGPUWorkCancellation(r.Context(), work.ID)
	if err != nil {
		http.Error(w, "cancellation unavailable", 503)
		return
	}
	if work.Phase == "reserved" || work.Phase == "preparing_resources" || work.Phase == "resources_ready" {
		// CAS dispatch now rejects cancellation; no HTTP prompt can follow.
		a.gpuCoordinator.finishInference(work.Kind, id, promptassistant.ExecutionNotDispatched)
	} else if work.State != domain.GPUWorkReleased && work.State != domain.GPUWorkCancelled {
		payload, _ := json.Marshal(map[string]any{"delete": []string{id}})
		_, _, _ = a.ultraComfy(r.Context(), "POST", "/queue", nil, bytes.NewReader(payload), "application/json", nil)
		payload, _ = json.Marshal(map[string]string{"prompt_id": id})
		_, _, _ = a.ultraComfy(r.Context(), "POST", "/interrupt", nil, bytes.NewReader(payload), "application/json", nil)
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
