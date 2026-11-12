package release

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
)

const dockerFixtureEngineID = "fixture-engine-000000000001"

var dockerFixtureContainerID = strings.Repeat("c", 64)

func dockerFixtureOptions(origin, hash string) DockerGatewayOptions {
	return DockerGatewayOptions{EngineEndpoint: "fixture-only", EngineID: dockerFixtureEngineID, ContainerID: dockerFixtureContainerID, GatewayPort: 8090,
		RuntimeBindingOptions: RuntimeBindingOptions{Origin: origin, ManifestHash: hash, SessionToken: "docker-binding-session-secret"}}
}

func dockerFixtureContainer(t *testing.T, manifest Manifest, origin string) map[string]any {
	t.Helper()
	base, err := validationOrigin(origin)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"Id": dockerFixtureContainerID, "Image": manifest.Images[0].ID, "Path": "/app/gateway", "Args": []string{}, "RestartCount": 0,
		"State":           map[string]any{"Status": "running", "Running": true, "Pid": 1234, "StartedAt": "2026-09-12T01:02:03.123456789Z"},
		"Config":          map[string]any{"Entrypoint": []string{"/app/gateway"}, "Cmd": []string{}, "WorkingDir": "/app", "Env": []string{"DATABASE_URL=must-not-appear", "SESSION_SECRET=must-not-appear"}},
		"HostConfig":      map[string]any{"ReadonlyRootfs": true, "Init": false, "NetworkMode": "fixture-net", "Tmpfs": map[string]string{"/tmp": "rw,noexec"}},
		"Mounts":          []map[string]any{{"Type": "volume", "Source": "/private-host-source", "Destination": "/var/lib/gateway-spool", "RW": true}},
		"NetworkSettings": map[string]any{"Ports": map[string]any{"8090/tcp": []dockerPortBinding{{HostIP: "127.0.0.1", HostPort: base.Port()}}}},
	}
}

