package updateagent

import (
	"encoding/json"
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

func TestComfyQueueBusySupportsCurrentAndLegacyFormats(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		busy    bool
		invalid bool
	}{
		{name: "current idle", payload: `{"queue_running":[],"queue_pending":[]}`},
		{name: "current pending", payload: `{"queue_running":[],"queue_pending":[[1]]}`, busy: true},
		{name: "legacy running", payload: `[[[1]],[]]`, busy: true},
		{name: "invalid", payload: `null`, invalid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			busy, err := comfyQueueBusy(json.RawMessage(test.payload))
			if test.invalid {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if busy != test.busy {
				t.Fatalf("busy = %v, want %v", busy, test.busy)
			}
		})
	}
}
