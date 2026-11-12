package loratraining

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledModelArchitecturesReadOnly(t *testing.T) {
	directory := os.Getenv("MODEL_INSPECTION_TEST_ROOT")
	if directory == "" {
		t.Skip("MODEL_INSPECTION_TEST_ROOT is not set")
	}
	var candidates map[string]string
	if err := json.Unmarshal([]byte(os.Getenv("MODEL_INSPECTION_TEST_FILES")), &candidates); err != nil || len(candidates) == 0 {
		t.Fatal("explicit model/architecture map is required", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for candidate, expected := range candidates {
		t.Run(candidate, func(t *testing.T) {
			name, ok := ModelRelativeName(candidate)
			if !ok {
				t.Fatal("invalid model reference")
			}
			file, err := root.Open(filepath.FromSlash(name))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			actual, err := SafetensorsArchitecture(file, info.Size())
			if err != nil || actual != expected {
				t.Fatalf("architecture = %q, want %q: %v", actual, expected, err)
			}
			t.Logf("header confirms %s (%d file bytes; weight payload not read)", actual, info.Size())
		})
	}
}