func dockerFixtureServer(t *testing.T, directory string, manifest Manifest, origin, mode string) (*dockerObserver, func(string) (*dockerObserver, error)) {
	t.Helper()
	binary, err := os.ReadFile(filepath.Join(directory, manifest.Artifacts[0].File.Name))
	if err != nil {
		t.Fatal(err)
	}
	var inspections, tops, infos atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("Engine observation sent credentials or a mutating request")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case dockerObservationAPI + "/info":
			id := dockerFixtureEngineID
			call := infos.Add(1)
			if mode == "engine" || mode == "engine_changed" && call > 1 {
				id = "different-engine-000000000001"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ID": id, "OSType": "linux", "Architecture": "x86_64", "FutureField": true})
		case dockerObservationAPI + "/containers/" + dockerFixtureContainerID + "/json":
			call := inspections.Add(1)
			body := dockerFixtureContainer(t, manifest, origin)
			state := body["State"].(map[string]any)
			config := body["Config"].(map[string]any)
			host := body["HostConfig"].(map[string]any)
			switch mode {
			case "image":
				body["Image"] = "sha256:" + strings.Repeat("e", 64)
			case "container":
				body["Id"] = strings.Repeat("b", 64)
			case "path":
				body["Path"] = "/bin/sh"
			case "args":
				body["Args"] = []string{"--version-json"}
			case "entrypoint":
				config["Entrypoint"] = []string{"/bin/sh", "/app/gateway"}
			case "cmd":
				config["Cmd"] = []string{"anything"}
			case "working_directory":
				config["WorkingDir"] = "/tmp"
			case "writable":
				host["ReadonlyRootfs"] = false
			case "privileged":
				host["Privileged"] = true
			case "pid_namespace":
				host["PidMode"] = "host"
			case "host_network":
				host["NetworkMode"] = "host"
			case "shared_network":
				host["NetworkMode"] = "container:other"
			case "mount":
				body["Mounts"] = []map[string]any{{"Type": "bind", "Destination": "/app/gateway"}}
			case "parent_mount":
				body["Mounts"] = []map[string]any{{"Type": "volume", "Destination": "/app"}}
			case "tmpfs":
				host["Tmpfs"] = map[string]string{"/app": "rw"}
			case "port":
				body["NetworkSettings"] = map[string]any{"Ports": map[string]any{}}
			case "paused":
				state["Paused"] = true
			case "stopped":
				state["Running"] = false
			case "restarting":
				state["Restarting"] = true
			case "dead":
				state["Dead"] = true
			case "oom":
				state["OOMKilled"] = true
			case "timestamp":
				state["StartedAt"] = "0001-01-01T00:00:00Z"
			case "pid":
				state["Pid"] = 0
			case "init":
				host["Init"] = true
			case "config_changed":
				if call > 1 {
					config["Env"] = []string{"DATABASE_URL=changed-secret"}
				}
			case "mount_changed":
				if call > 1 {
					body["Mounts"] = []map[string]any{{"Type": "volume", "Destination": "/var/lib/gateway-spool", "Source": "/different-private-source"}}
				}
			case "restart_changed":
				if call > 1 {
					body["RestartCount"] = 1
				}
			case "started_changed":
				if call > 1 {
					state["StartedAt"] = "2026-09-12T01:02:04Z"
				}
			}
			_ = json.NewEncoder(w).Encode(body)
		case dockerObservationAPI + "/containers/" + dockerFixtureContainerID + "/top":
			if r.URL.Query().Get("ps_args") != "-eo pid,ppid,args" {
				t.Error("unexpected process query")
			}
			call := tops.Add(1)
			titles := []string{"PID", "PPID", "COMMAND"}
			processes := [][]string{{"1234", "1000", "/app/gateway"}}
			switch mode {
			case "init":
				processes = [][]string{{"1234", "1000", "/sbin/docker-init -- /app/gateway"}, {"1235", "1234", "/app/gateway"}}
			case "missing_process":
				processes = [][]string{{"1234", "1000", "/bin/sleep"}}
			case "duplicate_process":
				processes = append(processes, []string{"1235", "1234", "/app/gateway"})
			case "wrong_parent":
				processes = [][]string{{"1234", "1000", "/bin/sh"}, {"1235", "1234", "/app/gateway"}}
			case "malformed_process":
				processes[0][0] = "bad"
			case "titles":
				titles[2] = "COMM"
			case "process_changed":
				if call > 1 {
					processes[0][0] = "1236"
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Titles": titles, "Processes": processes})
		case dockerObservationAPI + "/containers/" + dockerFixtureContainerID + "/archive":
			if r.URL.Query().Get("path") != "/app/gateway" {
				t.Error("unexpected file request")
			}
			stat := map[string]any{"name": "gateway", "size": len(binary), "linkTarget": ""}
			if mode == "stat_link" {
				stat["linkTarget"] = "/tmp/gateway"
			}
			if mode == "stat_size" {
				stat["size"] = len(binary) + 1
			}
			data, _ := json.Marshal(stat)
			if mode != "stat_missing" {
				w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(data))
			}
			w.Header().Set("Content-Type", "application/x-tar")
			if mode == "archive_type" {
				w.Header().Set("Content-Type", "application/json")
			}
			writer := tar.NewWriter(w)
			header := &tar.Header{Name: "gateway", Typeflag: tar.TypeReg, Size: int64(len(binary)), Mode: 0555}
			if mode == "symlink" {
				header.Typeflag, header.Linkname, header.Size = tar.TypeSymlink, "/tmp/gateway", 0
			}
			_ = writer.WriteHeader(header)
			if mode != "symlink" {
				data := bytes.Clone(binary)
				if mode == "executable" {
					data[0] ^= 1
				}
				_, _ = writer.Write(data)
			}
			if mode == "extra_file" {
				_ = writer.WriteHeader(&tar.Header{Name: "extra", Typeflag: tar.TypeReg})
			}
			_ = writer.Close()
		default:
			http.Error(w, "private Engine failure", 404)
		}
	}))
	t.Cleanup(server.Close)
	d := &dockerObserver{transport: &http.Transport{Proxy: nil}}
	d.transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	d.client = &http.Client{Transport: d.transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(d.close)
	return d, func(string) (*dockerObserver, error) { return d, nil }
}

