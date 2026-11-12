package release

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

var ErrHostPlanUnavailable = errors.New("release Docker preparation plan is unavailable or changed")

const maxHostPlanBytes = 4 << 20
const dockerHostPlanScope = "docker_gateway_preparation_v1"

type HostPackageReference struct {
	Directory      string `json:"-"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type DockerHostPlanOptions struct {
	OperationID string
	Previous    HostPackageReference
	Candidate   HostPackageReference
	Gateway     DockerGatewayOptions
}

type hostPlanPackage struct {
	Directory      string `json:"directory"`
	DirectoryID    string `json:"directory_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type dockerHostPlanData struct {
	Version        int                   `json:"version"`
	Scope          string                `json:"scope"`
	OperationID    string                `json:"operation_id"`
	Previous       hostPlanPackage       `json:"previous"`
	Candidate      hostPlanPackage       `json:"candidate"`
	EngineEndpoint string                `json:"engine_endpoint"`
	GatewayPort    uint16                `json:"gateway_port"`
	Origin         string                `json:"origin"`
	Identity       DockerGatewayIdentity `json:"identity"`
	Observed       dockerPlanObservation `json:"observed"`
}

// DockerHostPlan is an immutable, private, observed preparation plan. Its scope
// selects ONLY Gateway. It is not an installation recipe: worker/native agents,
// coherent backup destinations, DB login resolution and activation are not yet
// represented. A future deployment driver must explicitly reject this scope.
// Docker configuration can include secrets; public formatting is always redacted.
type DockerHostPlan struct{ data dockerHostPlanData }

func (p DockerHostPlan) String() string {
	return "DockerHostPlan(private, deployment_authorized=false)"
}
func (p DockerHostPlan) GoString() string { return p.String() }
func (p DockerHostPlan) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Scope                string            `json:"scope"`
		Operation            HostOperationSpec `json:"operation"`
		DeploymentAuthorized bool              `json:"deployment_authorized"`
	}{dockerHostPlanScope, p.Operation(), false})
}

// Operation derives the digest from the entire canonical private plan; callers
// cannot substitute an opaque digest. A zero/unbuilt plan has no operation.
func (p DockerHostPlan) Operation() HostOperationSpec {
	if !validDockerHostPlan(p.data) {
		return HostOperationSpec{}
	}
	body, _ := json.Marshal(p.data)
	defer clear(body)
	return HostOperationSpec{ID: p.data.OperationID, PreviousManifestSHA256: p.data.Previous.ManifestSHA256,
		CandidateManifestSHA256: p.data.Candidate.ManifestSHA256, PlanSHA256: hostRecoveryDigest(body)}
}

func validDockerHostPlan(p dockerHostPlanData) bool {
	if p.Version != 1 || p.Scope != dockerHostPlanScope || !validHostID(p.OperationID) ||
		!validHostHash(p.Previous.ManifestSHA256) || !validHostHash(p.Candidate.ManifestSHA256) ||
		!validHostDirectory(p.Previous.Directory) || !validHostDirectory(p.Candidate.Directory) ||
		!validHostHash(p.Previous.DirectoryID) || !validHostHash(p.Candidate.DirectoryID) ||
		!validHostHash(p.Identity.ContainerID) || !validEngineID(p.Identity.EngineID) ||
		!validDigest(p.Identity.ImageID) || !validHostHash(p.Identity.ExecutableSHA256) ||
		p.Identity.InitPID <= 0 || p.Identity.GatewayPID <= 0 || p.Identity.ContainerStartedAt == "" ||
		p.GatewayPort == 0 || p.EngineEndpoint == "" || p.Observed.RestartCount < 0 ||
		p.Observed.Database.DatabaseOID <= 0 || p.Observed.Database.SystemIdentifier == "" ||
		p.Observed.Database.DatabaseName == "" || p.Observed.Database.PostmasterStartedAt == "" {
		return false
	}
	if _, err := validationOrigin(p.Origin); err != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(p.Observed.Configuration, &fields) != nil || len(fields) != 4 {
		return false
	}
	for _, name := range []string{"Config", "HostConfig", "Mounts", "NetworkSettings"} {
		if len(fields[name]) == 0 || bytes.Equal(fields[name], []byte("null")) {
			return false
		}
	}
	return true
}

