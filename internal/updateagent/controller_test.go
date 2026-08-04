package updateagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteEnvValueReplacesOnlyRequestedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("ONE=keep\r\nOPENWEBUI_IMAGE=old\r\nTWO=keep\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvValue(path, "OPENWEBUI_IMAGE", "new"); err != nil {
		t.Fatal(err)
	}
	value, err := readEnvValue(path, "OPENWEBUI_IMAGE")
	if err != nil {
		t.Fatal(err)
	}
	if value != "new" {
		t.Fatalf("value = %q, want new", value)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "ONE=keep\r\nOPENWEBUI_IMAGE=new\r\nTWO=keep\r\n" {
		t.Fatalf("unexpected content: %q", content)
	}
}