func TestDockerGatewayEngineRefusals(t *testing.T) {
	for _, mode := range []string{"image", "container", "path", "args", "entrypoint", "cmd", "working_directory", "writable", "privileged", "pid_namespace", "host_network", "shared_network", "mount", "parent_mount", "tmpfs", "port", "paused", "stopped", "restarting", "dead", "oom", "timestamp", "pid"} {
		t.Run(mode, func(t *testing.T) {
			directory, manifest := fixtureManifest(t)
			d, _ := dockerFixtureServer(t, directory, manifest, "http://127.0.0.1:18120", mode)
			base, _ := validationOrigin("http://127.0.0.1:18120")
			if _, err := d.inspectGateway(context.Background(), dockerFixtureOptions(base.String(), ""), base, manifest.Images[0].ID); err == nil {
				t.Fatal("accepted changed runtime configuration")
			}
		})
	}
	for _, mode := range []string{"missing_process", "duplicate_process", "wrong_parent", "malformed_process", "titles"} {
		t.Run(mode, func(t *testing.T) {
			directory, manifest := fixtureManifest(t)
			d, _ := dockerFixtureServer(t, directory, manifest, "http://127.0.0.1:18120", mode)
			base, _ := validationOrigin("http://127.0.0.1:18120")
			observed, err := d.inspectGateway(context.Background(), dockerFixtureOptions(base.String(), ""), base, manifest.Images[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.gatewayProcess(context.Background(), observed); err == nil {
				t.Fatal("accepted wrong process")
			}
		})
	}
	for _, mode := range []string{"executable", "symlink", "extra_file", "stat_link", "stat_size", "stat_missing", "archive_type"} {
		t.Run(mode, func(t *testing.T) {
			directory, manifest := fixtureManifest(t)
			d, _ := dockerFixtureServer(t, directory, manifest, "http://127.0.0.1:18120", mode)
			if err := d.checkGatewayExecutable(context.Background(), dockerFixtureContainerID, manifest.Artifacts[0].File); err == nil {
				t.Fatal("accepted unbound executable")
			}
		})
	}
}

func TestDockerGatewayCombinedDatabaseObservation(t *testing.T) {
	db := releaseTestDatabase(t)
	other := releaseTestDatabase(t)
	for _, mode := range []string{"valid", "init", "engine", "image", "port", "executable", "engine_changed", "config_changed", "mount_changed", "restart_changed", "started_changed", "process_changed", "wrong_database", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			directory, manifest, fixture := smokeFixtureOptions(t)
			var gatewayCalls atomic.Int64
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gatewayCalls.Add(1)
				cookie, err := r.Cookie("gateway_session")
				if err != nil || cookie.Value != "docker-binding-session-secret" {
					t.Error("missing normal admin authentication")
				}
				if mode == "forbidden" {
					http.Error(w, "private Gateway failure", 403)
					return
				}
				pid, _ := strconv.ParseInt(r.Header.Get(RuntimeBindingPIDHeader), 10, 64)
				class, _ := strconv.ParseInt(r.Header.Get(RuntimeBindingClassHeader), 10, 64)
				request := database.RuntimeBindingRequest{BackendPID: pid, ClassID: class}
				observer := db
				if mode == "wrong_database" {
					observer = other
				}
				proof, err := database.ObserveRuntimeBinding(r.Context(), observer, request)
				if err != nil {
					http.Error(w, "private database failure", 409)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				_ = json.NewEncoder(w).Encode(RuntimeBindingStatus{SchemaVersion: 1, Mode: "normal", Probe: request, Proof: proof, Gateway: manifest.Artifacts[0].Report})
			}))
			defer gateway.Close()
			_, open := dockerFixtureServer(t, directory, manifest, gateway.URL, mode)
			report, err := observeDockerGateway(context.Background(), db, directory, dockerFixtureOptions(gateway.URL, fixture.Operation.CandidateManifestHash), open)
			if mode == "valid" || mode == "init" {
				if err != nil || !report.Success || report.Identity == nil || report.Gateway == nil || report.Identity.ExecutableSHA256 != manifest.Artifacts[0].File.SHA256 {
					t.Fatalf("binding: %+v %v", report, err)
				}
			} else if err == nil || report.Success || report.FailureCode == "" || report.Identity != nil || report.Gateway != nil {
				t.Fatalf("accepted %s: %+v %v", mode, report, err)
			}
			if slices.Contains([]string{"engine", "image", "port", "executable"}, mode) && gatewayCalls.Load() != 0 {
				t.Fatal("credentials sent before host identity was established")
			}
			if report.Scope != "docker_gateway_binding" || report.DeploymentAuthorized || report.FinishedAt.Before(report.StartedAt) {
				t.Fatal("incorrect observation scope")
			}
			body, _ := json.Marshal(report)
			for _, secret := range []string{"docker-binding-session-secret", "must-not-appear", "private-host-source", "private Engine failure", "private Gateway failure", "private database failure", gateway.URL, "configuration"} {
				if strings.Contains(string(body), secret) || err != nil && strings.Contains(err.Error(), secret) {
					t.Fatal("private diagnostics or configuration escaped")
				}
			}
		})
	}
}

