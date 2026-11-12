package release

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
)

const RuntimeBindingPIDHeader = "X-Gateway-Release-Binding-Pid"
const RuntimeBindingClassHeader = "X-Gateway-Release-Binding-Class"

type RuntimeBindingStatus struct {
	SchemaVersion int                            `json:"schema_version"`
	Mode          string                         `json:"mode"`
	Probe         database.RuntimeBindingRequest `json:"probe"`
	Proof         string                         `json:"proof"`
	Gateway       buildinfo.Report               `json:"gateway"`
}

type RuntimeBindingOptions struct {
	Origin       string
	ManifestHash string
	SessionToken string `json:"-"`
}

type RuntimeBindingReport struct {
	SchemaVersion        int               `json:"schema_version"`
	Scope                string            `json:"scope"`
	Success              bool              `json:"success"`
	DeploymentAuthorized bool              `json:"deployment_authorized"`
	ManifestHash         string            `json:"manifest_hash,omitempty"`
	Gateway              *buildinfo.Report `json:"gateway,omitempty"`
	StartedAt            time.Time         `json:"started_at"`
	FinishedAt           time.Time         `json:"finished_at"`
	FailureCode          string            `json:"failure_code,omitempty"`
}

// ObserveGatewayDatabase checks that a normally authenticated Gateway can read
// a fresh probe in the supplied database and reports the expected package build.
// The caller must obtain its admin session from an independently trusted target.
// This does not attest executable bytes, stop writers or authorize installation.
func ObserveGatewayDatabase(parent context.Context, db *sql.DB, directory string, options RuntimeBindingOptions) (report RuntimeBindingReport, err error) {
	report = RuntimeBindingReport{SchemaVersion: 1, Scope: "runtime_database_binding", StartedAt: time.Now().UTC()}
	defer func() {
		report.FinishedAt = time.Now().UTC()
		if report.FailureCode != "" {
			err = errors.New("runtime database binding failed: " + report.FailureCode)
		}
	}()
	base, cookie, valid := runtimeBindingCredentials(db, options)
	if !valid {
		report.FailureCode = "configuration"
		return
	}
	manifest, failure := verifiedBindingManifest(directory, options.ManifestHash)
	if failure != "" {
		report.FailureCode = failure
		return
	}
	report.ManifestHash = options.ManifestHash
	report.Gateway, report.FailureCode = probeGatewayDatabase(parent, db, manifest, base, cookie)
	report.Success = report.FailureCode == ""
	return
}

func runtimeBindingCredentials(db *sql.DB, options RuntimeBindingOptions) (*url.URL, *http.Cookie, bool) {
	base, err := validationOrigin(options.Origin)
	cookie := &http.Cookie{Name: "gateway_session", Value: options.SessionToken}
	valid := db != nil && err == nil && hashPattern.MatchString(options.ManifestHash) && len(options.SessionToken) > 0 && len(options.SessionToken) <= 512 && cookie.Valid() == nil
	return base, cookie, valid
}

func verifiedBindingManifest(directory, expectedHash string) (Manifest, string) {
	file, openErr := os.Open(filepath.Join(directory, "manifest.json"))
	if openErr != nil {
		return Manifest{}, "manifest_open"
	}
	hash := sha256.New()
	manifest, readErr := Read(io.TeeReader(file, hash))
	_ = file.Close()
	if readErr != nil || hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return Manifest{}, "manifest_identity"
	}
	if manifest.VerifyFiles(directory) != nil {
		return Manifest{}, "package_files"
	}
	return manifest, ""
}

func probeGatewayDatabase(parent context.Context, db *sql.DB, manifest Manifest, base *url.URL, cookie *http.Cookie) (*buildinfo.Report, string) {
	return probeGatewayDatabaseIdentity(parent, db, manifest, base, cookie, nil)
}

func probeGatewayDatabaseIdentity(parent context.Context, db *sql.DB, manifest Manifest, base *url.URL, cookie *http.Cookie, identity *database.RuntimeDatabaseIdentity) (*buildinfo.Report, string) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	probe, probeErr := database.BeginRuntimeBinding(ctx, db)
	if probeErr != nil {
		return nil, "probe_unavailable"
	}
	defer probe.Close()
	var before database.RuntimeDatabaseIdentity
	if identity != nil {
		var err error
		before, err = probe.DatabaseIdentity(ctx)
		if err != nil {
			return nil, "database_identity"
		}
	}
	challenge := probe.Request()
	endpoint := *base
	endpoint.Path = "/admin/release/binding"
	request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if requestErr != nil {
		return nil, "configuration"
	}
	request.Header.Set(RuntimeBindingPIDHeader, strconv.FormatInt(challenge.BackendPID, 10))
	request.Header.Set(RuntimeBindingClassHeader, strconv.FormatInt(challenge.ClassID, 10))
	request.AddCookie(cookie)
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 7 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, requestErr := client.Do(request)
	if requestErr != nil {
		return nil, "http_unavailable"
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "binding_unavailable"
	}
	body, bodyErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	var status RuntimeBindingStatus
	if bodyErr != nil || len(body) > 64<<10 || response.Header.Get("Cache-Control") != "no-store" || !validationJSON(response, body, &status) ||
		status.SchemaVersion != 1 || status.Mode != "normal" || status.Probe != challenge || !validationBuildMatches(status.Gateway, manifest) {
		return nil, "binding_response"
	}
	if probe.Verify(ctx, status.Proof) != nil {
		return nil, "database_proof"
	}
	if identity != nil {
		after, err := probe.DatabaseIdentity(ctx)
		if err != nil || before != after {
			return nil, "database_changed"
		}
		*identity = after
	}
	return &status.Gateway, ""
}
