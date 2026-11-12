package gateway

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestComfyGPUProtocolIntegration(t *testing.T) {
	python := os.Getenv("COMFY_GUARD_TEST_PYTHON")
	if python == "" {
		t.Skip("COMFY_GUARD_TEST_PYTHON is not set")
	}
	f := gpuDispatchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	f.app.cfg.ComfyGPUGuardRequired = true
	f.app.cfg.ComfyGPUGuardToken = strings.Repeat("k", 64)
	f.app.cfg.MiningFencedCommands = true
	_, loopback, _ := net.ParseCIDR("127.0.0.0/8")
	f.app.cfg.AdminAllowedNetworks = []*net.IPNet{loopback}
	callback := httptest.NewServer(f.app.adminLANOnly(http.HandlerFunc(f.app.handleComfyGPUPermit)))
	defer callback.Close()
	journal := filepath.Join(t.TempDir(), "receipts.sqlite")
	start := func() (string, func()) {
		t.Helper()
		run, stop := context.WithCancel(ctx)
		cmd := exec.CommandContext(run, python, "-B", "../../integrations/comfyui_gpu_guard/protocol_fixture.py", callback.URL+"/internal/gpu/comfy/permit", f.app.cfg.ComfyGPUGuardToken, journal)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		closed := false
		cleanup := func() {
			if !closed {
				closed = true
				stop()
				_ = cmd.Wait()
				if stderr.Len() > 0 {
					t.Log(stderr.String())
				}
			}
		}
		t.Cleanup(cleanup)
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() {
			cleanup()
			t.Fatalf("Python fixture did not start: %s", stderr.String())
		}
		base := scanner.Text()
		parsed, err := url.Parse(base)
		if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
			cleanup()
			t.Fatalf("invalid fixture URL %q", base)
		}
		f.app.cfg.ComfyUIUpstream = parsed
		return base, cleanup
	}
	base, stop := start()
	send := func(path string, managed *domain.GPUWork) int {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, "POST", base+path, strings.NewReader(`{"prompt":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if managed != nil {
			if err = f.app.authorizeManagedComfyGPURequest(ctx, r, f.user.ID, managed.JobKey); err != nil {
				t.Fatal(err)
			}
		}
		response, err := (&http.Client{Timeout: 15 * time.Second}).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if err := f.app.probeComfyGPUGuard(ctx); err != nil {
		t.Fatal(err)
	}
	if status := send("/prompt", nil); status != 200 {
		t.Fatalf("external submit: %d", status)
	}
	native, err := f.app.store.ActiveGPUExecutorPermit(ctx, domain.GPUPrimaryResource)
	if err != nil || native.State != "running" || native.WorkID != "" {
		t.Fatalf("native permit: %+v %v", native, err)
	}
	if status := send("/prompt", nil); status != 409 {
		t.Fatalf("second native prompt: %d", status)
	}
	if err = f.app.gpuCoordinator.observe(ctx); err != nil {
		t.Fatal(err)
	}
	resource, err := f.app.store.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil || resource.Observation == "idle" {
		t.Fatalf("native permit appeared idle: %+v %v", resource, err)
	}
	work, err := f.app.store.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "protocol-managed", ResourceID: domain.GPUPrimaryResource, Kind: domain.GPUWorkGeneration, JobKey: "protocol-managed", UserID: f.user.ID})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := f.app.store.AcquireGPUWork(ctx, work.ID, "protocol-lease", time.Minute)
	if err != nil || admission.Granted || admission.WaitCode != "external_work" {
		t.Fatalf("external overlap: %+v %v", admission, err)
	}
	if status := send("/test/run", nil); status != 200 {
		t.Fatalf("native completion: %d", status)
	}
	if _, err = f.app.store.ActiveGPUExecutorPermit(ctx, domain.GPUPrimaryResource); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("native still active: %v", err)
	}
	if err = f.app.gpuCoordinator.observe(ctx); err != nil {
		t.Fatal(err)
	}
	admission, err = f.app.store.AcquireGPUWork(ctx, work.ID, "protocol-lease", time.Minute)
	if err != nil || !admission.Granted {
		t.Fatalf("managed acquire: %+v %v", admission, err)
	}
	work, err = f.app.store.SetGPUWorkPhase(ctx, work.ID, "protocol-lease", "dispatching", "")
	if err != nil {
		t.Fatal(err)
	}
	wrongRequest := httptest.NewRequest("POST", "http://fixture/prompt", nil)
	if err = f.app.authorizeManagedComfyGPURequest(ctx, wrongRequest, f.user.ID+1, work.JobKey); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("foreign user authorized: %v", err)
	}
	if status := send("/prompt", &work); status != 200 {
		t.Fatalf("managed submit: %d", status)
	}
	native, err = f.app.store.ActiveGPUExecutorPermit(ctx, domain.GPUPrimaryResource)
	if err != nil || native.WorkID != work.ID {
		t.Fatalf("managed binding: %+v %v", native, err)
	}
	resource, _ = f.app.store.GPUResource(ctx, domain.GPUPrimaryResource)
	if _, err = f.app.store.ReleaseGPUWork(ctx, work.ID, work.LeaseToken, domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: resource.Revision}); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("history released managed native work: %v", err)
	}
	if status := send("/test/run", nil); status != 200 {
		t.Fatalf("managed completion: %d", status)
	}
	resource, _ = f.app.store.GPUResource(ctx, domain.GPUPrimaryResource)
	if _, err = f.app.store.ReleaseGPUWork(ctx, work.ID, work.LeaseToken, domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: resource.Revision}); err != nil {
		t.Fatal(err)
	}
	if status := send("/prompt", nil); status != 200 {
		t.Fatalf("pre-crash submit: %d", status)
	}
	before, err := f.app.store.ActiveGPUExecutorPermit(ctx, domain.GPUPrimaryResource)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	base, stop = start()
	defer stop()
	if status := send("/prompt", nil); status != 409 {
		t.Fatalf("restart inferred old work completion: %d", status)
	}
	after, err := f.app.store.ActiveGPUExecutorPermit(ctx, domain.GPUPrimaryResource)
	if err != nil || before.ID != after.ID || after.State != "running" {
		t.Fatalf("restart lost unknown permit: %+v %v", after, err)
	}
}