func TestDockerGatewayMountAndPortBoundary(t *testing.T) {
	for _, destination := range []string{"/", "/app", "/app/gateway", "/app/anything", "/app/../tmp", "relative", "/tmp\\app", "/tmp\x00"} {
		if safeGatewayMount(destination) {
			t.Fatal("accepted", destination)
		}
	}
	for _, destination := range []string{"/tmp", "/var/lib/gateway-spool", "/application"} {
		if !safeGatewayMount(destination) {
			t.Fatal("rejected", destination)
		}
	}
	base, _ := url.Parse("http://127.0.0.1:18090")
	for _, host := range []string{"127.0.0.1", "0.0.0.0"} {
		if !dockerPublishedOrigin(dockerPortBinding{HostIP: host, HostPort: "18090"}, base) {
			t.Fatal("rejected published port")
		}
	}
	for _, binding := range []dockerPortBinding{{"127.0.0.2", "18090"}, {"::", "18090"}, {"", "18090"}, {"127.0.0.1", "8090"}, {"127.0.0.1", "bad"}} {
		if dockerPublishedOrigin(binding, base) {
			t.Fatal("accepted wrong published port")
		}
	}
}

func TestDockerGatewayLocalTransportBoundary(t *testing.T) {
	for _, endpoint := range []string{"", "http://127.0.0.1:2375", "tcp://127.0.0.1:2375", "ssh://host", "npipe:////remote/pipe/docker_engine", "unix://remote/docker.sock"} {
		if _, err := newDockerObserver(endpoint); err == nil {
			t.Fatal("accepted non-local endpoint")
		}
	}
	valid := "unix:///var/run/docker.sock"
	if runtime.GOOS == "windows" {
		valid = "npipe:////./pipe/dockerDesktopLinuxEngine"
	}
	d, err := newDockerObserver(valid)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.transport.DialContext(ctx, "tcp", "attacker:80"); err == nil {
		t.Fatal("transport dialed an arbitrary target")
	}
}

func TestDockerGatewayPackageBeforeEngine(t *testing.T) {
	for _, mode := range []string{"container", "engine", "port", "session", "origin", "missing_manifest", "manifest_hash", "package_file"} {
		t.Run(mode, func(t *testing.T) {
			directory, manifest, fixture := smokeFixtureOptions(t)
			db, err := sql.Open("postgres", "unused")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			options := dockerFixtureOptions("http://127.0.0.1:18090", fixture.Operation.CandidateManifestHash)
			switch mode {
			case "container":
				options.ContainerID = "mutable-container-name"
			case "engine":
				options.EngineID = ""
			case "port":
				options.GatewayPort = 0
			case "session":
				options.SessionToken = ""
			case "origin":
				options.Origin = "http://192.0.2.1:18090"
			case "missing_manifest":
				if err := os.Remove(filepath.Join(directory, "manifest.json")); err != nil {
					t.Fatal(err)
				}
			case "manifest_hash":
				options.ManifestHash = strings.Repeat("0", 64)
			case "package_file":
				if err := os.WriteFile(filepath.Join(directory, manifest.Artifacts[0].File.Name), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			opened := false
			report, err := observeDockerGateway(context.Background(), db, directory, options, func(string) (*dockerObserver, error) { opened = true; return nil, errDockerObservation })
			if err == nil || report.Success || report.DeploymentAuthorized || report.ManifestHash != "" || opened {
				t.Fatalf("opened Engine before package/config verification: %+v %v", report, err)
			}
		})
	}
}
