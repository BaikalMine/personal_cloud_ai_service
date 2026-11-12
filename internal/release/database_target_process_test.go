package release

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestDatabaseTargetProcessHelper(t *testing.T) {
	mode := os.Getenv("RELEASE_TARGET_HELPER_MODE")
	if mode == "" {
		return
	}
	options := databaseArchiveTestOptions(t)
	j, err := OpenHostJournal(os.Getenv("RELEASE_TARGET_HELPER_DIRECTORY"), hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	controller, err := j.ResumeConfiguredDockerGatewayFence(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, _, _, archive, _, receipt, err := j.targetMaterial(ctx, 0, options)
	if err != nil {
		t.Fatal(err)
	}
	target := databaseTargetHash(plan, 0, archive.Target, receipt)
	if mode == "before-create" {
		if _, err := j.Intent(HostCreateRestoreDatabase, target); err != nil {
			t.Fatal(err)
		}
		os.Exit(77)
	}
	base := archiveProcessObserver(os.Getenv("RELEASE_TARGET_HELPER_ENGINE"))
	open := func(endpoint string) (*dockerObserver, error) {
		d, err := base(endpoint)
		if err != nil {
			return nil, err
		}
		if mode == "before-record" {
			d.client.Transport = dockerFixtureRoundTrip(func(request *http.Request) (*http.Response, error) {
				// The second stopped-service observation occurs after the actual
				// CREATE/bootstrap/property transaction, before any completion file.
				if j.state.Pending.Effect == HostCreateRestoreDatabase {
					os.Exit(77)
				}
				return d.transport.RoundTrip(request)
			})
		}
		return d, nil
	}
	if mode == "acknowledged" {
		if _, err := j.createDockerRestoreDatabase(ctx, controller, 0, options, open); err != nil {
			t.Fatal(err)
		}
	} else {
		j.mu.Lock()
		if _, _, err := j.createDatabaseTarget(ctx, controller, 0, options, open); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(77)
}

func TestDatabaseTargetProcessCrash(t *testing.T) {
	options := databaseArchiveTestOptions(t)
	for _, mode := range []string{"before-create", "before-record", "completed", "acknowledged"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			prepareDatabaseArchiveFixture(t, f)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := f.j.archiveDockerDatabase(ctx, f.controller, 0, options, f.open, runPostgreSQLDump); err != nil {
				t.Fatal(err)
			}
			plan, _, _, archive, _, receipt, err := f.j.targetMaterial(ctx, 0, options)
			if err != nil {
				t.Fatal(err)
			}
			target := databaseTargetHash(plan, 0, archive.Target, receipt)
			name := databaseTargetName(target)
			t.Cleanup(func() {
				var exists bool
				if err := f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil {
					t.Error(err)
					return
				}
				if exists {
					preflightExec(t, f.db, `DROP DATABASE `+pq.QuoteIdentifier(name)+` WITH (FORCE)`)
				}
			})
			d, err := f.open(plan.data.EngineEndpoint)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := d.transport.DialContext(ctx, "tcp", "docker")
			if err != nil {
				t.Fatal(err)
			}
			address := probe.RemoteAddr().String()
			probe.Close()
			d.close()
			f.controller.Close()
			f.j.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(ctx, executable, "-test.run=^TestDatabaseTargetProcessHelper$", "-test.v")
			command.Env = append(os.Environ(), "RELEASE_TARGET_HELPER_MODE="+mode,
				"RELEASE_TARGET_HELPER_DIRECTORY="+f.directory, "RELEASE_TARGET_HELPER_ENGINE="+address)
			command.WaitDelay = 5 * time.Second
			configureReleaseCommand(command)
			output, err := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 77 || bytes.Contains(output, []byte("WARNING: DATA RACE")) {
				t.Fatalf("target helper did not stop at crash point: %v\n%s", err, output)
			}
			f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			f.controller, err = f.j.ResumeConfiguredDockerGatewayFence(ctx, 0)
			if err != nil {
				t.Fatal("source controller did not resume", err)
			}
			var exists bool
			if err := f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil || exists != (mode != "before-create") {
				t.Fatal("crash boundary did not match creation", err)
			}
			if mode != "acknowledged" {
				observation, err := f.j.reconcileDockerRestoreDatabase(ctx, f.controller, 0, options, f.open)
				if mode == "completed" {
					if err != nil || observation != HostApplied {
						t.Fatal("completed target lost", observation, err)
					}
				} else {
					if err == nil || observation != HostUnknown || f.j.state.Pending.Effect != HostCreateRestoreDatabase {
						t.Fatal("unrecorded target adopted", observation, err)
					}
					if _, err := f.j.ReadDockerRestoreDatabase(ctx, 0, options); err == nil {
						t.Fatal("unknown creation exposed")
					}
					return
				}
			}
			if _, err := f.j.ReadDockerRestoreDatabase(ctx, 0, options); err != nil {
				t.Fatal(err)
			}
			payload, _, err := f.j.readDatabaseTargetMetadata(ctx, target, "")
			if err != nil {
				t.Fatal(err)
			}
			requireRestoreRuntimeBlocked(t, payload.Configuration.URL)
			if f.posts.Load() != 1 {
				t.Fatal("target crash/recovery changed service")
			}
		})
	}
}
