package trainingagent

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/loratraining"
)

func inspectionFixture(t *testing.T) (*Controller, string) {
	t.Helper()
	record, profile, config := provenanceTestFiles(t)
	record.Spec.Owner = "alice"
	config.ComfyLoraDir = filepath.Join(config.RootDir, "models", "loras")
	config.ComfyDiffusionDir = filepath.Join(config.RootDir, "models", "diffusion_models")
	p, _, err := captureTrainingProvenance(context.Background(), record, profile, config, profile.DiT)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := loratraining.ArtifactRelativeName(record.Spec.Owner, profile.Family, "result.safetensors")
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(config.ComfyLoraDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte("actual adapter bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := trainingFileIdentity(context.Background(), filename)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p.Artifact = &identity
	p.VerifiedAt = &now
	record.Status = loratraining.JobStatus{ID: "agent-id", GatewayJobID: record.Spec.GatewayJobID, State: "completed", ArtifactName: identity.Name, ArtifactBytes: identity.Bytes, Provenance: p}
	record.ArtifactPath = filename
	return &Controller{config: config, jobs: map[string]*jobRecord{"agent-id": record}}, filename
}

func TestArtifactInspectionRechecksInstalledBytesAndOwnerNamespace(t *testing.T) {
	controller, filename := inspectionFixture(t)
	request := loratraining.ArtifactInspectionRequest{Models: []string{"missing.safetensors"}}
	result, err := controller.InspectArtifact(context.Background(), "agent-id", request)
	if err != nil || result.State != "verified" || result.Owner != "alice" || result.Models[0].State != "unavailable" || !strings.HasPrefix(result.RelativeName, "Trained/Krea2/") {
		t.Fatalf("%+v %v", result, err)
	}
	if err := os.WriteFile(filename, []byte("changed adapter data"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = controller.InspectArtifact(context.Background(), "agent-id", request)
	if err != nil || result.State != "artifact_changed" {
		t.Fatalf("%+v %v", result, err)
	}
	if err := os.Remove(filename); err != nil {
		t.Fatal(err)
	}
	result, err = controller.InspectArtifact(context.Background(), "agent-id", request)
	if err != nil || result.State != "artifact_missing" {
		t.Fatalf("%+v %v", result, err)
	}
	controller.jobs["agent-id"].Status.Provenance = nil
	result, err = controller.InspectArtifact(context.Background(), "agent-id", request)
	if err != nil || result.State != "provenance_missing" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestArtifactInspectionIsBoundedReadOnlyAndAuthenticated(t *testing.T) {
	controller, _ := inspectionFixture(t)
	controller.inspectionMu.Lock()
	_, err := controller.InspectArtifact(context.Background(), "agent-id", loratraining.ArtifactInspectionRequest{})
	controller.inspectionMu.Unlock()
	if !errors.Is(err, errInspectionBusy) {
		t.Fatal(err)
	}
	server, err := NewServer(strings.Repeat("t", 32), controller)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, body, auth string
		want               int
	}{
		{"POST", `{"models":[]}`, "", 401}, {"GET", ``, strings.Repeat("t", 32), 405},
		{"POST", `{"models":["../secret.safetensors"]}`, strings.Repeat("t", 32), 400},
		{"POST", `{"models":[],"launch":true}`, strings.Repeat("t", 32), 400},
		{"POST", `{"models":[]} {}`, strings.Repeat("t", 32), 400},
		{"POST", `{"models":[]}`, strings.Repeat("t", 32), 200},
	} {
		r := httptest.NewRequest(tc.method, "/v1/jobs/agent-id/inspect", strings.NewReader(tc.body))
		if tc.auth != "" {
			r.Header.Set("Authorization", "Bearer "+tc.auth)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	if len(controller.jobs) != 1 || controller.jobs["agent-id"].Status.State != "completed" {
		t.Fatal("inspection mutated jobs")
	}
}

func TestArtifactInspectionCannotFollowOutOfRootModelLink(t *testing.T) {
	controller, _ := inspectionFixture(t)
	if err := os.MkdirAll(controller.config.ComfyDiffusionDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.safetensors")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(controller.config.ComfyDiffusionDir, "linked.safetensors")); err != nil {
		t.Skip("symlink unavailable", err)
	}
	result, err := controller.InspectArtifact(context.Background(), "agent-id", loratraining.ArtifactInspectionRequest{Models: []string{"linked.safetensors"}})
	if err != nil || len(result.Models) != 1 || result.Models[0].State != "unavailable" {
		t.Fatalf("%+v %v", result, err)
	}
}
