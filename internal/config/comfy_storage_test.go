package config

import (
	"strings"
	"testing"
)

func TestComfyStorageCleanupConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("MINING_FENCED_COMMANDS", "true")
	t.Setenv("COMFY_GPU_GUARD_TOKEN", strings.Repeat("g", 32))
	for _, tc := range []struct {
		flag, guard, token string
		ok                 bool
	}{
		{"", "false", "", true},
		{"false", "false", "", true},
		{"invalid", "true", strings.Repeat("u", 32), false},
		{"true", "false", strings.Repeat("u", 32), false},
		{"true", "true", "", false},
		{"true", "true", strings.Repeat("u", 32), true},
	} {
		t.Run(tc.flag+"/"+tc.guard+"/"+tc.token, func(t *testing.T) {
			t.Setenv("COMFY_STORAGE_CLEANUP_ENABLED", tc.flag)
			t.Setenv("COMFY_GPU_GUARD_REQUIRED", tc.guard)
			t.Setenv("UPDATE_AGENT_URL", "http://host.docker.internal:8093")
			t.Setenv("UPDATE_AGENT_TOKEN", tc.token)
			cfg, err := Load()
			if (err == nil) != tc.ok {
				t.Fatal(cfg.ComfyStorageCleanupEnabled, err)
			}
			if tc.ok && cfg.ComfyStorageCleanupEnabled != (tc.flag == "true") {
				t.Fatal("incorrect cleanup flag")
			}
		})
	}
}
