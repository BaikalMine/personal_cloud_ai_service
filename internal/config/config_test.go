package config

import (
	"strings"
	"testing"
	"time"
)

func TestComfyGPUGuardConfiguration(t *testing.T) {
	testGPUGuardConfiguration(t, "COMFY")
}

func TestOllamaGPUGuardConfiguration(t *testing.T) {
	testGPUGuardConfiguration(t, "OLLAMA")
}

func testGPUGuardConfiguration(t *testing.T, executor string) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	if executor == "OLLAMA" {
		t.Setenv("LORA_CAPTION_DURABLE_INFERENCE", "true")
		t.Setenv("PROMPT_ASSISTANT_DURABLE_JOBS", "true")
	}
	for _, tc := range []struct {
		name, required, fenced, key string
		ok                          bool
	}{
		{"default", "", "", "", true},
		{"enabled", "true", "true", strings.Repeat("k", 64), true},
		{"no mining fence", "true", "false", strings.Repeat("k", 64), false},
		{"short key", "true", "true", "short", false},
		{"long key", "true", "true", strings.Repeat("k", 257), false},
		{"whitespace key", "true", "true", strings.Repeat("k", 32) + " key", false},
		{"non ASCII key", "true", "true", strings.Repeat("k", 32) + "\u00e9", false},
		{"invalid switch", "invalid", "true", strings.Repeat("k", 64), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(executor+"_GPU_GUARD_REQUIRED", tc.required)
			t.Setenv(executor+"_GPU_GUARD_TOKEN", tc.key)
			t.Setenv("MINING_FENCED_COMMANDS", tc.fenced)
			cfg, err := Load()
			if (err == nil) != tc.ok {
				t.Fatalf("configuration: %v", err)
			}
			enabled := cfg.ComfyGPUGuardRequired
			if executor == "OLLAMA" {
				enabled = cfg.OllamaGPUGuardRequired
			}
			if tc.ok && enabled != (tc.required == "true") {
				t.Fatal("incorrect enabled state")
			}
		})
	}
}

func TestOllamaGPUGuardRequiresSeparateCredential(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	t.Setenv("MINING_FENCED_COMMANDS", "true")
	t.Setenv("LORA_CAPTION_DURABLE_INFERENCE", "true")
	t.Setenv("PROMPT_ASSISTANT_DURABLE_JOBS", "true")
	for _, prefix := range []string{"OLLAMA", "COMFY"} {
		t.Setenv(prefix+"_GPU_GUARD_REQUIRED", "true")
		t.Setenv(prefix+"_GPU_GUARD_TOKEN", strings.Repeat("k", 64))
	}
	if _, err := Load(); err == nil {
		t.Fatal("shared executor credential accepted")
	}
	t.Setenv("OLLAMA_GPU_GUARD_TOKEN", strings.Repeat("o", 64))
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"LORA_CAPTION_DURABLE_INFERENCE", "PROMPT_ASSISTANT_DURABLE_JOBS"} {
		t.Run(flag, func(t *testing.T) {
			t.Setenv(flag, "false")
			if _, err := Load(); err == nil {
				t.Fatal("direct model path accepted with owned admission required")
			}
		})
	}
}

func TestRestoreDrillModeConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("RESTORE_DRILL_MODE", value)
			cfg, err := Load()
			if value == "invalid" {
				if err == nil || !strings.Contains(err.Error(), "RESTORE_DRILL_MODE") {
					t.Fatal(err)
				}
				return
			}
			if err != nil || cfg.RestoreDrillMode != (value == "true") {
				t.Fatal(cfg.RestoreDrillMode, err)
			}
		})
	}
}

func TestInferenceWorkerURLConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	for _, address := range []string{"", "http://127.0.0.1:8096", "https://worker.example.test", "file:///tmp/worker", "http://secret:password@worker", "invalid"} {
		t.Run(address, func(t *testing.T) {
			t.Setenv("INFERENCE_WORKER_URL", address)
			cfg, err := Load()
			valid := address == "" || address == "http://127.0.0.1:8096" || address == "https://worker.example.test"
			if (err == nil) != valid {
				t.Fatal(err)
			}
			if valid && cfg.InferenceWorkerURL == nil {
				t.Fatal("missing endpoint")
			}
		})
	}
}

func TestDurableAssistantConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PROMPT_ASSISTANT_DURABLE_JOBS", value)
			cfg, err := Load()
			if value == "invalid" {
				if err == nil || !strings.Contains(err.Error(), "PROMPT_ASSISTANT_DURABLE_JOBS") {
					t.Fatalf("invalid switch: %v", err)
				}
				return
			}
			if err != nil || cfg.PromptAssistantDurableJobs != (value == "true") {
				t.Fatalf("switch %q: %v %v", value, cfg.PromptAssistantDurableJobs, err)
			}
		})
	}
}

func TestDurableCaptionConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("LORA_CAPTION_DURABLE_INFERENCE", value)
			cfg, err := Load()
			if value == "invalid" {
				if err == nil || !strings.Contains(err.Error(), "LORA_CAPTION_DURABLE_INFERENCE") {
					t.Fatalf("invalid switch: %v", err)
				}
				return
			}
			if err != nil || cfg.LoraCaptionDurableInference != (value == "true") {
				t.Fatalf("switch %q: %v %v", value, cfg.LoraCaptionDurableInference, err)
			}
		})
	}
}

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
	t.Setenv("PROMPT_ASSISTANT_VISION_MODEL", "huihui_ai/Qwen3.8-abliterated:27b-q8_0")
	t.Setenv("PROMPT_ASSISTANT_IMAGE_NUM_PREDICT", "1024")
	t.Setenv("PROMPT_ASSISTANT_IMAGE_THINK_NUM_PREDICT", "1792")
	t.Setenv("PROMPT_ASSISTANT_VIDEO_NUM_PREDICT", "3072")
	t.Setenv("PROMPT_ASSISTANT_VIDEO_THINK_NUM_PREDICT", "5120")
	t.Setenv("PROMPT_ASSISTANT_IMAGE_TIMEOUT", "90s")
	t.Setenv("PROMPT_ASSISTANT_VIDEO_TIMEOUT", "6m")
	t.Setenv("PROMPT_ASSISTANT_KEEP_ALIVE", "30s")
	t.Setenv("PROMPT_ASSISTANT_VISION_TIMEOUT", "7m")
	t.Setenv("PROMPT_ASSISTANT_VISION_KEEP_ALIVE", "45s")

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
	if cfg.OllamaUpstream == nil || cfg.OllamaUpstream.Host != "host.docker.internal:11434" ||
		cfg.PromptAssistantModel != "huihui_ai/gemma-4-abliterated:e4b" ||
		cfg.PromptAssistantVisionModel != "huihui_ai/Qwen3.8-abliterated:27b-q8_0" {
		t.Fatalf("prompt assistant config was not parsed: %+v", cfg)
	}
	if cfg.PromptAssistantImageNumPredict != 1024 || cfg.PromptAssistantImageThinkNumPredict != 1792 ||
		cfg.PromptAssistantVideoNumPredict != 3072 || cfg.PromptAssistantVideoThinkNumPredict != 5120 ||
		cfg.PromptAssistantImageTimeout != 90*time.Second || cfg.PromptAssistantVideoTimeout != 6*time.Minute ||
		cfg.PromptAssistantKeepAlive != "30s" || cfg.PromptAssistantVisionTimeout != 7*time.Minute ||
		cfg.PromptAssistantVisionKeepAlive != "45s" {
		t.Fatalf("prompt assistant model policy was not parsed: %+v", cfg)
	}
}

func TestRejectsInvalidPromptAssistantPolicy(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	t.Setenv("PROMPT_ASSISTANT_VIDEO_NUM_PREDICT", "64")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PROMPT_ASSISTANT_VIDEO_NUM_PREDICT") {
		t.Fatalf("expected video token budget validation error, got %v", err)
	}

	t.Setenv("PROMPT_ASSISTANT_VIDEO_NUM_PREDICT", "2400")
	t.Setenv("PROMPT_ASSISTANT_KEEP_ALIVE", "90m")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PROMPT_ASSISTANT_KEEP_ALIVE") {
		t.Fatalf("expected keep-alive validation error, got %v", err)
	}

	t.Setenv("PROMPT_ASSISTANT_KEEP_ALIVE", "0")
	t.Setenv("PROMPT_ASSISTANT_VISION_KEEP_ALIVE", "90m")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PROMPT_ASSISTANT_VISION_KEEP_ALIVE") {
		t.Fatalf("expected vision keep-alive validation error, got %v", err)
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
