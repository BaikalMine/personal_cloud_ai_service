package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ai-access-gateway/internal/modelruntime"
)

// Credentials live in an ACL-restricted local file, not scheduled-task arguments.
func readNativeConfiguration(path string) (map[string]string, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("native configuration requires an absolute path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("native configuration unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 {
		return nil, errors.New("invalid native configuration size")
	}
	d := json.NewDecoder(io.LimitReader(f, 16385))
	var values map[string]string
	if d.Decode(&values) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid native configuration")
	}
	allowed := map[string]bool{"DATABASE_URL": true, "SESSION_SECRET": true, "INFERENCE_RUNTIME": true, "INFERENCE_OUTCOME_DIR": true, "INFERENCE_WORKER_ADDR": true, "INFERENCE_MODEL_EXECUTABLE": true, "INFERENCE_MODEL_DIRECTORY": true, "INFERENCE_MODEL_STATE_DIR": true, "INFERENCE_MODEL_ADDR": true, "INFERENCE_OLLAMA_ADDR": true, "INFERENCE_OLLAMA_CLIENT_KEY": true, "INFERENCE_OLLAMA_ADMISSION_URL": true, "INFERENCE_OLLAMA_ADMISSION_KEY": true}
	for _, key := range []string{"INFERENCE_MODEL_CONTEXT_LENGTH", "INFERENCE_MODEL_FLASH_ATTENTION", "INFERENCE_MODEL_KV_CACHE_TYPE"} {
		allowed[key] = true
	}
	for key := range values {
		if !allowed[key] {
			return nil, errors.New("unknown native configuration field")
		}
	}
	if values["INFERENCE_RUNTIME"] != "windows-owned" {
		return nil, errors.New("native configuration requires owned execution")
	}
	return values, nil
}

func ownedModelConfig(env func(string) string) (modelruntime.Config, error) {
	config := modelruntime.Config{
		Executable:      strings.TrimSpace(env("INFERENCE_MODEL_EXECUTABLE")),
		ModelsDirectory: strings.TrimSpace(env("INFERENCE_MODEL_DIRECTORY")),
		StateDirectory:  strings.TrimSpace(env("INFERENCE_MODEL_STATE_DIR")),
		Address:         strings.TrimSpace(env("INFERENCE_MODEL_ADDR")),
		FlashAttention:  strings.TrimSpace(env("INFERENCE_MODEL_FLASH_ATTENTION")),
		KVCacheType:     strings.TrimSpace(env("INFERENCE_MODEL_KV_CACHE_TYPE")),
	}
	if value := strings.TrimSpace(env("INFERENCE_MODEL_CONTEXT_LENGTH")); value != "" {
		var err error
		config.ContextLength, err = strconv.Atoi(value)
		if err != nil || config.ContextLength == 0 {
			return config, errors.New("invalid owned model context length")
		}
	}
	_, err := config.OllamaEnvironment()
	return config, err
}
