package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-access-gateway/internal/config"
)

func TestHarnessModelBusyResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte("busy"))
	}))
	defer upstream.Close()
	app := &App{cfg: Config{Harness: config.HarnessConfig{Upstream: harnessURL(t, upstream.URL), ModelUpstream: harnessURL(t, upstream.URL), ModelKey: strings.Repeat("m", 64)}}}
	r := httptest.NewRequest("POST", harnessModelMount+"/v1/chat/completions", strings.NewReader(`{"model":"fixture"}`))
	r.Header.Set("Authorization", "Bearer "+app.cfg.Harness.ModelKey)
	w := httptest.NewRecorder()
	app.harnessModelProxy().ServeHTTP(w, r)
	var result struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if w.Code != http.StatusConflict || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Error.Code != "gpu_busy" {
		t.Fatalf("unexpected busy response: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Допуск GPU") || strings.Contains(w.Body.String(), "Майнер не остановлен") {
		t.Fatal("busy response must not assert an unverified miner state")
	}
}
