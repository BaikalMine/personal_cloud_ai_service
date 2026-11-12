package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-access-gateway/internal/database"
)

func TestDockerDatabaseConfigurationURLBoundary(t *testing.T) {
	base := "postgres://operator:private-password@127.0.0.1:5432/gateway?sslmode=disable"
	canonical, err := canonicalDockerDatabaseURL(base)
	if err != nil || canonical != "postgres://operator:private-password@127.0.0.1:5432/gateway?connect_timeout=5&sslmode=disable" {
		t.Fatal("canonical explicit URL", err)
	}
	for _, raw := range []string{
		"", "host=localhost dbname=gateway", "postgres:///gateway?sslmode=disable", "https://operator:password@127.0.0.1:5432/gateway?sslmode=disable",
		"postgres://operator@127.0.0.1:5432/gateway?sslmode=disable", "postgres://operator:@127.0.0.1:5432/gateway?sslmode=disable",
		"postgres://:password@127.0.0.1:5432/gateway?sslmode=disable", "postgres://operator:password@127.0.0.1/gateway?sslmode=disable",
		"postgres://operator:password@127.0.0.1:0/gateway?sslmode=disable", "postgres://operator:password@127.0.0.1:65536/gateway?sslmode=disable",
		"postgres://operator:password@127.0.0.1:5432/?sslmode=disable", strings.Replace(base, "sslmode=disable", "sslmode=prefer", 1),
		base + "#fragment", base + "&sslmode=require", base + "&sslmode=", base + "&host=another", base + "&password=another",
		base + "&options=-csearch_path%3Dprivate", base + "&service=other", base + "&connect_timeout=0", base + "&connect_timeout=31",
		base + "&sslrootcert=relative", strings.Replace(base, "sslmode=disable", "sslmode=verify-full", 1),
		strings.Replace(base, "private-password", "%00private", 1), base + "&sslinline=true",
	} {
		if _, err := canonicalDockerDatabaseURL(raw); !errors.Is(err, ErrDockerDatabaseConfiguration) {
			t.Fatal("accepted ambiguous connection or exposed parser diagnostics")
		}
	}
	for _, host := range []string{"[::1]:5432", "database.internal:5432"} {
		raw := strings.Replace(base, "127.0.0.1:5432", host, 1)
		if _, err := canonicalDockerDatabaseURL(raw); err != nil {
			t.Fatal("explicit endpoint refused", err)
		}
	}
	encoded := "postgresql://a%27b:p%27ass%5Cword@127.0.0.1:05432/gateway?sslmode=disable"
	if _, err := canonicalDockerDatabaseURL(encoded); err != nil {
		t.Fatal("URL escaping was not preserved", err)
	}
}

