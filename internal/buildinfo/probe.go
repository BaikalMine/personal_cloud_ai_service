package buildinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxReportBytes = 16 << 10

type Check struct {
	Status  string    `json:"status"`
	Report  *Report   `json:"report,omitempty"`
	Issues  []string  `json:"issues,omitempty"`
	Checked time.Time `json:"checked_at"`
}

func validate(report Report) error {
	if report.SchemaVersion != SchemaVersion || len(Contracts(report.Component)) == 0 ||
		len(report.Contracts) == 0 || len(report.Contracts) > 32 || !versionPattern.MatchString(report.Build.Version) ||
		(report.Build.Revision != "" && !revisionPattern.MatchString(report.Build.Revision)) ||
		!printable(report.Build.GoVersion, 100) || !printable(report.Build.Platform, 40) ||
		(report.Build.Modified != "true" && report.Build.Modified != "false" && report.Build.Modified != "unknown") {
		return errors.New("invalid build report")
	}
	if report.Build.BuiltAt != "" {
		if _, err := time.Parse(time.RFC3339, report.Build.BuiltAt); err != nil {
			return errors.New("invalid build timestamp")
		}
	}
	seen := make(map[string]bool)
	for _, contract := range report.Contracts {
		if !namePattern.MatchString(contract.Name) || contract.Version < 1 || contract.Version > 1000 || seen[contract.Name] {
			return errors.New("invalid API contract")
		}
		seen[contract.Name] = true
	}
	return nil
}

func printable(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	for _, ch := range value {
		if ch < 32 || ch > 126 {
			return false
		}
	}
	return true
}

// Compare requires the expected component and exact versions of known contracts.
// Extra contracts are additive. A matching commit is neither required nor proof
// of compatibility. Build identity and operational readiness are separate facts.
func Compare(component string, report Report) Check {
	check := Check{Status: "invalid", Checked: time.Now().UTC()}
	if validate(report) != nil || report.Component != component {
		return check
	}
	check.Report = &report
	actual := make(map[string]int, len(report.Contracts))
	for _, contract := range report.Contracts {
		actual[contract.Name] = contract.Version
	}
	for _, expected := range Contracts(component) {
		if actual[expected.Name] != expected.Version {
			check.Issues = append(check.Issues, expected.Name)
		}
	}
	check.Status = "compatible"
	if len(check.Issues) > 0 {
		check.Status = "incompatible"
	}
	return check
}

// Probe performs one bounded read. It never follows redirects, retries, changes
// configuration, calls an agent operation or returns endpoints/tokens to the UI.
func Probe(parent context.Context, component string, base *url.URL, token string) Check {
	check := Check{Status: "not_configured", Checked: time.Now().UTC()}
	if base == nil || base.Host == "" || (component != InferenceWorker && strings.TrimSpace(token) == "") {
		return check
	}
	if base.User != nil || (base.Scheme != "http" && base.Scheme != "https") || base.RawQuery != "" || base.Fragment != "" {
		check.Status = "invalid_configuration"
		return check
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/build"
	endpoint.RawPath = ""
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		check.Status = "invalid_configuration"
		return check
	}
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	check.Status = "unavailable"
	if err != nil {
		return check
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		check.Status = "unauthorized"
		return check
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		check.Status = "unsupported"
		return check
	case http.StatusOK:
	default:
		return check
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxReportBytes+1))
	check.Status = "invalid"
	if err != nil || len(body) > maxReportBytes {
		return check
	}
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&report) != nil {
		return check
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return check
	}
	return Compare(component, report)
}
