package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ai-access-gateway/internal/config"
	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/loratraining"
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/moderation"
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
	"ai-access-gateway/internal/virustotal"
)

//go:embed templates/*.html static/*
var embeddedFS embed.FS

var staticJavaScriptAssetPaths = []string{
	"static/theme.js",
	"static/shell.js",
	"static/app.js",
	"static/operations.js",
	"static/admin-content.js",
	"static/admin-ultra.js",
	"static/live-events.js",
	"static/admin-access.js",
	"static/admin-user-access.js",
	"static/storage-state.js",
	"static/storage-operations.js",
	"static/storage.js",
	"static/dialog-focus.js",
	"static/gallery.js",
	"static/gallery-state.js",
	"static/gallery-viewport.js",
	"static/gallery-viewer.js",
	"static/gallery-use.js",
	"static/lora-training.js",
	"static/lora-training-workspace.js",
	"static/lora-library.js",
	"static/model-library.js",
	"static/model-downloads.js",
	"static/comfy-runtime.js",
	"static/model-maintenance.js",
	"static/lora-repeat.js",
	"static/lora-dataset-state.js",
	"static/lora-dataset-quality.js",
	"static/lora-caption-state.js",
	"static/lora-caption-panel.js",
	"static/lora-dataset-editor.js",
	"static/vendor/lucide.js",
	"static/generate.js",
	"static/generation-assistant.js",
	"static/generation-assistant-progress.js",
	"static/generation-batch.js",
	"static/generation-draft.js",
	"static/generation-collections.js",
	"static/collection-workspace.js",
	"static/generation-job-source.js",
	"static/postprocess-view.js",
	"static/generation-repeat-state.js",
	"static/generation-repeat.js",
	"static/generation-postprocess-state.js",
	"static/generation-postprocess.js",
	"static/lora-verification.js",
	"static/generation-assistant-jobs.js",
	"static/generation-history.js",
	"static/generation-results.js",
	"static/generation-feedback.js",
	"static/generation-job.js",
	"static/generation-lightbox.js",
	"static/image-comparison.js",
	"static/generation-media.js",
	"static/generation-payload.js",
	"static/generation-submission.js",
	"static/generation-recipes.js",
	"static/generation-store.js",
	"static/generation-summary.js",
	"static/generation-output.js",
	"static/generation-krea-pro.js",
	"static/krea-pro-options.json",
	"static/generation-video.js",
	"static/generation-wizard.js",
	"static/generation-studio.js",
	"static/notifications.js",
	"static/suggestions.js",
	"static/auth-pages.js",
	"static/account-pages.js",
	"static/task-model.js",
	"static/task-gpu.js",
	"static/admin-training.js",
	"static/admin-resources.js",
	"static/admin-updates.js",
	"static/admin-security.js",
	"static/tasks.js",
}

var staticJavaScriptAssets = func() map[string]string {
	assets := make(map[string]string, len(staticJavaScriptAssetPaths))
	for _, asset := range staticJavaScriptAssetPaths {
		assets["/"+asset] = asset
	}
	return assets
}()

var staticCSSAssets = map[string]string{
	"/static/model-library.css":           "static/model-library.css",
	"/static/release.css":                 "static/release.css",
	"/static/admin-access.css":            "static/admin-access.css",
	"/static/admin-user-profile.css":      "static/admin-user-profile.css",
	"/static/theme.css":                   "static/theme.css",
	"/static/style.css":                   "static/style.css",
	"/static/controls.css":                "static/controls.css",
	"/static/studio.css":                  "static/studio.css",
	"/static/collection-workspace.css":    "static/collection-workspace.css",
	"/static/generation-assistant.css":    "static/generation-assistant.css",
	"/static/lora-caption-panel.css":      "static/lora-caption-panel.css",
	"/static/lora-training-workspace.css": "static/lora-training-workspace.css",
	"/static/shell.css":                   "static/shell.css",
	"/static/notifications.css":           "static/notifications.css",
	"/static/tasks.css":                   "static/tasks.css",
	"/static/lora-library.css":            "static/lora-library.css",
	"/static/lora-verification.css":       "static/lora-verification.css",
	"/static/generation-repeat.css":       "static/generation-repeat.css",
	"/static/generation-postprocess.css":  "static/generation-postprocess.css",
	"/static/gallery.css":                 "static/gallery.css",
	"/static/image-comparison.css":        "static/image-comparison.css",
	"/static/storage.css":                 "static/storage.css",
	"/static/admin-content.css":           "static/admin-content.css",
	"/static/admin-ultra.css":             "static/admin-ultra.css",
	"/static/admin-workflows.css":         "static/admin-workflows.css",
	"/static/admin-training.css":          "static/admin-training.css",
	"/static/admin-resources.css":         "static/admin-resources.css",
	"/static/admin-updates.css":           "static/admin-updates.css",
	"/static/admin-metrics.css":           "static/admin-metrics.css",
	"/static/admin-security.css":          "static/admin-security.css",
	"/static/suggestions.css":             "static/suggestions.css",
	"/static/auth-pages.css":              "static/auth-pages.css",
	"/static/account-pages.css":           "static/account-pages.css",
}

