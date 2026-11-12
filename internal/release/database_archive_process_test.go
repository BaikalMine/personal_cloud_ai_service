package release

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func archiveProcessObserver(address string) func(string) (*dockerObserver, error) {
	return func(string) (*dockerObserver, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return nil, ErrDatabaseArchive
		}
		transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", address)
		}}
		return &dockerObserver{transport: transport, client: &http.Client{Transport: transport, Timeout: 3 * time.Second}}, nil
	}
}

type abruptArchiveWriter struct{ output io.Writer }

func (w abruptArchiveWriter) Write(body []byte) (int, error) {
	n, err := w.output.Write(body)
	if n > 0 {
		os.Exit(77)
	}
	return n, err
}

func TestDatabaseArchiveProcessHelper(t *testing.T) {
	mode := os.Getenv("RELEASE_ARCHIVE_HELPER_MODE")
	if mode == "" {
		return
	}
	options := databaseArchiveTestOptions(t)
	j, err := OpenHostJournal(os.Getenv("RELEASE_ARCHIVE_HELPER_DIRECTORY"), hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	controller, err := j.ResumeConfiguredDockerGatewayFence(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := j.ReadDockerPlan()
	if err != nil {
		t.Fatal(err)
	}
	if mode == "before_file" {
		if _, err := j.Intent(HostArchiveDatabase, databaseArchiveTarget(plan, 0, options)); err != nil {
			t.Fatal(err)
		}
		os.Exit(77)
	}
	open := archiveProcessObserver(os.Getenv("RELEASE_ARCHIVE_HELPER_ENGINE"))
	dump := func(ctx context.Context, options DatabaseArchiveOptions, environment []string, directory, snapshot string, output io.Writer) error {
		if mode == "during_dump" {
			output = abruptArchiveWriter{output}
		}
		err := runPostgreSQLDump(ctx, options, environment, directory, snapshot, output)
		if err == nil && mode == "after_dump" {
			os.Exit(77)
		}
		return err
	}
	if mode == "acknowledged" {
		if _, err := j.archiveDockerDatabase(ctx, controller, 0, options, open, dump); err != nil {
			t.Fatal(err)
		}
	} else {
		j.mu.Lock()
		if _, err := j.createDockerDatabaseArchive(ctx, controller, 0, options, open, dump); err != nil {
			t.Fatal(err)
		}
	}
	// No Close, Rollback or temporary-file cleanup defers can run at this point.
	os.Exit(77)
}

func TestDatabaseArchiveProcessCrash(t *testing.T) {
	options := databaseArchiveTestOptions(t)
	for _, mode := range []string{"before_file", "during_dump", "after_dump", "completed", "acknowledged"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			prepareDatabaseArchiveFixture(t, f)
			observer, err := f.open(f.plan.data.EngineEndpoint)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := observer.transport.DialContext(context.Background(), "tcp", "docker")
			if err != nil {
				t.Fatal(err)
			}
			address := probe.RemoteAddr().String()
			probe.Close()
			observer.close()
			f.controller.Close()
			f.j.Close()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestDatabaseArchiveProcessHelper$", "-test.v")
			command.Env = append(os.Environ(), "RELEASE_ARCHIVE_HELPER_MODE="+mode,
				"RELEASE_ARCHIVE_HELPER_DIRECTORY="+f.directory, "RELEASE_ARCHIVE_HELPER_ENGINE="+address)
			command.WaitDelay = 5 * time.Second
			configureReleaseCommand(command)
			output, err := command.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 77 || bytes.Contains(output, []byte("WARNING: DATA RACE")) {
				t.Fatalf("archive helper did not stop at crash point: %v\n%s", err, output)
			}
			// The only pg_dump child belongs to this newly created fixture DB.
			// Its pipe loses its reader when the coordinator exits.
			deadline := time.Now().Add(5 * time.Second)
			for {
				var active int
				if err := f.db.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name='gateway-release-dump'`).Scan(&active); err != nil {
					t.Fatal(err)
				}
				if active == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("orphan pg_dump retained a database connection")
				}
				time.Sleep(20 * time.Millisecond)
			}
			f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			f.controller, err = f.j.ResumeConfiguredDockerGatewayFence(ctx, 0)
			if err != nil {
				t.Fatal("original controller did not resume", err)
			}
			target := databaseArchiveTarget(f.plan, 0, options)
			archiveName, _, passwordName, _ := databaseArchiveNames(target)
			before, _ := os.ReadFile(filepath.Join(f.directory, archiveName))
			if mode != "acknowledged" {
				result, err := f.j.reconcileDockerDatabaseArchive(ctx, f.controller, 0, options, f.open)
				switch mode {
				case "before_file":
					if err != nil || result != HostNotApplied {
						t.Fatal("absent dump not reconciled", result, err)
					}
					if _, err := f.j.archiveDockerDatabase(ctx, f.controller, 0, options, f.open, runPostgreSQLDump); err != nil {
						t.Fatal("explicit retry after proven absence failed", err)
					}
				case "completed":
					if err != nil || result != HostApplied {
						t.Fatal("complete original archive not recovered", result, err)
					}
				default:
					if err == nil || result != HostUnknown {
						t.Fatal("unreceipted output accepted", result, err)
					}
					if _, err := f.j.ReadDockerDatabaseArchive(ctx, 0, options); err == nil {
						t.Fatal("unknown output exposed as archive")
					}
					if _, err := f.j.archiveDockerDatabase(ctx, f.controller, 0, options, f.open, runPostgreSQLDump); !errors.Is(err, ErrHostJournalState) {
						t.Fatal("unknown output retried", err)
					}
					password, err := f.j.archiveProof(ctx, passwordName, maxDockerDatabaseURLBytes*2)
					if err != nil || password.Bytes == 0 {
						t.Fatal("crash credential file lost its private protection", err)
					}
				}
			}
			if mode == "completed" || mode == "acknowledged" {
				after, err := os.ReadFile(filepath.Join(f.directory, archiveName))
				if err != nil || len(before) == 0 || !bytes.Equal(before, after) {
					t.Fatal("recovery rewrote the original dump", err)
				}
				if _, err := f.j.ReadDockerDatabaseArchive(ctx, 0, options); err != nil {
					t.Fatal("recovered receipt unavailable", err)
				}
			}
			if f.posts.Load() != 1 {
				t.Fatal("archive recovery changed a service")
			}
		})
	}
}
