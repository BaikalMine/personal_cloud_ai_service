package release

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/security"
)

func TestValidationSmokeActualGatewayAndCLI(t *testing.T) {
	db := releaseTestDatabase(t)
	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	dsn := releaseTestDSN(t, os.Getenv("TEST_DATABASE_URL"), name)
	directory, manifest, options := smokeFixtureOptions(t)
	manifest.Build.GoVersion, manifest.Build.Platform = runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH
	manifest.Artifacts[0].Report.Build = manifest.Build
	// This fixture exercises a real stamped executable, not an OCI deployment.
	// The source and image archives remain synthetic and never authorize rollout.
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("synthetic package image is Linux/amd64")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	binary := buildStampedGatewayFixture(t, ctx, directory, &manifest)
	options.Operation.CandidateManifestHash = writeSmokeManifest(t, directory, manifest)
	cli := filepath.Join(t.TempDir(), "release-tool")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", cli, "../../cmd/release-tool").CombinedOutput(); err != nil {
		t.Fatalf("build release-tool: %v\n%s", err, output)
	}
	passwordHash, err := security.HashPassword(options.Password)
	if err != nil {
		t.Fatal(err)
	}
	preflightExec(t, db, `INSERT INTO users(username,password_hash,role) VALUES ($1,$2,'admin')`, options.Username, passwordHash)
	controller, err := database.BeginRuntimeRelease(ctx, db, database.RuntimeReleaseRequest{
		OperationID: options.Operation.OperationID, Token: options.Operation.Token,
		PreviousManifestHash: strings.Repeat("b", 64), CandidateManifestHash: options.Operation.CandidateManifestHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if ready, err := controller.TryQuiesce(ctx); err != nil || !ready {
		t.Fatalf("quiesce: %v %v", ready, err)
	}
	if err := controller.AllowValidationConnections(ctx); err != nil {
		t.Fatal(err)
	}
	public, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer public.Close()
	admin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	options.PublicURL, options.AdminURL = "http://"+public.Addr().String(), "http://"+admin.Addr().String()
	spool := t.TempDir()
	values := map[string]string{
		"DATABASE_URL": dsn, "SESSION_SECRET": strings.Repeat("s", 40),
		"ADMIN_USERNAME": "never-bootstrap", "ADMIN_PASSWORD": "NeverBootstrap!Password2026",
		"PUBLIC_ADDR": public.Addr().String(), "ADMIN_ADDR": admin.Addr().String(),
		"PUBLIC_BASE_URL": options.PublicURL, "ADMIN_BASE_URL": options.AdminURL, "ADMIN_ALLOWED_CIDRS": "127.0.0.0/8",
		"COOKIE_SECURE": "false", "MEDIA_SPOOL_DIR": spool, "TMPDIR": spool, "TEMP": spool, "TMP": spool,
		"RELEASE_VALIDATION_MODE": "true", "RELEASE_OPERATION_ID": options.Operation.OperationID,
		"RELEASE_OPERATION_TOKEN": options.Operation.Token, "RELEASE_OPERATION_EPOCH": "1",
		"RELEASE_CANDIDATE_MANIFEST_SHA256": options.Operation.CandidateManifestHash,
	}
	for _, key := range []string{"COMFYUI_UPSTREAM", "OPENWEBUI_UPSTREAM", "OLLAMA_UPSTREAM", "CONTENT_MODERATOR_UPSTREAM", "INFERENCE_WORKER_URL", "MINING_AGENT_URL", "SYSTEM_MONITOR_AGENT_URL", "UPDATE_AGENT_URL", "LORA_TRAINING_AGENT_URL"} {
		values[key] = "http://127.0.0.1:1"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	public.Close()
	admin.Close()
	processCtx, stopProcess := context.WithCancel(ctx)
	defer stopProcess()
	process := exec.CommandContext(processCtx, binary)
	process.Env, process.Dir = environment, spool
	var processOutput bytes.Buffer
	process.Stdout, process.Stderr = &processOutput, &processOutput
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	waited := false
	defer func() {
		stopProcess()
		if !waited {
			<-done
		}
		if t.Failed() {
			t.Log(processOutput.String())
		}
	}()
	base, _ := url.Parse(options.PublicURL)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	probe := validationSmokeClient{client: &http.Client{Timeout: time.Second, Transport: transport}, token: options.Operation.Token}
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			waited = true
			t.Fatalf("candidate exited: %v", err)
		default:
		}
		response, _, err := probe.call(ctx, base, "GET", "/internal/release/validation", nil, true)
		if err == nil && response.StatusCode == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("candidate did not open validation listener")
		}
		time.Sleep(25 * time.Millisecond)
	}
	report, err := SmokeValidation(ctx, directory, options)
	if err != nil || !report.Success || report.CleanupStatus != "verified" || report.DeploymentAuthorized {
		t.Fatalf("actual candidate: %+v %v", report, err)
	}
	command := exec.CommandContext(ctx, cli, "--mode", "validation-smoke", "--directory", directory, "--public-url", options.PublicURL, "--admin-url", options.AdminURL)
	command.Env = append(slices.Clone(environment), "RELEASE_SMOKE_USERNAME="+options.Username, "RELEASE_SMOKE_PASSWORD="+options.Password)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("actual smoke CLI: %v\n%s", err, output)
	}
	if err := json.Unmarshal(output, &report); err != nil || !report.Success || report.CleanupStatus != "verified" || report.DeploymentAuthorized {
		t.Fatalf("CLI report: %s %v", output, err)
	}
	if report.Operation == nil || report.Operation.OperationID != options.Operation.OperationID || report.Operation.Epoch != options.Operation.Epoch || report.Operation.CandidateManifestHash != options.Operation.CandidateManifestHash || report.Candidate == nil || *report.Candidate != manifest.Build {
		t.Fatal("actual CLI report lost its operation or build identity")
	}
	for _, secret := range []string{options.Password, options.Operation.Token} {
		if bytes.Contains(output, []byte(secret)) {
			t.Fatal("CLI report exposed credentials")
		}
	}
	var sessions, users, jobs int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM sessions),(SELECT count(*) FROM users),(SELECT count(*) FROM generation_jobs)+(SELECT count(*) FROM gpu_work)`).Scan(&sessions, &users, &jobs); err != nil || sessions != 0 || users != 1 || jobs != 0 {
		t.Fatalf("smoke side effects sessions=%d users=%d jobs=%d err=%v", sessions, users, jobs, err)
	}
	controller.Close()
	report, err = SmokeValidation(ctx, directory, options)
	if err == nil || report.Success || report.FailureCode != "public_identity" || report.CleanupStatus != "not_needed" {
		t.Fatalf("accepted revoked validation: %+v %v", report, err)
	}
	state, err := database.ReadRuntimeFence(ctx, db)
	if err != nil || !state.Active {
		t.Fatalf("smoke cleared durable fence: %+v %v", state, err)
	}
}

func buildStampedGatewayFixture(t *testing.T, ctx context.Context, directory string, manifest *Manifest) string {
	t.Helper()
	manifest.Build.GoVersion, manifest.Build.Platform = runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH
	manifest.Artifacts[0].Report.Build = manifest.Build
	binary := filepath.Join(directory, manifest.Artifacts[0].File.Name)
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	flags := "-X ai-access-gateway/internal/buildinfo.Version=" + manifest.Build.Version +
		" -X ai-access-gateway/internal/buildinfo.Revision=" + manifest.Build.Revision +
		" -X ai-access-gateway/internal/buildinfo.BuiltAt=" + manifest.Build.BuiltAt + " -X ai-access-gateway/internal/buildinfo.Modified=false"
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "-ldflags", flags, "../../cmd/gateway").CombinedOutput(); err != nil {
		t.Fatalf("build gateway: %v\n%s", err, output)
	}
	metadata, _ := json.Marshal(manifest.Artifacts[0].Report)
	if err := os.WriteFile(filepath.Join(directory, manifest.Artifacts[0].Metadata.Name), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	manifest.Artifacts[0].File, err = hashFile(root, manifest.Artifacts[0].File.Name)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Artifacts[0].Metadata, err = hashFile(root, manifest.Artifacts[0].Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	return binary
}
