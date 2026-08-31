package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/updates"
)

func TestUpdateRequestFromFormDirectInstallIgnoresBatchSelections(t *testing.T) {
	form := url.Values{
		"action":     {"install:openwebui"},
		"components": {"gateway", "comfyui", "openwebui"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/updates", nil)
	request.Form = form

	action, update := updateRequestFromForm(request)
	if action != "install" {
		t.Fatalf("action = %q, want install", action)
	}
	if len(update.Components) != 1 || update.Components[0] != "openwebui" {
		t.Fatalf("components = %#v, want only openwebui", update.Components)
	}
}

func TestComfyUpdateRunsCompatibilityDryRunAfterSuccessfulInstall(t *testing.T) {
	objectInfo, err := os.ReadFile("testdata/object_info/minimax_h3_v4.json")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/object_info" {
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(objectInfo)
	}))
	t.Cleanup(comfy.Close)
	upstream, err := url.Parse(comfy.URL)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{
		ComfyUIUpstream:         upstream,
		ComfyObjectInfoCacheTTL: time.Minute,
		ComfyObjectInfoMaxStale: time.Hour,
	}}

	message := app.appendComfyUpdateCompatibilitySummary(context.Background(), "ComfyUI обновлён.", updates.Request{Components: []string{updates.ComponentComfyUI}}, nil)
	if requests.Load() != 1 {
		t.Fatalf("object_info requests = %d, want 1", requests.Load())
	}
	if !strings.Contains(message, "Проверка workflow") {
		t.Fatalf("message = %q, want compatibility summary", message)
	}
}

func TestComfyUpdateSkipsCompatibilityDryRunWhenInstallFailsOrComponentIsDifferent(t *testing.T) {
	app := &App{}
	base := "Обновление не завершено."
	if got := app.appendComfyUpdateCompatibilitySummary(context.Background(), base, updates.Request{Components: []string{updates.ComponentComfyUI}}, errors.New("install failed")); got != base {
		t.Fatalf("failed install message = %q, want %q", got, base)
	}
	if got := app.appendComfyUpdateCompatibilitySummary(context.Background(), base, updates.Request{Components: []string{updates.ComponentGateway}}, nil); got != base {
		t.Fatalf("other component message = %q, want %q", got, base)
	}
}
