package updateagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ai-access-gateway/internal/updates"
)

type recordingRunner struct {
	runCalls   []string
	startCalls []string
	startErr   error
}

func (r *recordingRunner) Run(_ context.Context, _ string, name string, _ ...string) (string, error) {
	r.runCalls = append(r.runCalls, name)
	return "", nil
}

func (r *recordingRunner) Start(_ string, name string, _ ...string) error {
	r.startCalls = append(r.startCalls, name)
	return r.startErr
}

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

func TestInstallableComponentsRequiresSafeAvailableUpdate(t *testing.T) {
	requested := map[string]bool{
		"gateway": true, "comfyui": true, "openwebui": true,
	}
	status := updates.Status{Components: []updates.ComponentStatus{
		{Name: "gateway", UpdateAvailable: false, CanInstall: true},
		{Name: "comfyui", UpdateAvailable: true, CanInstall: false},
		{Name: "openwebui", UpdateAvailable: true, CanInstall: true},
	}}
	allowed := installableComponents(status, requested)
	if len(allowed) != 1 || !allowed["openwebui"] {
		t.Fatalf("allowed = %#v, want openwebui only", allowed)
	}
}

func TestSameReleaseVersionIgnoresVPrefix(t *testing.T) {
	if !sameReleaseVersion("0.11.0", "v0.11.0") {
		t.Fatal("expected OCI image version and release tag to match")
	}
	if sameReleaseVersion("0.11.0", "v0.11.1") {
		t.Fatal("different releases must not match")
	}
}

func TestOpenWebUILabelVersion(t *testing.T) {
	labels := `{"org.opencontainers.image.version":"0.11.0","other":"value"}`
	if version := openWebUILabelVersion(labels); version != "0.11.0" {
		t.Fatalf("version = %q, want 0.11.0", version)
	}
	if version := openWebUILabelVersion(`not json`); version != "" {
		t.Fatalf("version = %q, want empty", version)
	}
}

func TestRestartComfyUIStartsAndChecksHealth(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()
	runner := &recordingRunner{}
	controller := &ControllerImpl{
		config: Config{ComfyUI: ComfyTarget{
			WorkingDirectory: "C:\\ComfyUI",
			StopCommand:      []string{"stop"},
			LaunchCommand:    []string{"launch"},
			HealthURL:        health.URL,
		}},
		runner: runner,
		client: health.Client(),
	}
	if err := controller.restartComfyUI(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.runCalls) != 1 || runner.runCalls[0] != "stop" {
		t.Fatalf("stop calls = %#v", runner.runCalls)
	}
	if len(runner.startCalls) != 1 || runner.startCalls[0] != "launch" {
		t.Fatalf("start calls = %#v", runner.startCalls)
	}
}

func TestRestartComfyUIReportsStartFailure(t *testing.T) {
	runner := &recordingRunner{startErr: errors.New("launcher failed")}
	controller := &ControllerImpl{
		config: Config{ComfyUI: ComfyTarget{
			WorkingDirectory: "C:\\ComfyUI",
			StopCommand:      []string{"stop"},
			LaunchCommand:    []string{"launch"},
			HealthURL:        "http://127.0.0.1:8188/",
		}},
		runner: runner,
		client: &http.Client{},
	}
	if err := controller.restartComfyUI(context.Background()); err == nil {
		t.Fatal("expected launcher error")
	}
}
