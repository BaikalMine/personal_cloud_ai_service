package release

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
)

type DockerGatewayOptions struct {
	EngineEndpoint string `json:"-"`
	EngineID       string
	ContainerID    string
	GatewayPort    uint16
	RuntimeBindingOptions
}

type DockerGatewayReport struct {
	SchemaVersion        int                    `json:"schema_version"`
	Scope                string                 `json:"scope"`
	Success              bool                   `json:"success"`
	DeploymentAuthorized bool                   `json:"deployment_authorized"`
	ManifestHash         string                 `json:"manifest_hash,omitempty"`
	Identity             *DockerGatewayIdentity `json:"identity,omitempty"`
	Gateway              *buildinfo.Report      `json:"gateway,omitempty"`
	StartedAt            time.Time              `json:"started_at"`
	FinishedAt           time.Time              `json:"finished_at"`
	FailureCode          string                 `json:"failure_code,omitempty"`
}

type DockerGatewayIdentity struct {
	EngineID           string `json:"engine_id"`
	ContainerID        string `json:"container_id"`
	ImageID            string `json:"image_id"`
	ContainerStartedAt string `json:"container_started_at"`
	InitPID            int64  `json:"init_pid"`
	GatewayPID         int64  `json:"gateway_pid"`
	ExecutableSHA256   string `json:"executable_sha256"`
}

// ObserveDockerGateway binds a verified package, an explicitly selected local
// Engine/container, the running Gateway process, its published port and its DB.
// It performs only GET requests and temporary DB challenge locks. The result is
// an observation, not a lock, persistent operation handle or deployment permit.
func ObserveDockerGateway(parent context.Context, db *sql.DB, directory string, options DockerGatewayOptions) (DockerGatewayReport, error) {
	return observeDockerGateway(parent, db, directory, options, newDockerObserver)
}

func observeDockerGateway(parent context.Context, db *sql.DB, directory string, options DockerGatewayOptions, open func(string) (*dockerObserver, error)) (report DockerGatewayReport, err error) {
	return observeDockerGatewayPlan(parent, db, directory, options, open, nil)
}

type dockerPlanObservation struct {
	Configuration json.RawMessage                  `json:"configuration"`
	RestartCount  int64                            `json:"restart_count"`
	Database      database.RuntimeDatabaseIdentity `json:"database"`
}

func observeDockerGatewayPlan(parent context.Context, db *sql.DB, directory string, options DockerGatewayOptions, open func(string) (*dockerObserver, error), captured *dockerPlanObservation) (report DockerGatewayReport, err error) {
	report = DockerGatewayReport{SchemaVersion: 1, Scope: "docker_gateway_binding", StartedAt: time.Now().UTC()}
	defer func() {
		report.FinishedAt = time.Now().UTC()
		if report.FailureCode != "" {
			err = errors.New("docker gateway binding failed: " + report.FailureCode)
		}
	}()
	base, cookie, valid := runtimeBindingCredentials(db, options.RuntimeBindingOptions)
	if !valid || !hashPattern.MatchString(options.ContainerID) || !validEngineID(options.EngineID) || options.GatewayPort == 0 {
		report.FailureCode = "configuration"
		return
	}
	manifest, failure := verifiedBindingManifest(directory, options.ManifestHash)
	if failure != "" {
		report.FailureCode = failure
		return
	}
	artifact, imageID, ok := dockerGatewayArtifact(manifest)
	if !ok {
		report.FailureCode = "gateway_artifact"
		return
	}
	report.ManifestHash = options.ManifestHash
	d, openErr := open(options.EngineEndpoint)
	if openErr != nil {
		report.FailureCode = "engine_endpoint"
		return
	}
	defer d.close()
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	if d.checkEngine(ctx, options.EngineID) != nil {
		report.FailureCode = "engine_identity"
		return
	}
	before, inspectErr := d.inspectGateway(ctx, options, base, imageID)
	if inspectErr != nil {
		report.FailureCode = "container_identity"
		return
	}
	pid, processErr := d.gatewayProcess(ctx, before)
	if processErr != nil {
		report.FailureCode = "gateway_process"
		return
	}
	if d.checkGatewayExecutable(ctx, options.ContainerID, artifact.File) != nil {
		report.FailureCode = "gateway_executable"
		return
	}
	var dbIdentity database.RuntimeDatabaseIdentity
	var captureIdentity *database.RuntimeDatabaseIdentity
	if captured != nil {
		captureIdentity = &dbIdentity
	}
	gateway, failure := probeGatewayDatabaseIdentity(ctx, db, manifest, base, cookie, captureIdentity)
	if failure != "" {
		report.FailureCode = failure
		return
	}
	// Read the process list before the final inspect so the container's lifetime
	// observations enclose both process observations and the database proof.
	afterPID, processErr := d.gatewayProcess(ctx, before)
	after, inspectErr := d.inspectGateway(ctx, options, base, imageID)
	if processErr != nil || inspectErr != nil || pid != afterPID || !sameDockerGateway(before, after) {
		report.FailureCode = "container_changed"
		return
	}
	if d.checkEngine(ctx, options.EngineID) != nil {
		report.FailureCode = "engine_changed"
		return
	}
	report.Identity = &DockerGatewayIdentity{EngineID: options.EngineID, ContainerID: before.ID, ImageID: before.Image,
		ContainerStartedAt: before.State.StartedAt, InitPID: before.State.PID, GatewayPID: pid, ExecutableSHA256: artifact.File.SHA256}
	report.Gateway = gateway
	report.Success = true
	if captured != nil {
		*captured = dockerPlanObservation{Configuration: bytes.Clone(before.configurationJSON), RestartCount: before.RestartCount, Database: dbIdentity}
	}
	return
}

