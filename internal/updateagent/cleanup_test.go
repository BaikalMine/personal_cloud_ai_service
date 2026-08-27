package updateagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"ai-access-gateway/internal/updates"
)

func TestDeleteComfyOutputFilesRequiresExactContentMatch(t *testing.T) {
	directory := t.TempDir()
	matchedPath := filepath.Join(directory, "nested", "matched.png")
	mismatchPath := filepath.Join(directory, "mismatch.png")
	for path, body := range map[string]string{matchedPath: "same image", mismatchPath: "different image"} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256([]byte("same image"))
	request := []updates.ComfyOutputFile{
		{Filename: "matched.png", Subfolder: "nested", StorageType: "output", SizeBytes: int64(len("same image")), SHA256: hex.EncodeToString(digest[:])},
		{Filename: "mismatch.png", StorageType: "output", SizeBytes: int64(len("different image")), SHA256: hex.EncodeToString(digest[:])},
		{Filename: "..\\outside.png", StorageType: "output", SizeBytes: 0, SHA256: hex.EncodeToString(digest[:])},
	}
	result, err := DeleteComfyOutputFiles(context.Background(), ComfyTarget{OutputDirectory: directory}, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deleted != 1 || result.Mismatched != 1 || result.Rejected != 1 {
		t.Fatalf("delete result = %#v", result)
	}
	if _, err := os.Stat(matchedPath); !os.IsNotExist(err) {
		t.Fatalf("matched output remains: %v", err)
	}
	if _, err := os.Stat(mismatchPath); err != nil {
		t.Fatalf("mismatched output was removed: %v", err)
	}
}