func observeHostPackage(ref HostPackageReference) (hostPlanPackage, Manifest, error) {
	var empty hostPlanPackage
	if !validHostDirectory(ref.Directory) || !validHostHash(ref.ManifestSHA256) {
		return empty, Manifest{}, ErrHostPlanUnavailable
	}
	resolved, err := filepath.EvalSymlinks(ref.Directory)
	if err != nil || resolved != ref.Directory {
		return empty, Manifest{}, ErrHostPlanUnavailable
	}
	file, err := os.Open(ref.Directory)
	if err != nil {
		return empty, Manifest{}, ErrHostPlanUnavailable
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.IsDir() {
		return empty, Manifest{}, ErrHostPlanUnavailable
	}
	id, err := hostFileIdentity(file)
	if err != nil {
		return empty, Manifest{}, ErrHostPlanUnavailable
	}
	manifest, failure := verifiedBindingManifest(ref.Directory, ref.ManifestSHA256)
	if failure != "" {
		return empty, Manifest{}, ErrHostPlanUnavailable
	}
	if _, _, ok := dockerGatewayArtifact(manifest); !ok {
		return empty, Manifest{}, ErrHostPlanUnavailable
	}
	after, err := os.Stat(ref.Directory)
	resolved, resolveErr := filepath.EvalSymlinks(ref.Directory)
	if err != nil || resolveErr != nil || resolved != ref.Directory || !os.SameFile(before, after) {
		return empty, Manifest{}, ErrHostPlanUnavailable
	}
	return hostPlanPackage{Directory: ref.Directory, DirectoryID: id, ManifestSHA256: ref.ManifestSHA256}, manifest, nil
}

// ObserveDockerHostPlan verifies both complete packages and captures only a
// stable Docker/process/file/HTTP/database observation. No journal, DB fence or
// service is changed. PostgreSQL identity is read on the live challenge session;
// missing pg_control_system permission fails closed without granting privileges.
func ObserveDockerHostPlan(ctx context.Context, db *sql.DB, options DockerHostPlanOptions) (DockerHostPlan, error) {
	return observeDockerHostPlan(ctx, db, options, newDockerObserver)
}

func observeDockerHostPlan(ctx context.Context, db *sql.DB, options DockerHostPlanOptions, open func(string) (*dockerObserver, error)) (DockerHostPlan, error) {
	var empty DockerHostPlan
	if !validHostID(options.OperationID) || options.Gateway.ManifestHash != options.Previous.ManifestSHA256 {
		return empty, ErrHostPlanUnavailable
	}
	previous, _, err := observeHostPackage(options.Previous)
	if err != nil {
		return empty, err
	}
	candidate, _, err := observeHostPackage(options.Candidate)
	if err != nil {
		return empty, err
	}
	var captured dockerPlanObservation
	report, err := observeDockerGatewayPlan(ctx, db, options.Previous.Directory, options.Gateway, open, &captured)
	if err != nil || !report.Success || report.Identity == nil {
		return empty, ErrHostPlanUnavailable
	}
	// The package identities enclose the complete live observation as well.
	previousAfter, _, err := observeHostPackage(options.Previous)
	if err != nil || previousAfter != previous {
		return empty, ErrHostPlanUnavailable
	}
	candidateAfter, _, err := observeHostPackage(options.Candidate)
	if err != nil || candidateAfter != candidate {
		return empty, ErrHostPlanUnavailable
	}
	origin, err := validationOrigin(options.Gateway.Origin)
	if err != nil {
		return empty, ErrHostPlanUnavailable
	}
	plan := DockerHostPlan{data: dockerHostPlanData{Version: 1, Scope: dockerHostPlanScope, OperationID: options.OperationID,
		Previous: previous, Candidate: candidate, EngineEndpoint: options.Gateway.EngineEndpoint, GatewayPort: options.Gateway.GatewayPort,
		Origin: origin.String(), Identity: *report.Identity, Observed: captured}}
	if !validDockerHostPlan(plan.data) {
		return empty, ErrHostPlanUnavailable
	}
	return plan, nil
}

// Revalidate requires a fresh caller-supplied DB handle and ordinary admin
// session. It resolves no aliases or credentials and never revises the saved
// plan. Even a legitimate restart/configuration change requires a new plan.
func (p DockerHostPlan) Revalidate(ctx context.Context, db *sql.DB, sessionToken string) error {
	return p.revalidate(ctx, db, sessionToken, newDockerObserver)
}

func (p DockerHostPlan) revalidate(ctx context.Context, db *sql.DB, sessionToken string, open func(string) (*dockerObserver, error)) error {
	if !validDockerHostPlan(p.data) {
		return ErrHostPlanUnavailable
	}
	d := p.data
	fresh, err := observeDockerHostPlan(ctx, db, DockerHostPlanOptions{OperationID: d.OperationID,
		Previous:  HostPackageReference{d.Previous.Directory, d.Previous.ManifestSHA256},
		Candidate: HostPackageReference{d.Candidate.Directory, d.Candidate.ManifestSHA256},
		Gateway: DockerGatewayOptions{EngineEndpoint: d.EngineEndpoint, EngineID: d.Identity.EngineID, ContainerID: d.Identity.ContainerID, GatewayPort: d.GatewayPort,
			RuntimeBindingOptions: RuntimeBindingOptions{Origin: d.Origin, ManifestHash: d.Previous.ManifestSHA256, SessionToken: sessionToken}}}, open)
	if err != nil || fresh.Operation() != p.Operation() {
		return ErrHostPlanUnavailable
	}
	return nil
}
