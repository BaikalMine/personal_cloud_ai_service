package release

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/security"
)

// This opt-in test runs natively on the Docker host. Its only containers are
// newly created, randomly named/labelled fixtures; no existing service is used.
func TestDockerGatewayActualEngine(t *testing.T) {
	if os.Getenv("RELEASE_DOCKER_TESTS") != "1" {
		t.Skip("native Docker fixture gate is not enabled")
	}
	endpoint, source := os.Getenv("RELEASE_DOCKER_ENDPOINT"), os.Getenv("RELEASE_DOCKER_SOURCE")
	buildImage, postgresImage := os.Getenv("RELEASE_DOCKER_BUILD_IMAGE"), os.Getenv("RELEASE_DOCKER_POSTGRES_IMAGE")
	if !filepath.IsAbs(source) || !validDigest(buildImage) || !validDigest(postgresImage) {
		t.Fatal("native fixture requires absolute source and immutable local build/PostgreSQL images")
	}
	d, err := newDockerObserver(endpoint)
	if err != nil {
		t.Fatal("native fixture requires an explicit local Engine endpoint")
	}
	defer d.close()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var entropy [12]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	label := hex.EncodeToString(entropy[:])
	if selected := os.Getenv("RELEASE_DOCKER_FIXTURE_ID"); selected != "" {
		if decoded, err := hex.DecodeString(selected); err != nil || len(decoded) != 12 || strings.ToLower(selected) != selected {
			t.Fatal("invalid parent-owned fixture ID")
		}
		label = selected
	}
	prefix := "gateway-release-binding-" + label
	environment := make([]string, 0)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "DOCKER_") {
			environment = append(environment, entry)
		}
	}
	run := func(commandCtx context.Context, args ...string) (string, error) {
		command := exec.CommandContext(commandCtx, docker, append([]string{"--host", endpoint}, args...)...)
		command.Env = environment
		output, err := command.CombinedOutput()
		return strings.TrimSpace(string(output)), err
	}
	checked := func(args ...string) string {
		t.Helper()
		output, err := run(ctx, args...)
		if err != nil {
			t.Fatalf("fixture Docker command: %v\n%s", err, output)
		}
		return output
	}
	remove := func(kind, id string) {
		t.Helper()
		if (kind == "image" && !validDigest(id)) || (kind != "image" && !hashPattern.MatchString(id)) {
			t.Fatal("fixture returned an invalid immutable resource ID")
		}
		t.Cleanup(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			args := []string{kind, "rm"}
			if kind == "container" {
				args = append(args, "--force")
			}
			if output, err := run(cleanup, append(args, id)...); err != nil {
				t.Errorf("fixture cleanup: %v\n%s", err, output)
			}
		})
	}
	var engineID string
	if json.Unmarshal([]byte(checked("info", "--format", "{{json .ID}}")), &engineID) != nil || d.checkEngine(ctx, engineID) != nil {
		t.Fatal("fixture Engine identity mismatch")
	}
	networkID := checked("network", "create", "--driver", "bridge", "--label", "com.ai.gateway.release-test="+label, prefix)
	remove("network", networkID)
	password := "fixture-" + label
	postgresID := checked("run", "--detach", "--pull=never", "--name", prefix+"-postgres", "--label", "com.ai.gateway.release-test="+label,
		"--network", networkID, "--network-alias", "postgres-test", "--tmpfs", "/var/lib/postgresql/data:rw",
		"--publish", "127.0.0.1::5432", "--env", "POSTGRES_PASSWORD="+password, postgresImage)
	remove("container", postgresID)
	published := func(id, port string) string {
		t.Helper()
		var ports map[string][]dockerPortBinding
		observed := checked("container", "inspect", "--format", "{{json .NetworkSettings.Ports}}", id)
		if json.Unmarshal([]byte(observed), &ports) != nil || len(ports[port+"/tcp"]) != 1 {
			t.Fatalf("fixture did not publish exactly one loopback port: %s", observed)
		}
		binding := ports[port+"/tcp"][0]
		if binding.HostIP != "127.0.0.1" {
			t.Fatal("fixture port is not loopback-only")
		}
		return binding.HostPort
	}
	dsnURL := url.URL{Scheme: "postgres", User: url.UserPassword("postgres", password), Host: net.JoinHostPort("127.0.0.1", published(postgresID, "5432")), Path: "/postgres", RawQuery: "sslmode=disable"}
	db, err := sql.Open("postgres", dsnURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		probeCtx, stopProbe := context.WithTimeout(ctx, time.Second)
		err := db.PingContext(probeCtx)
		stopProbe()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture PostgreSQL did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"admin", "user"} {
		var id int64
		if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES($1,'unused',$1) RETURNING id`, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		preflightExec(t, db, `INSERT INTO sessions(user_id,token_hash,expires_at) VALUES($1,$2,now()+interval '1 hour')`, id, security.HashToken("docker-binding-"+role))
	}
	directory, manifest := fixtureManifest(t)
	manifest.Build.GoVersion = runtime.Version()
	manifest.Artifacts[0].Report.Build = manifest.Build
	binary := filepath.Join(directory, manifest.Artifacts[0].File.Name)
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	flags := "-s -w -X ai-access-gateway/internal/buildinfo.Version=" + manifest.Build.Version +
		" -X ai-access-gateway/internal/buildinfo.Revision=" + manifest.Build.Revision +
		" -X ai-access-gateway/internal/buildinfo.BuiltAt=" + manifest.Build.BuiltAt + " -X ai-access-gateway/internal/buildinfo.Modified=false"
	checked("run", "--rm", "--pull=never", "--network", "none", "--name", prefix+"-build", "--label", "com.ai.gateway.release-test="+label,
		"--mount", "type=bind,source="+source+",target=/src,readonly",
		"--mount", "type=bind,source="+filepath.Dir(binary)+",target=/out",
		"--mount", "type=volume,source=ai-gateway-go-mod,target=/go/pkg/mod",
		"--mount", "type=volume,source=ai-gateway-f04-go-build-cache,target=/go-build",
		"--env", "GOCACHE=/go-build", "--env", "GOPROXY=off", "--env", "CGO_ENABLED=0", "--workdir", "/src", buildImage,
		"go", "build", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", "/out/gateway", "./cmd/gateway")
	metadata, _ := json.Marshal(manifest.Artifacts[0].Report)
	if err := os.WriteFile(filepath.Join(directory, manifest.Artifacts[0].Metadata.Name), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	dockerfile := "FROM scratch\nWORKDIR /app\nCOPY --chmod=0555 gateway /app/gateway\nUSER 10001\nENTRYPOINT [\"/app/gateway\"]\nEXPOSE 8090 8091\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(binary), "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		t.Fatal(err)
	}
	checked("build", "--network", "none", "--pull=false", "--label", "com.ai.gateway.release-test="+label, "--tag", prefix, filepath.Dir(binary))
	var imageID string
	if json.Unmarshal([]byte(checked("image", "inspect", "--format", "{{json .Id}}", prefix)), &imageID) != nil {
		t.Fatal("fixture image ID is unreadable")
	}
	remove("image", imageID)
	checked("image", "save", "--output", filepath.Join(directory, manifest.Images[0].Archive.Name), imageID)
	manifest.Images[0].ID = imageID
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, file := range []*File{&manifest.Artifacts[0].File, &manifest.Artifacts[0].Metadata, &manifest.Images[0].Archive} {
		*file, err = hashFile(root, file.Name)
		if err != nil {
			t.Fatal(err)
		}
	}
	manifestHash := writeSmokeManifest(t, directory, manifest)
	containerDSN := dsnURL
	containerDSN.Host = "postgres-test:5432"
	waitReady := func(id, origin string) {
		t.Helper()
		transport := &http.Transport{Proxy: nil}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: time.Second}
		until := time.Now().Add(20 * time.Second)
		for {
			response, err := client.Get(origin + "/readyz")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					return
				}
			}
			if time.Now().After(until) {
				t.Fatalf("fixture Gateway did not become ready: %s", checked("logs", id))
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	startGateway := func(name string, init, readonly bool, overlay bool) (string, string) {
		t.Helper()
		// Pin free ephemeral host ports so a restart retains the same HTTP target.
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
		args := []string{"run", "--detach", "--pull=never", "--name", prefix + "-" + name, "--label", "com.ai.gateway.release-test=" + label,
			"--network", networkID, "--tmpfs", "/tmp:rw,noexec,nosuid,mode=1777", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
			"--publish", public.Addr().String() + ":8090", "--publish", admin.Addr().String() + ":8091"}
		if init {
			args = append(args, "--init")
		}
		if readonly {
			args = append(args, "--read-only")
		}
		if overlay {
			args = append(args, "--mount", "type=bind,source="+binary+",target=/app/gateway,readonly")
		}
		values := []string{"DATABASE_URL=" + containerDSN.String(), "SESSION_SECRET=" + strings.Repeat("s", 40), "ADMIN_USERNAME=admin", "ADMIN_PASSWORD=FixtureOnly!Password2026",
			"PUBLIC_ADDR=:8090", "ADMIN_ADDR=:8091", "PUBLIC_BASE_URL=http://127.0.0.1:8090", "ADMIN_BASE_URL=http://127.0.0.1:8091",
			"ADMIN_ALLOWED_CIDRS=0.0.0.0/0", "COOKIE_SECURE=false", "MEDIA_SPOOL_DIR=/tmp/spool"}
		for _, key := range []string{"COMFYUI_UPSTREAM", "OPENWEBUI_UPSTREAM", "OLLAMA_UPSTREAM", "CONTENT_MODERATOR_UPSTREAM", "INFERENCE_WORKER_URL", "MINING_AGENT_URL", "SYSTEM_MONITOR_AGENT_URL", "UPDATE_AGENT_URL", "LORA_TRAINING_AGENT_URL"} {
			values = append(values, key+"=http://127.0.0.1:1")
		}
		for _, value := range values {
			args = append(args, "--env", value)
		}
		public.Close()
		admin.Close()
		id := checked(append(args, imageID)...)
		remove("container", id)
		origin := "http://127.0.0.1:" + published(id, "8090")
		waitReady(id, origin)
		return id, origin
	}
	firstID, firstOrigin := startGateway("direct", false, true, false)
	options := DockerGatewayOptions{EngineEndpoint: endpoint, EngineID: engineID, ContainerID: firstID, GatewayPort: 8090,
		RuntimeBindingOptions: RuntimeBindingOptions{Origin: firstOrigin, ManifestHash: manifestHash, SessionToken: "docker-binding-admin"}}
	assertBinding := func(options DockerGatewayOptions, success bool) {
		t.Helper()
		report, err := ObserveDockerGateway(ctx, db, directory, options)
		if success && (err != nil || !report.Success || report.Identity == nil || report.Gateway == nil) || !success && (err == nil || report.Success) || report.DeploymentAuthorized {
			t.Fatalf("actual Engine binding: %+v %v", report, err)
		}
		body, _ := json.Marshal(report)
		for _, secret := range []string{password, "docker-binding-admin", containerDSN.String(), directory, endpoint} {
			if strings.Contains(string(body), secret) {
				t.Fatal("actual report exposed private data")
			}
		}
	}
	assertBinding(options, true)
	plan, err := ObserveDockerHostPlan(ctx, db, DockerHostPlanOptions{OperationID: hostTestSpec().ID,
		Previous: HostPackageReference{directory, manifestHash}, Candidate: HostPackageReference{directory, manifestHash}, Gateway: options})
	if err != nil {
		t.Fatal("actual Docker plan observation failed", err)
	}
	if !bytes.Contains(plan.data.Observed.Configuration, []byte(containerDSN.String())) {
		t.Fatal("actual plan omitted complete private runtime configuration")
	}
	journalDirectory, journal := hostTestJournal(t)
	if err := journal.Begin(plan.Operation()); err != nil {
		t.Fatal(err)
	}
	if err := journal.PrepareDockerPlan(plan); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	journal, err = OpenHostJournal(journalDirectory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	savedPlan, err := journal.ReadDockerPlan()
	if err != nil || savedPlan.Revalidate(ctx, db, options.SessionToken) != nil {
		t.Fatal("actual persisted plan did not revalidate", err)
	}
	if savedPlan.Revalidate(ctx, db, "docker-binding-user") == nil {
		t.Fatal("plan bypassed ordinary admin authorization")
	}
	adminOptions := options
	adminOptions.GatewayPort, adminOptions.Origin = 8091, "http://127.0.0.1:"+published(firstID, "8091")
	assertBinding(adminOptions, true)
	userOptions := options
	userOptions.SessionToken = "docker-binding-user"
	assertBinding(userOptions, false)
	secondID, secondOrigin := startGateway("init", true, true, false)
	secondOptions := options
	secondOptions.ContainerID, secondOptions.Origin = secondID, secondOrigin
	assertBinding(secondOptions, true)
	secondOptions.Origin = firstOrigin
	assertBinding(secondOptions, false)
	for _, fixture := range []struct {
		name              string
		readonly, overlay bool
	}{{"writable", false, false}, {"overlay", true, true}} {
		id, origin := startGateway(fixture.name, false, fixture.readonly, fixture.overlay)
		invalid := options
		invalid.ContainerID, invalid.Origin = id, origin
		assertBinding(invalid, false)
	}
	preflightExec(t, db, `CREATE DATABASE binding_wrong_database`)
	wrongDSN := dsnURL
	wrongDSN.Path = "/binding_wrong_database"
	wrongDB, err := sql.Open("postgres", wrongDSN.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrongDB.Close() })
	if report, err := ObserveDockerGateway(ctx, wrongDB, directory, options); err == nil || report.Success {
		t.Fatal("actual runtime accepted a different database")
	}
	restarted := false
	report, err := observeDockerGateway(ctx, db, directory, options, func(endpoint string) (*dockerObserver, error) {
		observer, err := newDockerObserver(endpoint)
		if err != nil {
			return nil, err
		}
		observer.client.Transport = dockerFixtureRoundTrip(func(request *http.Request) (*http.Response, error) {
			response, err := observer.transport.RoundTrip(request)
			if err == nil && strings.HasSuffix(request.URL.Path, "/archive") {
				response.Body = &dockerFixtureAfterClose{ReadCloser: response.Body, after: func() {
					checked("restart", "--time", "0", firstID)
					restarted = true
					waitReady(firstID, firstOrigin)
				}}
			}
			return response, err
		})
		return observer, nil
	})
	if !restarted || err == nil || report.Success || report.FailureCode != "container_changed" {
		t.Fatalf("actual restart passed observation: %+v %v", report, err)
	}
	assertBinding(options, true)
	if cli := os.Getenv("RELEASE_DOCKER_TOOL"); cli != "" {
		if savedPlan.Revalidate(ctx, db, options.SessionToken) == nil {
			t.Fatal("saved plan accepted restarted actual Gateway on same port")
		}
		freshPlan, err := ObserveDockerHostPlan(ctx, db, DockerHostPlanOptions{OperationID: hostTestSpec().ID,
			Previous: HostPackageReference{directory, manifestHash}, Candidate: HostPackageReference{directory, manifestHash}, Gateway: options})
		if err != nil || freshPlan.Operation().PlanSHA256 == savedPlan.Operation().PlanSHA256 {
			t.Fatal("fresh plan did not distinguish actual restart", err)
		}
		command := exec.CommandContext(ctx, cli, "--mode", "docker-binding", "--directory", directory, "--docker-endpoint", endpoint, "--engine-id", engineID,
			"--container-id", firstID, "--container-port", "8090", "--public-url", firstOrigin, "--manifest-sha256", manifestHash)
		command.Env = append(os.Environ(), "DATABASE_URL="+dsnURL.String(), "RELEASE_BINDING_SESSION=docker-binding-admin", "DOCKER_HOST=tcp://192.0.2.1:2375", "DOCKER_CONTEXT=untrusted-ignored-context")
		output, err := command.CombinedOutput()
		var report DockerGatewayReport
		if err != nil || json.Unmarshal(output, &report) != nil || !report.Success || report.DeploymentAuthorized {
			t.Fatalf("actual docker-binding CLI: %v\n%s", err, output)
		}
		if bytes.Contains(output, []byte(password)) || bytes.Contains(output, []byte("docker-binding-admin")) {
			t.Fatal("CLI disclosed credentials")
		}
	} else {
		t.Fatal("RELEASE_DOCKER_TOOL is required for the native CLI gate")
	}
	stopPlan, err := ObserveDockerHostPlan(ctx, db, DockerHostPlanOptions{OperationID: hostTestSpec().ID,
		Previous: HostPackageReference{directory, manifestHash}, Candidate: HostPackageReference{directory, manifestHash}, Gateway: options})
	if err != nil {
		t.Fatal("fresh service-control plan", err)
	}
	stopJournalDirectory, stopJournal := hostTestJournal(t)
	if err := stopJournal.Begin(stopPlan.Operation()); err != nil {
		t.Fatal(err)
	}
	if err := stopJournal.PrepareDockerPlan(stopPlan); err != nil {
		t.Fatal(err)
	}
	if err := stopJournal.PrepareRuntimeRecovery(stopPlan.RuntimeRecoveryBinding(0)); err != nil {
		t.Fatal(err)
	}
	if err := stopJournal.PrepareDockerDatabaseConfiguration(ctx, dsnURL.String()); err != nil {
		t.Fatal("actual protected DB configuration", err)
	}
	controller, err := stopJournal.BeginConfiguredDockerGatewayFence(ctx, options.SessionToken, 0)
	if err != nil {
		t.Fatal("actual service DB fence", err)
	}
	controller.Close()
	stopJournal.Close()
	stopJournal, err = OpenHostJournal(stopJournalDirectory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	defer stopJournal.Close()
	controller, err = stopJournal.ResumeConfiguredDockerGatewayFence(ctx, 0)
	if err != nil {
		t.Fatal("actual fence resume from saved connection", err)
	}
	defer controller.Close()
	if observation, err := stopJournal.StopDockerGateway(ctx, controller, 0, 5); err != nil || observation != HostApplied {
		t.Fatal("actual selected Gateway stop", observation, err)
	}
	selected, err := d.readContainer(ctx, firstID)
	if err != nil || selected.State.Running || selected.State.Status != "exited" {
		t.Fatal("selected fixture is still running", err)
	}
	unselected, err := d.readContainer(ctx, secondID)
	if err != nil || !unselected.State.Running || unselected.State.Status != "running" {
		t.Fatal("unselected Gateway was stopped", err)
	}
	// Other deliberately unselected fixtures still own runtime connections.
	// Stopping one declared service cannot establish whole-installation quiet.
	if quiet, err := controller.TryQuiesce(ctx); err != nil || quiet {
		t.Fatal("selected stop incorrectly claimed global DB quiescence", err)
	}
	// The archive must refuse before executing any client tool. This executable
	// is deliberately not pg_dump; the busy-runtime guard must precede --version.
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	testExecutableBytes, err := os.ReadFile(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	archiveOptions := DatabaseArchiveOptions{Tool: PostgreSQLDumpTool{Path: testExecutable, SHA256: hostRecoveryDigest(testExecutableBytes)}, MaxBytes: 1 << 20, TimeoutSeconds: 30}
	if _, err := stopJournal.ArchiveDockerDatabase(ctx, controller, 0, archiveOptions); err == nil {
		t.Fatal("archive accepted a live unselected runtime")
	}
	archiveState, archiveHistory, err := stopJournal.OperationHistory()
	if err != nil || archiveState.Pending.Sequence != 0 {
		t.Fatal("busy archive refusal created pending work", err)
	}
	for _, event := range archiveHistory {
		if event.Effect == HostArchiveDatabase {
			t.Fatal("busy archive refusal recorded a dump intent")
		}
	}
	t.Log("selected full-ID Gateway stopped; unselected Gateway remains running; DB fence remains nonquiescent")
}

type dockerFixtureRoundTrip func(*http.Request) (*http.Response, error)

func (f dockerFixtureRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type dockerFixtureAfterClose struct {
	io.ReadCloser
	once  sync.Once
	after func()
}

func (r *dockerFixtureAfterClose) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.after)
	return err
}
