package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ai-access-gateway/internal/config"
	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/database"
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

type Templates struct {
	*template.Template
	AssetVersion string
}

func Run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 2*time.Minute)
	db, err := database.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		cancelStartup()
		return err
	}
	defer db.Close()
	if err := database.Migrate(startupCtx, db); err != nil {
		cancelStartup()
		return err
	}
	repository := store.New(db)
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
	cancelStartup()
	if err != nil {
		return err
	}
	if temporaryCleanupErr != nil {
		return temporaryCleanupErr
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

	tpl, err := ParseTemplates()
	if err != nil {
		return err
	}
	contentCipher, err := contentcrypto.NewCipher(cfg.SessionSecret)
	if err != nil {
		return err
	}

	app := &App{
		cfg:                  cfg,
		tpl:                  tpl,
		loginLimiter:         security.NewLoginLimiter(10*time.Minute, 10),
		loginIPLimiter:       security.NewLoginLimiter(10*time.Minute, 30),
		loginAuditLimiter:    security.NewLoginLimiter(time.Minute, 1),
		inviteLimiter:        security.NewLoginLimiter(10*time.Minute, 20),
		comfyPromptLimiter:   security.NewLoginLimiter(time.Minute, 10),
		csrfSigner:           security.NewCSRFSigner(cfg.SessionSecret),
		store:                repository,
		mining:               mining.NewClient(cfg.MiningAgentURL, cfg.MiningAgentToken),
		systemMonitor:        mining.NewClient(cfg.SystemMonitorAgentURL, cfg.SystemMonitorAgentToken),
		promptAssistant:      promptassistant.NewClient(cfg.OllamaUpstream, cfg.PromptAssistantModel),
		contentModerator:     moderation.NewClient(cfg.ContentModeratorUpstream),
		updates:              updates.NewClient(cfg.UpdateAgentURL, cfg.UpdateAgentToken),
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
		proxyCounts:          map[string]int64{},
	}

	publicSrv := newHTTPServer(cfg.PublicAddr, app.securityHeaders(app.publicMux()))
	adminSrv := newHTTPServer(cfg.AdminAddr, app.securityHeaders(app.adminMux()))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go app.runMaintenance(ctx)
	go func() {
		classificationCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		app.classifyPendingSensitiveContent(classificationCtx)
	}()
	app.queueSensitiveMediaClassification()

	errs := make(chan error, 2)
	go func() {
		log.Printf("public listener on %s", cfg.PublicAddr)
		errs <- publicSrv.ListenAndServe()
	}()
	go func() {
		log.Printf("admin listener on %s", cfg.AdminAddr)
		errs <- adminSrv.ListenAndServe()
	}()
	select {
	case err = <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = publicSrv.Shutdown(shutdownCtx)
		_ = adminSrv.Shutdown(shutdownCtx)
	}
	return nil
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
		"formatTime":           formatTime,
		"formatBytes":          formatBytes,
		"formatNumber":         formatNumber,
		"formatDuration":       formatDuration,
		"accountLifetimeLabel": accountLifetimeLabel,
		"fileLabel":            russianFileLabel,
		"pct":                  pct,
		"divFloat":             divFloat,
		"roleLabel":            roleLabel,
		"inviteStatusLabel":    inviteStatusLabel,
		"auditActionLabel":     auditActionLabel,
		"auditTargetLabel":     auditTargetLabel,
		"auditMetadataLabel":   auditMetadataLabel,
		"hasString":            hasString,
	}).ParseFS(embeddedFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	for _, path := range []string{"static/style.css", "static/app.js", "static/generate.js", "static/gallery.js"} {
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
	mux.HandleFunc("/static/style.css", a.handleStaticCSS)
	mux.HandleFunc("/static/app.js", a.handleStaticJS)
	mux.HandleFunc("/static/generate.js", a.handleStaticJS)
	mux.HandleFunc("/static/gallery.js", a.handleStaticJS)
	mux.HandleFunc("/static/fonts/", a.handleStaticFont)
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("/login", a.handleLogin)
	mux.HandleFunc("/logout", a.handleLogout)
	mux.HandleFunc("/invite/", a.handleInvite)
	mux.Handle("/admin", a.requireAdmin(http.HandlerFunc(a.handleAdminDashboard)))
	mux.Handle("/admin/", a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes)))
	mux.Handle("/app", a.requireAuth(http.HandlerFunc(a.handleApp)))
	mux.Handle("/suggestions", a.requireAuth(a.featureSuggestionsOnly(http.HandlerFunc(a.handleSuggestions))))
	mux.Handle("/account/profile", a.requireAuth(http.HandlerFunc(a.handleAccountProfile)))
	mux.Handle("/account/password", a.requireAuth(http.HandlerFunc(a.handleAccountPassword)))
	mux.Handle("/account/quick-generation-priority", a.requireAuth(http.HandlerFunc(a.handleAccountQuickGenerationPriority)))
	mux.Handle("/account/sessions", a.requireAuth(http.HandlerFunc(a.handleAccountSessions)))
	mux.Handle("/mining/toggle", a.requireAuth(http.HandlerFunc(a.handleMiningToggle)))
	mux.Handle("/mining/icon/", a.requireAuth(http.HandlerFunc(a.handleMinerIcon)))
	a.registerGenerationRoutes(mux)
	a.registerServiceRoutes(mux)
	return mux
}

