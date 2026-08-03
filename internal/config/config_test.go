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
	t.Setenv("TRUSTED_PROXIES", "198.51.100.10,127.0.0.1")
	t.Setenv("ADMIN_ALLOWED_CIDRS", "10.0.0.0/24,127.0.0.1")
	t.Setenv("COMFYUI_UPSTREAM", "http://host.docker.internal:8088")
	t.Setenv("OPENWEBUI_UPSTREAM", "http://host.docker.internal:8089")

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
	if len(cfg.TrustedProxies) != 2 || len(cfg.AdminAllowedNetworks) != 2 {
		t.Fatal("network allow-lists were not parsed")
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
