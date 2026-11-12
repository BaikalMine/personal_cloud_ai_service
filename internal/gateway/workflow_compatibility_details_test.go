package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/security"
)

func TestWorkflowCompatibilityDetailsMatchGraphAndManifest(t *testing.T) {
	info := compatibilityFixtureObjectInfo(t)
	delete(info, "SeedVR2VideoUpscaler")
	catalog := buildGenerationModelCatalog(info)
	catalog.ObjectInfo.Schema = buildComfySchemaCatalog(info, time.Now(), "fixture")
	results := buildWorkflowCompatibilityResults(catalog)
	seen := map[string]bool{}
	for _, result := range results {
		if result.Status == workflowCompatibilityUnavailable {
			continue
		}
		if result.ModelFile == "" || len(result.Components) == 0 || len(result.Files) == 0 {
			t.Fatalf("missing graph/model: %+v", result)
		}
		manifest, exists := workflowManifestByID(result.DefinitionID)
		if exists && (result.ManifestName != manifest.Name || result.ManifestVersion != manifest.Version) {
			t.Fatal("manifest not bound to definition")
		}
		if !exists && (result.ManifestVersion != "" || len(result.Branches) != 0) {
			t.Fatal("unversioned definition inherited another manifest")
		}
		if strings.HasPrefix(result.ID, "flux-text:") {
			seen["unversioned"] = !exists
		}
		if strings.HasPrefix(result.ID, "flux-edit:") || strings.HasPrefix(result.ID, "flux-post:") {
			post := strings.HasPrefix(result.ID, "flux-post:")
			if post && result.Status != workflowCompatibilityErrorStatus || !post && result.Status != workflowCompatibilityOK {
				t.Fatalf("option changed base status: %+v", result)
			}
			found := false
			for _, branch := range result.Branches {
				if branch.Name == "SeedVR2" {
					found = true
					if branch.Selected != post || branch.Present != 4 || len(branch.Components) != 5 || branch.Components[0].Present {
						t.Fatalf("wrong option evidence: %+v", branch)
					}
				}
			}
			if !found {
				t.Fatal("missing optional branch")
			}
			for _, file := range result.Files {
				if file.HashState != "not_observed" || file.SHA256 != "" {
					t.Fatal("schema preflight claimed file inspection")
				}
			}
			if post && (result.Components[0].ClassType != "SeedVR2VideoUpscaler" || result.Components[0].Present) {
				t.Fatal("missing graph node not first")
			}
			seen[result.ID[:strings.Index(result.ID, ":")]] = true
		}
		if result.DefinitionID == "image-to-image-krea2" {
			for _, branch := range result.Branches {
				if branch.Name == "Identity LoRA" {
					t.Fatal("unconditional identity mislabeled optional")
				}
			}
			seen["identity"] = true
		}
	}
	for _, key := range []string{"unversioned", "flux-edit", "flux-post", "identity"} {
		if !seen[key] {
			t.Fatalf("case not exercised: %s", key)
		}
	}
}

