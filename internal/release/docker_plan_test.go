package release

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ai-access-gateway/internal/database"
)

func TestDockerHostPlanObservationAndRevalidation(t *testing.T) {
	db := releaseTestDatabase(t)
	other := releaseTestDatabase(t)
	directory, manifest, fixture := smokeFixtureOptions(t)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("gateway_session")
		if err != nil || cookie.Value != "docker-binding-session-secret" {
			http.Error(w, "private authentication", 403)
			return
		}
		pid, _ := strconv.ParseInt(r.Header.Get(RuntimeBindingPIDHeader), 10, 64)
		class, _ := strconv.ParseInt(r.Header.Get(RuntimeBindingClassHeader), 10, 64)
		request := database.RuntimeBindingRequest{BackendPID: pid, ClassID: class}
		proof, err := database.ObserveRuntimeBinding(r.Context(), db, request)
		if err != nil {
			http.Error(w, "private proof failure", 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(RuntimeBindingStatus{SchemaVersion: 1, Mode: "normal", Probe: request, Proof: proof, Gateway: manifest.Artifacts[0].Report})
	}))
	defer gateway.Close()
	_, open := dockerFixtureServer(t, directory, manifest, gateway.URL, "valid")
	ref := HostPackageReference{directory, fixture.Operation.CandidateManifestHash}
	options := DockerHostPlanOptions{OperationID: hostTestSpec().ID, Previous: ref, Candidate: ref, Gateway: dockerFixtureOptions(gateway.URL, ref.ManifestSHA256)}
	plan, err := observeDockerHostPlan(context.Background(), db, options, open)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan.data.Observed.Configuration), "DATABASE_URL=must-not-appear") || strings.Contains(string(plan.data.Observed.Configuration), options.Gateway.SessionToken) {
		t.Fatal("configuration not retained or session persisted")
	}
	if plan.data.Observed.Database.DatabaseOID == 0 || plan.data.Observed.Database.SystemIdentifier == "" {
		t.Fatal("DB identity omitted")
	}
	if err := plan.revalidate(context.Background(), db, options.Gateway.SessionToken, open); err != nil {
		t.Fatal(err)
	}
	if err := plan.revalidate(context.Background(), other, options.Gateway.SessionToken, open); err == nil {
		t.Fatal("different DB accepted")
	}
	if err := plan.revalidate(context.Background(), db, "wrong-cookie", open); err == nil {
		t.Fatal("different session accepted")
	}
	for _, mode := range []string{"config_changed", "mount_changed", "restart_changed", "started_changed", "process_changed", "image", "engine"} {
		_, changed := dockerFixtureServer(t, directory, manifest, gateway.URL, mode)
		if err := plan.revalidate(context.Background(), db, options.Gateway.SessionToken, changed); err == nil {
			t.Fatal("changed runtime accepted", mode)
		}
	}
	changed := plan
	changed.data.Observed.Database.DatabaseOID++
	if err := changed.revalidate(context.Background(), db, options.Gateway.SessionToken, open); err == nil {
		t.Fatal("persisted DB identity ignored")
	}
	changed = plan
	changed.data.Observed.Database.PostmasterStartedAt = "different"
	if err := changed.revalidate(context.Background(), db, options.Gateway.SessionToken, open); err == nil {
		t.Fatal("DB lifetime ignored")
	}
	if err := os.WriteFile(filepath.Join(directory, manifest.Artifacts[0].File.Name), []byte("changed executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened := false
	if err := plan.revalidate(context.Background(), db, options.Gateway.SessionToken, func(string) (*dockerObserver, error) { opened = true; return nil, errDockerObservation }); err == nil || opened {
		t.Fatal("package change did not fail before Engine")
	}
}

func TestDockerHostPlanPackageAndScopeBoundary(t *testing.T) {
	directory, _, fixture := smokeFixtureOptions(t)
	ref := HostPackageReference{directory, fixture.Operation.CandidateManifestHash}
	for _, mode := range []string{"operation", "previous_hash", "candidate_hash", "candidate_directory", "previous_directory"} {
		options := DockerHostPlanOptions{OperationID: hostTestSpec().ID, Previous: ref, Candidate: ref, Gateway: dockerFixtureOptions("http://127.0.0.1:18120", ref.ManifestSHA256)}
		switch mode {
		case "operation":
			options.OperationID = "invalid"
		case "previous_hash":
			options.Previous.ManifestSHA256 = strings.Repeat("a", 64)
		case "candidate_hash":
			options.Candidate.ManifestSHA256 = strings.Repeat("a", 64)
		case "candidate_directory":
			options.Candidate.Directory = "relative"
		case "previous_directory":
			options.Previous.Directory += "missing"
		}
		opened := false
		plan, err := observeDockerHostPlan(context.Background(), nil, options, func(string) (*dockerObserver, error) { opened = true; return nil, errDockerObservation })
		if err == nil || opened || plan.Operation().ID != "" {
			t.Fatal("invalid plan got past package gate", mode)
		}
	}
	plan := hostPlanFixture(t.TempDir())
	plan.data.Scope = "deployment"
	if plan.Operation().ID != "" {
		t.Fatal("scope silently promoted to deployment")
	}
}
