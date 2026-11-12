package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, tc := range []struct {
		body  string
		valid bool
	}{{`{"INFERENCE_RUNTIME":"windows-owned","SESSION_SECRET":"private"}`, true}, {`{"INFERENCE_RUNTIME":"direct"}`, false}, {`{"INFERENCE_RUNTIME":"windows-owned","PATH":"unexpected"}`, false}, {`{} {}`, false}} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := readNativeConfiguration(path)
		if (err == nil) != tc.valid {
			t.Fatalf("valid=%v err=%v", tc.valid, err)
		}
	}
	if _, err := readNativeConfiguration("relative.json"); err == nil {
		t.Fatal("relative configuration accepted")
	}
}

func TestOwnedModelConfig(t *testing.T) {
	for _, value := range []string{"", "131072", "262144", "zero", "0", "-1", "262145"} {
		_, err := ownedModelConfig(func(key string) string {
			if key == "INFERENCE_MODEL_CONTEXT_LENGTH" {
				return value
			}
			if key == "INFERENCE_MODEL_KV_CACHE_TYPE" {
				return "q8_0"
			}
			if key == "INFERENCE_MODEL_FLASH_ATTENTION" {
				return "true"
			}
			return ""
		})
		valid := value == "" || value == "131072" || value == "262144"
		if (err == nil) != valid {
			t.Fatalf("context=%q err=%v", value, err)
		}
	}
}