func (a *App) registerServiceRoutes(mux *http.ServeMux) {
	comfyUI := a.requireServiceAccess("comfyui", a.proxyRootHandler("comfyui", a.cfg.ComfyUIUpstream, a.cfg.ComfyUIUpstreamAuthHeader))
	openWebUI := a.requireServiceAccess("openwebui", a.proxyRootHandler("openwebui", a.cfg.OpenWebUIUpstream, a.cfg.OpenWebUIUpstreamAuth))
	services := a.requireAuth(a.serviceCompatibilityRouter(comfyUI, openWebUI))

	comfySelector := a.requireAuth(a.requireServiceAccess("comfyui", a.serviceSelector("comfyui")))
	openSelector := a.requireAuth(a.requireServiceAccess("openwebui", a.serviceSelector("openwebui")))
	mux.Handle("/comfyui", comfySelector)
	mux.Handle("/comfyui/", comfySelector)
	mux.Handle("/openwebui", openSelector)
	mux.Handle("/openwebui/", openSelector)
	mux.Handle("/", a.rootOrServices(services))
}

func (a *App) adminMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/static/style.css", a.handleStaticCSS)
	mux.HandleFunc("/static/app.js", a.handleStaticJS)
	mux.HandleFunc("/static/generate.js", a.handleStaticJS)
	mux.HandleFunc("/static/gallery.js", a.handleStaticJS)
	mux.HandleFunc("/static/fonts/", a.handleStaticFont)
	mux.HandleFunc("/healthz", a.handleHealthz)
	mux.HandleFunc("/login", a.handleLogin)
	mux.HandleFunc("/logout", a.handleLogout)
	mux.Handle("/account/profile", a.requireAuth(http.HandlerFunc(a.handleAccountProfile)))
	mux.Handle("/account/password", a.requireAuth(http.HandlerFunc(a.handleAccountPassword)))
	mux.Handle("/account/sessions", a.requireAuth(http.HandlerFunc(a.handleAccountSessions)))
	mux.Handle("/mining/toggle", a.requireAuth(http.HandlerFunc(a.handleMiningToggle)))
	mux.Handle("/mining/icon/", a.requireAuth(http.HandlerFunc(a.handleMinerIcon)))
	mux.Handle("/metrics", a.adminLANOnly(http.HandlerFunc(a.handlePrometheusMetrics)))
	mux.Handle("/admin", a.requireLANAdmin(http.HandlerFunc(a.handleAdminDashboard)))
	mux.Handle("/admin/", a.requireLANAdmin(http.HandlerFunc(a.handleAdminRoutes)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusFound)
	})
	return mux
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requestsTotal.Add(1)
		id := newRequestID()
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(self), microphone=(self), geolocation=()")
		if a.requestProto(r) == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic request_id=%s method=%s path=%s: %v", id, r.Method, r.URL.Path, recovered)
				_, proxyRequest := requestedService(r)
				if !proxyRequest && !strings.HasPrefix(r.URL.Path, "/comfyui/") && !strings.HasPrefix(r.URL.Path, "/openwebui/") {
					http.Error(w, "внутренняя ошибка сервера", http.StatusInternalServerError)
				}
			}
		}()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
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
	body, err := embeddedFS.ReadFile("static/style.css")
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
	asset := "static/app.js"
	if r.URL.Path == "/static/generate.js" {
		asset = "static/generate.js"
	} else if r.URL.Path == "/static/gallery.js" {
		asset = "static/gallery.js"
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

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
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
	data["CurrentUser"] = a.currentUser(r)
	data["CSRF"] = a.csrfToken(r)
	data["PublicBaseURL"] = a.cfg.PublicBaseURL
	data["AdminBaseURL"] = a.cfg.AdminBaseURL
	data["RequestID"] = requestID(r)
	data["AssetVersion"] = a.tpl.AssetVersion
	data["FeatureSuggestionsEnabled"] = a.cfg.FeatureSuggestionsEnabled
	data["Retention"] = newRetentionPolicyView(a.retentionPolicy())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := a.tpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template %s: %v", name, err)
	}
}

func requestID(r *http.Request) string {
	if id, ok := r.Context().Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}
