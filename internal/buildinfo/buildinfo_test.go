package buildinfo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime/debug"
	"strings"
	"testing"
)

func releaseInfo() Info {
	return Info{Version: "v2026.09.10", Revision: strings.Repeat("a", 40), BuiltAt: "2026-09-10T09:00:00Z", Modified: "false", GoVersion: "go1.26.6", Platform: "windows/amd64"}
}

func TestBuildIdentityUsesEmbeddedValuesAndDoesNotInventDates(t *testing.T) {
	vcs := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: strings.Repeat("b", 40)}, {Key: "vcs.modified", Value: "true"}, {Key: "vcs.time", Value: "2026-09-01T00:00:00Z"},
	}}
	dev := resolve(Info{}, vcs)
	if dev.Version != "dev" || dev.Revision != strings.Repeat("b", 40) || dev.Modified != "true" || dev.BuiltAt != "" || dev.ReleaseIdentified() {
		t.Fatalf("development identity = %+v", dev)
	}
	stamp := resolve(releaseInfo(), vcs)
	if !stamp.ReleaseIdentified() || stamp.Revision != strings.Repeat("a", 40) || stamp.Modified != "false" {
		t.Fatalf("release identity = %+v", stamp)
	}
	stamp.BuiltAt = "not-a-date"
	if stamp.ReleaseIdentified() {
		t.Fatal("invalid date claimed an identified release")
	}
	if resolve(Info{Version: "bad version", Revision: "HEAD", Modified: "maybe"}, nil).ReleaseIdentified() {
		t.Fatal("invalid injected values claimed a release")
	}
}

func TestCompatibilityRequiresContractsNotMatchingCommits(t *testing.T) {
	for _, component := range []string{Gateway, UpdateAgent, TrainingAgent, MiningAgent, SystemMonitor, InferenceWorker} {
		report := NewReport(component)
		report.Build = releaseInfo()
		if check := Compare(component, report); check.Status != "compatible" {
			t.Fatalf("%s: %+v", component, check)
		}
		report.Contracts = append(report.Contracts, Contract{"future-additive-api", 1})
		if Compare(component, report).Status != "compatible" {
			t.Fatal("additive contract rejected")
		}
		report.Contracts[0].Version++
		if check := Compare(component, report); check.Status != "incompatible" || len(check.Issues) != 1 {
			t.Fatalf("mismatch = %+v", check)
		}
	}
	if Compare(Gateway, NewReport(TrainingAgent)).Status != "invalid" {
		t.Fatal("wrong component accepted")
	}
}

func TestBuildReportHTTPBoundary(t *testing.T) {
	handler := Handler(UpdateAgent)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/v1/build", nil))
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		if method == http.MethodPost {
			if response.Code != 405 {
				t.Fatal(response.Code)
			}
			continue
		}
		if response.Code != 200 {
			t.Fatal(response.Code)
		}
		if method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
		if method == http.MethodGet {
			var report Report
			if json.Unmarshal(response.Body.Bytes(), &report) != nil || Compare(UpdateAgent, report).Status != "compatible" {
				t.Fatal(response.Body.String())
			}
		}
	}
}

func TestBuildProbeTreatsOldInvalidAndUnauthorizedAgentsAsUnconfirmed(t *testing.T) {
	for _, mode := range []string{"ok", "old", "unauthorized", "redirect", "trailing", "oversized", "wrong", "future", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/prefix/v1/build" || r.Header.Get("Authorization") != "Bearer private-token" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				report := NewReport(UpdateAgent)
				switch mode {
				case "old":
					w.WriteHeader(404)
					return
				case "unauthorized":
					w.WriteHeader(401)
					return
				case "redirect":
					http.Redirect(w, r, "/private-target", 302)
					return
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat(" ", maxReportBytes+1)))
					return
				case "wrong":
					report.Component = TrainingAgent
				case "future":
					report.SchemaVersion++
				case "duplicate":
					report.Contracts = append(report.Contracts, report.Contracts[0])
				}
				_ = json.NewEncoder(w).Encode(report)
				if mode == "trailing" {
					_, _ = w.Write([]byte(`{}`))
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL + "/prefix")
			check := Probe(context.Background(), UpdateAgent, base, "private-token")
			expected := map[string]string{"ok": "compatible", "old": "unsupported", "unauthorized": "unauthorized", "redirect": "unavailable"}[mode]
			if expected == "" {
				expected = "invalid"
			}
			if check.Status != expected || calls != 1 {
				t.Fatalf("%+v, calls=%d", check, calls)
			}
			encoded, _ := json.Marshal(check)
			if strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), server.URL) {
				t.Fatal("private address/token in report")
			}
		})
	}
}

func TestBuildProbeHonorsCancellationWithoutNetworkRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base, _ := url.Parse("http://127.0.0.1:1")
	if check := Probe(ctx, UpdateAgent, base, "token"); check.Status != "unavailable" {
		t.Fatal(check)
	}
	if Probe(ctx, UpdateAgent, base, "").Status != "not_configured" {
		t.Fatal("missing auth probed")
	}
}
