package trainingagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigFileAndProfileReadiness(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	python := touchTestFile(t, directory, "python.exe")
	tuner := mkdirTest(t, directory, "tuner")
	loras := mkdirTest(t, directory, "loras")
	profile := ProfileConfig{
		ID: "krea2-test", Family: "krea2", Name: "Krea2", BaseModel: "raw.safetensors",
		DiT: touchTestFile(t, directory, "dit.safetensors"), VAE: touchTestFile(t, directory, "vae.safetensors"),
		TextEncoder: touchTestFile(t, directory, "text.safetensors"), FP8Base: true,
	}
	configPath := filepath.Join(directory, "config.json")
	payload, err := json.Marshal(Config{
		Addr: "127.0.0.1:8095", Token: "01234567890123456789012345678901", RootDir: filepath.Join(directory, "data"),
		TunerDir: tuner, PythonExe: python, ComfyLoraDir: loras, Profiles: []ProfileConfig{profile},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfigFile(configPath)
	if err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if detail := config.Profiles[0].Readiness(config); detail != "" {
		t.Fatalf("ready profile reported unavailable: %s", detail)
	}
}

func TestConfigRejectsVideoTrainingProfile(t *testing.T) {
	t.Parallel()
	config := Config{Profiles: []ProfileConfig{{ID: "minimax-video", Family: "minimax-h3", Name: "MiniMax", BaseModel: "video.safetensors"}}}
	if err := config.Validate(); err == nil {
		t.Fatal("expected video profile to be rejected")
	}
}

func touchTestFile(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mkdirTest(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
