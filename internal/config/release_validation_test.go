package config

import (
	"strings"
	"testing"
)

func TestReleaseValidationConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 40))
	base := map[string]string{
		"RELEASE_VALIDATION_MODE": "true", "RELEASE_OPERATION_ID": strings.Repeat("a", 32),
		"RELEASE_OPERATION_TOKEN": strings.Repeat("b", 64), "RELEASE_OPERATION_EPOCH": "1",
		"RELEASE_CANDIDATE_MANIFEST_SHA256": strings.Repeat("c", 64),
		"PUBLIC_ADDR":                       "127.0.0.1:18080", "ADMIN_ADDR": "127.0.0.1:18081", "RESTORE_DRILL_MODE": "false",
	}
	for key, value := range base {
		t.Setenv(key, value)
	}
	for _, tc := range []struct {
		name, key, value string
		valid            bool
	}{
		{"valid", "RELEASE_OPERATION_EPOCH", "12", true},
		{"ipv6 loopback", "ADMIN_ADDR", "[::1]:18081", true},
		{"missing mode", "RELEASE_VALIDATION_MODE", "", false},
		{"disabled credentials", "RELEASE_VALIDATION_MODE", "false", false},
		{"invalid mode", "RELEASE_VALIDATION_MODE", "maybe", false},
		{"restore collision", "RESTORE_DRILL_MODE", "true", false},
		{"missing identity", "RELEASE_OPERATION_ID", "", false},
		{"invalid identity", "RELEASE_OPERATION_ID", strings.Repeat("A", 32), false},
		{"missing token", "RELEASE_OPERATION_TOKEN", "", false},
		{"invalid token", "RELEASE_OPERATION_TOKEN", strings.Repeat("x", 64), false},
		{"zero epoch", "RELEASE_OPERATION_EPOCH", "0", false},
		{"invalid epoch", "RELEASE_OPERATION_EPOCH", "1e2", false},
		{"overflow epoch", "RELEASE_OPERATION_EPOCH", "9223372036854775808", false},
		{"missing candidate", "RELEASE_CANDIDATE_MANIFEST_SHA256", "", false},
		{"public wildcard", "PUBLIC_ADDR", ":8090", false},
		{"admin wildcard", "ADMIN_ADDR", "0.0.0.0:8091", false},
		{"dns bind", "ADMIN_ADDR", "localhost:8091", false},
		{"remote bind", "PUBLIC_ADDR", "192.0.2.1:8090", false},
		{"ephemeral bind", "PUBLIC_ADDR", "127.0.0.1:0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			cfg, err := Load()
			if (err == nil) != tc.valid || (tc.valid && cfg.ReleaseValidation == nil) {
				t.Fatalf("validation config accepted=%v error=%v", cfg.ReleaseValidation != nil, err)
			}
			if err != nil && strings.Contains(err.Error(), base["RELEASE_OPERATION_TOKEN"]) {
				t.Fatal("configuration error leaks token")
			}
		})
	}
	for _, key := range []string{"RELEASE_VALIDATION_MODE", "RELEASE_OPERATION_ID", "RELEASE_OPERATION_TOKEN", "RELEASE_OPERATION_EPOCH", "RELEASE_CANDIDATE_MANIFEST_SHA256"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil || cfg.ReleaseValidation != nil {
		t.Fatalf("ordinary startup changed: %v", err)
	}
}
