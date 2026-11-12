package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"ai-access-gateway/internal/database"
)

func TestRuntimeBindingHTTPProof(t *testing.T) {
	db := releaseTestDatabase(t)
	other := releaseTestDatabase(t)
	for _, mode := range []string{"valid", "other_database", "fabricated", "lost_backend", "wrong_pid", "wrong_class", "wrong_build", "wrong_mode", "wrong_schema", "unknown_field", "trailing_json", "cacheable", "wrong_type", "oversized", "forbidden", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			directory, manifest, fixture := smokeFixtureOptions(t)
			var pid atomic.Int64
			var leaked atomic.Bool
			redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
			defer redirect.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/admin/release/binding" || r.URL.RawQuery != "" || r.ContentLength != 0 {
					t.Error("unexpected binding request")
				}
				cookie, err := r.Cookie("gateway_session")
				if err != nil || cookie.Value != "binding-admin-secret" {
					t.Error("missing ordinary admin session")
				}
				backend, _ := strconv.ParseInt(r.Header.Get(RuntimeBindingPIDHeader), 10, 64)
				class, _ := strconv.ParseInt(r.Header.Get(RuntimeBindingClassHeader), 10, 64)
				pid.Store(backend)
				request := database.RuntimeBindingRequest{BackendPID: backend, ClassID: class}
				observer := db
				if mode == "other_database" {
					observer = other
				}
				proof, err := database.ObserveRuntimeBinding(r.Context(), observer, request)
				if mode == "other_database" {
					if err == nil {
						t.Error("other database observed the challenge")
					}
					proof = strings.Repeat("0", 64)
				} else if err != nil {
					t.Error(err)
				}
				status := RuntimeBindingStatus{SchemaVersion: 1, Mode: "normal", Probe: request, Proof: proof, Gateway: manifest.Artifacts[0].Report}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				switch mode {
				case "fabricated":
					status.Proof = strings.Repeat("0", 64)
				case "lost_backend":
					if _, err := db.Exec(`SELECT pg_terminate_backend($1)`, backend); err != nil {
						t.Error(err)
					}
				case "wrong_pid":
					status.Probe.BackendPID++
				case "wrong_class":
					status.Probe.ClassID++
				case "wrong_build":
					status.Gateway.Build.Revision = strings.Repeat("d", 40)
				case "wrong_mode":
					status.Mode = "validation"
				case "wrong_schema":
					status.SchemaVersion = 2
				case "cacheable":
					w.Header().Set("Cache-Control", "private, max-age=60")
				case "wrong_type":
					w.Header().Set("Content-Type", "text/html")
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat("x", 65537)))
					return
				case "forbidden":
					http.Error(w, "secret server error", 403)
					return
				case "redirect":
					http.Redirect(w, r, redirect.URL, 302)
					return
				}
				body, _ := json.Marshal(status)
				if mode == "unknown_field" {
					body = append(body[:len(body)-1], []byte(`,"unexpected":"secret server error"}`)...)
				}
				if mode == "trailing_json" {
					body = append(body, []byte(` {}`)...)
				}
				_, _ = w.Write(body)
			}))
			defer server.Close()
			report, err := ObserveGatewayDatabase(context.Background(), db, directory, RuntimeBindingOptions{Origin: server.URL, ManifestHash: fixture.Operation.CandidateManifestHash, SessionToken: "binding-admin-secret"})
			if mode == "valid" {
				if err != nil || !report.Success || report.Gateway == nil || report.ManifestHash != fixture.Operation.CandidateManifestHash {
					t.Fatalf("binding: %+v %v", report, err)
				}
			} else if err == nil || report.Success || report.FailureCode == "" || report.Gateway != nil {
				t.Fatalf("accepted %s: %+v %v", mode, report, err)
			}
			if report.DeploymentAuthorized || report.Scope != "runtime_database_binding" || report.FinishedAt.Before(report.StartedAt) || leaked.Load() {
				t.Fatal("incorrect scope or redirect leaked a request")
			}
			body, _ := json.Marshal(report)
			for _, secret := range []string{"binding-admin-secret", "secret server error", server.URL, `"proof"`, `"backend_pid"`} {
				if strings.Contains(string(body), secret) || (err != nil && strings.Contains(err.Error(), secret)) {
					t.Fatal("public report exposed request/response secrets")
				}
			}
			var locks int
			if err := db.QueryRow(`SELECT count(*) FROM pg_locks WHERE pid=$1 AND locktype='advisory'`, pid.Load()).Scan(&locks); err != nil || locks != 0 {
				t.Fatalf("leaked challenge locks: %d %v", locks, err)
			}
		})
	}
}

func TestRuntimeBindingRejectsBeforeHTTP(t *testing.T) {
	for _, mode := range []string{"nil_database", "remote", "bad_hash", "missing_session", "bad_session", "large_session", "manifest_missing", "manifest_identity", "package_files"} {
		t.Run(mode, func(t *testing.T) {
			directory, manifest, fixture := smokeFixtureOptions(t)
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer server.Close()
			db, err := sql.Open("postgres", "unused")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			options := RuntimeBindingOptions{Origin: server.URL, ManifestHash: fixture.Operation.CandidateManifestHash, SessionToken: "binding-admin-secret"}
			want := "configuration"
			switch mode {
			case "nil_database":
				db = nil
			case "remote":
				options.Origin = "http://192.0.2.1:8080"
			case "bad_hash":
				options.ManifestHash = "bad"
			case "missing_session":
				options.SessionToken = ""
			case "bad_session":
				options.SessionToken = "bad\nsecret"
			case "large_session":
				options.SessionToken = strings.Repeat("x", 513)
			case "manifest_missing":
				want = "manifest_open"
				if err := os.Remove(filepath.Join(directory, "manifest.json")); err != nil {
					t.Fatal(err)
				}
			case "manifest_identity":
				options.ManifestHash = strings.Repeat("0", 64)
				want = "manifest_identity"
			case "package_files":
				want = "package_files"
				if err := os.WriteFile(filepath.Join(directory, manifest.Artifacts[0].File.Name), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			report, err := ObserveGatewayDatabase(context.Background(), db, directory, options)
			if err == nil || report.Success || report.FailureCode != want || calls.Load() != 0 || report.ManifestHash != "" || report.DeploymentAuthorized {
				t.Fatalf("unsafe precheck: %+v %v requests=%d", report, err, calls.Load())
			}
		})
	}
}
