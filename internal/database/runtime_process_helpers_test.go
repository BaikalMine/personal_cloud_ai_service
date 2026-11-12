package database

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func buildRuntimeProcess(t *testing.T, component string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), component)
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/"+component)
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return binary
}

func runtimeProcessEnvironment(dsn, spool string, overrides map[string]string) []string {
	values := map[string]string{
		"DATABASE_URL": dsn, "SESSION_SECRET": strings.Repeat("s", 40),
		"ADMIN_USERNAME": "release-test-admin", "ADMIN_PASSWORD": "ReleaseTest!Password2026",
		"PUBLIC_ADDR": "127.0.0.1:0", "ADMIN_ADDR": "127.0.0.1:0",
		"PUBLIC_BASE_URL": "http://127.0.0.1:18080", "ADMIN_BASE_URL": "http://127.0.0.1:18081",
		"COOKIE_SECURE": "false", "MEDIA_SPOOL_DIR": spool,
		"TMPDIR": spool, "TEMP": spool, "TMP": spool,
		"INFERENCE_OUTCOME_DIR": filepath.Join(spool, "outcomes"),
		"INFERENCE_RUNTIME":     "direct", "INFERENCE_WORKER_ADDR": "127.0.0.1:0",
		"COMFYUI_UPSTREAM": "http://127.0.0.1:1", "OPENWEBUI_UPSTREAM": "http://127.0.0.1:1",
		"OLLAMA_UPSTREAM": "http://127.0.0.1:1", "CONTENT_MODERATOR_UPSTREAM": "http://127.0.0.1:1",
		"INFERENCE_WORKER_URL": "http://127.0.0.1:1", "MINING_AGENT_URL": "http://127.0.0.1:1",
		"SYSTEM_MONITOR_AGENT_URL": "http://127.0.0.1:1", "UPDATE_AGENT_URL": "http://127.0.0.1:1",
		"LORA_TRAINING_AGENT_URL": "http://127.0.0.1:1",
	}
	if runtime.GOOS == "windows" {
		values["SYSTEMROOT"] = os.Getenv("SYSTEMROOT")
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}
