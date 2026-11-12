package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
)

type dockerStopFixture struct {
	db         *sql.DB
	plan       DockerHostPlan
	j          *HostJournal
	directory  string
	open       func(string) (*dockerObserver, error)
	controller *database.RuntimeRelease
	mode       atomic.Value
	stopped    atomic.Bool
	posts      atomic.Int64
	intentFile atomic.Pointer[os.File]
}

func newDockerStopFixture(t *testing.T) *dockerStopFixture {
	t.Helper()
	return newDockerStopFixtureWithFence(t, true)
}

func newDockerStopFixtureWithFence(t *testing.T, begin bool) *dockerStopFixture {
	t.Helper()
	f := &dockerStopFixture{db: releaseTestDatabase(t)}
	f.mode.Store("")
	directory, manifest, options := smokeFixtureOptions(t)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("gateway_session")
		if err != nil || cookie.Value != "docker-binding-session-secret" {
			http.Error(w, "private session", 403)
			return
		}
		pid, _ := strconv.ParseInt(r.Header.Get(RuntimeBindingPIDHeader), 10, 64)
		class, _ := strconv.ParseInt(r.Header.Get(RuntimeBindingClassHeader), 10, 64)
		request := database.RuntimeBindingRequest{BackendPID: pid, ClassID: class}
		proof, err := database.ObserveRuntimeBinding(r.Context(), f.db, request)
		if err != nil {
			http.Error(w, "private proof", 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(RuntimeBindingStatus{SchemaVersion: 1, Mode: "normal", Probe: request, Proof: proof, Gateway: manifest.Artifacts[0].Report})
	}))
	t.Cleanup(gateway.Close)
	baseline, _ := dockerFixtureServer(t, directory, manifest, gateway.URL, "valid")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := f.mode.Load().(string)
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("Engine credentials leaked")
		}
		if r.Method == http.MethodPost {
			f.posts.Add(1)
			if r.URL.Path != dockerObservationAPI+"/containers/"+dockerFixtureContainerID+"/stop" || r.URL.RawQuery != "t=2" {
				t.Error("wrong stop target", r.URL)
			}
			file := f.intentFile.Load()
			if file == nil {
				t.Error("stop without journal owner")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			info, err := file.Stat()
			if err != nil || info.Size() <= 0 || info.Size() > maxHostJournalBytes {
				t.Error("stop without readable journal", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			// Windows byte-range ownership applies to this exact handle. A
			// second open would correctly fail while the owner keeps its lock.
			journal, err := io.ReadAll(io.NewSectionReader(file, 0, info.Size()))
			if err != nil || !strings.Contains(string(journal), `"effect":"stop_service"`) {
				t.Error("stop before durable intent", err)
			}
			if mode != "running" {
				f.stopped.Store(true)
			}
			if mode == "controller_lost_after_stop" {
				if _, err := f.db.Exec(`SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND mode='ExclusiveLock' AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`); err != nil {
					t.Error(err)
				}
			}
			if mode == "lost" || mode == "running" {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			t.Error("unexpected method", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "/json") {
			if mode == "missing" {
				http.NotFound(w, r)
				return
			}
			body := dockerFixtureContainer(t, manifest, gateway.URL)
			state := body["State"].(map[string]any)
			if f.stopped.Load() {
				state["Running"], state["Status"], state["Pid"], state["FinishedAt"] = false, "exited", 0, "2026-09-13T01:02:03Z"
				body["NetworkSettings"] = map[string]any{"Ports": map[string]any{}}
			}
			switch mode {
			case "restarted":
				state["StartedAt"] = "2026-09-13T00:00:00Z"
			case "replaced":
				body["Id"] = strings.Repeat("d", 64)
			case "config":
				body["Config"].(map[string]any)["Env"] = []string{"private changed config"}
			case "oom":
				state["OOMKilled"] = true
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host, copy.RequestURI = "http", "docker", ""
		response, err := baseline.client.Do(copy)
		if err != nil {
			http.Error(w, "fixture failure", 500)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(server.Close)
	f.open = func(string) (*dockerObserver, error) {
		if f.j != nil {
			f.intentFile.Store(f.j.file)
		}
		transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
		}}
		return &dockerObserver{transport: transport, client: &http.Client{Transport: transport, Timeout: 3 * time.Second}}, nil
	}
	ref := HostPackageReference{directory, options.Operation.CandidateManifestHash}
	var err error
	f.plan, err = observeDockerHostPlan(context.Background(), f.db, DockerHostPlanOptions{OperationID: hostTestSpec().ID, Previous: ref, Candidate: ref,
		Gateway: dockerFixtureOptions(gateway.URL, ref.ManifestSHA256)}, f.open)
	if err != nil {
		t.Fatal(err)
	}
	f.directory, f.j = hostTestJournal(t)
	if err := f.j.Begin(f.plan.Operation()); err != nil {
		t.Fatal(err)
	}
	if err := f.j.PrepareDockerPlan(f.plan); err != nil {
		t.Fatal(err)
	}
	if err := f.j.PrepareRuntimeRecovery(f.plan.RuntimeRecoveryBinding(0)); err != nil {
		t.Fatal(err)
	}
	if begin {
		f.controller, err = f.j.beginDockerGatewayFence(context.Background(), f.db, "docker-binding-session-secret", 0, f.open)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if f.controller != nil {
			f.controller.Close()
		}
		f.j.Close()
	})
	return f
}

func TestDockerGatewayFenceRecovery(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(strconv.FormatBool(applied), func(t *testing.T) {
			f := newDockerStopFixtureWithFence(t, false)
			if controller, err := f.j.beginDockerGatewayFence(context.Background(), f.db, "wrong-cookie", 0, f.open); err == nil {
				controller.Close()
				t.Fatal("fence accepted wrong ordinary session")
			}
			target := f.j.recoveryTarget(f.plan.RuntimeRecoveryBinding(0))
			if _, err := f.j.Intent(HostFence, target); err != nil {
				t.Fatal(err)
			}
			if applied {
				if err := f.j.WithRuntimeRecovery(f.plan.RuntimeRecoveryBinding(0), func(request database.RuntimeReleaseRequest) error {
					controller, err := database.BeginRuntimeReleaseForDatabase(context.Background(), f.db, request, f.plan.data.Observed.Database)
					if controller != nil {
						controller.Close()
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			f.j.Close()
			var err error
			f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
			if err != nil {
				t.Fatal(err)
			}
			f.controller, err = f.j.ResumeDockerGatewayFence(context.Background(), f.db, 0)
			if applied {
				if err != nil || f.j.state.Pending.Sequence != 0 {
					t.Fatal("active fence not recovered", err)
				}
			} else {
				if err == nil || f.j.state.Pending.Effect != HostFence {
					t.Fatal("never-applied fence resolved", err)
				}
			}
			state, err := database.ReadRuntimeFence(context.Background(), f.db)
			if err != nil || state.Active != applied {
				t.Fatal("recovery changed fence", err)
			}
			if f.posts.Load() != 0 {
				t.Fatal("fence recovery sent service request")
			}
		})
	}
}

func TestDockerGatewayStopAndLostReplyRecovery(t *testing.T) {
	for _, mode := range []string{"", "lost", "running", "controller_lost_after_stop"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixture(t)
			f.mode.Store(mode)
			observed, err := f.j.dockerStop(context.Background(), f.controller, 0, 2, false, f.open)
			if mode == "" {
				if err != nil || observed != HostApplied {
					t.Fatal(observed, err)
				}
			} else {
				if err == nil || observed != HostUnknown {
					t.Fatal("lost reply accepted", observed, err)
				}
				if _, err := f.j.dockerStop(context.Background(), f.controller, 0, 2, false, f.open); err == nil {
					t.Fatal("ambiguous stop replayed")
				}
				f.controller.Close()
				f.j.Close()
				f.j, err = OpenHostJournal(f.directory, hostTestInstallation)
				if err != nil {
					t.Fatal(err)
				}
				f.controller, err = f.j.ResumeDockerGatewayFence(context.Background(), f.db, 0)
				if err != nil {
					t.Fatal(err)
				}
				observed, err = f.j.dockerStop(context.Background(), f.controller, 0, 2, true, f.open)
				if mode == "running" {
					if err == nil || observed != HostUnknown {
						t.Fatal("running inferred not applied", observed, err)
					}
					f.stopped.Store(true)
					observed, err = f.j.dockerStop(context.Background(), f.controller, 0, 2, true, f.open)
				}
				if err != nil || observed != HostApplied {
					t.Fatal("stop not recovered", observed, err)
				}
			}
			if f.posts.Load() != 1 {
				t.Fatal("stop count", f.posts.Load())
			}
			if _, err := f.j.dockerStop(context.Background(), f.controller, 0, 2, false, f.open); err == nil {
				t.Fatal("committed stop repeated")
			}
			fence, err := database.ReadRuntimeFence(context.Background(), f.db)
			if err != nil || !fence.Active {
				t.Fatal("service stop removed DB fence", err)
			}
		})
	}
}

func TestDockerGatewayStopRefusals(t *testing.T) {
	for _, mode := range []string{"restarted", "replaced", "config", "missing", "oom", "controller", "database", "token", "plan", "epoch", "grace"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixture(t)
			f.mode.Store(mode)
			epoch, grace := int64(0), 2
			switch mode {
			case "controller":
				f.controller.Close()
			case "database":
				other := releaseTestDatabase(t)
				if err := f.j.WithRuntimeRecovery(f.plan.RuntimeRecoveryBinding(0), func(request database.RuntimeReleaseRequest) error {
					controller, err := database.BeginRuntimeRelease(context.Background(), other, request)
					if err == nil {
						f.controller.Close()
						f.controller = controller
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "token":
				if err := os.Remove(filepath.Join(f.directory, f.j.recoveryName(f.plan.RuntimeRecoveryBinding(0)))); err != nil {
					t.Fatal(err)
				}
			case "plan":
				if err := os.Remove(filepath.Join(f.directory, f.j.planName())); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				epoch++
			case "grace":
				grace = 121
			}
			if _, err := f.j.dockerStop(context.Background(), f.controller, epoch, grace, false, f.open); err == nil {
				t.Fatal("unsafe stop accepted")
			}
			if f.posts.Load() != 0 {
				t.Fatal("unsafe stop sent")
			}
		})
	}
}

func TestDockerGatewayStopReconcileChangedState(t *testing.T) {
	for _, mode := range []string{"restarted", "replaced", "config", "missing", "oom"} {
		t.Run(mode, func(t *testing.T) {
			f := newDockerStopFixture(t)
			f.mode.Store("lost")
			if result, err := f.j.dockerStop(context.Background(), f.controller, 0, 2, false, f.open); err == nil || result != HostUnknown {
				t.Fatal(result, err)
			}
			f.mode.Store(mode)
			if result, err := f.j.dockerStop(context.Background(), f.controller, 0, 2, true, f.open); err == nil || result != HostUnknown {
				t.Fatal("changed state accepted", result, err)
			}
			if _, err := f.j.dockerStop(context.Background(), f.controller, 0, 1, true, f.open); err == nil {
				t.Fatal("different target reconciled")
			}
			if f.posts.Load() != 1 {
				t.Fatal("reconcile sent stop")
			}
		})
	}
}
