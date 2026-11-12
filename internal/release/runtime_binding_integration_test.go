package release

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/security"
)

func TestRuntimeBindingActualNormalGateway(t *testing.T) {
	db := releaseTestDatabase(t)
	other := releaseTestDatabase(t)
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("synthetic package image is Linux/amd64")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	directory, manifest, _ := smokeFixtureOptions(t)
	binary := buildStampedGatewayFixture(t, ctx, directory, &manifest)
	manifestHash := writeSmokeManifest(t, directory, manifest)
	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	dsn := releaseTestDSN(t, os.Getenv("TEST_DATABASE_URL"), name)
	for _, role := range []string{"admin", "user"} {
		var id int64
		if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES($1,'unused',$1) RETURNING id`, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		preflightExec(t, db, `INSERT INTO sessions(user_id,token_hash,expires_at) VALUES($1,$2,now()+interval '1 hour')`, id, security.HashToken("runtime-binding-"+role))
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
	publicURL, adminURL := "http://"+public.Addr().String(), "http://"+admin.Addr().String()
	spool := t.TempDir()
	// No inherited production environment, real upstream or host data directory.
	environment := []string{
		"DATABASE_URL=" + dsn, "SESSION_SECRET=" + strings.Repeat("s", 40),
		"ADMIN_USERNAME=admin", "ADMIN_PASSWORD=FixtureOnly!Password2026",
		"PUBLIC_ADDR=" + public.Addr().String(), "ADMIN_ADDR=" + admin.Addr().String(),
		"PUBLIC_BASE_URL=" + publicURL, "ADMIN_BASE_URL=" + adminURL, "ADMIN_ALLOWED_CIDRS=127.0.0.0/8",
		"COOKIE_SECURE=false", "MEDIA_SPOOL_DIR=" + spool, "TMPDIR=" + spool, "TEMP=" + spool, "TMP=" + spool,
	}
	for _, key := range []string{"COMFYUI_UPSTREAM", "OPENWEBUI_UPSTREAM", "OLLAMA_UPSTREAM", "CONTENT_MODERATOR_UPSTREAM", "INFERENCE_WORKER_URL", "MINING_AGENT_URL", "SYSTEM_MONITOR_AGENT_URL", "UPDATE_AGENT_URL", "LORA_TRAINING_AGENT_URL"} {
		environment = append(environment, key+"=http://127.0.0.1:1")
	}
	public.Close()
	admin.Close()
	processCtx, stop := context.WithCancel(ctx)
	defer stop()
	process := exec.CommandContext(processCtx, binary)
	process.Env, process.Dir = environment, spool
	var output bytes.Buffer
	process.Stdout, process.Stderr = &output, &output
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	waited := false
	defer func() {
		stop()
		if !waited {
			<-done
		}
		if t.Failed() {
			t.Log(output.String())
		}
	}()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			waited = true
			t.Fatalf("normal Gateway exited: %v", err)
		default:
		}
		response, err := client.Get(publicURL + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("normal Gateway did not become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, origin := range []string{publicURL, adminURL} {
		t.Run(origin, func(t *testing.T) {
			options := RuntimeBindingOptions{Origin: origin, ManifestHash: manifestHash, SessionToken: "runtime-binding-admin"}
			report, err := ObserveGatewayDatabase(ctx, db, directory, options)
			if err != nil || !report.Success || report.DeploymentAuthorized || report.Gateway == nil || report.Gateway.Build != manifest.Build {
				t.Fatalf("actual normal Gateway: %+v %v", report, err)
			}
			report, err = ObserveGatewayDatabase(ctx, other, directory, options)
			if err == nil || report.Success || report.FailureCode != "binding_unavailable" {
				t.Fatalf("accepted other database: %+v %v", report, err)
			}
			for _, token := range []string{"runtime-binding-user", "expired-or-forged"} {
				options.SessionToken = token
				report, err = ObserveGatewayDatabase(ctx, db, directory, options)
				if err == nil || report.Success || report.FailureCode != "binding_unavailable" {
					t.Fatalf("accepted nonadmin session: %+v %v", report, err)
				}
			}
			response, err := client.Get(origin + "/admin/release/binding")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				t.Fatal("anonymous binding succeeded")
			}
		})
	}
	var work, probeLocks int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM generation_jobs)+(SELECT count(*) FROM gpu_work),
	 (SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid<>0 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&work, &probeLocks); err != nil || work != 0 || probeLocks != 0 {
		t.Fatalf("unexpected side effects: work=%d locks=%d err=%v", work, probeLocks, err)
	}
}
