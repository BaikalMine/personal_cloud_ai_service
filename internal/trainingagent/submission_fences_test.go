package trainingagent

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/loratraining"
)

func fenceTestController(t *testing.T) (*Controller, loratraining.JobSpec) {
	t.Helper()
	root := t.TempDir()
	model := filepath.Join(root, "model")
	if err := os.WriteFile(model, []byte("fixture, never executed"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := ProfileConfig{ID: "krea-test", Family: "krea2", DiT: model, VAE: model, TextEncoder: model}
	controller := &Controller{
		config: Config{RootDir: root, PythonExe: model, TunerDir: root, ComfyLoraDir: root, MaxDatasetBytes: 1024, Profiles: []ProfileConfig{profile}},
		ctx:    context.Background(), jobs: make(map[string]*jobRecord), byGateway: make(map[string]string), queue: make(chan string, 10),
	}
	spec := loratraining.JobSpec{
		GatewayJobID: "gateway-fence-012345", ProfileID: profile.ID, Name: "Fence fixture", OutputName: "fence_fixture",
		TriggerWord: "test_person", ConceptType: "character", Resolution: 512, MaxSteps: 100, NetworkDim: 16,
		NetworkAlpha: 16, LearningRate: 0.0001, Seed: 42, SampleCount: 5,
	}
	return controller, spec
}

type signalledReader struct {
	io.Reader
	started chan struct{}
}

func (reader *signalledReader) Read(buffer []byte) (int, error) {
	select {
	case reader.started <- struct{}{}:
	default:
	}
	return reader.Reader.Read(buffer)
}

func TestSubmissionFenceStopsAnUploadAlreadyInFlight(t *testing.T) {
	controller, spec := fenceTestController(t)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := controller.Submit(context.Background(), spec, &signalledReader{Reader: reader, started: started})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not start")
	}
	result, err := controller.FenceGatewaySubmission(spec.GatewayJobID)
	if err != nil || !result.Fenced || !result.Settled || result.Job != nil {
		t.Fatalf("fence while upload is pending: %+v %v", result, err)
	}
	_, _ = io.WriteString(writer, "dataset fixture")
	_ = writer.Close()
	if err := <-done; err == nil {
		t.Fatal("late upload was admitted after its fence")
	}
	if len(controller.jobs) != 0 || len(controller.queue) != 0 {
		t.Fatal("late upload reached the executor queue")
	}
	if err := controller.loadSubmissionFences(); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Submit(context.Background(), spec, strings.NewReader("retry")); err == nil {
		t.Fatal("fence was lost after reload")
	}
}

func TestSubmissionFenceSurvivesJobDeletion(t *testing.T) {
	controller, spec := fenceTestController(t)
	status, err := controller.Submit(context.Background(), spec, strings.NewReader("dataset"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.FenceGatewaySubmission(spec.GatewayJobID)
	if err != nil || !result.Settled || result.Job == nil || result.Job.State != "cancelled" {
		t.Fatalf("queued cancellation: %+v %v", result, err)
	}
	if _, err := controller.Delete(status.ID); err != nil {
		t.Fatal(err)
	}
	if err := controller.loadSubmissionFences(); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Submit(context.Background(), spec, strings.NewReader("retry")); err == nil {
		t.Fatal("deleted card erased its submission fence")
	}
}

func TestSubmissionFenceDoesNotDeclareRunningOrRestartedExecutorSettled(t *testing.T) {
	controller, spec := fenceTestController(t)
	status, err := controller.Submit(context.Background(), spec, strings.NewReader("dataset"))
	if err != nil {
		t.Fatal(err)
	}
	controller.jobs[status.ID].Status.State = "running"
	called := false
	controller.jobs[status.ID].cancel = func() { called = true }
	result, err := controller.FenceGatewaySubmission(spec.GatewayJobID)
	if err != nil || result.Settled || !called {
		t.Fatalf("active cancellation must await execution: %+v %v", result, err)
	}
	if err := controller.persist(controller.jobs[status.ID]); err != nil {
		t.Fatal(err)
	}
	if err := controller.loadJobs(); err != nil {
		t.Fatal(err)
	}
	result, err = controller.FenceGatewaySubmission(spec.GatewayJobID)
	if err != nil || result.Settled || result.Job == nil || !result.Job.ExecutionUnconfirmed {
		t.Fatalf("agent restart did not retain uncertainty: %+v %v", result, err)
	}
	if _, err := controller.Delete(status.ID); !errors.Is(err, ErrJobNotTerminal) {
		t.Fatalf("unconfirmed executor record deleted: %v", err)
	}
	controller.deleteExpiredFailedJobs(time.Now().Add(48 * time.Hour))
	if _, err := controller.Status(status.ID); err != nil {
		t.Fatalf("retention removed an unresolved executor: %v", err)
	}
}

func TestSubmissionFencePersistenceFailureCannotConfirmSafety(t *testing.T) {
	controller, spec := fenceTestController(t)
	if err := os.WriteFile(filepath.Join(controller.config.RootDir, "submission-fences"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := controller.FenceGatewaySubmission(spec.GatewayJobID)
	if err == nil || result.Fenced || result.Settled {
		t.Fatalf("unpersisted fence reported safe: %+v %v", result, err)
	}
}

func TestSubmissionFenceHTTPClientAndServer(t *testing.T) {
	controller, spec := fenceTestController(t)
	token := strings.Repeat("f", 32)
	server, err := NewServer(token, controller)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	base, _ := url.Parse(httpServer.URL)
	client := loratraining.NewClient(base, token)
	for range 2 {
		result, err := client.FenceSubmission(context.Background(), spec.GatewayJobID)
		if err != nil || !result.Fenced || !result.Settled || result.GatewayJobID != spec.GatewayJobID {
			t.Fatalf("fence HTTP round trip: %+v %v", result, err)
		}
	}
	if _, err := controller.Submit(context.Background(), spec, strings.NewReader("late retry")); err == nil {
		t.Fatal("HTTP fence did not prevent dispatch")
	}
}

func TestCorruptExecutionInventoryCannotLookLikeAnIdleAgent(t *testing.T) {
	for _, payload := range []string{"{", `{}`, `{"spec":{"gateway_job_id":"gateway"},"status":{"id":"wrong-folder","state":"running"}}`} {
		controller, _ := fenceTestController(t)
		directory := filepath.Join(controller.config.RootDir, "jobs", "old-agent-job")
		if err := os.MkdirAll(directory, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "status.json"), []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := controller.loadJobs(); err == nil {
			t.Fatal("corrupt execution record was silently discarded")
		}
	}
}
