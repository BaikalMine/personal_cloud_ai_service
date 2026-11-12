package gateway

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/config"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/release"
)

const releaseValidationTokenHeader = release.ValidationTokenHeader

func openGatewayRuntime(ctx context.Context, cfg Config) (*sql.DB, error) {
	if cfg.ReleaseValidation != nil {
		return database.OpenRuntimeValidation(ctx, cfg.DatabaseURL, *cfg.ReleaseValidation)
	}
	return database.OpenRuntime(ctx, cfg.DatabaseURL)
}

// Candidate route smoke is local and deliberately has no upstream clients. Agent
// compatibility and actual GPU readiness must be verified by the coordinator.
func releaseValidationConfig(cfg Config, spool string) Config {
	cfg.MediaSpoolDir = spool
	cfg.Harness = config.HarnessConfig{}
	cfg.ComfyUIUpstream, cfg.OpenWebUIUpstream, cfg.OllamaUpstream = nil, nil, nil
	cfg.InferenceWorkerURL, cfg.ContentModeratorUpstream = nil, nil
	cfg.MiningAgentURL, cfg.SystemMonitorAgentURL, cfg.UpdateAgentURL, cfg.LoraTrainingAgentURL = nil, nil, nil, nil
	cfg.ComfyUIUpstreamAuthHeader, cfg.OpenWebUIUpstreamAuth = "", ""
	cfg.MiningAgentToken, cfg.SystemMonitorAgentToken, cfg.UpdateAgentToken, cfg.LoraTrainingAgentToken = "", "", "", ""
	cfg.ComfyGPUGuardToken, cfg.OllamaGPUGuardToken, cfg.VirusTotalAPIKey = "", "", ""
	cfg.PprofEnabled = false
	return cfg
}

// This allowlist is a release-smoke surface, not a general read-only UI. In
// particular, GET proxies, downloads, streaming and internal permits are denied.
func releaseValidationRouteAllowed(r *http.Request) bool {
	if r.URL.RawPath != "" || r.Header.Get("Upgrade") != "" {
		return false
	}
	if r.Method == http.MethodPost {
		return r.URL.Path == "/login" || r.URL.Path == "/logout"
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if _, ok := staticCSSAssets[r.URL.Path]; ok {
		return true
	}
	if _, ok := staticJavaScriptAssets[r.URL.Path]; ok {
		return true
	}
	switch r.URL.Path {
	case "/static/fonts/manrope-cyrillic.woff2", "/static/fonts/manrope-latin.woff2",
		"/healthz", "/internal/release/validation", "/login", "/app", "/generate", "/gallery", "/train-lora", "/tasks",
		"/api/tasks", "/admin", "/admin/users", "/admin/invites", "/admin/release", "/admin/release/status":
		return true
	default:
		return false
	}
}

func releaseValidationHTTP(cfg Config, db *sql.DB, next http.Handler) http.Handler {
	if cfg.ReleaseValidation == nil {
		return next
	}
	request := *cfg.ReleaseValidation
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = &releaseValidationWriter{ResponseWriter: w}
		w.Header().Set("Cache-Control", "no-store")
		tokens := r.Header.Values(releaseValidationTokenHeader)
		if request.Validate() != nil || len(tokens) != 1 || subtle.ConstantTimeCompare([]byte(tokens[0]), []byte(request.Token)) != 1 || !releaseValidationRouteAllowed(r) {
			http.Error(w, "release_validation_unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		err := database.CheckRuntimeValidation(ctx, db, request)
		cancel()
		if err != nil {
			http.Error(w, "release_validation_unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/internal/release/validation" {
			w.Header().Set("Content-Type", "application/json")
			if r.Method != http.MethodHead {
				_ = json.NewEncoder(w).Encode(release.ValidationStatus{
					SchemaVersion: 1, Mode: "release_validation", ProductionReady: new(bool),
					Operation: request, Gateway: buildinfo.NewReport(buildinfo.Gateway),
				})
			}
			return
		}
		// The separate release token does not replace ordinary login, CSRF or
		// per-user authorization in the real application handlers.
		next.ServeHTTP(w, r)
	})
}

type releaseValidationWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *releaseValidationWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.Header().Set("Cache-Control", "no-store")
		w.wroteHeader = true
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *releaseValidationWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
