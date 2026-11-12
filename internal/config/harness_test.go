package config

import (
	"strings"
	"testing"
)

func TestHarnessConfig(t *testing.T) {
	t.Setenv("HARNESS_INGRESS_HOST", "")
	t.Setenv("HARNESS_INGRESS_PEERS", "")
	t.Setenv("HARNESS_UPSTREAM", "")
	t.Setenv("HARNESS_AGENT_API_KEY", "")
	h, err := loadHarnessConfig()
	if err != nil || h.Enabled() {
		t.Fatal("disabled by default")
	}
	for k, v := range map[string]string{"HARNESS_UPSTREAM": "http://harness:3080", "HARNESS_BOOTSTRAP_URL": "http://harness:3082", "HARNESS_MODEL_UPSTREAM": "http://host.docker.internal:8097", "HARNESS_BOOTSTRAP_KEY": strings.Repeat("b", 64), "HARNESS_MODEL_KEY": strings.Repeat("m", 64), "HARNESS_CLIENT_KEY": strings.Repeat("c", 64), "HARNESS_ADMISSION_KEY": strings.Repeat("a", 64)} {
		t.Setenv(k, v)
	}
	h, err = loadHarnessConfig()
	if err != nil || !h.Enabled() {
		t.Fatalf("valid config: %v", err)
	}
	t.Run("agent API is opt-in and has a separate credential", func(t *testing.T) {
		if h.AgentAPIKey != "" {
			t.Fatal("agent API enabled by default")
		}
		t.Setenv("HARNESS_AGENT_API_KEY", strings.Repeat("z", 64))
		loaded, err := loadHarnessConfig()
		if err != nil || loaded.AgentAPIKey != strings.Repeat("z", 64) {
			t.Fatalf("agent API config: %v", err)
		}
		for _, value := range []string{"short", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("x", 257), strings.Repeat("x", 32) + "\nsecret"} {
			t.Setenv("HARNESS_AGENT_API_KEY", value)
			if _, err := loadHarnessConfig(); err == nil {
				t.Fatal("invalid/shared agent credential accepted")
			}
		}
	})
	t.Run("read-only Ultra history", func(t *testing.T) {
		t.Setenv("HARNESS_ULTRA_HISTORY_URL", "http://krea-ultra:8097")
		t.Setenv("HARNESS_ULTRA_HISTORY_KEY", strings.Repeat("h", 64))
		if h, err := loadHarnessConfig(); err != nil || h.UltraHistoryURL == nil {
			t.Fatalf("history config: %v", err)
		}
		for _, value := range []string{"", "http://user:pass@krea-ultra:8097", "http://krea-ultra:8097/path", "http://krea-ultra:8097/?key=x"} {
			t.Setenv("HARNESS_ULTRA_HISTORY_URL", value)
			if _, err := loadHarnessConfig(); err == nil {
				t.Fatal("invalid history origin accepted", value)
			}
		}
		t.Setenv("HARNESS_ULTRA_HISTORY_URL", "http://krea-ultra:8097")
		t.Setenv("HARNESS_ULTRA_HISTORY_KEY", strings.Repeat("a", 64))
		if _, err := loadHarnessConfig(); err == nil {
			t.Fatal("shared history credential accepted")
		}
	})
	t.Run("trusted ingress", func(t *testing.T) {
		t.Setenv("HARNESS_INGRESS_HOST", "localhost:8080")
		t.Setenv("HARNESS_INGRESS_PEERS", "10.0.0.7/32")
		h, err := loadHarnessConfig()
		if err != nil || h.IngressHost != "localhost:8080" || len(h.IngressPeers) != 1 {
			t.Fatalf("valid ingress: %v", err)
		}
	})
	for _, tc := range []struct{ host, peers string }{
		{"localhost:8080", ""}, {"", "10.0.0.7"},
		{"localhost:8080/path", "10.0.0.7"}, {"user:password@host:8080", "10.0.0.7"},
		{"host:8080?token=x", "10.0.0.7"}, {"host:8080", "invalid"},
	} {
		t.Run("invalid ingress "+tc.host, func(t *testing.T) {
			t.Setenv("HARNESS_INGRESS_HOST", tc.host)
			t.Setenv("HARNESS_INGRESS_PEERS", tc.peers)
			if _, err := loadHarnessConfig(); err == nil {
				t.Fatal("invalid ingress accepted")
			}
		})
	}
	for _, tc := range []struct{ k, v string }{{"HARNESS_MODEL_KEY", "short"}, {"HARNESS_BOOTSTRAP_URL", "http://user:pass@harness:3082"}, {"HARNESS_MODEL_UPSTREAM", "http://host:8097/?token=x"}, {"HARNESS_CLIENT_KEY", strings.Repeat("a", 64)}} {
		t.Run(tc.k, func(t *testing.T) {
			t.Setenv(tc.k, tc.v)
			if _, err := loadHarnessConfig(); err == nil {
				t.Error("invalid config accepted")
			}
		})
	}
}
