package config

import (
	"strings"
	"testing"
)

func TestGenerationComponentInspectionConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GENERATION_COMPONENT_INSPECTION_ENABLED", value)
			cfg, err := Load()
			if value == "invalid" {
				if err == nil || !strings.Contains(err.Error(), "GENERATION_COMPONENT_INSPECTION_ENABLED") {
					t.Fatal("invalid flag accepted", err)
				}
				return
			}
			if err != nil || cfg.GenerationComponentInspectionEnabled != (value == "true") {
				t.Fatal(cfg.GenerationComponentInspectionEnabled, err)
			}
		})
	}
}
