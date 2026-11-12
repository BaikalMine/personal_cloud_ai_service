package updateagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelCapacityRoot(t *testing.T) {
	working := t.TempDir()
	root := filepath.Join(working, "models", "loras")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := modelCapacityRoot(working, root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{working, filepath.Join(working, "models-other"), "relative", ""} {
		if _, err := modelCapacityRoot(working, path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	outside := t.TempDir()
	link := filepath.Join(working, "models", "linked")
	if err := os.Symlink(outside, link); err == nil {
		if _, err = modelCapacityRoot(working, link); err == nil {
			t.Fatal("accepted external symlink")
		}
	}
	if free, err := modelDiskFree(root); err != nil || free == 0 {
		t.Fatalf("capacity = %d %v", free, err)
	}
}
