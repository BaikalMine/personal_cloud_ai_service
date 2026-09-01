package gateway

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"ai-access-gateway/internal/config"
	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/moderation"
	"ai-access-gateway/internal/promptassistant"
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
	"ai-access-gateway/internal/virustotal"
)

const (
	sessionCookieName         = "gateway_session"
	serviceCookieName         = "gateway_service"
	openWebIdentityCookieName = "gateway_openweb_identity"
)

type ctxKey string

const (
	userCtxKey       ctxKey = "user"
	requestIDKey     ctxKey = "request_id"
	correlationIDKey ctxKey = "correlation_id"
	generationJobKey ctxKey = "generation_job_id"
	comfyPromptIDKey ctxKey = "comfy_prompt_id"
)

type Config = config.Config

type App struct {
	cfg                    Config
	tpl                    *Templates
	loginLimiter           *security.LoginLimiter
	loginIPLimiter         *security.LoginLimiter
	loginAuditLimiter      *security.LoginLimiter
	inviteLimiter          *security.LoginLimiter
	comfyPromptLimiter     *security.LoginLimiter
	csrfSigner             *security.CSRFSigner
	store                  *store.Store
	mining                 *mining.Client
	systemMonitor          *mining.Client
	promptAssistant        *promptassistant.Client
	contentModerator       *moderation.Client
	updates                *updates.Client
	virusTotal             *virustotal.Client
	contentCipher          *contentcrypto.Cipher
	mediaCaptureSlots      chan struct{}
	adminMediaSlots        chan struct{}
	comfyUploadSlots       chan struct{}
	sensitiveMediaSlots    chan struct{}
	comfyMemorySlots       chan struct{}
	passwordWorkSlots      chan struct{}
	promptAssistantSlots   chan struct{}
	mediaDownloadSlots     chan struct{}
	comfyPromptSlots       chan struct{}
	generationMu           sync.Mutex
	miningPauseMu          sync.Mutex
	comfyQueueMu           sync.Mutex
	comfyPromptAdmissionMu sync.Mutex
	websocketMu            sync.Mutex
	maintenanceOnce        sync.Once
	dependencyOnce         sync.Once
	observabilityOnce      sync.Once
	mediaObservabilityOnce sync.Once
	maintenanceWorkers     *maintenanceRegistry
	maintenanceDone        chan struct{}
	dependencyHealth       *dependencyMonitor
	serviceLatencies       *serviceLatencyRegistry
	mediaOperations        *mediaOperationRegistry
	mediaBytes             *weightedByteLimiter
	objectInfoOnce         sync.Once
	objectInfoCache        *comfyObjectInfoCache
	comfyQueueWasBusy      bool
	generationJobs         map[string]*generationJob
	websocketConnections   map[*trackedWebSocket]struct{}

	requestsTotal atomic.Int64
	loginFailures atomic.Int64
	activeWS      atomic.Int64

	proxyMu     sync.Mutex
	proxyCounts map[string]int64
}

type generationJob struct {
	UserID    int64
	CreatedAt time.Time
	Outputs   map[string]struct{}
}

type User = domain.User

type Activity = domain.Activity

type ServiceUsage = domain.ServiceUsage

type ServiceTrendPoint = domain.ServiceTrendPoint

type ServiceAnalytics = domain.ServiceAnalytics

type ChartPoint = domain.ChartPoint

type UserStats = domain.UserStats

type ServiceStatus struct {
	Name    string
	Online  bool
	Status  int
	Latency time.Duration
	Detail  string
}

type TopUser = domain.TopUser

type AdminStats = domain.AdminStats

type OnlineUser = domain.OnlineUser

type HostMetric = domain.HostMetric

type SystemOverview struct {
	DatabaseBytes     int64                        `json:"database_bytes"`
	OnlineUsers       []OnlineUser                 `json:"online_users"`
	Host              *HostMetric                  `json:"host,omitempty"`
	History           []HostMetric                 `json:"history"`
	GenerationMarkers []domain.GenerationJobMarker `json:"generation_markers"`
	AgentAvailable    bool                         `json:"agent_available"`
	AgentMessage      string                       `json:"agent_message,omitempty"`
	Agent             DependencyStatus             `json:"agent"`
	Dependencies      []DependencyStatus           `json:"dependencies"`
	Workers           []MaintenanceWorkerState     `json:"workers"`
}

type UserRow = domain.UserRow

type InviteRow = domain.InviteRow

type SessionRow = domain.SessionRow

type AccountSession = domain.AccountSession

type AuditRow = domain.AuditRow

type Miner = domain.Miner

type MinerView struct {
	domain.Miner
	State mining.State
}

type MiningOverview struct {
	Available bool
	Running   bool
	Agent     DependencyStatus
	Active    *MinerView
	Default   *MinerView
	Miners    []MinerView
	Message   string
	Script    mining.Script
}

type UpdateOverview struct {
	Available int
	Current   int
	Blocked   int
}

type ContentEventView struct {
	ID                  int64
	Key                 string
	Version             string
	UserID              int64
	Username            string
	AuthorDeleted       bool
	GenerationJobID     *int64
	JobID               string
	RequestID           string
	CorrelationID       string
	Service             string
	Kind                string
	ExternalID          string
	Model               string
	Prompt              string
	Response            string
	Metadata            string
	Assistant           *ContentAssistantView
	GenerationState     string
	JobState            string
	StateLabel          string
	StateClass          string
	StatusMessage       string
	ErrorCode           string
	ErrorMessage        string
	Stages              []ContentStageView
	Sensitive           bool
	VisualPending       bool
	GeneratedMediaCount int64
	MediaExpiresAt      time.Time
	MediaExpired        bool
	MediaCount          int64
	Media               []domain.ContentMediaSummary
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ExpiresAt           time.Time
}

type ContentAssistantView struct {
	Applied        bool
	Decision       string
	Template       string
	Think          bool
	Model          string
	OriginalPrompt string
	Suggestion     string
}

type ContentStageView struct {
	State        string
	Label        string
	Message      string
	ErrorMessage string
	Tone         string
	DurationMS   int64
	CreatedAt    time.Time
}

type ContentOverview struct {
	Total     int
	ComfyUI   int
	OpenWebUI int
	WithMedia int
}

type captureWriter struct {
	http.ResponseWriter
	status   int
	bytes    int64
	onHijack func(net.Conn)
}

func (w *captureWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *captureWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *captureWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *captureWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if reader, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := reader.ReadFrom(src)
		w.bytes += n
		return n, err
	}
	n, err := io.Copy(writerOnly{w}, src)
	return n, err
}

type writerOnly struct{ io.Writer }

func (w *captureWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *captureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("underlying response writer does not implement hijacker")
	}
	conn, rw, err := h.Hijack()
	if err == nil && w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	if err == nil && w.onHijack != nil {
		w.onHijack(conn)
	}
	return conn, rw, err
}

func (w *captureWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}