func validEngineID(id string) bool {
	if len(id) < 16 || len(id) > 128 {
		return false
	}
	for _, char := range id {
		if char != '-' && char != ':' && (char < '0' || char > '9') && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') {
			return false
		}
	}
	return true
}

func dockerGatewayArtifact(manifest Manifest) (Artifact, string, bool) {
	var selected Artifact
	var imageID string
	for _, artifact := range manifest.Artifacts {
		if artifact.Report.Component == buildinfo.Gateway && artifact.Report.Build.Platform == "linux/amd64" {
			selected = artifact
		}
	}
	for _, image := range manifest.Images {
		if image.Component == buildinfo.Gateway {
			imageID = image.ID
		}
	}
	return selected, imageID, selected.File.Bytes > 0 && selected.File.Bytes <= 512<<20 && validDigest(imageID) && validationBuildMatches(selected.Report, manifest)
}

func (d *dockerObserver) checkEngine(ctx context.Context, expected string) error {
	var info struct{ ID, OSType, Architecture string }
	if d.readJSON(ctx, "/info", nil, &info) != nil || info.ID != expected || info.OSType != "linux" || (info.Architecture != "x86_64" && info.Architecture != "amd64") {
		return errDockerObservation
	}
	return nil
}

type dockerPortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string
}

type dockerContainerObservation struct {
	ID           string `json:"Id"`
	Image, Path  string
	Args         []string
	RestartCount int64
	State        struct {
		Status                                       string
		Running, Paused, Restarting, Dead, OOMKilled bool
		PID                                          int64 `json:"Pid"`
		StartedAt                                    string
		FinishedAt, Error                            string
		ExitCode                                     int
	}
	Config struct {
		Entrypoint, Cmd []string
		WorkingDir      string
	}
	HostConfig struct {
		ReadonlyRootfs, Privileged bool
		Init                       *bool
		PidMode, NetworkMode       string
		Tmpfs                      map[string]string
	}
	Mounts          []struct{ Type, Destination string }
	NetworkSettings struct {
		Ports map[string][]dockerPortBinding
	}
	configuration     [32]byte
	configurationJSON json.RawMessage
}

func (d *dockerObserver) inspectGateway(ctx context.Context, options DockerGatewayOptions, base *url.URL, imageID string) (dockerContainerObservation, error) {
	observed, readErr := d.readContainer(ctx, options.ContainerID)
	if readErr != nil {
		return observed, errDockerObservation
	}
	s := observed.State
	started, err := time.Parse(time.RFC3339Nano, s.StartedAt)
	if observed.ID != options.ContainerID || observed.Image != imageID || observed.Path != "/app/gateway" || len(observed.Args) != 0 || observed.RestartCount < 0 ||
		!s.Running || s.Status != "running" || s.Paused || s.Restarting || s.Dead || s.OOMKilled || s.PID <= 0 || err != nil || started.IsZero() ||
		!slices.Equal(observed.Config.Entrypoint, []string{"/app/gateway"}) || len(observed.Config.Cmd) != 0 || observed.Config.WorkingDir != "/app" ||
		!observed.HostConfig.ReadonlyRootfs || observed.HostConfig.Privileged || observed.HostConfig.PidMode != "" ||
		observed.HostConfig.NetworkMode == "host" || observed.HostConfig.NetworkMode == "none" || strings.HasPrefix(observed.HostConfig.NetworkMode, "container:") {
		return observed, errDockerObservation
	}
	for _, mount := range observed.Mounts {
		if (mount.Type != "bind" && mount.Type != "volume" && mount.Type != "tmpfs") || !safeGatewayMount(mount.Destination) {
			return observed, errDockerObservation
		}
	}
	for destination := range observed.HostConfig.Tmpfs {
		if !safeGatewayMount(destination) {
			return observed, errDockerObservation
		}
	}
	portKey := strconv.Itoa(int(options.GatewayPort)) + "/tcp"
	matched := false
	for _, binding := range observed.NetworkSettings.Ports[portKey] {
		if dockerPublishedOrigin(binding, base) {
			matched = true
		}
	}
	if !matched {
		return observed, errDockerObservation
	}
	return observed, nil
}