func TestWorkflowCompatibilityFiltersAndEscaping(t *testing.T) {
	results := []workflowCompatibilityResult{
		{ID: "a", Family: "Krea2", Model: "Портрет", ModelFile: "models/photo.safetensors", Status: "ok", ManifestVersion: "1.2", Files: []generationExecutionComponent{{Name: "vae-preview.safetensors"}}},
		{ID: "b", Family: "Flux2", Model: "Flux", Status: "error", Issues: []workflowCompatibilityIssue{{InputName: "resize_type"}}, Branches: []workflowCompatibilityBranch{{Name: "SeedVR2", Components: []workflowCompatibilityComponent{{ClassType: "MissingNode"}}}}},
	}
	for _, tc := range []struct{ query, id string }{
		{"q=ПОРТРЕТ", "a"}, {"q=photo.safetensors", "a"}, {"q=1.2", "a"}, {"q=vae-preview", "a"},
		{"q=resize_type&state=error&family=Flux2", "b"}, {"q=missingnode", "b"},
		{"q=seedvr2&state=ok", ""}, {"family=Krea2&state=ok", "a"},
	} {
		q, _ := url.ParseQuery(tc.query)
		view := newWorkflowCompatibilityView(results, q)
		if view.Total != 2 || len(view.Families) != 2 {
			t.Fatal("filter changed totals")
		}
		if tc.id == "" {
			if len(view.Results) != 0 {
				t.Fatalf("unexpected match: %+v", view)
			}
			continue
		}
		if len(view.Results) != 1 || view.Results[0].ID != tc.id {
			t.Fatalf("filter %s: %+v", tc.query, view)
		}
	}
	view := newWorkflowCompatibilityView(results, url.Values{"q": {strings.Repeat("я", 201)}, "state": {"invalid"}, "family": {"invalid"}})
	if len([]rune(view.Query)) != 200 || view.State != "" || view.Family != "" {
		t.Fatal("invalid filters not normalized")
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	results[0].ModelFile = `<script>alert(1)</script>`
	results[0].Components = []workflowCompatibilityComponent{{ClassType: `<img src=x onerror=alert(1)>`}}
	view = newWorkflowCompatibilityView(results, url.Values{"q": {`<script>`}})
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "admin_workflows", map[string]any{"Report": workflowCompatibilityReport{}, "View": view}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `<script>alert`) || strings.Contains(out.String(), `<img src=x`) || !strings.Contains(out.String(), "&lt;script&gt;") {
		t.Fatal("unsafe or missing text")
	}
}

func TestAdminWorkflowsHTTPRefreshFiltersAndAccess(t *testing.T) {
	info := compatibilityFixtureObjectInfo(t)
	var reads atomic.Int32
	var offline atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/object_info" {
			t.Errorf("unexpected upstream action: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		reads.Add(1)
		if offline.Load() {
			http.Error(w, "offline", 503)
			return
		}
		_ = json.NewEncoder(w).Encode(info)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{tpl: tpl, cfg: Config{ComfyUIUpstream: base}, csrfSigner: security.NewCSRFSigner("workflow-http-test")}
	request := func(method, path, role, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(url.Values{"csrf": {csrf}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if role != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "test-session"})
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: role}))
		}
		w := httptest.NewRecorder()
		a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes)).ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		method, role string
		want         int
	}{{"GET", "", 302}, {"GET", "user", 403}, {"POST", "admin", 403}, {"DELETE", "admin", 405}} {
		if w := request(tc.method, "/admin/workflows", tc.role, "invalid"); w.Code != tc.want {
			t.Fatalf("%s/%s status %d", tc.method, tc.role, w.Code)
		}
	}
	if reads.Load() != 0 {
		t.Fatal("unauthorized request reached ComfyUI")
	}
	w := request("GET", "/admin/workflows?q=Flux2&state=ok", "admin", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "image-to-image-flux2") || strings.Contains(w.Body.String(), `data-workflow-id="krea-`) {
		t.Fatalf("filtered response: %s", w.Body.String())
	}
	request("GET", "/admin/workflows", "admin", "")
	if reads.Load() != 1 {
		t.Fatal("GET bypassed schema cache")
	}
	token := a.csrfSigner.Token("test-session")
	w = request("POST", "/admin/workflows?q=Flux2&state=ok", "admin", token)
	if reads.Load() != 2 || !strings.Contains(w.Body.String(), `value="Flux2"`) || !strings.Contains(w.Body.String(), "Проверка workflow завершена") {
		t.Fatalf("forced refresh missing: %s", w.Body.String())
	}
	offline.Store(true)
	w = request("POST", "/admin/workflows", "admin", token)
	if !strings.Contains(w.Body.String(), "Каталог устарел") || !strings.Contains(w.Body.String(), "HTTP 503") {
		t.Fatal("last-known-good not visibly stale")
	}
	a.objectInfoCache = newComfyObjectInfoCache(time.Second, time.Hour, time.Now)
	w = request("GET", "/admin/workflows", "admin", "")
	if !strings.Contains(w.Body.String(), "Совместимость не определена") || strings.Contains(w.Body.String(), "compatibility-table\"") {
		t.Fatal("offline treated as empty or successful")
	}
}
