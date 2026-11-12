package inference

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/domain"
)

const harnessTaskRoute = "/harness/task"
const harnessSessionHeader = "X-AI-Harness-Session"
const harnessTaskTTL = 90 * time.Second

type retainedHTTPRuntime interface {
	StopRetained(context.Context) (bool, error)
}

type harnessTaskRef struct {
	session string
	expires time.Time
}

// All live Harness agents share one fenced executor. This allows delegation
// without resuming mining or deadlocking a child behind its parent's permit.
type harnessTaskPool struct {
	permit  domain.GPUExecutorPermitRequest
	refs    map[string]harnessTaskRef
	ctx     context.Context
	cancel  context.CancelFunc
	calls   sync.Mutex
	closing bool
	done    chan struct{}
}

func (a *OllamaAPI) harnessTaskEnabled() bool {
	u, err := url.Parse(a.config.AdmissionURL)
	_, capable := a.runtime.(retainedHTTPRuntime)
	return err == nil && u.Path == "/internal/gpu/harness/permit" && capable
}

func validHarnessIdentity(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) < 0
}

func (a *OllamaAPI) handleHarnessTask(w http.ResponseWriter, r *http.Request) {
	if !a.harnessTaskEnabled() || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		ollamaHTTPError(w, r, 404, "route_unavailable")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		ollamaHTTPError(w, r, 405, "method_not_allowed")
		return
	}
	if a.closed.Load() || !a.worker.running.Load() {
		ollamaHTTPError(w, r, 503, "executor_unavailable")
		return
	}
	var input struct {
		Action    string `json:"action"`
		TaskID    string `json:"taskId"`
		SessionID string `json:"sessionId"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var tail any
	if r.Header.Get("Content-Encoding") != "" || d.Decode(&input) != nil || d.Decode(&tail) != io.EOF ||
		!validHarnessIdentity(input.TaskID) || !validHarnessIdentity(input.SessionID) {
		ollamaHTTPError(w, r, 400, "invalid_task_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var err error
	switch input.Action {
	case "begin":
		err = a.beginHarnessTask(ctx, input.TaskID, input.SessionID)
	case "touch":
		a.tasksMu.Lock()
		p := a.taskPool
		if p == nil || p.closing || p.refs[input.TaskID].session != input.SessionID {
			err = errors.New("Harness task is no longer admitted")
		} else {
			p.refs[input.TaskID] = harnessTaskRef{input.SessionID, time.Now().Add(harnessTaskTTL)}
		}
		a.tasksMu.Unlock()
	case "end":
		a.tasksMu.Lock()
		p := a.taskPool
		last := false
		if p != nil && p.refs[input.TaskID].session == input.SessionID {
			delete(p.refs, input.TaskID)
			last = len(p.refs) == 0
		}
		a.tasksMu.Unlock()
		if last {
			err = a.closeHarnessPool(p, false)
		}
	default:
		ollamaHTTPError(w, r, 400, "invalid_task_action")
		return
	}
	if err != nil {
		if errors.Is(err, errOllamaAdmissionBusy) {
			a.replyBusy(w, r)
		} else {
			log.Printf("Harness task lifecycle failed: action=%s", input.Action)
			ollamaHTTPError(w, r, 503, "harness_task_unconfirmed")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"ok":true}`)
}

