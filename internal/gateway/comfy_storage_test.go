package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/updates"
)

func comfyStorageRequest(app *App, method, path, body string, actor *User, csrf bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if actor != nil {
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, actor))
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "comfy-storage-test-session"})
	}
	if csrf {
		r.Header.Set("X-CSRF-Token", app.csrfSigner.Token("comfy-storage-test-session"))
	}
	w := httptest.NewRecorder()
	app.handleAdminRoutes(w, r)
	return w
}

func TestComfyStorageHTTPValidation(t *testing.T) {
	a := &App{csrfSigner: security.NewCSRFSigner("storage-test-secret")}
	admin := &User{ID: 1, Role: "admin"}
	valid := `{"items":[{"key":"cleanup:1","version":"` + strings.Repeat("a", 64) + `"}]}`
	for _, tc := range []struct {
		method, path, body string
		actor              *User
		csrf               bool
		status             int
	}{
		{"GET", "/admin/storage/comfy", "", nil, false, 403},
		{"GET", "/admin/storage/comfy", "", &User{Role: "user"}, false, 403},
		{"POST", "/admin/storage/comfy", "", admin, true, 405},
		{"GET", "/admin/storage/comfy/inspect", "", admin, false, 405},
		{"POST", "/admin/storage/comfy/inspect", valid, admin, false, 403},
		{"GET", "/admin/storage/comfy?limit=0", "", admin, false, 400},
		{"GET", "/admin/storage/comfy?limit=101", "", admin, false, 400},
		{"GET", "/admin/storage/comfy?limit=01", "", admin, false, 400},
		{"GET", "/admin/storage/comfy?offset=-1", "", admin, false, 400},
		{"GET", "/admin/storage/comfy?offset=100001", "", admin, false, 400},
		{"GET", "/admin/storage/comfy?kind=input&kind=output", "", admin, false, 400},
		{"GET", "/admin/storage/comfy?path=C:/input", "", admin, false, 400},
		{"GET", "/admin/storage/comfy?state=deleted", "", admin, false, 400},
		{"GET", "/admin/storage/comfy?q=%00", "", admin, false, 400},
		{"GET", "/admin/storage/comfy?q=" + strings.Repeat("x", 201), "", admin, false, 400},
		{"POST", "/admin/storage/comfy/inspect?delete=true", valid, admin, true, 400},
		{"POST", "/admin/storage/comfy/inspect", `{"items":[]}`, admin, true, 400},
		{"POST", "/admin/storage/comfy/inspect", `{"items":[{"key":"cleanup:1","version":"short"}]}`, admin, true, 400},
		{"POST", "/admin/storage/comfy/inspect", strings.Replace(valid, "cleanup:1", "cleanup:01", 1), admin, true, 400},
		{"POST", "/admin/storage/comfy/inspect", strings.TrimSuffix(valid, "}") + `,"delete":true}`, admin, true, 400},
		{"POST", "/admin/storage/comfy/inspect", valid + `{}`, admin, true, 400},
		{"POST", "/admin/storage/comfy/inspect", `{"items":[{"key":"` + strings.Repeat("x", 17000) + `"}]}`, admin, true, 400},
		{"POST", "/admin/storage/comfy/delete", valid, admin, true, 404},
		{"POST", "/admin/storage/comfy/retry", valid, admin, true, 404},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := comfyStorageRequest(a, tc.method, tc.path, tc.body, tc.actor, tc.csrf)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" && w.Code != 404 {
				t.Fatal(w.Header())
			}
		})
	}
}

func TestComfyStorageInspectionValidatesHostEvidence(t *testing.T) {
	item := domain.ComfyStorageItem{Key: "cleanup:1", Version: strings.Repeat("a", 64), Filename: "expected.png", Subfolder: "owner", StorageType: "output", SizeBytes: 42, ContentHash: strings.Repeat("b", 64), SizeKnown: true, CanInspect: true, Protection: []string{"executor_not_fenced"}}
	for _, mode := range []string{"matched", "missing", "mismatched", "partial", "foreign", "redirect", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v1/comfy-asset/inspect" {
					t.Error("unexpected destructive or redirected call", r.Method, r.URL.Path)
					http.Error(w, "unexpected", 500)
					return
				}
				if mode == "redirect" {
					http.Redirect(w, r, "/v1/comfy-asset/delete", 307)
					return
				}
				if mode == "unavailable" {
					http.Error(w, "private C:/host/path", 500)
					return
				}
				var request updates.ComfyAssetDeleteRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Files) != 1 {
					t.Error("invalid inspection payload")
					http.Error(w, "bad", 400)
					return
				}
				file := request.Files[0]
				status := mode
				if mode == "foreign" {
					file.Filename = "foreign.png"
					status = "matched"
				}
				if mode == "partial" {
					status = "io_error"
				}
				result := updates.ComfyAssetInspectResult{Protocol: 1, Complete: mode != "partial", CheckedAt: time.Now().UTC(), Items: []updates.ComfyAssetDeleteOutcome{{Filename: file.Filename, Subfolder: file.Subfolder, StorageType: file.StorageType, SizeBytes: file.SizeBytes, SHA256: file.SHA256, Status: status}}}
				code := 200
				if mode == "partial" {
					code = 503
				}
				writeJSON(w, code, result)
			}))
			defer agent.Close()
			base, _ := url.Parse(agent.URL)
			app := &App{updates: updates.NewClient(base, strings.Repeat("s", 32))}
			result := app.inspectComfyStorage(context.Background(), []domain.ComfyStorageItem{item})
			if calls.Load() != 1 || len(result.Items) != 1 || result.Items[0].Key != item.Key || len(result.Items[0].Protection) != 1 {
				t.Fatal(result, calls.Load())
			}
			switch mode {
			case "matched", "missing", "mismatched":
				if !result.Complete || result.Items[0].Status != mode || result.HostState != "available" {
					t.Fatal(result)
				}
			case "partial":
				if result.Complete || result.Items[0].Status != "io_error" {
					t.Fatal(result)
				}
			case "foreign":
				if result.Complete || result.Items[0].Status != "host_unavailable" || result.HostState != "invalid_response" {
					t.Fatal(result)
				}
			default:
				if result.Complete || result.Items[0].Status != "host_unavailable" || result.HostState != "unavailable" {
					t.Fatal(result)
				}
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "C:/") {
				t.Fatal("host path leaked")
			}
		})
	}
	result := (&App{}).inspectComfyStorage(context.Background(), []domain.ComfyStorageItem{item})
	if result.Complete || result.HostState != "not_configured" {
		t.Fatal(result)
	}
}