func TestDockerDatabaseConfigurationTLSFiles(t *testing.T) {
	for _, change := range []string{"content", "replace", "missing"} {
		t.Run(change, func(t *testing.T) {
			directory := t.TempDir()
			files := make(map[string]string)
			for _, option := range []string{"sslrootcert", "sslcert", "sslkey"} {
				path := filepath.Join(directory, option+".pem")
				if err := os.WriteFile(path, []byte("fixture certificate bytes "+option), 0o600); err != nil {
					t.Fatal(err)
				}
				files[option] = path
			}
			u, _ := url.Parse("postgres://operator:private-password@127.0.0.1:5432/gateway?sslmode=verify-full")
			query := u.Query()
			for key, path := range files {
				query.Set(key, path)
			}
			u.RawQuery = query.Encode()
			configuration, err := observeDockerDatabaseConfiguration(u.String())
			if err != nil || len(configuration.TLSFiles) != 3 || configuration.validate() != nil {
				t.Fatal(err)
			}
			path := files["sslkey"]
			switch change {
			case "content":
				if err := os.WriteFile(path, []byte("changed private bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "replace":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("fixture certificate bytes sslkey"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if configuration.validate() == nil {
				t.Fatal("changed TLS file accepted")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "absent-client.pem")
	target := dockerDatabaseTLSFile{Option: "default_sslcert_absent", Path: path, Absent: true}
	if _, err := observeDockerDatabaseTLS(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unexpected ambient client certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := observeDockerDatabaseTLS(target); err == nil {
		t.Fatal("ambient client certificate adopted")
	}
}

func dockerConfigurationFixtureURL(t *testing.T, f *dockerStopFixture) string {
	t.Helper()
	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil || u.Scheme == "" {
		t.Fatal("configuration fixture needs explicit URL")
	}
	u.Path = "/" + f.plan.data.Observed.Database.DatabaseName
	return u.String()
}

func TestDockerDatabaseConfigurationFenceLifecycle(t *testing.T) {
	f := newDockerStopFixtureWithFence(t, false)
	connectionURL := dockerConfigurationFixtureURL(t, f)
	if err := f.j.PrepareDockerDatabaseConfiguration(context.Background(), connectionURL); err != nil {
		t.Fatal(err)
	}
	if err := f.j.PrepareDockerDatabaseConfiguration(context.Background(), connectionURL); !errors.Is(err, ErrHostJournalState) {
		t.Fatal("rewrote connection", err)
	}
	before, err := os.ReadFile(filepath.Join(f.directory, f.j.databaseConfigName()))
	if err != nil {
		t.Fatal(err)
	}
	_, history, err := f.j.OperationHistory()
	if err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(history)
	log, _ := os.ReadFile(filepath.Join(f.directory, hostJournalName))
	u, _ := url.Parse(connectionURL)
	password, _ := u.User.Password()
	for _, secret := range []string{password, connectionURL, f.plan.data.Observed.Database.DatabaseName} {
		if bytes.Contains(public, []byte(secret)) || bytes.Contains(log, []byte(secret)) {
			t.Fatal("connection escaped into public journal")
		}
	}
	f.controller, err = f.j.configuredDockerGatewayFence(context.Background(), "docker-binding-session-secret", 0, false, f.open)
	if err != nil {
		t.Fatal(err)
	}
	f.controller.Close()
	f.j.Close()
	f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
	if err != nil {
		t.Fatal(err)
	}
	f.controller, err = f.j.ResumeConfiguredDockerGatewayFence(context.Background(), 0)
	if err != nil {
		t.Fatal("could not resume from original configuration", err)
	}
	if result, err := f.j.dockerStop(context.Background(), f.controller, 0, 2, false, f.open); err != nil || result != HostApplied {
		t.Fatal(result, err)
	}
	after, err := os.ReadFile(filepath.Join(f.directory, f.j.databaseConfigName()))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("resume rewrote credentials", err)
	}
	state, err := database.ReadRuntimeFence(context.Background(), f.db)
	if err != nil || !state.Active || f.posts.Load() != 1 {
		t.Fatal("configured controller lost fence", err)
	}
}

func TestDockerDatabaseConfigurationRefusals(t *testing.T) {
	for _, mode := range []string{"wrong_database", "wrong_password", "ambient", "after_fence", "missing", "truncated", "replaced", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			raw := dockerConfigurationFixtureURL(t, f)
			if mode == "wrong_database" {
				other := releaseTestDatabase(t)
				var name string
				if err := other.QueryRow(`SELECT current_database()`).Scan(&name); err != nil {
					t.Fatal(err)
				}
				u, _ := url.Parse(raw)
				u.Path = "/" + name
				raw = u.String()
			} else if mode == "wrong_password" {
				u, _ := url.Parse(raw)
				u.User = url.UserPassword(u.User.Username(), "must-not-appear-private-password")
				raw = u.String()
			} else if mode == "ambient" {
				t.Setenv("PGOPTIONS", "--search_path=private")
			} else if mode == "after_fence" {
				if _, err := f.j.Intent(HostFence, f.j.recoveryTarget(f.plan.RuntimeRecoveryBinding(0))); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if mode == "receipt" {
				configuration, configErr := observeDockerDatabaseConfiguration(raw)
				if configErr != nil {
					t.Fatal(configErr)
				}
				pending, intentErr := f.j.Intent(HostPrepareDockerDatabase, f.j.state.Operation.PlanSHA256)
				if intentErr != nil {
					t.Fatal(intentErr)
				}
				if _, createErr := f.j.createDockerDatabaseConfiguration(configuration); createErr != nil {
					t.Fatal(createErr)
				}
				err = f.j.Observe(pending.Sequence, HostApplied, strings.Repeat("f", 64))
			} else {
				err = f.j.PrepareDockerDatabaseConfiguration(context.Background(), raw)
			}
			switch mode {
			case "wrong_database", "wrong_password", "ambient", "after_fence":
				if err == nil {
					t.Fatal("unsafe configuration prepared")
				}
				if strings.Contains(err.Error(), "must-not-appear") {
					t.Fatal("password leaked")
				}
				if _, err := os.Stat(filepath.Join(f.directory, f.j.databaseConfigName())); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("refused connection wrote material")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.directory, f.j.databaseConfigName())
			switch mode {
			case "missing":
				err = os.Remove(path)
			case "truncated":
				err = os.WriteFile(path, []byte("{"), 0o600)
			case "replaced":
				payload, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(path, payload, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if controller, err := f.j.configuredDockerGatewayFence(context.Background(), "docker-binding-session-secret", 0, false, f.open); err == nil {
				controller.Close()
				t.Fatal("lost configuration used")
			}
			state, err := database.ReadRuntimeFence(context.Background(), f.db)
			if err != nil || state.Active || state.Epoch != 0 || f.posts.Load() != 0 {
				t.Fatal("bad configuration mutated service or fence", err)
			}
		})
	}
}

func TestDockerDatabaseConfigurationLossBlocksExistingController(t *testing.T) {
	f := newDockerStopFixtureWithFence(t, false)
	if err := f.j.PrepareDockerDatabaseConfiguration(context.Background(), dockerConfigurationFixtureURL(t, f)); err != nil {
		t.Fatal(err)
	}
	var err error
	f.controller, err = f.j.configuredDockerGatewayFence(context.Background(), "docker-binding-session-secret", 0, false, f.open)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.directory, f.j.databaseConfigName())); err != nil {
		t.Fatal(err)
	}
	if _, err := f.j.dockerStop(context.Background(), f.controller, 0, 2, false, f.open); err == nil || f.posts.Load() != 0 {
		t.Fatal("lost recovery connection did not stop dispatch")
	}
	f.controller.Close()
	if controller, err := f.j.ResumeConfiguredDockerGatewayFence(context.Background(), 0); err == nil {
		controller.Close()
		t.Fatal("missing credentials regenerated")
	}
	state, err := database.ReadRuntimeFence(context.Background(), f.db)
	if err != nil || !state.Active {
		t.Fatal("missing credentials unfenced DB", err)
	}
}

func TestDockerDatabaseConfigurationConnectorIsImmutable(t *testing.T) {
	f := newDockerStopFixtureWithFence(t, false)
	configuration, err := observeDockerDatabaseConfiguration(dockerConfigurationFixtureURL(t, f))
	if err != nil {
		t.Fatal(err)
	}
	db, err := openDockerDatabase(context.Background(), configuration, f.plan.data.Observed.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Open's probe was closed with MaxIdleConns(0). This must be a new physical
	// connection without reparsing ambient lib/pq options or its unsupported keys.
	t.Setenv("PGSERVICE", "unexpected-ambient-service")
	t.Setenv("PGHOST", "192.0.2.1")
	var name string
	if err := db.QueryRowContext(context.Background(), `SELECT current_database()`).Scan(&name); err != nil || name != f.plan.data.Observed.Database.DatabaseName {
		t.Fatal("connector adopted later environment", err)
	}
	if reopened, err := openDockerDatabase(context.Background(), configuration, f.plan.data.Observed.Database); err == nil {
		reopened.Close()
		t.Fatal("new configuration open accepted ambient settings")
	}
}
