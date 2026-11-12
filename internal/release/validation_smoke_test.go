package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"ai-access-gateway/internal/database"
)

func writeSmokeManifest(t *testing.T, directory string, manifest Manifest) string {
	t.Helper()
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

func smokeFixtureOptions(t *testing.T) (string, Manifest, ValidationSmokeOptions) {
	t.Helper()
	directory, manifest := fixtureManifest(t)
	return directory, manifest, ValidationSmokeOptions{
		Operation: database.RuntimeValidationRequest{OperationID: strings.Repeat("a", 32), Epoch: 1,
			CandidateManifestHash: writeSmokeManifest(t, directory, manifest), Token: strings.Repeat("f", 64)},
		Username: "smoke-admin", Password: "smoke-password-must-not-appear",
	}
}

func smokeFixturePage(path string) string {
	marker := ""
	for _, page := range validationPages {
		if page.path == path && page.marker != "" {
			marker = fmt.Sprintf(`<%s %s="" %s="42" data-can-train="true"></%s>`, page.element, page.marker, "data-owner", page.element)
			if page.ownerAttribute != "" {
				marker = strings.ReplaceAll(marker, "data-owner", page.ownerAttribute)
			}
		}
	}
	return `<html><body><div data-notification-center data-notification-owner="42"></div>` + marker +
		`<form method="post" action="/logout"><input type="hidden" name="csrf" value="` + strings.Repeat("A", 43) + `"></form></body></html>`
}

func TestValidationSmokeOrigins(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:18120", "http://127.2.3.4:1", "https://[::1]:443"} {
		if _, err := validationOrigin(raw); err != nil {
			t.Errorf("rejected %q", raw)
		}
	}
	for _, raw := range []string{"", "http://localhost:8000", "http://192.168.1.1:8000", "http://0.0.0.0:8000", "http://127.0.0.1:0",
		"http://127.0.0.1:65536", "http://127.0.0.1", "http://user:password@127.0.0.1:8000", "http://127.0.0.1:8000/",
		"http://127.0.0.1:8000/path", "http://127.0.0.1:8000?", "http://127.0.0.1:8000?q=x", "http://127.0.0.1:8000#fragment",
		"ftp://127.0.0.1:8000", "http://[::1%25eth0]:8000"} {
		if _, err := validationOrigin(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestValidationSmokeProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, failure, cleanup string
		logins                 int64
	}{
		{"success", "", "verified", 1},
		{"admin_identity", "admin_identity", "not_needed", 0},
		{"missing_readiness", "public_identity", "not_needed", 0},
		{"ready", "public_identity", "not_needed", 0},
		{"extra_token", "public_identity", "not_needed", 0},
		{"wrong_contract", "public_identity", "not_needed", 0},
		{"identity_redirect", "public_identity", "not_needed", 0},
		{"oversized", "public_identity", "not_needed", 0},
		{"cacheable", "public_identity", "not_needed", 0},
		{"public_token", "public_token_required", "not_needed", 0},
		{"readyz", "public_not_production_ready", "not_needed", 0},
		{"no_auth", "ordinary_auth_required", "not_needed", 0},
		{"login_redirect", "password_login", "verified", 1},
		{"gallery_owner", "gallery_page", "verified", 1},
		{"truncated", "gallery_page", "verified", 1},
		{"login_instead_of_page", "gallery_page", "verified", 1},
		{"admin_role", "users_page", "verified", 1},
		{"admin_build", "admin_build_report", "verified", 1},
		{"mutation_admitted", "generation_denied", "verified", 1},
		{"lost_grant", "gallery_page", "unconfirmed", 1},
		{"logout_missing_cookie", "session_cleanup", "unconfirmed", 1},
		{"logout_not_revoked", "session_cleanup", "unconfirmed", 1},
		{"tampered", "package_files", "not_needed", 0},
		{"manifest_hash", "manifest_identity", "not_needed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory, manifest, options := smokeFixtureOptions(t)
			var logins, requests, escaped atomic.Int64
			var revoked, grantLost atomic.Bool
			other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { escaped.Add(1) }))
			defer other.Close()
			handler := func(admin bool) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Cache-Control", "no-store")
					if grantLost.Load() {
						w.WriteHeader(503)
						return
					}
					if r.Header.Get(ValidationTokenHeader) != options.Operation.Token {
						if tc.name == "public_token" {
							w.WriteHeader(200)
						} else {
							w.WriteHeader(503)
						}
						return
					}
					if r.URL.Path == "/internal/release/validation" {
						status := ValidationStatus{SchemaVersion: 1, Mode: "release_validation", ProductionReady: new(bool), Operation: options.Operation, Gateway: manifest.Artifacts[0].Report}
						if tc.name == "admin_identity" && admin {
							status.Operation.Epoch++
						}
						if tc.name == "missing_readiness" {
							status.ProductionReady = nil
						}
						if tc.name == "ready" {
							*status.ProductionReady = true
						}
						if tc.name == "wrong_contract" {
							status.Gateway.Contracts = nil
						}
						if tc.name == "identity_redirect" {
							http.Redirect(w, r, other.URL, 307)
							return
						}
						if tc.name == "cacheable" {
							w.Header().Del("Cache-Control")
						}
						w.Header().Set("Content-Type", "application/json")
						if tc.name == "extra_token" {
							body, _ := json.Marshal(status)
							fmt.Fprint(w, strings.TrimSuffix(string(body), "}")+`,"token":"secret"}`)
							return
						}
						if tc.name == "oversized" {
							fmt.Fprint(w, strings.Repeat(" ", (2<<20)+1))
							return
						}
						_ = json.NewEncoder(w).Encode(status)
						return
					}
					if r.URL.Path == "/readyz" {
						if tc.name == "readyz" {
							w.WriteHeader(200)
						} else {
							w.WriteHeader(503)
						}
						return
					}
					if r.URL.Path == "/login" && r.Method == "POST" {
						logins.Add(1)
						if err := r.ParseForm(); err != nil || r.Form.Get("login") != options.Username || r.Form.Get("password") != options.Password || r.Header.Get("Origin") != options.PublicURL {
							t.Error("incorrect ordinary login form")
						}
						http.SetCookie(w, &http.Cookie{Name: "gateway_session", Value: "private-session-cookie", Path: "/", HttpOnly: true})
						location := "/app"
						if tc.name == "login_redirect" {
							location = other.URL
						}
						http.Redirect(w, r, location, 302)
						return
					}
					cookie, err := r.Cookie("gateway_session")
					if err != nil || cookie.Value != "private-session-cookie" || revoked.Load() {
						if tc.name == "no_auth" {
							w.WriteHeader(200)
						} else {
							http.Redirect(w, r, "/login?next=%2Fapp", 302)
						}
						return
					}
					if r.URL.Path == "/logout" {
						if err := r.ParseForm(); err != nil || r.Form.Get("csrf") != strings.Repeat("A", 43) || r.Header.Get("Origin") != options.PublicURL {
							t.Error("incorrect logout form")
						}
						if tc.name != "logout_not_revoked" {
							revoked.Store(true)
						}
						if tc.name != "logout_missing_cookie" {
							http.SetCookie(w, &http.Cookie{Name: "gateway_session", Path: "/", MaxAge: -1})
						}
						http.Redirect(w, r, "/login", 302)
						return
					}
					if r.URL.Path == "/admin/release/status" {
						report := manifest.Artifacts[0].Report
						if tc.name == "admin_build" {
							report.Build.Revision = strings.Repeat("b", 40)
						}
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "gateway": report, "peers": []any{}})
						return
					}
					if tc.name == "mutation_admitted" && r.URL.Path == "/generate/run" {
						w.WriteHeader(200)
						return
					}
					for _, page := range validationPages {
						if r.Method != "GET" || r.URL.Path != page.path {
							continue
						}
						w.Header().Set("Content-Type", "text/html; charset=utf-8")
						body := smokeFixturePage(page.path)
						if r.URL.Path == "/gallery" {
							switch tc.name {
							case "gallery_owner":
								body = strings.ReplaceAll(body, `data-owner="42"`, `data-owner=""`)
							case "truncated":
								body = strings.TrimSuffix(body, "</html>")
							case "login_instead_of_page":
								body = "<html><body>login</body></html>"
							case "lost_grant":
								grantLost.Store(true)
								w.WriteHeader(503)
								return
							}
						}
						if tc.name == "admin_role" && page.path == "/admin/users" {
							w.WriteHeader(403)
							return
						}
						fmt.Fprint(w, body)
						return
					}
					w.WriteHeader(503)
				})
			}
			public := httptest.NewServer(handler(false))
			defer public.Close()
			admin := httptest.NewServer(handler(true))
			defer admin.Close()
			options.PublicURL, options.AdminURL = public.URL, admin.URL
			if tc.name == "tampered" {
				if err := os.WriteFile(filepath.Join(directory, manifest.Source.Name), []byte("tamper"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "manifest_hash" {
				options.Operation.CandidateManifestHash = strings.Repeat("e", 64)
			}
			report, err := SmokeValidation(context.Background(), directory, options)
			if (err == nil) != (tc.failure == "") || report.FailureCode != tc.failure || report.Success != (tc.failure == "") || report.CleanupStatus != tc.cleanup || report.DeploymentAuthorized {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if logins.Load() != tc.logins || escaped.Load() != 0 {
				t.Fatalf("logins=%d escaped=%d", logins.Load(), escaped.Load())
			}
			if report.Operation == nil || report.Operation.Token != "" || report.Operation.OperationID != options.Operation.OperationID || report.Operation.Epoch != options.Operation.Epoch || report.Operation.CandidateManifestHash != options.Operation.CandidateManifestHash {
				t.Fatal("report is not bound to the exact token-free operation")
			}
			if tc.failure == "" && (report.Candidate == nil || *report.Candidate != manifest.Build) {
				t.Fatal("successful report is not bound to the candidate build")
			}
			if (tc.name == "tampered" || tc.name == "manifest_hash") && requests.Load() != 0 {
				t.Fatal("contacted HTTP before verifying package")
			}
			body, _ := json.Marshal(report)
			for _, secret := range []string{options.Password, options.Operation.Token, "private-session-cookie", options.PublicURL, strings.Repeat("A", 43)} {
				if strings.Contains(string(body), secret) || (err != nil && strings.Contains(err.Error(), secret)) {
					t.Fatal("smoke report exposed private data")
				}
			}
		})
	}
}

