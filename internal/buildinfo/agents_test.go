package buildinfo_test

import (
	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/inference"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/miningagent"
	"ai-access-gateway/internal/trainingagent"
	"ai-access-gateway/internal/updateagent"
	"ai-access-gateway/internal/updates"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Nil embedded controllers panic if metadata accidentally invokes an operation.
type noUpdateOperations struct{ updateagent.Controller }
type noMiningOperations struct{ miningagent.Controller }

func TestAgentBuildAuthenticationAndNoOperations(t *testing.T) {
	token := strings.Repeat("t", 32)
	update, err := updateagent.NewServer(token, &noUpdateOperations{})
	if err != nil {
		t.Fatal(err)
	}
	mining, err := miningagent.NewServer(token, &noMiningOperations{})
	if err != nil {
		t.Fatal(err)
	}
	training, err := trainingagent.NewServer(token, &trainingagent.Controller{})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []struct {
		name    string
		handler http.Handler
	}{
		{buildinfo.UpdateAgent, update.Handler()}, {buildinfo.MiningAgent, mining.Handler()}, {buildinfo.TrainingAgent, training.Handler()},
	} {
		t.Run(agent.name, func(t *testing.T) {
			for _, tc := range []struct {
				method, auth string
				status       int
			}{
				{"GET", "", 401}, {"GET", "Bearer wrong", 401}, {"GET", "Bearer " + token, 200}, {"HEAD", "Bearer " + token, 200}, {"POST", "Bearer " + token, 405},
			} {
				r := httptest.NewRequest(tc.method, "/v1/build", nil)
				r.Header.Set("Authorization", tc.auth)
				w := httptest.NewRecorder()
				agent.handler.ServeHTTP(w, r)
				if w.Code != tc.status {
					t.Fatalf("%s: %d %s", tc.method, w.Code, w.Body.String())
				}
				if tc.status == 200 {
					if w.Header().Get("Cache-Control") != "no-store" {
						t.Fatal("cacheable metadata")
					}
					if tc.method == "HEAD" {
						if w.Body.Len() != 0 {
							t.Fatal("HEAD body")
						}
						continue
					}
					var report buildinfo.Report
					if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
						t.Fatal(err)
					}
					if check := buildinfo.Compare(agent.name, report); check.Status != "compatible" {
						t.Fatal(check)
					}
				}
			}
		})
	}
	worker := &inference.Worker{}
	handler := worker.ReadinessHandler(func(context.Context) error { t.Fatal("build read ran readiness storage probe"); return nil })
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/v1/build", nil))
	var report buildinfo.Report
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &report) != nil || buildinfo.Compare(buildinfo.InferenceWorker, report).Status != "compatible" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestBuildContractsMatchVersionedWireProtocols(t *testing.T) {
	version := func(component, name string) int {
		for _, contract := range buildinfo.Contracts(component) {
			if contract.Name == name {
				return contract.Version
			}
		}
		t.Fatalf("missing contract %s/%s", component, name)
		return 0
	}
	if version(buildinfo.UpdateAgent, "comfy-storage") != updates.ComfyStorageProtocol ||
		version(buildinfo.TrainingAgent, "training-storage") != loratraining.StorageProtocolVersion {
		t.Fatal("storage wire protocol differs from build metadata")
	}
	token := strings.Repeat("t", 32)
	server, err := miningagent.NewFencedServer(token, &noMiningOperations{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v2/session", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	var session mining.CommandSession
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &session) != nil || !session.Required ||
		session.Protocol != version(buildinfo.MiningAgent, "mining-fencing") {
		t.Fatal("mining wire protocol differs", w.Code, w.Body.String())
	}
	worker := &inference.Worker{}
	w = httptest.NewRecorder()
	worker.ReadinessHandler(nil).ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	var readiness struct {
		Protocol int `json:"protocol"`
	}
	if w.Code != 503 || json.Unmarshal(w.Body.Bytes(), &readiness) != nil || readiness.Protocol != version(buildinfo.InferenceWorker, "inference-readiness") {
		t.Fatal("inference wire protocol differs", w.Code, w.Body.String())
	}
}