func (d *dockerObserver) readContainer(ctx context.Context, id string) (dockerContainerObservation, error) {
	var raw json.RawMessage
	var observed dockerContainerObservation
	if !validHostHash(id) || d.readJSON(ctx, "/containers/"+id+"/json", nil, &raw) != nil || json.Unmarshal(raw, &observed) != nil {
		return observed, errDockerObservation
	}
	// Keep complete configuration/mount/network fingerprints private. Docker's
	// configuration includes credentials, so neither it nor raw diagnostics are
	// copied to the public observation report.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return observed, errDockerObservation
	}
	selected := make(map[string]any)
	for _, key := range []string{"Config", "HostConfig", "Mounts", "NetworkSettings"} {
		decoder := json.NewDecoder(bytes.NewReader(fields[key]))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) != nil {
			return observed, errDockerObservation
		}
		selected[key] = value
	}
	canonical, err := json.Marshal(selected)
	if err != nil {
		return observed, errDockerObservation
	}
	observed.configuration = sha256.Sum256(canonical)
	observed.configurationJSON = canonical
	return observed, nil
}

func safeGatewayMount(destination string) bool {
	if !strings.HasPrefix(destination, "/") || path.Clean(destination) != destination || strings.ContainsAny(destination, "\\\x00") {
		return false
	}
	return destination != "/" && destination != "/app" && !strings.HasPrefix(destination, "/app/")
}

func dockerPublishedOrigin(binding dockerPortBinding, base *url.URL) bool {
	port, err := strconv.Atoi(binding.HostPort)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != base.Port() {
		return false
	}
	host, err := netip.ParseAddr(binding.HostIP)
	requested, requestedErr := netip.ParseAddr(base.Hostname())
	if err != nil || requestedErr != nil || host.Zone() != "" || host.Is4() != requested.Is4() {
		return false
	}
	return host == requested || host.IsUnspecified()
}

func sameDockerGateway(a, b dockerContainerObservation) bool {
	return a.ID == b.ID && a.Image == b.Image && a.State.PID == b.State.PID && a.State.StartedAt == b.State.StartedAt && a.RestartCount == b.RestartCount && a.configuration == b.configuration
}

func (d *dockerObserver) gatewayProcess(ctx context.Context, observed dockerContainerObservation) (int64, error) {
	var top struct {
		Titles    []string
		Processes [][]string
	}
	if d.readJSON(ctx, "/containers/"+observed.ID+"/top", url.Values{"ps_args": {"-eo pid,ppid,args"}}, &top) != nil || len(top.Titles) != 3 ||
		top.Titles[0] != "PID" || top.Titles[1] != "PPID" || (top.Titles[2] != "COMMAND" && top.Titles[2] != "CMD") || len(top.Processes) == 0 || len(top.Processes) > 8192 {
		return 0, errDockerObservation
	}
	var pid, parent int64
	rootCommand := ""
	seen := make(map[int64]bool)
	for _, process := range top.Processes {
		if len(process) != 3 {
			return 0, errDockerObservation
		}
		id, idErr := strconv.ParseInt(strings.TrimSpace(process[0]), 10, 64)
		ppid, parentErr := strconv.ParseInt(strings.TrimSpace(process[1]), 10, 64)
		if idErr != nil || parentErr != nil || id <= 0 || ppid < 0 || seen[id] {
			return 0, errDockerObservation
		}
		seen[id] = true
		if id == observed.State.PID {
			rootCommand = process[2]
		}
		if process[2] == "/app/gateway" {
			if pid != 0 {
				return 0, errDockerObservation
			}
			pid, parent = id, ppid
		}
	}
	if pid == 0 {
		return 0, errDockerObservation
	}
	if pid == observed.State.PID && (observed.HostConfig.Init == nil || !*observed.HostConfig.Init) {
		return pid, nil
	}
	if parent == observed.State.PID && rootCommand == "/sbin/docker-init -- /app/gateway" && (observed.HostConfig.Init == nil || *observed.HostConfig.Init) {
		return pid, nil
	}
	return 0, errDockerObservation
}

func (d *dockerObserver) checkGatewayExecutable(ctx context.Context, containerID string, expected File) error {
	response, err := d.get(ctx, "/containers/"+containerID+"/archive", url.Values{"path": {"/app/gateway"}})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if !validationContentType(response, "application/x-tar") || response.ContentLength > expected.Bytes+(1<<20) {
		return errDockerObservation
	}
	encoded := response.Header.Values("X-Docker-Container-Path-Stat")
	if len(encoded) != 1 || len(encoded[0]) > 8192 {
		return errDockerObservation
	}
	metadata, err := base64.StdEncoding.DecodeString(encoded[0])
	var stat struct {
		Name, LinkTarget string
		Size             int64
	}
	if err != nil || json.Unmarshal(metadata, &stat) != nil || stat.Name != "gateway" || stat.Size != expected.Bytes || stat.LinkTarget != "" {
		return errDockerObservation
	}
	reader := tar.NewReader(io.LimitReader(response.Body, expected.Bytes+(1<<20)+1))
	header, err := reader.Next()
	if err != nil || header.Typeflag != tar.TypeReg || header.Name != "gateway" || header.Linkname != "" || header.Size != expected.Bytes {
		return errDockerObservation
	}
	hash := sha256.New()
	written, err := io.Copy(hash, reader)
	if err != nil || written != expected.Bytes || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return errDockerObservation
	}
	if _, err = reader.Next(); err != io.EOF {
		return errDockerObservation
	}
	return nil
}
