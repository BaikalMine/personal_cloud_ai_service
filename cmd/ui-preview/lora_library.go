package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

func registerLoraLibraryPreview(mux *http.ServeMux, now time.Time) {
	for _, prefix := range []string{"/api/lora-training/usage/", "/api/lora-training/source/"} {
		mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			id := strings.TrimPrefix(r.URL.Path, prefix)
			if id != "task-training-completed" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "private, no-store")
			if strings.Contains(prefix, "/source/") {
				if r.URL.Query().Get("model") != "krea2:test" {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": "Модель недоступна в демонстрации."})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"source_kind": "lora_training", "source_id": id, "requires_inputs": false, "values": map[string]string{
					"template_id": "text-to-image", "generation_workflow": "photoflow-krea2", "model": "krea2:test", "positive_prompt": "ceramicset",
					"loras_configured": "true", "lora_1": "Trained/Krea2/preview/ceramic-study.safetensors", "lora_model_strength_1": "1", "lora_clip_strength_1": "1",
					"lora_2": "", "lora_model_strength_2": "0", "lora_clip_strength_2": "0", "lora_3": "", "lora_model_strength_3": "0", "lora_clip_strength_3": "0",
					"assistant_enabled": "false", "assistant_job_id": "", "batch_enabled": "false",
				}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"job_id": id, "state": "ready", "message": "Демонстрационные данные: совместимая модель Krea2.", "checked_at": time.Now().UnixMilli(), "expires_at": time.Now().Add(time.Minute).UnixMilli(), "models": []map[string]any{
				{"id": "krea2:test", "name": "Krea2 / Raw INT8 Mixed", "architecture": "krea2", "available": true},
				{"id": "krea2:removed", "name": "Недоступная модель (демо)", "available": false, "reason": "Файл не найден."},
			}})
		})
	}
	mux.HandleFunc("/api/lora-training/passports/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/lora-training/passports/")
		if id != "task-training-running" && id != "task-training-completed" {
			http.NotFound(w, r)
			return
		}
		params := domain.LoraExecutionParameters{Resolution: 768, MaxSteps: 1600, NetworkDim: 32, NetworkAlpha: 32, LearningRate: 0.0001, Seed: 42, SampleCount: 24}
		file := func(name string) domain.LoraFileIdentity {
			return domain.LoraFileIdentity{Name: name, Bytes: 104857600, SHA256: strings.Repeat("a", 64)}
		}
		profile := domain.LoraExecutionProfile{ID: "krea2-raw-official-bf16", Family: "krea2", BaseModel: "krea2-raw.safetensors"}
		execution := &domain.LoraTrainingProvenance{Version: 1, GatewayJobID: id, CapturedAt: now.Add(-time.Hour), Profile: profile,
			ProfileSHA256: profile.Hash(), Parameters: params, DatasetArchive: file("dataset.zip"),
			Tuner: domain.LoraTunerIdentity{SourceSHA256: strings.Repeat("b", 64), SourceScope: "python-and-dependencies-v1"}}
		for _, role := range []string{"base_source", "base_execution", "vae", "text_encoder"} {
			execution.Models = append(execution.Models, domain.LoraModelIdentity{Role: role, LoraFileIdentity: file(role + ".safetensors")})
		}
		state, evidence := "running", "inputs_recorded"
		if id == "task-training-completed" {
			state, evidence = "completed", "artifact_recorded"
			artifact := file("ceramic-study.safetensors")
			execution.Artifact, execution.VerifiedAt = &artifact, &now
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "job_id": id, "state": state, "family": "krea2", "trigger": "ceramicset", "evidence_state": evidence,
			"requested_parameters": map[string]any{"resolution": 768, "max_steps": 1600, "network_dim": 32, "network_alpha": 32, "learning_rate": 0.0001, "seed": "9223372036854775806", "sample_count": 24}, "execution": execution, "dataset_snapshot_available": false})
	})
}
