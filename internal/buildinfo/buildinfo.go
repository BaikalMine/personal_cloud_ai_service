// Package buildinfo describes the running artifact, never the current checkout.
package buildinfo

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
)

// Set by the release builder with -X. Empty values retain honest development
// defaults or embedded Go VCS information; environment variables are not read.
var (
	Version  string
	Revision string
	BuiltAt  string
	Modified string
)

const SchemaVersion = 1

const (
	Gateway         = "gateway"
	UpdateAgent     = "update-agent"
	TrainingAgent   = "lora-training-agent"
	MiningAgent     = "mining-agent"
	SystemMonitor   = "system-monitor"
	InferenceWorker = "inference-worker"
	ReleaseTool     = "release-tool"
)

type Info struct {
	Version   string `json:"version"`
	Revision  string `json:"revision,omitempty"`
	BuiltAt   string `json:"built_at,omitempty"`
	Modified  string `json:"modified"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

type Contract struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}

type Report struct {
	SchemaVersion int        `json:"schema_version"`
	Component     string     `json:"component"`
	Build         Info       `json:"build"`
	Contracts     []Contract `json:"contracts"`
}

var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+\-]{0,79}$`)
var revisionPattern = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9\-]{0,63}$`)

var current = sync.OnceValue(func() Info {
	vcs, _ := debug.ReadBuildInfo()
	return resolve(Info{Version: Version, Revision: Revision, BuiltAt: BuiltAt, Modified: Modified}, vcs)
})

func Current() Info { return current() }

func resolve(stamp Info, vcs *debug.BuildInfo) Info {
	if vcs != nil {
		if stamp.Version == "" && vcs.Main.Version != "(devel)" {
			stamp.Version = vcs.Main.Version
		}
		for _, setting := range vcs.Settings {
			switch setting.Key {
			case "vcs.revision":
				if stamp.Revision == "" {
					stamp.Revision = setting.Value
				}
			case "vcs.modified":
				if stamp.Modified == "" {
					stamp.Modified = setting.Value
				}
			}
		}
	}
	if !versionPattern.MatchString(stamp.Version) {
		stamp.Version = "dev"
	}
	if !revisionPattern.MatchString(stamp.Revision) {
		stamp.Revision = ""
	}
	if parsed, err := time.Parse(time.RFC3339, stamp.BuiltAt); err == nil {
		stamp.BuiltAt = parsed.UTC().Format(time.RFC3339)
	} else {
		stamp.BuiltAt = ""
	}
	if stamp.Modified != "true" && stamp.Modified != "false" {
		stamp.Modified = "unknown"
	}
	stamp.GoVersion, stamp.Platform = runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH
	return stamp
}

func (info Info) ReleaseIdentified() bool {
	_, timestampErr := time.Parse(time.RFC3339, info.BuiltAt)
	return info.Version != "dev" && versionPattern.MatchString(info.Version) && revisionPattern.MatchString(info.Revision) &&
		timestampErr == nil && info.Modified == "false"
}

func (info Info) ShortRevision() string {
	if len(info.Revision) > 12 {
		return info.Revision[:12]
	}
	return info.Revision
}

// Contracts describe implemented wire APIs, not current operational readiness.
// Bump a contract for a breaking wire change and retain additive APIs separately.
func Contracts(component string) []Contract {
	var contracts []Contract
	switch component {
	case Gateway:
		contracts = []Contract{{"gateway-admin", 1}, {"generation-submission", 1}, {"gpu-admission", 1}, {"release-runtime-binding", 1}, {"release-runtime-fence", 1}, {"release-validation", 1}}
	case UpdateAgent:
		contracts = []Contract{{"updates", 1}, {"comfy-assets", 1}, {"comfy-storage", 1}}
	case TrainingAgent:
		contracts = []Contract{{"training-jobs", 1}, {"training-submission", 1}, {"training-storage", 1}, {"generation-components", 1}}
	case MiningAgent:
		contracts = []Contract{{"mining-state", 1}, {"mining-fencing", 2}}
	case SystemMonitor:
		contracts = []Contract{{"system-metrics", 1}, {"comfy-memory-trim", 1}}
	case InferenceWorker:
		contracts = []Contract{{"inference-readiness", 2}, {"owned-inference", 1}, {"release-runtime-fence", 1}, {"assistant-model-access", 1}}
	case ReleaseTool:
		contracts = []Contract{{"release-manifest", 1}}
	}
	slices.SortFunc(contracts, func(a, b Contract) int { return strings.Compare(a.Name, b.Name) })
	return contracts
}

func NewReport(component string) Report {
	return Report{SchemaVersion: SchemaVersion, Component: component, Build: Current(), Contracts: Contracts(component)}
}

func Write(output io.Writer, component string) error {
	return json.NewEncoder(output).Encode(NewReport(component))
}

// Handler must be mounted behind the agent's existing authentication boundary.
// The inference worker uses its existing internal-only readiness listener.
func Handler(component string) http.Handler {
	report := NewReport(component)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(report)
		}
	})
}