var frontendAssetPaths = append([]string{"static/model-library.css", "static/theme.css", "static/style.css", "static/controls.css", "static/studio.css", "static/generation-assistant.css", "static/lora-caption-panel.css", "static/lora-training-workspace.css", "static/shell.css", "static/notifications.css", "static/tasks.css", "static/lora-library.css", "static/lora-verification.css", "static/generation-repeat.css", "static/generation-postprocess.css", "static/gallery.css", "static/image-comparison.css", "static/storage.css", "static/release.css", "static/admin-content.css", "static/admin-workflows.css", "static/admin-training.css", "static/admin-resources.css", "static/admin-updates.css", "static/admin-metrics.css", "static/admin-security.css", "static/suggestions.css", "static/auth-pages.css", "static/account-pages.css"}, staticJavaScriptAssetPaths...)

type Templates struct {
	*template.Template
	AssetVersion string
}

func Run() error {
	configureGatewayLogging()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 2*time.Minute)
	db, err := openGatewayRuntime(startupCtx, cfg)
	if err != nil {
		cancelStartup()
		return err
	}
	defer db.Close()
	if cfg.ReleaseValidation != nil {
		if err := database.RequireCurrentSchema(startupCtx, db); err != nil {
			cancelStartup()
			return err
		}
		spool, err := os.MkdirTemp("", "gateway-release-validation-")
		if err != nil {
			cancelStartup()
			return err
		}
		defer os.RemoveAll(spool)
		cfg = releaseValidationConfig(cfg, spool)
	} else {
		if err := prepareMediaSpool(cfg.MediaSpoolDir); err != nil {
			cancelStartup()
			return err
		}
		if err := database.Migrate(startupCtx, db); err != nil {
			cancelStartup()
			return err
		}
	}
	repository := store.New(db)
	if !cfg.RestoreDrillMode && cfg.ReleaseValidation == nil {
		adminPasswordHash, err := security.HashPassword(cfg.AdminPassword)
		if err != nil {
			cancelStartup()
			return err
		}
		if err := repository.EnsureBootstrapAdmin(startupCtx, cfg.AdminUsername, adminPasswordHash); err != nil {
			cancelStartup()
			return err
		}
		closedWebSockets, err := repository.CloseStaleWebSockets(startupCtx)
		if err != nil {
			cancelStartup()
			return err
		}
		deletedSessions, err := repository.DeleteExpiredSessions(startupCtx, cfg.SessionIdleTimeout)
		deletedTemporaryUsers, temporaryCleanupErr := repository.DeleteExpiredTemporaryUsers(startupCtx)
		recoveredLoraJobs, loraRecoveryErr := repository.RecoverLoraTrainingJobs(startupCtx)
		cancelStartup()
		if err != nil {
			return err
		}
		if temporaryCleanupErr != nil {
			return temporaryCleanupErr
		}
		if loraRecoveryErr != nil {
			return loraRecoveryErr
		}
		if closedWebSockets > 0 {
			log.Printf("closed %d stale websocket sessions", closedWebSockets)
		}
		if deletedSessions > 0 {
			log.Printf("deleted %d expired sessions", deletedSessions)
		}
		if deletedTemporaryUsers > 0 {
			log.Printf("deleted %d expired temporary users", deletedTemporaryUsers)
		}
		if recoveredLoraJobs > 0 {
			log.Printf("requeued %d interrupted LoRA training jobs", recoveredLoraJobs)
		}
	}
	cancelStartup()

	tpl, err := ParseTemplates()
	if err != nil {
		return err
	}
	contentCipher, err := contentcrypto.NewCipher(cfg.SessionSecret)
	if err != nil {
		return err
	}

	app := &App{
		cfg:                cfg,
		tpl:                tpl,
		loginLimiter:       security.NewLoginLimiter(10*time.Minute, 10),
		loginIPLimiter:     security.NewLoginLimiter(10*time.Minute, 30),
		loginAuditLimiter:  security.NewLoginLimiter(time.Minute, 1),
		inviteLimiter:      security.NewLoginLimiter(10*time.Minute, 20),
		comfyPromptLimiter: security.NewLoginLimiter(time.Minute, 10),
		csrfSigner:         security.NewCSRFSigner(cfg.SessionSecret),
		store:              repository,
		mining:             mining.NewClient(cfg.MiningAgentURL, cfg.MiningAgentToken),
		systemMonitor:      mining.NewClient(cfg.SystemMonitorAgentURL, cfg.SystemMonitorAgentToken),
		promptAssistant: promptassistant.NewClientWithPolicy(cfg.OllamaUpstream, cfg.PromptAssistantModel, promptassistant.ModelPolicy{
			ImageNumPredict: cfg.PromptAssistantImageNumPredict, ImageThinkNumPredict: cfg.PromptAssistantImageThinkNumPredict,
			VideoNumPredict: cfg.PromptAssistantVideoNumPredict, VideoThinkNumPredict: cfg.PromptAssistantVideoThinkNumPredict,
			ImageTimeout: cfg.PromptAssistantImageTimeout, VideoTimeout: cfg.PromptAssistantVideoTimeout,
			ReasoningTimeout: cfg.PromptAssistantReasoningTimeout,
			KeepAlive:        cfg.PromptAssistantKeepAlive,
		}).WithVisionModel(cfg.PromptAssistantVisionModel, cfg.PromptAssistantVisionTimeout, cfg.PromptAssistantVisionKeepAlive),
		contentModerator:     moderation.NewClient(cfg.ContentModeratorUpstream),
		updates:              updates.NewClient(cfg.UpdateAgentURL, cfg.UpdateAgentToken),
		loraTraining:         loratraining.NewClient(cfg.LoraTrainingAgentURL, cfg.LoraTrainingAgentToken),
		virusTotal:           virustotal.NewClient(cfg.VirusTotalAPIKey),
		contentCipher:        contentCipher,
		mediaCaptureSlots:    make(chan struct{}, maxConcurrentMediaCaptures),
		adminMediaSlots:      make(chan struct{}, maxConcurrentAdminMediaResponses),
		comfyUploadSlots:     make(chan struct{}, maxConcurrentComfyUploads),
		sensitiveMediaSlots:  make(chan struct{}, 1),
		comfyMemorySlots:     make(chan struct{}, 1),
		passwordWorkSlots:    make(chan struct{}, 4),
		promptAssistantSlots: make(chan struct{}, 1),
		mediaDownloadSlots:   make(chan struct{}, 4),
		comfyPromptSlots:     make(chan struct{}, maxComfyPromptAdmissionWaiters),
		generationJobs:       make(map[string]*generationJob),
		websocketConnections: make(map[*trackedWebSocket]struct{}),
		maintenanceWorkers:   newMaintenanceRegistry(time.Now),
		maintenanceDone:      make(chan struct{}),
		serviceLatencies:     newServiceLatencyRegistry(),
		mediaOperations:      newMediaOperationRegistry(),
		mediaBytes:           newWeightedByteLimiter(cfg.MediaInFlightLimitBytes),
		proxyCounts:          map[string]int64{},
	}

	app.gpuCoordinator = &gpuCoordinator{app: app}
	publicSrv := newHTTPServer(cfg.PublicAddr, app.securityHeaders(releaseValidationHTTP(cfg, db, app.publicMux())))
	adminSrv := newHTTPServer(cfg.AdminAddr, app.securityHeaders(releaseValidationHTTP(cfg, db, app.adminMux())))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.startBackgroundWork(ctx)

	errs := make(chan error, 2)
	go func() {
		log.Printf("public listener on %s", cfg.PublicAddr)
		errs <- publicSrv.ListenAndServe()
	}()
	go func() {
		log.Printf("admin listener on %s", cfg.AdminAddr)
		errs <- adminSrv.ListenAndServe()
	}()
	var serveErr error
	select {
	case err = <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	case <-ctx.Done():
	}
	stop()
	app.liveUpdates().close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = publicSrv.Shutdown(shutdownCtx)
	_ = adminSrv.Shutdown(shutdownCtx)
	select {
	case <-app.maintenanceDone:
	case <-shutdownCtx.Done():
		log.Printf("maintenance workers did not stop before shutdown timeout")
	}
	return serveErr
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func ParseTemplates() (*Templates, error) {
	tpl, err := template.New("views").Funcs(template.FuncMap{
		"formatTime":             formatTime,
		"inviteForm":             inviteForm,
		"userAccessForm":         userAccessForm,
		"adminProfileTabs":       adminProfileTabs,
		"adminProfileTaskStates": adminProfileTaskStates,
		"inviteDefaultsJSON":     inviteDefaultsJSON,
		"accessSummary":          accessSummary,
		"formatBytes":            formatBytes,
		"formatNumber":           formatNumber,
		"formatDuration":         formatDuration,
		"formatDurationLong":     formatDurationLong,
		"formatAgeSeconds":       formatAgeSeconds,
		"formatSignedBytes":      formatSignedBytes,
		"accountLifetimeLabel":   accountLifetimeLabel,
		"fileLabel":              russianFileLabel,
		"pct":                    pct,
		"divFloat":               divFloat,
		"roleLabel":              roleLabel,
		"inviteStatusLabel":      inviteStatusLabel,
		"auditActionLabel":       auditActionLabel,
		"auditTargetLabel":       auditTargetLabel,
		"auditMetadataLabel":     auditMetadataLabel,
		"generationStateLabel":   generationJobStateLabel,
		"observationOutcome":     serviceObservationOutcomeLabel,
		"componentLabel":         observabilityComponentLabel,
		"operationLabel":         observabilityOperationLabel,
		"hasString":              hasString,
		"workspaceShell":         workspaceShell,
	}).ParseFS(embeddedFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	for _, path := range frontendAssetPaths {
		body, err := embeddedFS.ReadFile(path)
		if err != nil {
			return nil, err
		}
		_, _ = hash.Write(body)
	}
	assetVersion := hex.EncodeToString(hash.Sum(nil))[:12]
	return &Templates{Template: tpl, AssetVersion: assetVersion}, nil
}

func hasString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (a *App) publicMux() http.Handler {
	mux := http.NewServeMux()
	a.registerStaticRoutes(mux)
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("/readyz", a.handleReadyz)
	mux.HandleFunc("/login", a.handleLogin)
	mux.HandleFunc("/logout", a.handleLogout)
	mux.HandleFunc("/invite/", a.handleInvite)
	mux.Handle("/admin", a.requireAdmin(http.HandlerFunc(a.handleAdminDashboard)))
	mux.Handle("/admin/", a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes)))
	mux.Handle("/app", a.requireAuth(http.HandlerFunc(a.handleApp)))
	mux.Handle("/suggestions", a.requireAuth(a.featureSuggestionsOnly(http.HandlerFunc(a.handleSuggestions))))
	mux.Handle("/suggestions/", a.requireAuth(a.featureSuggestionsOnly(http.HandlerFunc(a.handleSuggestions))))
	mux.Handle("/account/profile", a.requireAuth(http.HandlerFunc(a.handleAccountProfile)))
	mux.Handle("/account/theme", a.requireAuth(http.HandlerFunc(a.handleAccountTheme)))
	mux.Handle("/account/password", a.requireAuth(http.HandlerFunc(a.handleAccountPassword)))
	mux.Handle("/account/quick-generation-priority", a.requireAuth(http.HandlerFunc(a.handleAccountQuickGenerationPriority)))
	mux.Handle("/account/generation-mining", a.requireAuth(http.HandlerFunc(a.handleAccountGenerationMining)))
	mux.Handle("/account/sessions", a.requireAuth(http.HandlerFunc(a.handleAccountSessions)))
	mux.Handle("/mining/toggle", a.requireAuth(http.HandlerFunc(a.handleMiningToggle)))
	mux.Handle("/mining/icon/", a.requireAuth(http.HandlerFunc(a.handleMinerIcon)))
	a.registerNotificationRoutes(mux)
	a.registerGenerationRoutes(mux)
	a.registerLoraTrainingRoutes(mux)
	a.registerModelLibraryRoutes(mux)
	mux.Handle("/api/tasks", a.requireAuth(http.HandlerFunc(a.handleTasks)))
	mux.HandleFunc("/api/tasks/thumbnail", a.handleTaskThumbnail)
	mux.Handle("/api/tasks/detail", a.requireAuth(http.HandlerFunc(a.handleTaskDetail)))
	mux.Handle("/api/tasks/gpu", a.requireAuth(http.HandlerFunc(a.handleTaskGPU)))
	// The JSON handler authenticates itself and keeps 401 responses private/no-store.
	mux.HandleFunc("/api/tasks/gpu-snapshots", a.handleTaskGPUSnapshots)
	mux.Handle("/api/tasks/events", a.requireAuth(http.HandlerFunc(a.handleTaskEvents)))
	mux.Handle("/tasks", a.requireAuth(http.HandlerFunc(a.handleTasksPage)))
	a.registerServiceRoutes(mux)
	return mux
}

