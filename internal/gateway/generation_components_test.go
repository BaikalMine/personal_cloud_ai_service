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

	"ai-access-gateway/internal/componentinspection"
	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

func componentTestPreparation() generationPreparation {
	return generationPreparation{Input: generationForm{Seed: 42}, Prompt: map[string]any{
		"1": map[string]any{"class_type": "LoraLoader", "inputs": map[string]any{"lora_name": `folder\adapter.safetensors`}},
		"2": map[string]any{"class_type": "LoraLoader", "inputs": map[string]any{"lora_name": `folder/adapter.safetensors`}},
	}}
}

func TestGenerationComponentPreparationAndCapture(t *testing.T) {
	job := domain.GenerationJob{ID: 1, PublicID: "generation-request-001"}
	request, err := generationComponentRequest(job.PublicID, componentTestPreparation().Prompt)
	if err != nil || len(request.References) != 1 || request.References[0].Name != "folder/adapter.safetensors" {
		t.Fatalf("deduplication: %+v %v", request, err)
	}
	for _, state := range []string{"pending", "verified", "missing", "unconfigured", "ambiguous", "unavailable", "unstable", "too_large", "old-agent", "malformed"} {
		t.Run(state, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if state == "old-agent" {
					http.NotFound(w, r)
					return
				}
				if state == "malformed" {
					_, _ = w.Write([]byte("{"))
					return
				}
				now := time.Now().UTC()
				item := componentinspection.Observation{Reference: request.References[0], State: state, CheckedAt: now}
				if state == "verified" {
					item.SHA256 = strings.Repeat("a", 64)
					item.HashedAt = now
					item.Bytes = 4
				}
				result := componentinspection.Response{Version: 1, RequestID: job.PublicID, RequestSHA256: request.Digest(), Source: componentinspection.Source, State: "complete", Items: []componentinspection.Observation{item}}
				if state == "pending" {
					result.State = "pending"
				}
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			cipher, _ := contentcrypto.NewCipher("component-evidence-secret-at-least-32-characters")
			a := &App{cfg: Config{GenerationComponentInspectionEnabled: true}, loraTraining: loratraining.NewClient(base, strings.Repeat("t", 32)), contentCipher: cipher}
			prepared := componentTestPreparation()
			waiting, err := a.inspectGenerationPreparation(context.Background(), job, &prepared)
			wantWait := state == "pending" || state == "unavailable" || state == "unstable" || state == "old-agent" || state == "malformed"
			wantErr := state == "missing" || state == "unconfigured" || state == "ambiguous" || state == "too_large"
			if waiting != wantWait || (err != nil) != wantErr || (prepared.FileObservations != nil) != (state == "verified") {
				t.Fatalf("wait=%v err=%v observations=%v", waiting, err, prepared.FileObservations)
			}
			if state != "verified" {
				return
			}
			record, err := a.captureGenerationExecution(context.Background(), job, prepared)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := a.decodeGenerationExecution(job, record)
			if err != nil || len(snapshot.Components) != 2 || snapshot.FileObservations == nil {
				t.Fatalf("capture: %+v %v", snapshot, err)
			}
			for _, c := range snapshot.Components {
				if c.HashState != "verified" || c.SHA256 != strings.Repeat("a", 64) {
					t.Fatal("hash not attached", c)
				}
			}
			if strings.Contains(strings.Join(snapshot.EvidenceGaps, ","), "model_file_hashes_not_observed") || !strings.Contains(strings.Join(snapshot.EvidenceGaps, ","), "runtime_loaded_bytes_not_attested") {
				t.Fatal("evidence scope changed", snapshot.EvidenceGaps)
			}
			prepared.Prompt["1"].(map[string]any)["inputs"].(map[string]any)["lora_name"] = "other.safetensors"
			if _, err = a.captureGenerationExecution(context.Background(), job, prepared); err == nil {
				t.Fatal("changed graph accepted old observations")
			}
		})
	}
	prepared := componentTestPreparation()
	a := &App{}
	if waiting, err := a.inspectGenerationPreparation(context.Background(), job, &prepared); waiting || err != nil || prepared.FileObservations != nil {
		t.Fatal("disabled flag changed preparation")
	}
	a.cfg.GenerationComponentInspectionEnabled = true
	if waiting, err := a.inspectGenerationPreparation(context.Background(), job, &prepared); !waiting || err != nil {
		t.Fatal("unconfigured agent allowed send")
	}
}
