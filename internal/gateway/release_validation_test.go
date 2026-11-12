package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/moderation"
	"ai-access-gateway/internal/store"
)

func testReleaseValidationRequest() *database.RuntimeValidationRequest {
	return &database.RuntimeValidationRequest{OperationID: strings.Repeat("a", 32), Epoch: 1, Token: strings.Repeat("b", 64), CandidateManifestHash: strings.Repeat("c", 64)}
}

func TestReleaseValidationRouteBoundary(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/login", true}, {"POST", "/login", true}, {"POST", "/logout", true},
		{"GET", "/app", true}, {"GET", "/generate", true}, {"GET", "/gallery", true},
		{"GET", "/train-lora?new=1", true}, {"GET", "/tasks", true}, {"GET", "/api/tasks", true},
		{"GET", "/admin/users", true}, {"GET", "/admin/invites", true}, {"GET", "/admin/release/status", true},
		{"GET", "/internal/release/validation", true}, {"HEAD", "/internal/release/validation", true},
		{"GET", "/static/app.js", true}, {"HEAD", "/static/theme.css", true},
		{"GET", "/static/fonts/manrope-cyrillic.woff2", true},
		{"GET", "/readyz", false}, {"GET", "/", false}, {"GET", "/comfyui", false}, {"GET", "/openwebui/", false},
		{"GET", "/api/queue", false}, {"POST", "/generate/run", false}, {"POST", "/train-lora", false},
		{"POST", "/admin/users", false}, {"GET", "/api/tasks/events", false}, {"GET", "/admin/storage", false},
		{"POST", "/internal/gpu/comfy/permit", false}, {"POST", "/internal/gpu/ollama/permit", false},
		{"GET", "/generate/library/1", false}, {"GET", "/gallery/../comfyui", false}, {"GET", "/%61pp", false},
		{"GET", "/static/fonts/../app.js", false}, {"GET", "/admin/users/delete", false},
		{"POST", "/internal/release/validation", false}, {"PUT", "/login", false}, {"CONNECT", "/app", false},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if got := releaseValidationRouteAllowed(r); got != tc.allowed {
				t.Fatalf("allowed=%v want %v", got, tc.allowed)
			}
			r.Header.Set("Upgrade", "websocket")
			if releaseValidationRouteAllowed(r) {
				t.Fatal("upgrade passed validation boundary")
			}
		})
	}
}

func TestReleaseValidationFailsClosedBeforeHandlers(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) })
	request := testReleaseValidationRequest()
	handler := releaseValidationHTTP(Config{ReleaseValidation: request}, nil, next)
	for _, tokens := range [][]string{nil, {"wrong"}, {request.Token}, {request.Token, request.Token}} {
		r := httptest.NewRequest(http.MethodGet, "/app", nil)
		for _, token := range tokens {
			r.Header.Add(releaseValidationTokenHeader, token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if called || w.Code != http.StatusServiceUnavailable || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), request.Token) {
			t.Fatal("validation did not fail closed before ordinary handlers")
		}
	}
	w := httptest.NewRecorder()
	releaseValidationHTTP(Config{}, nil, next).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/app", nil))
	if !called || w.Code != http.StatusNoContent {
		t.Fatal("ordinary routing changed")
	}
}

func TestReleaseValidationDisablesUpstreamsAndBackgroundWork(t *testing.T) {
	base, _ := url.Parse("http://127.0.0.1:12345")
	cfg := Config{ReleaseValidation: testReleaseValidationRequest(), PublicURL: base, AdminURL: base, PprofEnabled: true}
	value := reflect.ValueOf(&cfg).Elem()
	urlType := reflect.TypeOf(base)
	for index := 0; index < value.NumField(); index++ {
		if value.Field(index).Type() == urlType {
			value.Field(index).Set(reflect.ValueOf(base))
		}
	}
	cfg = releaseValidationConfig(cfg, "/owned-test-spool")
	value = reflect.ValueOf(cfg)
	for index := 0; index < value.NumField(); index++ {
		name := value.Type().Field(index).Name
		if value.Field(index).Type() == urlType && name != "PublicURL" && name != "AdminURL" && !value.Field(index).IsNil() {
			t.Errorf("validation retained upstream %s", name)
		}
	}
	if cfg.PprofEnabled || cfg.PublicURL == nil || cfg.AdminURL == nil || cfg.ReleaseValidation == nil || cfg.MediaSpoolDir != "/owned-test-spool" {
		t.Fatal("incorrect validation configuration")
	}
	cipher, err := contentcrypto.NewCipher(strings.Repeat("s", 40))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: cfg, store: store.New(nil), contentCipher: cipher, contentModerator: moderation.NewClient(base), sensitiveMediaSlots: make(chan struct{}, 1), maintenanceDone: make(chan struct{})}
	app.startBackgroundWork(context.Background())
	select {
	case <-app.maintenanceDone:
	default:
		t.Fatal("background work started in validation")
	}
	app.classifyPendingSensitiveContent(context.Background())
	app.queueSensitiveMediaClassification()
	if len(app.sensitiveMediaSlots) != 0 {
		t.Fatal("request-triggered classification started in validation")
	}
}

func TestReleaseValidationResponsesAreNotCacheable(t *testing.T) {
	w := httptest.NewRecorder()
	guard := &releaseValidationWriter{ResponseWriter: w}
	guard.Header().Set("Cache-Control", "public, max-age=31536000")
	_, _ = guard.Write([]byte("asset"))
	guard.WriteHeader(http.StatusAccepted)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("validation response retained a public cache policy")
	}
}
