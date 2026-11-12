package inference

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
)

type OllamaAPIConfig struct {
	ClientKey    string
	AdmissionURL string
	AdmissionKey string
}

type ownedHTTPRuntime interface {
	ExecuteHTTP(context.Context, modelruntime.HTTPRequest, modelruntime.HTTPConsumer) (modelruntime.Result, error)
}

type OllamaAPI struct {
	worker    *Worker
	runtime   ownedHTTPRuntime
	config    OllamaAPIConfig
	instance  string
	client    *http.Client
	closed    atomic.Bool
	catalog   ollamaCatalogCache
	tasksMu   sync.Mutex
	taskPool  *harnessTaskPool
	tasksStop chan struct{}
	tasksDone chan struct{}
}

var errOllamaAdmissionBusy = errors.New("Ollama GPU admission is busy")

func validOllamaKey(key string) bool {
	return len(key) >= 32 && len(key) <= 256 && strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) < 0
}

func ValidateOllamaAPIConfig(config OllamaAPIConfig) error {
	if !validOllamaKey(config.ClientKey) || !validOllamaKey(config.AdmissionKey) || config.ClientKey == config.AdmissionKey {
		return errors.New("Ollama API requires distinct 32-256 character client and admission keys")
	}
	u, err := url.Parse(config.AdmissionURL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") ||
		(u.Path != "/internal/gpu/ollama/permit" && u.Path != "/internal/gpu/harness/permit") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return errors.New("Ollama admission URL must be the Gateway internal Ollama permit endpoint")
	}
	return nil
}