func (a *App) registerServiceRoutes(mux *http.ServeMux) {
	a.registerHarnessRoutes(mux)
	// Generation and Harness own their API routes; legacy browser service
	// selectors and root-relative upstream APIs must no longer be reachable.
	mux.HandleFunc("/", a.handleRoot)
}

func (a *App) adminMux() http.Handler {
	mux := http.NewServeMux()
	a.registerHarnessRoutes(mux)
	mux.Handle("/internal/gpu/harness/permit", a.adminLANOnly(http.HandlerFunc(a.handleHarnessGPUPermit)))
	mux.Handle("/internal/gpu/comfy/permit", a.adminLANOnly(http.HandlerFunc(a.handleComfyGPUPermit)))
	mux.Handle("/internal/gpu/ollama/permit", a.adminLANOnly(http.HandlerFunc(a.handleOllamaGPUPermit)))
	a.registerStaticRoutes(mux)
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("/readyz", a.handleReadyz)
	mux.HandleFunc("/login", a.handleLogin)
	mux.HandleFunc("/logout", a.handleLogout)
	mux.Handle("/account/profile", a.requireAuth(http.HandlerFunc(a.handleAccountProfile)))
	mux.Handle("/account/theme", a.requireAuth(http.HandlerFunc(a.handleAccountTheme)))
	mux.Handle("/account/quick-generation-priority", a.requireAuth(http.HandlerFunc(a.handleAccountQuickGenerationPriority)))
	mux.Handle("/account/generation-mining", a.requireAuth(http.HandlerFunc(a.handleAccountGenerationMining)))
	mux.Handle("/account/password", a.requireAuth(http.HandlerFunc(a.handleAccountPassword)))
	mux.Handle("/account/sessions", a.requireAuth(http.HandlerFunc(a.handleAccountSessions)))
	mux.Handle("/mining/toggle", a.requireAuth(http.HandlerFunc(a.handleMiningToggle)))
	mux.Handle("/mining/icon/", a.requireAuth(http.HandlerFunc(a.handleMinerIcon)))
	a.registerNotificationRoutes(mux)
	mux.Handle("/metrics", a.adminLANOnly(http.HandlerFunc(a.handlePrometheusMetrics)))
	a.registerPprofRoutes(mux)
	a.registerModelLibraryRoutes(mux)
	mux.Handle("/admin", a.requireLANAdmin(http.HandlerFunc(a.handleAdminDashboard)))
	mux.Handle("/admin/", a.requireLANAdmin(http.HandlerFunc(a.handleAdminRoutes)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			a.notFoundPage(w, r)
			return
		}
		http.Redirect(w, r, "/admin", http.StatusFound)
	})
	return withAdminWorkspace(mux)
}

