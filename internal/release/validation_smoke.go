package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/database"
)

type ValidationSmokeOptions struct {
	PublicURL, AdminURL string
	Operation           database.RuntimeValidationRequest
	Username, Password  string
}

type ValidationSmokeCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

type ValidationSmokeReport struct {
	SchemaVersion        int                                `json:"schema_version"`
	Scope                string                             `json:"scope"`
	Success              bool                               `json:"success"`
	DeploymentAuthorized bool                               `json:"deployment_authorized"`
	Operation            *database.RuntimeValidationRequest `json:"operation,omitempty"`
	Candidate            *buildinfo.Info                    `json:"candidate,omitempty"`
	StartedAt            time.Time                          `json:"started_at"`
	FinishedAt           time.Time                          `json:"finished_at"`
	FailureCode          string                             `json:"failure_code,omitempty"`
	CleanupStatus        string                             `json:"cleanup_status"`
	Checks               []ValidationSmokeCheck             `json:"checks"`
}

// SmokeValidation verifies the package before contacting the explicitly local
// restricted candidate. It neither starts processes nor authorizes activation.
// Reports intentionally contain no credentials, cookies, response bodies or URLs.
func SmokeValidation(ctx context.Context, directory string, options ValidationSmokeOptions) (report ValidationSmokeReport, err error) {
	report = ValidationSmokeReport{SchemaVersion: 1, Scope: "candidate_route_smoke", StartedAt: time.Now().UTC(), CleanupStatus: "not_needed"}
	defer func() {
		report.FinishedAt = time.Now().UTC()
		if report.FailureCode != "" {
			err = errors.New("candidate route smoke failed: " + report.FailureCode)
		}
	}()
	check := func(name string, passed bool) bool {
		report.Checks = append(report.Checks, ValidationSmokeCheck{Name: name, Passed: passed})
		if !passed && report.FailureCode == "" {
			report.FailureCode = name
		}
		return passed
	}
	public, publicErr := validationOrigin(options.PublicURL)
	admin, adminErr := validationOrigin(options.AdminURL)
	if !check("configuration", options.Operation.Validate() == nil && publicErr == nil && adminErr == nil &&
		public.Hostname() == admin.Hostname() && public.Scheme == admin.Scheme && public.Host != admin.Host &&
		strings.TrimSpace(options.Username) != "" && len(options.Username) <= 254 && options.Password != "" && len(options.Password) <= 4096) {
		return
	}
	operation := options.Operation
	operation.Token = ""
	report.Operation = &operation
	file, openErr := os.Open(filepath.Join(directory, "manifest.json"))
	if !check("manifest_open", openErr == nil) {
		return
	}
	hash := sha256.New()
	manifest, readErr := Read(io.TeeReader(file, hash))
	_ = file.Close()
	if !check("manifest_identity", readErr == nil && hex.EncodeToString(hash.Sum(nil)) == options.Operation.CandidateManifestHash) ||
		!check("package_files", manifest.VerifyFiles(directory) == nil) {
		return
	}
	report.Candidate = &manifest.Build
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 64 << 10,
		DisableCompression: true, MaxIdleConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	smoke := validationSmokeClient{client: &http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		token: options.Operation.Token, public: public, admin: admin}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	// Both listeners must identify the same candidate before any password is sent.
	for _, endpoint := range []struct {
		name string
		base *url.URL
	}{{"public", public}, {"admin", admin}} {
		response, body, requestErr := smoke.call(ctx, endpoint.base, "GET", "/internal/release/validation", nil, true)
		var status ValidationStatus
		if !check(endpoint.name+"_identity", requestErr == nil && response.StatusCode == http.StatusOK &&
			validationJSON(response, body, &status) && validationStatusMatches(status, options.Operation, manifest)) {
			return
		}
		response, _, requestErr = smoke.call(ctx, endpoint.base, "GET", "/login", nil, false)
		if !check(endpoint.name+"_token_required", requestErr == nil && response.StatusCode == http.StatusServiceUnavailable) {
			return
		}
		response, _, requestErr = smoke.call(ctx, endpoint.base, "GET", "/readyz", nil, true)
		if !check(endpoint.name+"_not_production_ready", requestErr == nil && response.StatusCode == http.StatusServiceUnavailable) {
			return
		}
	}
	response, _, requestErr := smoke.call(ctx, public, "GET", "/app", nil, true)
	if !check("ordinary_auth_required", requestErr == nil && validationLoginRedirect(response)) {
		return
	}
	loginAttempted := false
	defer func() {
		if !loginAttempted {
			return
		}
		// Cleanup is independently bounded even when the caller cancelled the smoke.
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleaned := smoke.logout(cleanup)
		report.CleanupStatus = "verified"
		if !check("session_cleanup", cleaned) {
			report.CleanupStatus = "unconfirmed"
		}
		report.Success = report.FailureCode == ""
	}()
	loginAttempted = true
	response, _, requestErr = smoke.call(ctx, public, "POST", "/login", url.Values{"login": {options.Username}, "password": {options.Password}, "next": {"/app"}}, true)
	if response != nil {
		for _, cookie := range response.Cookies() {
			if cookie.Name == "gateway_session" && cookie.Value != "" && cookie.MaxAge >= 0 && cookie.Path == "/" && cookie.Domain == "" && cookie.HttpOnly && (!cookie.Secure || public.Scheme == "https") {
				smoke.session = cookie
			}
		}
	}
	if !check("password_login", requestErr == nil && response.StatusCode == http.StatusFound && response.Header.Get("Location") == "/app" && smoke.session != nil) {
		return
	}
	for _, page := range validationPages {
		base := public
		if page.admin {
			base = admin
		}
		response, body, requestErr := smoke.call(ctx, base, "GET", page.path, nil, true)
		var document validationDocument
		if requestErr == nil && response.StatusCode == http.StatusOK && validationContentType(response, "text/html") {
			document, requestErr = readValidationDocument(body)
		} else {
			requestErr = errors.New("page unavailable")
		}
		if !check(page.name, requestErr == nil && smoke.acceptPage(document, page)) {
			return
		}
	}
	response, body, requestErr := smoke.call(ctx, admin, "GET", "/admin/release/status", nil, true)
	var snapshot struct {
		SchemaVersion int              `json:"schema_version"`
		Gateway       buildinfo.Report `json:"gateway"`
	}
	// The admin snapshot also contains peer observations, outside this smoke scope.
	if !check("admin_build_report", requestErr == nil && response.StatusCode == http.StatusOK && validationContentType(response, "application/json") &&
		json.Unmarshal(body, &snapshot) == nil && snapshot.SchemaVersion == 1 && validationBuildMatches(snapshot.Gateway, manifest)) {
		return
	}
	for _, probe := range []struct {
		name, method, path string
		admin              bool
	}{
		{"generation_denied", "POST", "/generate/run", false},
		{"gpu_permit_denied", "POST", "/internal/gpu/comfy/permit", false},
		{"admin_mutation_denied", "POST", "/admin/users", true},
		{"proxy_denied", "GET", "/comfyui", false},
		{"stream_denied", "GET", "/api/tasks/events", false},
	} {
		base := public
		if probe.admin {
			base = admin
		}
		response, _, requestErr := smoke.call(ctx, base, probe.method, probe.path, nil, true)
		if !check(probe.name, requestErr == nil && response.StatusCode == http.StatusServiceUnavailable) {
			return
		}
	}
	return
}

