package release

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
)

func hostDatabaseConfigurationFixture(t *testing.T) (string, *HostJournal, dockerDatabaseConfiguration) {
	t.Helper()
	directory, j := hostTestJournal(t)
	if err := j.Begin(hostPlanFixture(directory).Operation()); err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareDockerPlan(hostPlanFixture(directory)); err != nil {
		t.Fatal(err)
	}
	configuration, err := observeDockerDatabaseConfiguration("postgres://fixture-user:fixture-private-password@127.0.0.1:5432/fixture-private-db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := j.Intent(HostPrepareDockerDatabase, j.state.Operation.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := j.createDockerDatabaseConfiguration(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Observe(pending.Sequence, HostApplied, digest); err != nil {
		t.Fatal(err)
	}
	return directory, j, configuration
}

func TestHostDatabaseConfigurationFileContext(t *testing.T) {
	for _, change := range []string{"reopen", "missing", "partial", "replace", "copy", "receipt"} {
		t.Run(change, func(t *testing.T) {
			directory, j, configuration := hostDatabaseConfigurationFixture(t)
			receipt, err := j.databaseConfigReceipt()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, j.databaseConfigName())
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "reopen":
				j.Close()
				j, err = OpenHostJournal(directory, hostTestInstallation)
				if err != nil {
					t.Fatal(err)
				}
				defer j.Close()
			case "missing":
				err = os.Remove(path)
			case "partial":
				err = os.WriteFile(path, []byte("{"), 0o600)
			case "replace", "copy":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(path, before, 0o600)
				if change == "copy" {
					otherDirectory, other, _ := hostDatabaseConfigurationFixture(t)
					if err := os.WriteFile(filepath.Join(otherDirectory, other.databaseConfigName()), before, 0o600); err != nil {
						t.Fatal(err)
					}
					j = other
				}
			case "receipt":
				receipt = strings.Repeat("f", 64)
			}
			if err != nil {
				t.Fatal(err)
			}
			loaded, _, err := j.readDockerDatabaseConfiguration(receipt)
			if change == "reopen" {
				if err != nil || loaded.URL != configuration.URL {
					t.Fatal("configuration did not survive owner change", err)
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("read rewrote secret configuration")
				}
			} else if err == nil {
				t.Fatal("invalid private context accepted")
			}
		})
	}
}

func TestHostDatabaseConfigurationProcessHelper(t *testing.T) {
	mode := os.Getenv("RELEASE_HOST_DATABASE_HELPER")
	if mode == "" {
		return
	}
	j, err := OpenHostJournal(os.Getenv("RELEASE_HOST_DATABASE_DIRECTORY"), hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw := os.Getenv("RELEASE_HOST_DATABASE_URL")
	if mode == "prepared" || mode == "fenced" {
		if err := j.PrepareDockerDatabaseConfiguration(ctx, raw); err != nil {
			t.Fatal(err)
		}
		if mode == "fenced" {
			plan, err := j.ReadDockerPlan()
			if err != nil {
				t.Fatal(err)
			}
			binding := plan.RuntimeRecoveryBinding(0)
			if _, err := j.Intent(HostFence, j.recoveryTarget(binding)); err != nil {
				t.Fatal(err)
			}
			if err := j.WithRuntimeRecovery(binding, func(request database.RuntimeReleaseRequest) error {
				receipt, err := j.databaseConfigReceipt()
				if err != nil {
					return err
				}
				configuration, _, err := j.readDockerDatabaseConfiguration(receipt)
				if err != nil {
					return err
				}
				db, err := openDockerDatabase(ctx, configuration, plan.data.Observed.Database)
				if err != nil {
					return err
				}
				defer db.Close()
				controller, err := database.BeginRuntimeReleaseForDatabase(ctx, db, request, plan.data.Observed.Database)
				if err != nil {
					return err
				}
				defer controller.Close()
				os.Exit(77)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		os.Exit(77)
	}
	if _, err := j.Intent(HostPrepareDockerDatabase, j.state.Operation.PlanSHA256); err != nil {
		t.Fatal(err)
	}
	if mode == "before_file" {
		os.Exit(77)
	}
	if mode == "partial" {
		file, err := j.root.OpenFile(j.databaseConfigName(), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("{\"version\":1")); err != nil {
			t.Fatal(err)
		}
		if err := file.Sync(); err != nil {
			t.Fatal(err)
		}
		os.Exit(77)
	}
	if mode != "before_observe" {
		t.Fatal("unknown helper mode")
	}
	configuration, err := observeDockerDatabaseConfiguration(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.createDockerDatabaseConfiguration(configuration); err != nil {
		t.Fatal(err)
	}
	os.Exit(77)
}

func TestHostDatabaseConfigurationProcessCrash(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" && os.Getenv("RELEASE_DOCKER_TESTS") == "1" {
		hostRecoveryNativePostgres(t)
	}
	for _, mode := range []string{"before_file", "partial", "before_observe", "prepared", "fenced"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			raw := dockerConfigurationFixtureURL(t, f)
			f.j.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHostDatabaseConfigurationProcessHelper$")
			hideHostRecoveryProcess(command)
			command.Env = append(os.Environ(), "RELEASE_HOST_DATABASE_HELPER="+mode, "RELEASE_HOST_DATABASE_DIRECTORY="+f.directory, "RELEASE_HOST_DATABASE_URL="+raw)
			output, err := command.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 77 {
				t.Fatalf("process did not reach selected crash boundary: %v\n%s", err, output)
			}
			f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "fenced" {
				f.controller, err = f.j.ResumeConfiguredDockerGatewayFence(ctx, 0)
				if err != nil {
					t.Fatal("original credentials did not reclaim actual crashed DB controller", err)
				}
				if result, err := f.j.dockerStop(ctx, f.controller, 0, 2, false, f.open); err != nil || result != HostApplied {
					t.Fatal(result, err)
				}
				return
			}
			if mode != "prepared" {
				observed, err := f.j.ReconcileDockerDatabaseConfiguration(ctx)
				if mode == "partial" {
					if err == nil || f.j.state.Pending.Effect != HostPrepareDockerDatabase {
						t.Fatal("partial file resolved", observed, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if mode == "before_file" {
					if observed != HostNotApplied {
						t.Fatal("absence misclassified", observed)
					}
					if err := f.j.PrepareDockerDatabaseConfiguration(ctx, raw); err != nil {
						t.Fatal(err)
					}
				} else if observed != HostApplied {
					t.Fatal("complete original file not recovered", observed)
				}
			}
			f.controller, err = f.j.configuredDockerGatewayFence(ctx, "docker-binding-session-secret", 0, false, f.open)
			if err != nil {
				t.Fatal("recovered credentials unavailable", err)
			}
		})
	}
}