func (a *App) registerStaticRoutes(mux *http.ServeMux) {
	for path := range staticCSSAssets {
		mux.HandleFunc(path, a.handleStaticCSS)
	}
	for path := range staticJavaScriptAssets {
		mux.HandleFunc(path, a.handleStaticJS)
	}
	mux.HandleFunc("/static/fonts/", a.handleStaticFont)
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		a.requestsTotal.Add(1)
		id := newRequestID()
		correlation := strings.TrimSpace(r.Header.Get(correlationHeader))
		if !validCorrelationID(correlation) {
			correlation = id
		}
		w.Header().Set("X-Request-ID", id)
		w.Header().Set(correlationHeader, correlation)
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(self), microphone=(self), geolocation=()")
		if a.requestProto(r) == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		ctx = context.WithValue(ctx, correlationIDKey, correlation)
		stateWriter := &captureWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				logGateway(ctx, slog.LevelError, "http_panic", "Unhandled request panic", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(recovered))
				_, proxyRequest := requestedService(r)
				if !proxyRequest && !strings.HasPrefix(r.URL.Path, "/comfyui/") && !strings.HasPrefix(r.URL.Path, "/openwebui/") {
					http.Error(stateWriter, "внутренняя ошибка сервера", http.StatusInternalServerError)
				}
			}
			status := stateWriter.status
			if status == 0 {
				status = http.StatusOK
			}
			logHTTPRequest(ctx, r, status, stateWriter.bytes, started)
		}()
		next.ServeHTTP(stateWriter, r.WithContext(ctx))
	})
}

