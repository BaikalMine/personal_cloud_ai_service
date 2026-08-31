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
	t.Setenv("GENERATION_RETENTION", "30h")
	t.Setenv("AI_CONTENT_RETENTION", "240h")
	t.Setenv("COMFY_INPUT_RETENTION", "96h")
	t.Setenv("HOST_METRIC_RETENTION", "240h")
	t.Setenv("AUDIT_LOG_RETENTION", "2400h")
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
	if cfg.Retention.GenerationHistory != 30*time.Hour || cfg.Retention.GenerationMedia != 30*time.Hour || cfg.Retention.AIContent != 240*time.Hour || cfg.Retention.ComfyInputs != 96*time.Hour || cfg.Retention.HostMetrics != 240*time.Hour || cfg.Retention.AuditLog != 2400*time.Hour {
		t.Fatalf("unexpected retention policy: %+v", cfg.Retention)
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
