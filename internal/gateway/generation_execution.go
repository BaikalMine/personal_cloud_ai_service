package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/domain"
)

const maxGenerationExecutionBytes = 1 << 20

type generationExecutionWorkflow struct {
	ID               string          `json:"id"`
	Version          string          `json:"version,omitempty"`
	DefinitionID     string          `json:"definition_id"`
	Manifest         json.RawMessage `json:"manifest,omitempty"`
	ManifestSHA256   string          `json:"manifest_sha256,omitempty"`
	Definition       json.RawMessage `json:"definition"`
	DefinitionSHA256 string          `json:"definition_sha256"`
}

type generationExecutionComponent struct {
	NodeID    string `json:"node_id"`
	Class     string `json:"class"`
	Field     string `json:"field"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	SHA256    string `json:"sha256,omitempty"`
	HashState string `json:"hash_state"`
}

type generationExecutionAsset struct {
	Kind       string `json:"kind"`
	Number     int    `json:"number"`
	Role       string `json:"role"`
	InputName  string `json:"input_name"`
	Source     string `json:"source"`
	SourceID   string `json:"source_id,omitempty"`
	SourceName string `json:"source_name,omitempty"`
}

// This describes the submitted graph, not a claim that each referenced node
// executed or that catalog names prove the bytes loaded by ComfyUI.
type generationExecutionSnapshot struct {
	Version          int                            `json:"version"`
	JobID            string                         `json:"job_id"`
	ParentJobID      string                         `json:"parent_job_id,omitempty"`
	CapturedAt       time.Time                      `json:"captured_at"`
	TemplateID       string                         `json:"template_id"`
	ModelFamily      string                         `json:"model_family,omitempty"`
	Seed             string                         `json:"seed,omitempty"`
	Workflow         generationExecutionWorkflow    `json:"workflow"`
	ResolvedInput    json.RawMessage                `json:"resolved_input,omitempty"`
	Graph            json.RawMessage                `json:"graph"`
	GraphSHA256      string                         `json:"graph_sha256"`
	CatalogSHA256    string                         `json:"catalog_sha256,omitempty"`
	CatalogFetchedAt time.Time                      `json:"catalog_fetched_at"`
	CatalogSource    comfyObjectInfoSource          `json:"catalog_source"`
	Components       []generationExecutionComponent `json:"components"`
	Assets           []generationExecutionAsset     `json:"assets"`
	FileObservations *componentinspection.Response  `json:"file_observations,omitempty"`
	Repeat           *generationRepeatConstraint    `json:"repeat_confirmation,omitempty"`
	Postprocess      *postprocessExecutionEvidence  `json:"postprocess,omitempty"`
	EvidenceGaps     []string                       `json:"evidence_gaps"`
}

func generationEvidenceDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func generationExecutionComponents(prompt map[string]any) []generationExecutionComponent {
	components := []generationExecutionComponent{}
	fields := map[string]string{
		"ckpt_name": "checkpoint", "unet_name": "diffusion_model", "model_name": "model",
		"clip_name": "text_encoder", "clip_name1": "text_encoder", "clip_name2": "text_encoder",
		"vae_name": "vae", "lora_name": "lora", "lora": "lora", "lut_name": "lut",
	}
	for id, raw := range prompt {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		class, _ := node["class_type"].(string)
		inputs, _ := node["inputs"].(map[string]any)
		add := func(field, kind, name string) {
			if strings.TrimSpace(name) == "" || strings.EqualFold(name, "none") {
				return
			}
			components = append(components, generationExecutionComponent{NodeID: id, Class: class, Field: field, Kind: kind, Name: name, HashState: "not_observed"})
		}
		for field, kind := range fields {
			if name, ok := inputs[field].(string); ok {
				if class == "RIFE VFI" && field == "ckpt_name" {
					kind = "interpolation_model"
				}
				if class == "UpscaleModelLoader" && field == "model_name" {
					kind = "upscale_model"
				}
				add(field, kind, name)
			}
		}
		if class == "CR LoRA Stack" {
			for field, raw := range inputs {
				if !strings.HasPrefix(field, "lora_name_") {
					continue
				}
				slot := strings.TrimPrefix(field, "lora_name_")
				if inputs["switch_"+slot] != "On" {
					continue
				}
				if name, ok := raw.(string); ok {
					add(field, "lora", name)
				}
			}
		}
		if class == "Power Lora Loader (rgthree)" {
			for field, raw := range inputs {
				if !strings.HasPrefix(field, "lora_") {
					continue
				}
				entry, ok := raw.(map[string]any)
				if !ok || entry["on"] != true {
					continue
				}
				if name, ok := entry["lora"].(string); ok {
					add(field+".lora", "lora", name)
				}
			}
		}
	}
	sort.Slice(components, func(i, j int) bool {
		if components[i].NodeID != components[j].NodeID {
			return components[i].NodeID < components[j].NodeID
		}
		return components[i].Field < components[j].Field
	})
	return components
}

func generationExecutionAssets(input generationForm) []generationExecutionAsset {
	assets := []generationExecutionAsset{}
	paths := [maxGenerationReferenceSlots]string{input.InputImage, input.ReferenceImages[0], input.ReferenceImages[1], input.ReferenceImages[2]}
	if input.isAIHub() {
		copy(paths[4:], input.AIHubExtraImages[:])
	} else if input.PresetID == "qwen-image21-edit" {
		copy(paths[4:], input.QwenExtraImages[:])
	}
	for _, reference := range input.references() {
		assets = append(assets, generationExecutionAsset{Kind: "image", Number: reference.Number, Role: reference.Role,
			InputName: paths[reference.Number-1], Source: reference.Source, SourceID: reference.SourceID, SourceName: reference.SourceName})
	}
	if input.InputAudio != "" {
		assets = append(assets, generationExecutionAsset{Kind: "audio", Number: 1, Role: "audio_reference", InputName: input.InputAudio, Source: "upload"})
	}
	if input.InputVideo != "" {
		assets = append(assets, generationExecutionAsset{Kind: "video", Number: 1, Role: "video_reference", InputName: input.InputVideo, Source: "upload"})
	}
	return assets
}

func (a *App) captureGenerationExecution(ctx context.Context, job domain.GenerationJob, preparation generationPreparation) (domain.GenerationExecutionRecord, error) {
	if preparation.Postprocess != nil {
		postprocess := *preparation.Postprocess
		postprocess.Prompt = preparation.Prompt
		return a.capturePostprocessExecution(job, postprocess)
	}
	if a.contentCipher == nil || job.ID <= 0 || job.PublicID == "" || preparation.Input.Seed < 0 || len(preparation.Prompt) == 0 {
		return domain.GenerationExecutionRecord{}, errors.New("generation execution is incomplete")
	}
	graph, err := json.Marshal(preparation.Prompt)
	if err != nil {
		return domain.GenerationExecutionRecord{}, err
	}
	definition, err := json.Marshal(preparation.Definition)
	if err != nil {
		return domain.GenerationExecutionRecord{}, err
	}
	input := preparation.Input
	// Assistant drafts are not execution parameters and have their own audit.
	input.AssistantOriginal, input.AssistantSuggestion = "", ""
	resolved, err := json.Marshal(input)
	if err != nil {
		return domain.GenerationExecutionRecord{}, err
	}
	snapshot := generationExecutionSnapshot{
		Version: 1, JobID: job.PublicID, CapturedAt: time.Now().UTC(), TemplateID: input.TemplateID,
		ModelFamily: input.ModelFamily, Seed: strconv.FormatInt(input.Seed, 10), ResolvedInput: resolved,
		Workflow: generationExecutionWorkflow{ID: input.PresetID, DefinitionID: preparation.Definition.ID,
			Definition: definition, DefinitionSHA256: generationEvidenceDigest(definition)},
		Graph: graph, GraphSHA256: generationEvidenceDigest(graph), CatalogSHA256: preparation.ObjectInfo.Fingerprint,
		CatalogFetchedAt: preparation.ObjectInfo.FetchedAt, CatalogSource: preparation.ObjectInfo.Source,
		Components: generationExecutionComponents(preparation.Prompt), Assets: generationExecutionAssets(input),
		EvidenceGaps: []string{"model_file_hashes_not_observed", "runtime_loaded_bytes_not_attested", "asset_bytes_not_recorded", "output_measurements_not_recorded"},
	}
	if preparation.FileObservations != nil {
		request, err := generationComponentRequest(job.PublicID, preparation.Prompt)
		if err != nil || !preparation.FileObservations.ValidFor(request) || preparation.FileObservations.State != "complete" {
			return domain.GenerationExecutionRecord{}, errors.New("invalid file observations for generation")
		}
		observed := map[string]componentinspection.Observation{}
		for _, item := range preparation.FileObservations.Items {
			if item.State != "verified" {
				return domain.GenerationExecutionRecord{}, errors.New("generation component was not verified")
			}
			observed[item.Reference.Key()] = item
		}
		for index, component := range snapshot.Components {
			name, _ := componentinspection.NormalizeName(component.Name)
			item := observed[(componentinspection.Reference{Kind: component.Kind, Name: name}).Key()]
			snapshot.Components[index].SHA256 = item.SHA256
			snapshot.Components[index].HashState = "verified"
		}
		snapshot.FileObservations = preparation.FileObservations
		snapshot.EvidenceGaps = snapshot.EvidenceGaps[1:]
	}
	if job.ParentJobID != nil {
		parent, err := a.store.GenerationJobByID(ctx, *job.ParentJobID)
		if err != nil {
			return domain.GenerationExecutionRecord{}, err
		}
		if parent.UserID == nil || job.UserID == nil || *parent.UserID != *job.UserID {
			return domain.GenerationExecutionRecord{}, errors.New("generation parent owner mismatch")
		}
		snapshot.ParentJobID = parent.PublicID
	}
	if preparation.Repeat != nil {
		if snapshot.ParentJobID != "" && snapshot.ParentJobID != preparation.Repeat.SourceID {
			return domain.GenerationExecutionRecord{}, errors.New("generation repeat source mismatch")
		}
		snapshot.ParentJobID = preparation.Repeat.SourceID
		snapshot.Repeat = preparation.Repeat
	}
	if manifest, ok := workflowManifestForInput(input); ok && manifest.Family == input.ModelFamily {
		encoded, err := json.Marshal(manifest)
		if err != nil {
			return domain.GenerationExecutionRecord{}, err
		}
		snapshot.Workflow.Version = manifest.Version
		snapshot.Workflow.Manifest = encoded
		snapshot.Workflow.ManifestSHA256 = generationEvidenceDigest(encoded)
	} else {
		snapshot.EvidenceGaps = append(snapshot.EvidenceGaps, "workflow_manifest_not_available")
	}
	plain, err := json.Marshal(snapshot)
	if err != nil {
		return domain.GenerationExecutionRecord{}, err
	}
	if len(plain) > maxGenerationExecutionBytes {
		return domain.GenerationExecutionRecord{}, errors.New("generation execution evidence is too large")
	}
	cipher, err := a.contentCipher.Encrypt(string(plain))
	if err != nil {
		return domain.GenerationExecutionRecord{}, err
	}
	return domain.GenerationExecutionRecord{JobID: job.ID, PayloadCipher: cipher, PayloadSHA256: generationEvidenceDigest(plain)}, nil
}

func (a *App) decodeGenerationExecution(job domain.GenerationJob, record domain.GenerationExecutionRecord) (generationExecutionSnapshot, error) {
	var snapshot generationExecutionSnapshot
	if a.contentCipher == nil || record.JobID != job.ID {
		return snapshot, errors.New("generation execution identity mismatch")
	}
	plain, err := a.contentCipher.Decrypt(record.PayloadCipher)
	if err != nil {
		return snapshot, err
	}
	if len(plain) > maxGenerationExecutionBytes || generationEvidenceDigest([]byte(plain)) != record.PayloadSHA256 {
		return snapshot, errors.New("generation execution digest mismatch")
	}
	if err := json.Unmarshal([]byte(plain), &snapshot); err != nil {
		return snapshot, err
	}
	if (snapshot.Version != 1 && snapshot.Version != 2) || snapshot.JobID != job.PublicID || len(snapshot.Graph) == 0 ||
		generationEvidenceDigest(snapshot.Graph) != snapshot.GraphSHA256 || generationEvidenceDigest(snapshot.Workflow.Definition) != snapshot.Workflow.DefinitionSHA256 ||
		(len(snapshot.Workflow.Manifest) > 0 && generationEvidenceDigest(snapshot.Workflow.Manifest) != snapshot.Workflow.ManifestSHA256) {
		return generationExecutionSnapshot{}, errors.New("generation execution schema mismatch")
	}
	if isPostprocessJob(job) {
		if err := validatePostprocessExecution(job, snapshot); err != nil {
			return generationExecutionSnapshot{}, err
		}
	} else if snapshot.Version != 1 || snapshot.Postprocess != nil {
		return generationExecutionSnapshot{}, errors.New("generation execution purpose mismatch")
	}
	return snapshot, nil
}

func (a *App) generationProjectionParameters(ctx context.Context, job domain.GenerationJob, form url.Values) (workflowDefinition, generationForm, error) {
	if job.UserID == nil {
		return workflowDefinition{}, generationForm{}, errors.New("generation owner is unavailable")
	}
	record, err := a.store.GenerationExecutionForJob(ctx, *job.UserID, job.PublicID)
	if err == nil {
		snapshot, err := a.decodeGenerationExecution(job, record)
		if err != nil {
			return workflowDefinition{}, generationForm{}, err
		}
		var definition workflowDefinition
		var input generationForm
		if err = json.Unmarshal(snapshot.Workflow.Definition, &definition); err != nil {
			return definition, input, err
		}
		err = json.Unmarshal(snapshot.ResolvedInput, &input)
		return definition, input, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return workflowDefinition{}, generationForm{}, err
	}
	// Legacy jobs have no execution record. Keep their existing projection path,
	// but do not persist its reconstruction as historical execution evidence.
	input, err := parseGenerationValues(ctx, form)
	if err != nil {
		return workflowDefinition{}, input, err
	}
	definition, family, err := generationJobDefinition(job)
	input.TemplateID, input.PresetID, input.ModelName = job.TemplateID, job.WorkflowID, job.ModelName
	input.ModelFamily, input.Seed = family, job.Seed
	return definition, input, err
}

func (a *App) handleGenerationPassport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	if user == nil || user.ID <= 0 || user.Disabled || user.AccountExpiresAt.Valid && !user.AccountExpiresAt.Time.After(time.Now()) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
		return
	}
	if !user.CanUseQuickGenerationType("text-to-image") && !user.CanUseQuickGenerationType("image-to-image") && !user.CanUseQuickGenerationType("minimax-h3-video") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "generation_access_denied"})
		return
	}
	id := r.URL.Query().Get("job")
	if !validGenerationRequestID(id) || len(r.URL.Query()) != 1 || len(r.URL.Query()["job"]) != 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_job_id"})
		return
	}
	job, err := a.store.GenerationJobByPublicID(r.Context(), user.ID, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "passport_not_found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "passport_unavailable"})
		return
	}
	if !user.CanUseQuickGenerationType(job.TemplateID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "generation_access_denied"})
		return
	}
	view := map[string]any{"version": 1, "job_id": job.PublicID, "state": job.State, "seed": strconv.FormatInt(job.Seed, 10), "evidence_state": "not_recorded"}
	if isPostprocessJob(job) {
		delete(view, "seed")
		view["operation"] = "postprocess"
	}
	if job.ParentJobID != nil {
		parent, parentErr := a.store.GenerationJobByID(r.Context(), *job.ParentJobID)
		if parentErr != nil && !errors.Is(parentErr, sql.ErrNoRows) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "passport_unavailable"})
			return
		}
		if parentErr == nil && parent.UserID != nil && *parent.UserID == user.ID {
			view["parent_job_id"] = parent.PublicID
			view["parent_available"] = true
		}
	}
	record, err := a.store.GenerationExecutionForJob(r.Context(), user.ID, id)
	if errors.Is(err, sql.ErrNoRows) {
		view["evidence_gaps"] = []string{"submission_snapshot_not_recorded"}
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "passport_unavailable"})
		return
	} else {
		snapshot, err := a.decodeGenerationExecution(job, record)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "passport_unavailable"})
			return
		}
		view["execution"], view["evidence_state"], view["recorded_at"] = snapshot, "submission_recorded", record.RecordedAt
		if snapshot.Postprocess != nil {
			postprocess := postprocessViewFromQuote(snapshot.Postprocess.Quote)
			if err := a.resolvePostprocessViewSources(r.Context(), user.ID, &postprocess); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "passport_unavailable"})
				return
			}
			view["postprocess"] = postprocess
		}
		view["submission_confirmed"] = job.PromptID != ""
		view["submission_rejected"] = job.SubmissionRejectedAt != nil
		if snapshot.ParentJobID != "" {
			view["parent_available"] = view["parent_job_id"] == snapshot.ParentJobID
			view["parent_job_id"] = snapshot.ParentJobID
		}
	}
	outputs, truncated, err := a.generationOutputViews(r.Context(), user.ID, job.PublicID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "passport_unavailable"})
		return
	}
	view["outputs"], view["outputs_truncated"] = outputs, truncated
	writeJSON(w, http.StatusOK, view)
}