func newRequestID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b)
}

func (a *App) handleStaticCSS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	assetPath, ok := staticCSSAssets[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := embeddedFS.ReadFile(assetPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(body)
}

func (a *App) handleStaticJS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	asset, ok := staticJavaScriptAssets[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := embeddedFS.ReadFile(asset)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(body)
}

func (a *App) handleStaticFont(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	asset := ""
	switch r.URL.Path {
	case "/static/fonts/manrope-cyrillic.woff2":
		asset = "static/fonts/manrope-cyrillic.woff2"
	case "/static/fonts/manrope-latin.woff2":
		asset = "static/fonts/manrope-latin.woff2"
	default:
		http.NotFound(w, r)
		return
	}
	body, err := embeddedFS.ReadFile(asset)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "font/woff2")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(body)
}

func (a *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		http.Error(w, "база данных недоступна", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (a *App) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	started := time.Now()
	databaseErr := a.store.Ping(ctx)
	a.observeServiceCall(r.Context(), "database", "readiness", started, databaseErr, false, "database_ping_failed", "")
	dependencies := a.dependencyStatuses()
	status, degraded := readinessStatus(databaseErr, dependencies)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, map[string]any{
		"ready":       databaseErr == nil,
		"degraded":    degraded,
		"checked_at":  time.Now().UTC(),
		"required":    map[string]any{"database": map[string]any{"ready": databaseErr == nil, "latency_ms": time.Since(started).Milliseconds()}},
		"optional":    dependencies,
		"correlation": correlationID(r),
	})
}

