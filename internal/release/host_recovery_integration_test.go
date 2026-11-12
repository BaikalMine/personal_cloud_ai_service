package release

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The native DPAPI crash test also resumes a real PostgreSQL fence. The parent
// native-gate script already knows this fixture label and provides cleanup even
// if this process exits before Go cleanup. No existing database is selected.
func hostRecoveryNativePostgres(t *testing.T) {
	t.Helper()
	endpoint, image, label := os.Getenv("RELEASE_DOCKER_ENDPOINT"), os.Getenv("RELEASE_DOCKER_POSTGRES_IMAGE"), os.Getenv("RELEASE_DOCKER_FIXTURE_ID")
	decoded, err := hex.DecodeString(label)
	if err != nil || len(decoded) != 12 || strings.ToLower(label) != label || !validDigest(image) {
		t.Fatal("native recovery fixture configuration is invalid")
	}
	observer, err := newDockerObserver(endpoint)
	if err != nil {
		t.Fatal("native recovery fixture requires local Engine")
	}
	defer observer.close()
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("Docker unavailable for native recovery fixture")
	}
	environment := make([]string, 0)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "DOCKER_") {
			environment = append(environment, entry)
		}
	}
	run := func(ctx context.Context, args ...string) (string, error) {
		command := exec.CommandContext(ctx, docker, append([]string{"--host", endpoint}, args...)...)
		hideHostRecoveryProcess(command)
		command.Env = environment
		output, err := command.CombinedOutput()
		return strings.TrimSpace(string(output)), err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	checked := func(args ...string) string {
		t.Helper()
		output, err := run(ctx, args...)
		if err != nil {
			t.Fatal("native recovery fixture command failed")
		}
		return output
	}
	remove := func(kind, id string) {
		t.Helper()
		if !hashPattern.MatchString(id) {
			t.Fatal("native recovery fixture ID is invalid")
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			args := []string{kind, "rm"}
			if kind == "container" {
				args = append(args, "--force")
			}
			if _, err := run(ctx, append(args, id)...); err != nil {
				t.Error("native recovery fixture cleanup failed")
			}
		})
	}
	prefix := "gateway-release-recovery-" + label
	network := checked("network", "create", "--driver", "bridge", "--label", "com.ai.gateway.release-test="+label, prefix)
	remove("network", network)
	password := "fixture-" + label
	container := checked("run", "--detach", "--pull=never", "--name", prefix+"-postgres", "--label", "com.ai.gateway.release-test="+label,
		"--network", network, "--tmpfs", "/var/lib/postgresql/data:rw", "--publish", "127.0.0.1::5432", "--env", "POSTGRES_PASSWORD="+password, image)
	remove("container", container)
	var ports map[string][]dockerPortBinding
	if json.Unmarshal([]byte(checked("container", "inspect", "--format", "{{json .NetworkSettings.Ports}}", container)), &ports) != nil ||
		len(ports["5432/tcp"]) != 1 || ports["5432/tcp"][0].HostIP != "127.0.0.1" {
		t.Fatal("native recovery fixture port is not loopback-only")
	}
	dsn := url.URL{Scheme: "postgres", User: url.UserPassword("postgres", password), Host: net.JoinHostPort("127.0.0.1", ports["5432/tcp"][0].HostPort), Path: "/postgres", RawQuery: "sslmode=disable"}
	db, err := sql.Open("postgres", dsn.String())
	if err != nil {
		t.Fatal("native recovery fixture DB open failed")
	}
	defer db.Close()
	for {
		probe, stop := context.WithTimeout(ctx, time.Second)
		err := db.PingContext(probe)
		stop()
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("native recovery fixture PostgreSQL did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Setenv("TEST_DATABASE_URL", dsn.String())
}
