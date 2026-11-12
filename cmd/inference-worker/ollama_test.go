package main

import (
	"strings"
	"testing"
)

func TestOllamaAPISettings(t *testing.T) {
	for _, tc := range []struct {
		name, mode, address, changed, value string
		ok                                  bool
	}{
		{"disabled", "direct", "", "", "", true},
		{"loopback", "windows-owned", "127.0.0.1:11446", "", "", true},
		{"private", "windows-owned", "192.168.1.86:11446", "", "", true},
		{"IPv6 loopback", "windows-owned", "[::1]:11446", "", "", true},
		{"direct runtime", "direct", "127.0.0.1:11446", "", "", false},
		{"wildcard", "windows-owned", ":11446", "", "", false},
		{"all interfaces", "windows-owned", "0.0.0.0:11446", "", "", false},
		{"public IP", "windows-owned", "8.8.8.8:11446", "", "", false},
		{"hostname", "windows-owned", "localhost:11446", "", "", false},
		{"privileged port", "windows-owned", "127.0.0.1:80", "", "", false},
		{"oversize port", "windows-owned", "127.0.0.1:65536", "", "", false},
		{"partial settings", "windows-owned", "", "INFERENCE_OLLAMA_CLIENT_KEY", strings.Repeat("c", 64), false},
		{"same native listener", "windows-owned", "127.0.0.1:11446", "INFERENCE_MODEL_ADDR", "127.0.0.1:11446", false},
		{"missing client key", "windows-owned", "127.0.0.1:11446", "INFERENCE_OLLAMA_CLIENT_KEY", "", false},
		{"shared key", "windows-owned", "127.0.0.1:11446", "INFERENCE_OLLAMA_CLIENT_KEY", strings.Repeat("a", 64), false},
		{"URL credentials", "windows-owned", "127.0.0.1:11446", "INFERENCE_OLLAMA_ADMISSION_URL", "http://secret:secret@127.0.0.1:8080/internal/gpu/ollama/permit", false},
		{"wrong permit scope", "windows-owned", "127.0.0.1:11446", "INFERENCE_OLLAMA_ADMISSION_URL", "http://127.0.0.1:8080/internal/gpu/comfy/permit", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"INFERENCE_OLLAMA_ADDR": tc.address}
			if tc.address != "" {
				env["INFERENCE_OLLAMA_CLIENT_KEY"] = strings.Repeat("c", 64)
				env["INFERENCE_OLLAMA_ADMISSION_KEY"] = strings.Repeat("a", 64)
				env["INFERENCE_OLLAMA_ADMISSION_URL"] = "http://127.0.0.1:8080/internal/gpu/ollama/permit"
			}
			if tc.changed != "" {
				env[tc.changed] = tc.value
			}
			address, _, err := ollamaAPISettings(tc.mode, func(key string) string { return env[key] })
			if (err == nil) != tc.ok || (tc.ok && address != tc.address) {
				t.Fatalf("settings: %q %v", address, err)
			}
		})
	}
}