func readinessStatus(databaseErr error, dependencies []DependencyStatus) (int, bool) {
	degraded := false
	for _, dependency := range dependencies {
		if dependency.State != DependencyOnline {
			degraded = true
			break
		}
	}
	if databaseErr != nil {
		return http.StatusServiceUnavailable, degraded
	}
	return http.StatusOK, degraded
}

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		a.notFoundPage(w, r)
		return
	}
	if a.currentUser(r) != nil {
		http.Redirect(w, r, "/app", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, data map[string]any) {
	a.renderStatus(w, r, http.StatusOK, name, data)
}

func (a *App) renderStatus(w http.ResponseWriter, r *http.Request, status int, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	if _, ok := data["Title"]; !ok {
		data["Title"] = "AI Access Gateway"
	}
	currentUser := a.currentUser(r)
	data["CurrentUser"] = currentUser
	if _, provided := data["NotificationSummary"]; !provided && currentUser != nil && a.store != nil {
		if summary, err := a.store.UserNotificationSummary(r.Context(), currentUser.ID); err == nil {
			data["NotificationSummary"] = summary
		}
	}
	data["CSRF"] = a.csrfToken(r)
	data["PublicBaseURL"] = a.cfg.PublicBaseURL
	data["AdminBaseURL"] = a.cfg.AdminBaseURL
	data["RequestID"] = requestID(r)
	data["AssetVersion"] = a.tpl.AssetVersion
	data["ThemePreference"] = ThemePreference(r)
	data["NavigationPath"] = r.URL.Path
	if name == "admin_release" {
		data["NavigationPath"] = "/admin/updates"
	}
	data["AdminWorkspace"], _ = r.Context().Value(adminWorkspaceKey).(bool)
	data["FeatureSuggestionsEnabled"] = a.cfg.FeatureSuggestionsEnabled
	data["Retention"] = newRetentionPolicyView(a.retentionPolicy())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := a.tpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template %s: %v", name, err)
	}
}

func requestID(r *http.Request) string {
	return requestIDFromContext(r.Context())
}