func validationOrigin(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return nil, errors.New("invalid local origin")
	}
	address, addressErr := netip.ParseAddr(parsed.Hostname())
	port, portErr := strconv.Atoi(parsed.Port())
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Opaque != "" ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" ||
		addressErr != nil || !address.IsLoopback() || address.Zone() != "" || portErr != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid local origin")
	}
	parsed.Host = net.JoinHostPort(address.String(), strconv.Itoa(port))
	return parsed, nil
}

func validationBuildMatches(actual buildinfo.Report, manifest Manifest) bool {
	if actual.Component != buildinfo.Gateway || buildinfo.Compare(buildinfo.Gateway, actual).Status != "compatible" {
		return false
	}
	for _, artifact := range manifest.Artifacts {
		expected := artifact.Report
		if expected.Component == actual.Component && expected.SchemaVersion == actual.SchemaVersion && expected.Build == actual.Build && slices.Equal(expected.Contracts, actual.Contracts) {
			return true
		}
	}
	return false
}

func validationStatusMatches(status ValidationStatus, operation database.RuntimeValidationRequest, manifest Manifest) bool {
	return status.SchemaVersion == 1 && status.Mode == "release_validation" && status.ProductionReady != nil && !*status.ProductionReady &&
		status.Operation.OperationID == operation.OperationID && status.Operation.Epoch == operation.Epoch &&
		status.Operation.CandidateManifestHash == operation.CandidateManifestHash && validationBuildMatches(status.Gateway, manifest)
}

func validationContentType(response *http.Response, expected string) bool {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	return err == nil && mediaType == expected
}

