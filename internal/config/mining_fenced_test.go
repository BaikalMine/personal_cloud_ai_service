package config

import (
	"strings"
	"testing"
)

func TestMiningFencedConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://gateway:test@postgres:5432/gateway?sslmode=disable")
	t.Setenv("ADMIN_PASSWORD", "strong-admin-password")
	t.Setenv("SESSION_SECRET", "01234567890123456789012345678901")
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("MINING_FENCED_COMMANDS", value)
			cfg, err := Load()
			if value == "invalid" {
				if err == nil || !strings.Contains(err.Error(), "MINING_FENCED_COMMANDS") {
					t.Fatal(err)
				}
				return
			}
			if err != nil || cfg.MiningFencedCommands != (value == "true") {
				t.Fatal(cfg.MiningFencedCommands, err)
			}
		})
	}
}
