package modelruntime

import (
	"reflect"
	"testing"
)

func TestOllamaEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  Config
		want    []string
		invalid bool
	}{
		{name: "legacy unchanged"},
		{name: "long context", config: Config{ContextLength: 131072, FlashAttention: "true", KVCacheType: "q8_0"}, want: []string{"OLLAMA_CONTEXT_LENGTH=131072", "OLLAMA_FLASH_ATTENTION=true", "OLLAMA_KV_CACHE_TYPE=q8_0"}},
		{name: "256k", config: Config{ContextLength: 262144}, want: []string{"OLLAMA_CONTEXT_LENGTH=262144"}},
		{name: "context too large", config: Config{ContextLength: 262145}, invalid: true},
		{name: "negative context", config: Config{ContextLength: -1}, invalid: true},
		{name: "cache typo", config: Config{KVCacheType: "q8"}, invalid: true},
		{name: "quantization needs flash", config: Config{KVCacheType: "q8_0"}, invalid: true},
		{name: "flash typo", config: Config{FlashAttention: "1"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.config.OllamaEnvironment()
			if (err != nil) != tc.invalid || (err == nil && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("environment=%v err=%v", got, err)
			}
		})
	}
}

func TestOllamaContextIsScopedToOpenAI(t *testing.T) {
	c := Config{ContextLength: 131072, FlashAttention: "true", KVCacheType: "q8_0"}
	for _, path := range []string{"/api/chat", "/api/show", "/v1/chat/completions"} {
		environment, err := c.OllamaEnvironmentFor(path)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, value := range environment {
			if value == "OLLAMA_CONTEXT_LENGTH=131072" {
				found = true
			}
		}
		if found != (path == "/v1/chat/completions") {
			t.Fatalf("path=%s environment=%v", path, environment)
		}
	}
}