func validationJSON(response *http.Response, body []byte, target any) bool {
	if !validationContentType(response, "application/json") {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var extra any
	return decoder.Decode(target) == nil && decoder.Decode(&extra) == io.EOF
}

type validationSmokeClient struct {
	client        *http.Client
	public, admin *url.URL
	token         string
	session       *http.Cookie
	csrf, owner   string
}

func (s *validationSmokeClient) call(ctx context.Context, base *url.URL, method, path string, form url.Values, token bool) (*http.Response, []byte, error) {
	endpoint := *base
	endpoint.Path = path
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, errors.New("request configuration")
	}
	if token {
		request.Header.Set(ValidationTokenHeader, s.token)
	}
	if s.session != nil {
		request.AddCookie(s.session)
	}
	if method == "POST" {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", base.String())
	}
	response, err := s.client.Do(request)
	if err != nil {
		return response, nil, errors.New("request unavailable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 || response.Header.Get("Cache-Control") != "no-store" {
		return response, nil, errors.New("response invalid")
	}
	return response, body, nil
}

func validationLoginRedirect(response *http.Response) bool {
	if response == nil || response.StatusCode != http.StatusFound {
		return false
	}
	location, err := url.Parse(response.Header.Get("Location"))
	return err == nil && location.Scheme == "" && location.Host == "" && location.Path == "/login" && location.Fragment == ""
}

func (s *validationSmokeClient) logout(ctx context.Context) bool {
	if s.session == nil {
		return false
	}
	if s.csrf == "" {
		response, body, err := s.call(ctx, s.public, "GET", "/app", nil, true)
		if err != nil || response.StatusCode != http.StatusOK {
			return false
		}
		document, err := readValidationDocument(body)
		if err != nil {
			return false
		}
		s.csrf = document.csrf
	}
	if s.csrf == "" {
		return false
	}
	response, _, err := s.call(ctx, s.public, "POST", "/logout", url.Values{"csrf": {s.csrf}}, true)
	if err != nil || !validationLoginRedirect(response) {
		return false
	}
	cleared := false
	for _, cookie := range response.Cookies() {
		if cookie.Name == "gateway_session" && cookie.Value == "" && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		return false
	}
	// Replay the old cookie: clearing a browser cookie alone does not prove that
	// the server actually invalidated the session in the database.
	response, _, err = s.call(ctx, s.public, "GET", "/app", nil, true)
	return err == nil && validationLoginRedirect(response)
}

type validationPage struct {
	name, path, marker, element, ownerAttribute string
	admin                                       bool
}

var validationPages = []validationPage{
	{"app_page", "/app", "", "", "", false},
	{"generation_page", "/generate", "data-comfy-generation", "main", "data-generation-owner", false},
	{"gallery_page", "/gallery", "data-user-gallery", "main", "data-owner", false},
	{"lora_page", "/train-lora", "data-lora-library", "main", "data-owner", false},
	{"tasks_page", "/tasks", "data-task-page", "main", "data-owner", false},
	{"admin_page", "/admin", "", "", "", true},
	{"users_page", "/admin/users", "data-access-users", "table", "", true},
	{"invites_page", "/admin/invites", "data-admin-invite-form", "form", "", true},
	{"release_page", "/admin/release", "", "", "", true},
}

type validationElement struct {
	name       string
	attributes map[string]string
}
type validationDocument struct {
	elements    []validationElement
	owner, csrf string
}

func readValidationDocument(body []byte) (validationDocument, error) {
	document := validationDocument{}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.Strict, decoder.AutoClose, decoder.Entity = false, xml.HTMLAutoClose, xml.HTMLEntity
	htmlStart, htmlEnd, logout := false, false, false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return document, errors.New("invalid page document")
		}
		switch element := token.(type) {
		case xml.StartElement:
			attributes := make(map[string]string, len(element.Attr))
			for _, attribute := range element.Attr {
				attributes[attribute.Name.Local] = attribute.Value
			}
			document.elements = append(document.elements, validationElement{element.Name.Local, attributes})
			if element.Name.Local == "html" {
				htmlStart = true
			}
			if _, ok := attributes["data-notification-center"]; ok {
				owner := attributes["data-notification-owner"]
				if document.owner != "" && owner != document.owner {
					return document, errors.New("ambiguous page owner")
				}
				document.owner = owner
			}
			if element.Name.Local == "form" {
				logout = attributes["action"] == "/logout" && attributes["method"] == "post"
			}
			if logout && element.Name.Local == "input" && attributes["name"] == "csrf" && attributes["type"] == "hidden" {
				csrf := attributes["value"]
				decoded, err := base64.RawURLEncoding.DecodeString(csrf)
				if err != nil || len(decoded) != 32 || (document.csrf != "" && document.csrf != csrf) {
					return document, errors.New("invalid page csrf")
				}
				document.csrf = csrf
			}
		case xml.EndElement:
			if element.Name.Local == "html" {
				htmlEnd = true
			}
			if element.Name.Local == "form" {
				logout = false
			}
		}
	}
	owner, ownerErr := strconv.ParseInt(document.owner, 10, 64)
	if !htmlStart || !htmlEnd || !bytes.HasSuffix(bytes.TrimSpace(body), []byte("</html>")) || ownerErr != nil || owner <= 0 || document.csrf == "" {
		return document, errors.New("incomplete authenticated page")
	}
	return document, nil
}

func (s *validationSmokeClient) acceptPage(document validationDocument, page validationPage) bool {
	if s.owner == "" {
		s.owner = document.owner
	}
	if document.owner != s.owner {
		return false
	}
	s.csrf = document.csrf
	if page.marker == "" {
		return true
	}
	matches := 0
	for _, element := range document.elements {
		if _, ok := element.attributes[page.marker]; !ok || element.name != page.element {
			continue
		}
		matches++
		if page.ownerAttribute != "" && element.attributes[page.ownerAttribute] != s.owner {
			return false
		}
		if page.marker == "data-user-gallery" && element.attributes["data-can-train"] != "true" {
			return false
		}
	}
	return matches == 1
}
