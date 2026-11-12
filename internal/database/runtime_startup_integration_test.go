package database

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/buildinfo"
)

// Exercise the production entry points with a synthetic environment, never the
// host's configuration. All service endpoints are loopback in the test network.
func TestRuntimeFenceActualProcesses(t *testing.T) {
	db, dsn := runtimeTestDatabase(t, true)
	c, err := BeginRuntimeRelease(context.Background(), db, runtimeRequest())
	if err != nil {
		t.Fatal(err)
	}
	c.Close() // A persisted fence must survive loss of the controller process.
	for _, component := range []string{"gateway", "inference-worker"} {
		t.Run(component, func(t *testing.T) {
			binary := buildRuntimeProcess(t, component)
			for _, mode := range []string{"normal", "restore-drill"} {
				t.Run(mode, func(t *testing.T) {
					spool := t.TempDir()
					marker := filepath.Join(spool, "gateway-media-release-sentinel")
					if err := os.WriteFile(marker, []byte("preserve until admitted"), 0600); err != nil {
						t.Fatal(err)
					}
					env := runtimeProcessEnvironment(dsn, spool, map[string]string{"RESTORE_DRILL_MODE": strconv.FormatBool(mode == "restore-drill")})
					runCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
					defer stop()
					cmd := exec.CommandContext(runCtx, binary)
					cmd.Env, cmd.Dir = env, spool
					out, err := cmd.CombinedOutput()
					if runCtx.Err() != nil || err == nil || !strings.Contains(string(out), ErrRuntimeFenced.Error()) {
						t.Fatalf("process did not fail promptly at runtime fence: %v (%v)\n%s", err, runCtx.Err(), out)
					}
					data, err := os.ReadFile(marker)
					if err != nil || string(data) != "preserve until admitted" {
						t.Fatalf("fenced process changed media spool: %q %v", data, err)
					}
					if _, err := os.Stat(filepath.Join(spool, "outcomes")); !os.IsNotExist(err) {
						t.Fatalf("fenced worker initialized outcome storage: %v", err)
					}
					cmd = exec.CommandContext(runCtx, binary, "--version-json")
					cmd.Env, cmd.Dir = env, spool
					out, err = cmd.CombinedOutput()
					var report buildinfo.Report
					if err != nil || json.Unmarshal(out, &report) != nil || report.Component != component {
						t.Fatalf("fenced build metadata unavailable: %v\n%s", err, out)
					}
					found := false
					for _, contract := range report.Contracts {
						found = found || contract.Name == "release-runtime-fence" && contract.Version == RuntimeFenceProtocol
					}
					if !found {
						t.Fatalf("missing runtime fence contract: %+v", report.Contracts)
					}
				})
			}
		})
	}
	var users int
	if err := db.QueryRow(`SELECT count(*) FROM users`).Scan(&users); err != nil || users != 0 {
		t.Fatalf("fenced Gateway performed account bootstrap: %d %v", users, err)
	}
	state, err := ReadRuntimeFence(context.Background(), db)
	if err != nil || !state.Active || state.Epoch != 1 {
		t.Fatalf("startup changed durable fence: %+v %v", state, err)
	}
}
