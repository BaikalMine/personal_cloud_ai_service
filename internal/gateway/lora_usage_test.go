package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

func loraUsageFixture(t *testing.T) domain.LoraTrainingJob {
	t.Helper()
	job := domain.LoraTrainingJob{PublicID: "usage-job-00001", AgentJobID: "agent-job-00001", UsernameSnapshot: "owner", Family: "krea2", ProfileID: "profile", BaseModel: "base.safetensors", State: domain.LoraTrainingCompleted, ArtifactName: "adapter.safetensors", ArtifactBytes: 100, TriggerWord: "exacttrigger"}
	verified := time.Now().UTC()
	p := &domain.LoraTrainingProvenance{Version: 1, GatewayJobID: job.PublicID, CapturedAt: verified.Add(-time.Minute), VerifiedAt: &verified,
		Profile:        domain.LoraExecutionProfile{ID: job.ProfileID, Family: job.Family, BaseModel: job.BaseModel},
		Parameters:     domain.LoraExecutionParameters{Resolution: 512, MaxSteps: 100, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.001, Seed: 1, SampleCount: 5},
		Tuner:          domain.LoraTunerIdentity{SourceScope: "python-and-dependencies-v1", SourceSHA256: strings.Repeat("a", 64)},
		DatasetArchive: domain.LoraFileIdentity{Name: "dataset.zip", Bytes: 100, SHA256: strings.Repeat("b", 64)},
		Artifact:       &domain.LoraFileIdentity{Name: job.ArtifactName, Bytes: job.ArtifactBytes, SHA256: strings.Repeat("d", 64)}}
	p.ProfileSHA256 = p.Profile.Hash()
	for _, role := range []string{"base_source", "base_execution", "vae", "text_encoder"} {
		p.Models = append(p.Models, domain.LoraModelIdentity{Role: role, LoraFileIdentity: domain.LoraFileIdentity{Name: role + ".safetensors", Bytes: 10, SHA256: strings.Repeat("c", 64)}})
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	job.Provenance = p
	return job
}

func TestLoraUsageLiveOptions(t *testing.T) {
	job := loraUsageFixture(t)
	reference, _ := loratraining.ArtifactRelativeName(job.UsernameSnapshot, job.Family, job.ArtifactName)
	var info map[string]comfyNodeInfo
	if err := json.Unmarshal([]byte(`{
"UNETLoader":{"input":{"required":{"unet_name":[["Krea2/base.safetensors","Krea2/wrong.safetensors"]]}}},
"CLIPLoader":{"input":{"required":{"clip_name":[["qwen3vl_4b_fp8_scaled.safetensors"]]}}},
"VAELoader":{"input":{"required":{"vae_name":[["qwen_image_vae.safetensors"]]}}},
"LoraLoader":{"input":{"required":{"lora_name":[["lenovo_krea2.safetensors"]]}}}
}`), &info); err != nil {
		t.Fatal(err)
	}
	loraInfo := info["LoraLoader"]
	loraInfo.Input.Required["lora_name"], _ = json.Marshal([]any{[]string{"lenovo_krea2.safetensors", reference}})
	info["LoraLoader"] = loraInfo
	offline := false
	comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/object_info" {
			t.Errorf("unexpected Comfy call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		if offline {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(info)
	}))
	defer comfy.Close()
	inspected := 0
	state := "verified"
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/jobs/"+job.AgentJobID+"/inspect" {
			t.Errorf("unexpected agent call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		inspected++
		var request loratraining.ArtifactInspectionRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || !request.Valid() {
			t.Error("invalid inspection request")
		}
		result := loratraining.ArtifactInspection{Version: 1, AgentJobID: job.AgentJobID, GatewayJobID: job.PublicID, Owner: job.UsernameSnapshot, Family: job.Family, ProfileSHA256: job.Provenance.ProfileSHA256, Artifact: job.Provenance.Artifact, RelativeName: reference, CheckedAt: time.Now(), State: state}
		for _, name := range request.Models {
			item := loratraining.ModelInspection{Name: name, State: "compatible", Architecture: "krea2"}
			if strings.Contains(name, "wrong") {
				item.State = "incompatible"
				item.Architecture = "flux2-klein-4b"
			}
			result.Models = append(result.Models, item)
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer agent.Close()
	upstream, _ := url.Parse(comfy.URL)
	agentURL, _ := url.Parse(agent.URL)
	app := &App{cfg: Config{ComfyUIUpstream: upstream}, loraTraining: loratraining.NewClient(agentURL, strings.Repeat("test", 8))}
	view, _, err := app.loraUseOptions(context.Background(), &User{ID: 1, Role: "admin"}, job)
	if err != nil || view.State != "ready" || inspected != 1 || len(view.Models) != 2 {
		t.Fatalf("live options: %+v %v, inspected=%d", view, err, inspected)
	}
	for _, model := range view.Models {
		if model.Available {
			if !strings.Contains(model.UseURL, "lora_job=") || model.Architecture != "krea2" {
				t.Fatal(model)
			}
		} else if model.UseURL != "" {
			t.Fatal("incompatible model has a use link")
		}
	}
	state = "artifact_changed"
	view, _, err = app.loraUseOptions(context.Background(), &User{ID: 1, Role: "admin"}, job)
	if err != nil || view.State != "artifact_unconfirmed" {
		t.Fatal("changed adapter accepted", view, err)
	}
	offline = true
	view, _, err = app.loraUseOptions(context.Background(), &User{ID: 1, Role: "admin"}, job)
	if err != nil || view.State != "comfy_offline" || inspected != 2 {
		t.Fatal("stale catalog accepted", view, err, inspected)
	}
}

func TestLoraUsageExactReferenceAndIdentity(t *testing.T) {
	job := domain.LoraTrainingJob{PublicID: "gateway-job", AgentJobID: "agent-job", UsernameSnapshot: "owner", Family: "krea2", ArtifactName: "result.safetensors", ArtifactBytes: 100}
	expected, err := loratraining.ArtifactRelativeName(job.UsernameSnapshot, job.Family, job.ArtifactName)
	if err != nil {
		t.Fatal(err)
	}
	catalog := generationModelCatalog{LoraGroups: []generationLoraGroup{{Loras: []generationLora{{Name: "result.safetensors"}, {Name: "Other/" + job.ArtifactName}}}}}
	if _, ok := loraCatalogReference(catalog, job); ok {
		t.Fatal("basename matched an unrelated adapter")
	}
	catalog.LoraGroups[0].Loras = append(catalog.LoraGroups[0].Loras, generationLora{Name: strings.ReplaceAll(expected, "/", `\`)})
	reference, ok := loraCatalogReference(catalog, job)
	if !ok || reference != strings.ReplaceAll(expected, "/", `\`) {
		t.Fatal("exact normalized namespace did not match", reference)
	}
	catalog.LoraGroups[0].Loras = append(catalog.LoraGroups[0].Loras, generationLora{Name: expected})
	if _, ok := loraCatalogReference(catalog, job); ok {
		t.Fatal("ambiguous adapter accepted")
	}
	artifact := domain.LoraFileIdentity{Name: job.ArtifactName, Bytes: 100, SHA256: strings.Repeat("a", 64)}
	job.Provenance = &domain.LoraTrainingProvenance{GatewayJobID: job.PublicID, Profile: domain.LoraExecutionProfile{Family: job.Family}, ProfileSHA256: strings.Repeat("b", 64), Artifact: &artifact}
	inspection := loratraining.ArtifactInspection{Version: 1, State: "verified", AgentJobID: job.AgentJobID, GatewayJobID: job.PublicID, Owner: job.UsernameSnapshot, Family: job.Family, ProfileSHA256: job.Provenance.ProfileSHA256, RelativeName: expected, Artifact: &artifact}
	if !loraInspectionMatches(job, inspection) {
		t.Fatal("exact evidence rejected")
	}
	for name, change := range map[string]func(*loratraining.ArtifactInspection){
		"owner":   func(i *loratraining.ArtifactInspection) { i.Owner = "other" },
		"job":     func(i *loratraining.ArtifactInspection) { i.AgentJobID = "other" },
		"gateway": func(i *loratraining.ArtifactInspection) { i.GatewayJobID = "other" },
		"profile": func(i *loratraining.ArtifactInspection) { i.ProfileSHA256 = strings.Repeat("c", 64) },
		"hash": func(i *loratraining.ArtifactInspection) {
			a := *i.Artifact
			a.SHA256 = strings.Repeat("c", 64)
			i.Artifact = &a
		},
		"missing":   func(i *loratraining.ArtifactInspection) { i.State = "artifact_missing" },
		"reference": func(i *loratraining.ArtifactInspection) { i.RelativeName = "result.safetensors" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := inspection
			change(&copy)
			if loraInspectionMatches(job, copy) {
				t.Fatal("mismatched evidence accepted")
			}
		})
	}
}

func TestLoraUsageAccessAndCleanSource(t *testing.T) {
	job := domain.LoraTrainingJob{PublicID: "training-id", Family: "krea2", State: domain.LoraTrainingCompleted, TriggerWord: "exacttrigger"}
	for _, tc := range []struct {
		user *User
		want string
	}{
		{nil, "generation_denied"},
		{&User{CanUseQuickGeneration: true}, "lora_access_denied"},
		{&User{CanUseQuickGeneration: true, CanUseAdvancedGenerationSettings: true}, "generation_denied"},
		{&User{Role: "admin"}, "provenance_missing"},
	} {
		if state, _ := loraUseAccess(tc.user, job); state != tc.want {
			t.Fatalf("state %q, want %q", state, tc.want)
		}
	}
	values := loraGenerationSourceValues(job, loraUseView{Reference: "Trained/Exact/result.safetensors", Preset: generationPreset{ID: "recipe", TemplateID: "text-to-image"}}, generationModel{ID: "model", DefaultSteps: 32, DefaultCFG: 3, DefaultSampler: "euler", DefaultScheduler: "normal"})
	if values["positive_prompt"] != "exacttrigger" || values["lora_1"] != "Trained/Exact/result.safetensors" || values["parent_job_id"] != "" || values["batch_enabled"] != "false" || values["assistant_enabled"] != "false" {
		t.Fatal(values)
	}
	for key, value := range values {
		if strings.HasPrefix(key, "lora_") && !strings.HasSuffix(key, "_1") && value != "" && value != "0" {
			t.Fatal("residual adapter", key, value)
		}
	}
	data, _ := json.Marshal(values)
	if strings.Contains(string(data), job.PublicID) {
		t.Fatal("training ID became generation lineage")
	}
}

func TestLoraUsagePublicBoundary(t *testing.T) {
	app := &App{}
	for _, prefix := range []string{"/api/lora-training/usage/", "/api/lora-training/source/"} {
		for _, tc := range []struct {
			method, path string
			user         *User
			want         int
		}{
			{"GET", prefix + "fixture", nil, 401}, {"POST", prefix + "fixture", nil, 405},
			{"GET", prefix + "bad/id", &User{ID: 1}, 400},
			{"GET", prefix + "fixture?model=a&model=b", &User{ID: 1}, 400},
		} {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.user != nil {
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey, tc.user))
			}
			w := httptest.NewRecorder()
			app.handleLoraUsage(w, r)
			if w.Code != tc.want || w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
			}
		}
	}
}
