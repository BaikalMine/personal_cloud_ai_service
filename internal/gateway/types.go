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
	"ai-access-gateway/internal/security"
	"ai-access-gateway/internal/store"
	"ai-access-gateway/internal/updates"
)

const (
	sessionCookieName         = "gateway_session"
	serviceCookieName         = "gateway_service"
	openWebIdentityCookieName = "gateway_openweb_identity"
)

type ctxKey string

const (
	userCtxKey   ctxKey = "user"
	requestIDKey ctxKey = "request_id"
)

type Config = config.Config

type App struct {
	cfg               Config
	tpl               *Templates
	loginLimiter      *security.LoginLimiter
	csrfSigner        *security.CSRFSigner
	store             *store.Store
	mining            *mining.Client
	updates           *updates.Client
	contentCipher     *contentcrypto.Cipher
	mediaCaptureSlots chan struct{}
	adminMediaSlots   chan struct{}
	comfyUploadSlots  chan struct{}

	requestsTotal atomic.Int64
	loginFailures atomic.Int64
	activeWS      atomic.Int64

	proxyMu     sync.Mutex
	proxyCounts map[string]int64
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
	ID         int64
	UserID     int64
	Username   string
	Service    string
	Kind       string
	ExternalID string
	Model      string
	Prompt     string
	Response   string
	Metadata   string
	MediaCount int64
	Media      []domain.ContentMediaSummary
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

type ContentOverview struct {
	Total     int
	ComfyUI   int
	OpenWebUI int
	WithMedia int
}

type captureWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
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
	return conn, rw, err
}

func (w *captureWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}