func (a *OllamaAPI) beginHarnessTask(ctx context.Context, task, session string) error {
	a.tasksMu.Lock()
	defer a.tasksMu.Unlock()
	if a.closed.Load() {
		return errors.New("executor closed")
	}
	if p := a.taskPool; p != nil {
		if p.closing {
			return errOllamaAdmissionBusy
		}
		if ref, exists := p.refs[task]; exists && ref.session != session {
			return errors.New("task identity conflict")
		}
		p.refs[task] = harnessTaskRef{session, time.Now().Add(harnessTaskTTL)}
		return nil
	}
	if !a.worker.runMu.TryLock() {
		return errOllamaAdmissionBusy
	}
	retained := false
	defer func() {
		if !retained {
			a.worker.runMu.Unlock()
		}
	}()
	if err := a.worker.prepareExecution(ctx); err != nil {
		return err
	}
	var identity [48]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return err
	}
	owner := a.worker.owned.executor
	permit := domain.GPUExecutorPermitRequest{Executor: domain.GPUExecutorOllama, ID: hex.EncodeToString(identity[:16]),
		Token: hex.EncodeToString(identity[16:]), InstanceID: a.instance, OwnerID: owner.ID, OwnerGeneration: owner.Generation, OwnerToken: owner.Token}
	if err := a.reserve(ctx, permit); err != nil {
		if !errors.Is(err, errOllamaAdmissionBusy) {
			a.worker.invalidateOwned()
		}
		return err
	}
	started, err := a.worker.store.StartGPUExecutorPermit(ctx, permit)
	if err != nil || !started {
		a.worker.invalidateOwned()
		return errors.New("Harness permit start unconfirmed")
	}
	if ctx.Err() != nil {
		settle, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := a.worker.store.FinishGPUExecutorPermit(settle, permit, "quiesced")
		cancel()
		if err != nil {
			a.worker.invalidateOwned()
		}
		return ctx.Err()
	}
	lifetime, cancel := context.WithCancel(context.Background())
	a.taskPool = &harnessTaskPool{permit: permit, refs: map[string]harnessTaskRef{task: {session, time.Now().Add(harnessTaskTTL)}},
		ctx: lifetime, cancel: cancel, done: make(chan struct{})}
	retained = true
	return nil
}

func (a *OllamaAPI) acquireHarnessCall(session string) (*harnessTaskPool, error) {
	a.tasksMu.Lock()
	defer a.tasksMu.Unlock()
	p := a.taskPool
	if p == nil || p.closing {
		return nil, errors.New("Harness task unavailable")
	}
	for _, ref := range p.refs {
		if ref.session == session && ref.expires.After(time.Now()) {
			if p.calls.TryLock() {
				return p, nil
			}
			return nil, errOllamaAdmissionBusy
		}
	}
	return nil, errors.New("Harness task unavailable")
}

func (a *OllamaAPI) closeHarnessPool(p *harnessTaskPool, force bool) error {
	if p == nil {
		return nil
	}
	a.tasksMu.Lock()
	if a.taskPool != p {
		a.tasksMu.Unlock()
		return nil
	}
	if !force && len(p.refs) != 0 {
		a.tasksMu.Unlock()
		return nil
	}
	if p.closing {
		a.tasksMu.Unlock()
		<-p.done
		return nil
	}
	p.closing = true
	p.cancel()
	a.tasksMu.Unlock()
	p.calls.Lock()
	defer p.calls.Unlock()
	stop, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	stopped, err := a.runtime.(retainedHTTPRuntime).StopRetained(stop)
	cancel()
	if stopped && err == nil {
		settle, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = a.worker.store.FinishGPUExecutorPermit(settle, p.permit, "quiesced")
		cancel()
	} else if err == nil {
		err = errors.New("Harness physical stop unconfirmed")
	}
	if err != nil {
		a.worker.invalidateOwned()
		log.Printf("Harness task stop unconfirmed; executor fenced")
	}
	a.tasksMu.Lock()
	a.taskPool = nil
	a.tasksMu.Unlock()
	a.worker.runMu.Unlock()
	close(p.done)
	return err
}

func (a *OllamaAPI) expireHarnessTasks(now time.Time) {
	a.tasksMu.Lock()
	p := a.taskPool
	last := false
	if p != nil && !p.closing {
		for id, ref := range p.refs {
			if !ref.expires.After(now) {
				delete(p.refs, id)
			}
		}
		last = len(p.refs) == 0
	}
	a.tasksMu.Unlock()
	if last {
		_ = a.closeHarnessPool(p, false)
	}
}

func (a *OllamaAPI) watchHarnessTasks() {
	defer close(a.tasksDone)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			a.expireHarnessTasks(now)
		case <-a.tasksStop:
			return
		}
	}
}
