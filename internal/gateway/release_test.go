package gateway

import (
	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/config"
	"ai-access-gateway/internal/security"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdminReleaseReadBoundary(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/build" || r.Header.Get("Authorization") != "Bearer release-test-token" {
			t.Error("unexpected request", r.Method, r.URL.Path)
		}
		buildinfo.Handler(buildinfo.UpdateAgent).ServeHTTP(w, r)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: config.Config{UpdateAgentURL: base, UpdateAgentToken: "release-test-token"}, tpl: tpl, csrfSigner: security.NewCSRFSigner("release-test")}
	invoke := func(method, path string, actor *User, handler http.Handler) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if actor != nil {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, actor))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	admin := &User{ID: 1, Role: "admin"}
	handler := http.HandlerFunc(a.handleAdminRoutes)
	for _, tc := range []struct {
		method, path string
		actor        *User
		status       int
	}{
		{"GET", "/admin/release/status", nil, 403},
		{"GET", "/admin/release", &User{Role: "user"}, 403},
		{"POST", "/admin/release/status", admin, 405},
		{"DELETE", "/admin/release", admin, 405},
		{"HEAD", "/admin/release/status", admin, 405},
		{"GET", "/admin/release/status?url=http://untrusted.local", admin, 400},
		{"GET", "/admin/release/unknown", admin, 404},
	} {
		if w := invoke(tc.method, tc.path, tc.actor, handler); w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	if reads.Load() != 0 {
		t.Fatal("rejected requests reached agent")
	}
	if w := invoke("GET", "/admin/release/status", nil, a.requireAdmin(handler)); w.Code != 302 {
		t.Fatal("missing authentication", w.Code)
	}
	for _, path := range []string{"/admin/release/status", "/admin/release"} {
		w := invoke("GET", path, admin, a.requireAdmin(handler))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, secret := range []string{server.URL, "release-test-token"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("private configuration leaked")
			}
		}
		if path == "/admin/release/status" {
			var snapshot ReleaseSnapshot
			if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.SchemaVersion != 1 || len(snapshot.Peers) != 5 || snapshot.Peers[0].Check.Status != "compatible" || snapshot.Gateway.Build != buildinfo.Current() {
				t.Fatalf("invalid snapshot %+v", snapshot)
			}
			for _, peer := range snapshot.Peers[1:] {
				if peer.Check.Status != "not_configured" {
					t.Fatal(peer)
				}
			}
		} else if !strings.Contains(w.Body.String(), "Коммит работающей сборки") || !strings.Contains(w.Body.String(), "Сборка не привязана") {
			t.Fatal("missing runtime identity", w.Body.String())
		}
	}
	if reads.Load() != 2 {
		t.Fatal("unexpected requests", reads.Load())
	}
	if w := invoke("GET", "/admin/release/status", admin, a.requireLANAdmin(handler)); w.Code != 403 {
		t.Fatal("LAN restriction bypassed", w.Code)
	}
	_, network, _ := net.ParseCIDR("192.0.2.0/24")
	a.cfg.AdminAllowedNetworks = []*net.IPNet{network}
	if w := invoke("GET", "/admin/release/status", admin, a.requireLANAdmin(handler)); w.Code != 200 {
		t.Fatal("allowed LAN request rejected", w.Code)
	}
}

func TestReleaseSnapshotProbesConcurrentlyAndCancels(t *testing.T) {
	entered := make(chan struct{}, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-r.Context().Done() }))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	a := &App{cfg: config.Config{UpdateAgentURL: base, UpdateAgentToken: "t", LoraTrainingAgentURL: base, LoraTrainingAgentToken: "t", MiningAgentURL: base, MiningAgentToken: "t", SystemMonitorAgentURL: base, SystemMonitorAgentToken: "t", InferenceWorkerURL: base}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan ReleaseSnapshot, 1)
	go func() { done <- a.releaseSnapshot(ctx) }()
	for range 5 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("probes were serial or missing")
		}
	}
	cancel()
	select {
	case snapshot := <-done:
		for _, peer := range snapshot.Peers {
			if peer.Check.Status != "unavailable" {
				t.Fatal(peer)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation was ignored")
	}
}
