package gateway

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/config"
)

// An opt-in browser fixture backed by the real, isolated Harness web process.
// It grants a test identity only in this test executable, never in Gateway.
func TestHarnessUIIntegration(t *testing.T) {
	if os.Getenv("HARNESS_UI_TEST") != "1" {
		t.Skip("opt-in browser fixture")
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	address, public, upstream, bootstrap := "127.0.0.1:3089", "http://127.0.0.1:3089", "http://127.0.0.1:3085", "http://127.0.0.1:3086"
	if os.Getenv("HARNESS_UI_DOCKER") == "1" {
		address, public, upstream, bootstrap = ":3089", "http://gateway-fixture:3089", "http://harness-web:3080", "http://harness-web:3082"
	}
	app := &App{tpl: tpl, cfg: Config{PublicURL: harnessURL(t, public), Harness: config.HarnessConfig{
		Upstream: harnessURL(t, upstream), BootstrapURL: harnessURL(t, bootstrap), BootstrapKey: strings.Repeat("b", 64),
	}}}
	mux := http.NewServeMux()
	app.registerHarnessRoutes(mux)
	done := make(chan struct{}, 1)
	mux.HandleFunc("/test/complete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		done <- struct{}{}
		w.WriteHeader(204)
	})
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mux.ServeHTTP(w, harnessAdmin(r)) }), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	t.Log("Harness browser fixture ready")
	select {
	case <-done:
	case <-time.After(15 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}