func TestValidationDocumentOwnerAndRootScope(t *testing.T) {
	body := smokeFixturePage("/gallery")
	for _, edit := range []struct{ name, body string }{
		{"valid", body},
		{"wrong_element", strings.ReplaceAll(body, "main", "div")},
		{"duplicate", strings.Replace(body, "</body>", `<main data-user-gallery data-owner="42" data-can-train="true"></main></body>`, 1)},
		{"wrong_owner", strings.Replace(body, `data-owner="42"`, `data-owner="43"`, 1)},
		{"no_training", strings.Replace(body, `data-can-train="true"`, `data-can-train="false"`, 1)},
	} {
		t.Run(edit.name, func(t *testing.T) {
			document, err := readValidationDocument([]byte(edit.body))
			if err != nil {
				t.Fatal(err)
			}
			client := validationSmokeClient{}
			if client.acceptPage(document, validationPages[2]) != (edit.name == "valid") {
				t.Fatal("incorrect gallery scope accepted")
			}
		})
	}
}

func TestValidationSmokeInvalidConfigurationIsNotReportedAsIdentity(t *testing.T) {
	directory, _, options := smokeFixtureOptions(t)
	options.PublicURL, options.AdminURL = "http://127.0.0.1:18122", "http://127.0.0.1:18123"
	options.Operation.OperationID = "private-invalid-identity"
	report, err := SmokeValidation(context.Background(), directory, options)
	if err == nil || report.FailureCode != "configuration" || report.Operation != nil || report.Candidate != nil {
		t.Fatalf("invalid configuration: %+v %v", report, err)
	}
	body, _ := json.Marshal(report)
	if bytes.Contains(body, []byte(options.Operation.OperationID)) || strings.Contains(err.Error(), options.Operation.OperationID) {
		t.Fatal("invalid identity was copied to report")
	}
}

func TestValidationSmokeRequiresDistinctListeners(t *testing.T) {
	for _, admin := range []string{"http://127.0.0.1:18122", "http://127.0.0.1:018122"} {
		directory, _, options := smokeFixtureOptions(t)
		options.PublicURL, options.AdminURL = "http://127.0.0.1:18122", admin
		report, err := SmokeValidation(context.Background(), directory, options)
		if err == nil || report.FailureCode != "configuration" || len(report.Checks) != 1 {
			t.Fatalf("same listener accepted for both roles: %+v %v", report, err)
		}
	}
}