// This listener is opt-in. It never proxies to the user's ordinary Ollama.
// Managed inference and external HTTP calls share the same native owner/runMu.
func NewOllamaAPI(worker *Worker, config OllamaAPIConfig) (*OllamaAPI, error) {
	if err := ValidateOllamaAPIConfig(config); err != nil {
		return nil, err
	}
	if worker == nil || worker.owned == nil || worker.journal == nil {
		return nil, errors.New("Ollama API requires a durable owned inference worker")
	}
	runtime, ok := worker.owned.runtime.(ownedHTTPRuntime)
	if !ok {
		return nil, errors.New("owned runtime does not support Ollama HTTP")
	}
	var instance [16]byte
	if _, err := rand.Read(instance[:]); err != nil {
		return nil, err
	}
	if !worker.ollamaAdmission.CompareAndSwap(false, true) {
		return nil, errors.New("worker already has an Ollama API")
	}
	a := &OllamaAPI{worker: worker, runtime: runtime, config: config, instance: hex.EncodeToString(instance[:]), tasksStop: make(chan struct{}), tasksDone: make(chan struct{}), client: &http.Client{
		// The admission handler has a 10-second transaction deadline followed
		// by up to 15 seconds of mining-pause cleanup before acknowledging it.
		Timeout:       35 * time.Second,
		Transport:     &http.Transport{MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	go a.watchHarnessTasks()
	return a, nil
}

func (a *OllamaAPI) Close() {
	if !a.closed.Swap(true) {
		close(a.tasksStop)
		<-a.tasksDone
		a.tasksMu.Lock()
		p := a.taskPool
		a.tasksMu.Unlock()
		_ = a.closeHarnessPool(p, true)
		a.worker.ollamaAdmission.Store(false)
		a.client.CloseIdleConnections()
	}
}

func (a *OllamaAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.config.ClientKey)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="Ollama"`)
		ollamaHTTPError(w, r, http.StatusUnauthorized, "authentication_required")
		return
	}
	if r.URL.Path == harnessTaskRoute {
		a.handleHarnessTask(w, r)
		return
	}
	method := ollamaRouteMethod(r.URL)
	if method == "" {
		ollamaHTTPError(w, r, http.StatusNotFound, "route_unavailable")
		return
	}
	if r.Method != method && !(method == http.MethodGet && r.Method == http.MethodHead) {
		allow := method
		if method == http.MethodGet {
			allow += ", HEAD"
		}
		w.Header().Set("Allow", allow)
		ollamaHTTPError(w, r, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if a.closed.Load() || !a.worker.running.Load() {
		ollamaHTTPError(w, r, http.StatusServiceUnavailable, "executor_unavailable")
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	if r.Header.Get("Content-Encoding") != "" {
		ollamaHTTPError(w, r, http.StatusUnsupportedMediaType, "content_encoding_unavailable")
		return
	}
	if r.ContentLength > domain.MaxGPUInferenceRequestBytes {
		ollamaHTTPError(w, r, http.StatusRequestEntityTooLarge, "invalid_request_body")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, domain.MaxGPUInferenceRequestBytes)
	body, err := io.ReadAll(r.Body)
	defer clear(body)
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		ollamaHTTPError(w, r, status, "invalid_request_body")
		return
	}
	_ = controller.SetReadDeadline(time.Time{})
	if method == http.MethodGet {
		if len(body) != 0 {
			ollamaHTTPError(w, r, http.StatusBadRequest, "unexpected_request_body")
			return
		}
	} else {
		var object map[string]json.RawMessage
		if json.Unmarshal(body, &object) != nil || object == nil {
			ollamaHTTPError(w, r, http.StatusBadRequest, "invalid_request_json")
			return
		}
	}
	var pool *harnessTaskPool
	retainedOK := false
	if len(r.Header.Values(harnessSessionHeader)) > 0 {
		if !a.harnessTaskEnabled() || r.URL.Path != "/v1/chat/completions" || len(r.Header.Values(harnessSessionHeader)) != 1 || !validHarnessIdentity(r.Header.Get(harnessSessionHeader)) {
			ollamaHTTPError(w, r, 400, "invalid_harness_session")
			return
		}
		pool, err = a.acquireHarnessCall(r.Header.Get(harnessSessionHeader))
		if err != nil {
			if errors.Is(err, errOllamaAdmissionBusy) {
				a.replyBusy(w, r)
			} else {
				ollamaHTTPError(w, r, 503, "harness_task_unavailable")
			}
			return
		}
		defer func() {
			pool.calls.Unlock()
			if !retainedOK {
				_ = a.closeHarnessPool(pool, true)
			}
		}()
	} else {
		if !a.worker.runMu.TryLock() {
			a.replyBusy(w, r)
			return
		}
		defer a.worker.runMu.Unlock()
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Hour)
	defer cancel()
	if pool != nil {
		stopTask := context.AfterFunc(pool.ctx, cancel)
		defer stopTask()
	}
	var permit domain.GPUExecutorPermitRequest
	if pool != nil {
		permit = pool.permit
	} else {
		if err = a.worker.prepareExecution(ctx); err != nil {
			ollamaHTTPError(w, r, http.StatusServiceUnavailable, "executor_unavailable")
			return
		}
		var identity [48]byte
		if _, err = rand.Read(identity[:]); err != nil {
			ollamaHTTPError(w, r, http.StatusServiceUnavailable, "executor_unavailable")
			return
		}
		owner := a.worker.owned.executor
		permit = domain.GPUExecutorPermitRequest{Executor: domain.GPUExecutorOllama, ID: hex.EncodeToString(identity[:16]),
			Token: hex.EncodeToString(identity[16:]), InstanceID: a.instance, OwnerID: owner.ID, OwnerGeneration: owner.Generation, OwnerToken: owner.Token}
		if err = a.reserve(ctx, permit); err != nil {
			if errors.Is(err, errOllamaAdmissionBusy) {
				a.replyBusy(w, r)
			} else {
				// A lost response can precede a late commit. Advancing ownership,
				// not a missing-row lookup, fences that outstanding reservation.
				a.worker.invalidateOwned()
				ollamaHTTPError(w, r, http.StatusServiceUnavailable, "admission_unconfirmed")
			}
			return
		}
		started, err := a.worker.store.StartGPUExecutorPermit(ctx, permit)
		if err != nil || !started {
			a.worker.invalidateOwned()
			ollamaHTTPError(w, r, http.StatusServiceUnavailable, "admission_unconfirmed")
			return
		}
	}
	settled := false
	defer func() {
		if pool == nil && !settled {
			a.worker.invalidateOwned()
		}
	}()
	wroteHeader := false
	var catalogBody ollamaCatalogCapture
	var catalogType string
	catalogOK := false
	var completionTail []byte
	defer func() { clear(completionTail) }()
	result, executeErr := a.runtime.ExecuteHTTP(ctx, modelruntime.HTTPRequest{Method: r.Method, Path: r.URL.EscapedPath(), Body: body, ResponseLimit: modelruntime.MaxHTTPResponseBytes, Retain: pool != nil}, func(status int, headers http.Header, reader io.Reader) error {
		if status < 200 || status > 599 {
			return errors.New("invalid model response status")
		}
		if contentType := headers.Get("Content-Type"); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("X-Accel-Buffering", "no")
		_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
		w.WriteHeader(status)
		wroteHeader = true
		if r.Method == http.MethodGet && ollamaCatalogRoute(r.URL.Path) && status == http.StatusOK {
			catalogOK = true
			catalogType = headers.Get("Content-Type")
			reader = io.TeeReader(reader, &catalogBody)
		}
		writer := ollamaStreamWriter{w, controller}
		if r.URL.Path == "/v1/chat/completions" && status == http.StatusOK && strings.HasPrefix(headers.Get("Content-Type"), "text/event-stream") {
			var err error
			completionTail, err = copyOllamaCompletion(writer, reader)
			return err
		}
		_, err := io.Copy(writer, reader)
		return err
	})
	clear(body)
	if pool != nil {
		retainedOK = executeErr == nil && result.Retained && !result.Stopped && result.Status >= 200 && result.Status < 300 && ctx.Err() == nil && wroteHeader
		settled = retainedOK
	} else if !result.Stopped {
		a.worker.invalidateOwned()
	} else {
		// Client cancellation must not cancel the stop receipt transaction.
		settle, stopSettle := context.WithTimeout(context.Background(), 10*time.Second)
		err = a.worker.store.FinishGPUExecutorPermit(settle, permit, "quiesced")
		stopSettle()
		if err != nil {
			a.worker.invalidateOwned()
		} else {
			settled = true
		}
	}
	if executeErr != nil || (!result.Stopped && !retainedOK) || (pool != nil && !retainedOK) || err != nil || !wroteHeader {
		log.Printf("owned Ollama HTTP failed: dispatched=%t stopped=%t response_started=%t runtime_error=%v settlement_error=%v", result.Dispatched, result.Stopped, wroteHeader, executeErr, err)
		if wroteHeader {
			panic(http.ErrAbortHandler)
		}
		ollamaHTTPError(w, r, http.StatusBadGateway, "executor_response_unconfirmed")
		return
	}
	if catalogOK && !catalogBody.overflow {
		a.catalog.put(r.URL.Path, catalogType, catalogBody.body, time.Now())
	}
	if len(completionTail) > 0 {
		if _, err := (ollamaStreamWriter{w, controller}).Write(completionTail); err != nil {
			retainedOK = false
			panic(http.ErrAbortHandler)
		}
	}
}

func (a *OllamaAPI) replyBusy(w http.ResponseWriter, r *http.Request) {
	if a.catalog.serve(w, r, time.Now()) {
		return
	}
	w.Header().Set("Retry-After", "3")
	ollamaHTTPError(w, r, http.StatusConflict, "gpu_busy")
}

type ollamaStreamWriter struct {
	writer     http.ResponseWriter
	controller *http.ResponseController
}

func (w ollamaStreamWriter) Write(data []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	n, err := w.writer.Write(data)
	if err == nil {
		err = w.controller.Flush()
		if errors.Is(err, http.ErrNotSupported) {
			err = nil
		}
	}
	return n, err
}

func ollamaRouteMethod(u *url.URL) string {
	if u == nil || u.IsAbs() || u.Host != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.Opaque != "" || u.Fragment != "" ||
		path.Clean(u.Path) != u.Path || strings.ContainsAny(u.Path, "\\\r\n") {
		return ""
	}
	switch u.Path {
	case "/", "/api/version", "/api/tags", "/api/ps", "/v1/models":
		return http.MethodGet
	case "/api/chat", "/api/generate", "/api/embed", "/api/embeddings", "/api/show", "/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/responses":
		return http.MethodPost
	default:
		if strings.HasPrefix(u.Path, "/v1/models/") && len(strings.TrimPrefix(u.Path, "/v1/models/")) > 0 && !strings.HasSuffix(u.Path, "/") {
			return http.MethodGet
		}
		return ""
	}
}

func ollamaHTTPError(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == harnessTaskRoute {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": code, "type": "gateway_error", "code": code}})
	} else {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
}

func (a *OllamaAPI) reserve(ctx context.Context, permit domain.GPUExecutorPermitRequest) error {
	body, err := json.Marshal(struct {
		Action string `json:"action"`
		domain.GPUExecutorPermitRequest
	}{"reserve", permit})
	if err != nil {
		return err
	}
	defer clear(body)
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, a.config.AdmissionURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+a.config.AdmissionKey)
	r.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(r)
	if err != nil {
		return errors.New("Ollama admission did not respond")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(data) > 4096 {
		return errors.New("invalid Ollama admission response")
	}
	var reply struct {
		Protocol int    `json:"protocol"`
		ID       string `json:"id"`
		State    string `json:"state"`
		Started  bool   `json:"started"`
		Error    string `json:"error"`
	}
	if json.Unmarshal(data, &reply) != nil || reply.Protocol != 1 {
		return errors.New("invalid Ollama admission response")
	}
	if response.StatusCode == http.StatusConflict && reply.Error == "executor_permit_unavailable" && reply.ID == "" && reply.State == "" && !reply.Started {
		return errOllamaAdmissionBusy
	}
	if response.StatusCode != http.StatusOK || reply.ID != permit.ID || reply.State != "reserved" || reply.Started || reply.Error != "" {
		return errors.New("Ollama admission was not confirmed")
	}
	return nil
}
