package modelruntime

import (
	"errors"
	"strconv"
	"strings"
)

func (c Config) OllamaEnvironment() ([]string, error) {
	var environment []string
	if c.ContextLength != 0 {
		if c.ContextLength < 4096 || c.ContextLength > 262144 {
			return nil, errors.New("owned model context must be between 4096 and 262144 tokens")
		}
		environment = append(environment, "OLLAMA_CONTEXT_LENGTH="+strconv.Itoa(c.ContextLength))
	}
	if c.FlashAttention != "" {
		if c.FlashAttention != "true" && c.FlashAttention != "false" {
			return nil, errors.New("invalid owned model flash attention setting")
		}
		environment = append(environment, "OLLAMA_FLASH_ATTENTION="+c.FlashAttention)
	}
	if c.KVCacheType != "" {
		if c.KVCacheType != "f16" && c.KVCacheType != "q8_0" && c.KVCacheType != "q4_0" {
			return nil, errors.New("invalid owned model KV cache type")
		}
		if c.KVCacheType != "f16" && c.FlashAttention != "true" {
			return nil, errors.New("quantized owned model KV cache requires flash attention")
		}
		environment = append(environment, "OLLAMA_KV_CACHE_TYPE="+c.KVCacheType)
	}
	return environment, nil
}

// OpenAI has no num_ctx field. Keep native assistant requests on their own policy.
func (c Config) OllamaEnvironmentFor(path string) ([]string, error) {
	if _, err := c.OllamaEnvironment(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, "/v1/") {
		c.ContextLength = 0
	}
	return c.OllamaEnvironment()
}
