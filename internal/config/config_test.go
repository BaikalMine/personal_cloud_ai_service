package config

import (
	"testing"
	"time"
)

func TestLoadValidConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_USERNAME", "admin")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	t.Setenv("PUBLIC_BASE_URL", "https://ai.example.test")
	t.Setenv("ADMIN_BASE_URL", "http://127.0.0.1:8091")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("SESSION_TTL", "48h")
	t.Setenv("SESSION_IDLE_TIMEOUT", "12h")
	t.Setenv("ACCOUNT_LOCK_THRESHOLD", "7")
	t.Setenv("ACCOUNT_LOCK_DURATION", "20m")
	t.Setenv("DEPENDENCY_CHECK_INTERVAL", "12s")
	t.Setenv("DEPENDENCY_STALE_AFTER", "36s")
	t.Setenv("DEPENDENCY_OFFLINE_AFTER", "4m")
	t.Setenv("COMFY_OBJECT_INFO_CACHE_TTL", "45s")
	t.Setenv("COMFY_OBJECT_INFO_MAX_STALE", "12h")
	t.Setenv("MEDIA_INFLIGHT_LIMIT_MB", "384")
	t.Setenv("MEDIA_SPOOL_DIR", t.TempDir())
	t.Setenv("PPROF_ENABLED", "true")
	t.Setenv("GENERATION_RETENTION", "30h")
	t.Setenv("PINNED_GENERATION_RETENTION", "720h")
	t.Setenv("AI_CONTENT_RETENTION", "240h")
	t.Setenv("COMFY_INPUT_RETENTION", "96h")
	t.Setenv("HOST_METRIC_RETENTION", "240h")
	t.Setenv("AUDIT_LOG_RETENTION", "2400h")
	t.Setenv("PROXY_REQUEST_RETENTION", "2880h")
	t.Setenv("WEBSOCKET_SESSION_RETENTION", "1440h")
	t.Setenv("GENERATION_REQUEST_RETENTION", "240h")
	t.Setenv("DAILY_USAGE_RETENTION", "3360h")
	t.Setenv("INVITE_HISTORY_RETENTION", "4320h")
	t.Setenv("DATABASE_CLEANUP_BATCH_SIZE", "777")
	t.Setenv("DATABASE_CLEANUP_MAX_BATCHES", "9")
	t.Setenv("TRUSTED_PROXIES", "198.51.100.10,127.0.0.1")
	t.Setenv("ADMIN_ALLOWED_CIDRS", "10.0.0.0/24,127.0.0.1")
	t.Setenv("COMFYUI_UPSTREAM", "http://host.docker.internal:8088")
	t.Setenv("OPENWEBUI_UPSTREAM", "http://host.docker.internal:8089")
	t.Setenv("OLLAMA_UPSTREAM", "http://host.docker.internal:11434")
	t.Setenv("PROMPT_ASSISTANT_MODEL", "huihui_ai/gemma-4-abliterated:e4b")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicBaseURL != "https://ai.example.test" || cfg.SessionTTL != 48*time.Hour || !cfg.CookieSecure {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.PublicURL == nil || cfg.PublicURL.Host != "ai.example.test" || cfg.PublicURL.Scheme != "https" {
		t.Fatalf("canonical public URL was not retained: %+v", cfg.PublicURL)
	}
	if cfg.AdminBaseURL != "http://127.0.0.1:8091" || cfg.AdminURL == nil || cfg.AdminURL.Port() != "8091" {
		t.Fatalf("canonical admin URL was not retained: %+v", cfg.AdminURL)
	}
	if cfg.SessionIdleTimeout != 12*time.Hour || cfg.AccountLockThreshold != 7 || cfg.AccountLockDuration != 20*time.Minute {
		t.Fatalf("unexpected auth policy: %+v", cfg)
	}
	if cfg.DependencyCheckInterval != 12*time.Second || cfg.DependencyStaleAfter != 36*time.Second || cfg.DependencyOfflineAfter != 4*time.Minute {
		t.Fatalf("unexpected dependency health policy: %+v", cfg)
	}
	if cfg.ComfyObjectInfoCacheTTL != 45*time.Second || cfg.ComfyObjectInfoMaxStale != 12*time.Hour {
		t.Fatalf("unexpected ComfyUI schema cache policy: %+v", cfg)
	}
	if cfg.MediaInFlightLimitBytes != 384<<20 || cfg.MediaSpoolDir == "" || !cfg.PprofEnabled {
		t.Fatalf("unexpected media memory policy: bytes=%d spool=%q pprof=%t", cfg.MediaInFlightLimitBytes, cfg.MediaSpoolDir, cfg.PprofEnabled)
	}
	if cfg.Retention.GenerationHistory != 30*time.Hour || cfg.Retention.GenerationMedia != 30*time.Hour || cfg.Retention.PinnedMedia != 720*time.Hour || cfg.Retention.AIContent != 240*time.Hour || cfg.Retention.ComfyInputs != 96*time.Hour || cfg.Retention.HostMetrics != 240*time.Hour || cfg.Retention.AuditLog != 2400*time.Hour || cfg.Retention.ProxyRequests != 2880*time.Hour || cfg.Retention.WebSocketSessions != 1440*time.Hour || cfg.Retention.GenerationRequests != 240*time.Hour || cfg.Retention.DailyUsage != 3360*time.Hour || cfg.Retention.InviteHistory != 4320*time.Hour {
		t.Fatalf("unexpected retention policy: %+v", cfg.Retention)
	}
	if cfg.DatabaseCleanupBatchSize != 777 || cfg.DatabaseCleanupMaxBatches != 9 {
		t.Fatalf("unexpected cleanup limits: batch=%d max_batches=%d", cfg.DatabaseCleanupBatchSize, cfg.DatabaseCleanupMaxBatches)
	}
	if len(cfg.TrustedProxies) != 2 || len(cfg.AdminAllowedNetworks) != 2 {
		t.Fatal("network allow-lists were not parsed")
	}
	if cfg.OllamaUpstream == nil || cfg.OllamaUpstream.Host != "host.docker.internal:11434" || cfg.PromptAssistantModel != "huihui_ai/gemma-4-abliterated:e4b" {
		t.Fatalf("prompt assistant config was not parsed: %+v", cfg)
	}
}

func TestDefaultRetentionPolicyKeepsGenerationHistoryAndMediaTogether(t *testing.T) {
	policy := (RetentionPolicy{GenerationHistory: 12 * time.Hour, AIContent: time.Hour}).WithDefaults()
	if policy.GenerationHistory != 12*time.Hour || policy.GenerationMedia != 12*time.Hour {
		t.Fatalf("generation retention was not unified: %+v", policy)
	}
	if policy.PinnedMedia != 30*24*time.Hour {
		t.Fatalf("pinned media retention default was not applied: %+v", policy)
	}
	if policy.AIContent != 12*time.Hour {
		t.Fatalf("AI content must not expire before generation media: %+v", policy)
	}
	defaults := (RetentionPolicy{}).WithDefaults()
	if defaults != DefaultRetentionPolicy() {
		t.Fatalf("zero policy defaults = %+v, want %+v", defaults, DefaultRetentionPolicy())
	}
}

func TestRejectsAIContentRetentionShorterThanGeneration(t *testing.T) {
	t.Setenv("GENERATION_RETENTION", "48h")
	t.Setenv("AI_CONTENT_RETENTION", "24h")
	if _, err := loadRetentionPolicy(); err == nil {
		t.Fatal("expected retention ordering validation error")
	}
}

func TestRejectsPinnedMediaRetentionShorterThanGeneration(t *testing.T) {
	t.Setenv("GENERATION_RETENTION", "48h")
	t.Setenv("PINNED_GENERATION_RETENTION", "24h")
	if _, err := loadRetentionPolicy(); err == nil {
		t.Fatal("expected pinned media retention ordering validation error")
	}
}

func TestRejectsGenerationRequestRetentionShorterThanGeneration(t *testing.T) {
	t.Setenv("GENERATION_RETENTION", "48h")
	t.Setenv("AI_CONTENT_RETENTION", "72h")
	t.Setenv("GENERATION_REQUEST_RETENTION", "24h")
	if _, err := loadRetentionPolicy(); err == nil {
		t.Fatal("expected generation request retention ordering validation error")
	}
}

func TestRejectsIdleTimeoutBeyondSessionTTL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	t.Setenv("SESSION_TTL", "2h")
	t.Setenv("SESSION_IDLE_TIMEOUT", "3h")
	if _, err := Load(); err == nil {
		t.Fatal("expected SESSION_IDLE_TIMEOUT validation error")
	}
}

func TestSecureCookieRequiresHTTPSOrigin(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	t.Setenv("PUBLIC_BASE_URL", "http://127.0.0.1:8090")
	t.Setenv("COOKIE_SECURE", "true")
	if _, err := Load(); err == nil {
		t.Fatal("expected COOKIE_SECURE validation error")
	}
}

func TestRejectsCredentialsInsideUpstreamURL(t *testing.T) {
	if _, err := parseUpstream("http://user:password@example.test"); err == nil {
		t.Fatal("expected credentials in upstream URL to be rejected")
	}
}

func TestRejectsInvalidDependencyHealthIntervals(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	t.Setenv("DEPENDENCY_CHECK_INTERVAL", "20s")
	t.Setenv("DEPENDENCY_STALE_AFTER", "30s")
	if _, err := Load(); err == nil {
		t.Fatal("expected stale interval validation error")
	}
}

func TestRejectsObjectInfoMaxStaleShorterThanCacheTTL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	t.Setenv("COMFY_OBJECT_INFO_CACHE_TTL", "2m")
	t.Setenv("COMFY_OBJECT_INFO_MAX_STALE", "1m")
	if _, err := Load(); err == nil {
		t.Fatal("expected object-info cache ordering validation error")
	}
}

func TestRejectsInvalidMediaMemoryPolicy(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	t.Setenv("MEDIA_INFLIGHT_LIMIT_MB", "32")
	if _, err := Load(); err == nil {
		t.Fatal("expected media in-flight limit validation error")
	}
	t.Setenv("MEDIA_INFLIGHT_LIMIT_MB", "256")
	t.Setenv("MEDIA_SPOOL_DIR", "relative/spool")
	if _, err := Load(); err == nil {
		t.Fatal("expected media spool path validation error")
	}
	t.Setenv("MEDIA_SPOOL_DIR", t.TempDir())
	t.Setenv("PPROF_ENABLED", "sometimes")
	if _, err := Load(); err == nil {
		t.Fatal("expected pprof boolean validation error")
	}
}
