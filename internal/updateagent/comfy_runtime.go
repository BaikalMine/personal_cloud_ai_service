package updateagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"ai-access-gateway/internal/updates"
)

type comfyProcess struct {
	PID     int
	Created uint64
}

func (p comfyProcess) version() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%d", p.PID, p.Created))))
}

type comfyRuntimeBackend interface {
	Inspect(context.Context, ComfyTarget) ([]comfyProcess, error)
	Start(ComfyTarget, string, func(comfyProcess, uint32)) error
	Stop(context.Context, ComfyTarget, comfyProcess) error
	Watch(comfyProcess, func(uint32)) error
}
type comfyRuntimeMonitor struct {
	sync.Mutex
	watched   map[string]bool
	requested map[string]bool
	recorded  map[string]bool
}

func (c *ControllerImpl) runtimeDirectory() string {
	return filepath.Join(c.config.ComfyUI.WorkingDirectory, "user", "ai-gateway-runtime")
}
func (c *ControllerImpl) runtimeReceipt(id string) (updates.ComfyRuntimeReceipt, error) {
	var result updates.ComfyRuntimeReceipt
	file, err := os.Open(filepath.Join(c.runtimeDirectory(), id+".json"))
	if err != nil {
		return result, err
	}
	defer file.Close()
	err = json.NewDecoder(io.LimitReader(file, 4096)).Decode(&result)
	return result, err
}
func (c *ControllerImpl) saveRuntimeJSON(name string, value any) error {
	dir := c.runtimeDirectory()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".runtime-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
func (c *ControllerImpl) watchRuntime(process comfyProcess) {
	m := &c.runtimeMonitor
	m.Lock()
	if m.watched == nil {
		m.watched = map[string]bool{}
	}
	if m.requested == nil {
		m.requested = map[string]bool{}
	}
	if m.recorded == nil {
		m.recorded = map[string]bool{}
	}
	version := process.version()
	if m.watched[version] {
		m.Unlock()
		return
	}
	m.watched[version] = true
	m.Unlock()
	err := c.runtimeBackend.Watch(process, func(code uint32) { c.recordRuntimeExit(process, code) })
	if err != nil {
		m.Lock()
		delete(m.watched, version)
		m.Unlock()
	}
}
func (c *ControllerImpl) recordRuntimeExit(process comfyProcess, code uint32) {
	m := &c.runtimeMonitor
	m.Lock()
	defer m.Unlock()
	if m.recorded == nil {
		m.recorded = map[string]bool{}
	}
	version := process.version()
	if m.recorded[version] {
		return
	}
	m.recorded[version] = true
	requested := m.requested[version]
	delete(m.requested, version)
	delete(m.watched, version)
	_ = c.saveRuntimeJSON("last-exit.json", updates.ComfyRuntimeExit{At: time.Now().UTC(), Code: code, Requested: requested})
}
func (c *ControllerImpl) ComfyRuntimeStatus(ctx context.Context, id string) (updates.ComfyRuntimeStatus, error) {
	result := updates.ComfyRuntimeStatus{Version: "stopped", State: "stopped"}
	processes, err := c.runtimeBackend.Inspect(ctx, c.config.ComfyUI)
	if err != nil {
		return result, err
	}
	result.Available = true
	if len(processes) > 1 {
		result.State, result.Version = "ambiguous", ""
		return result, nil
	}
	result.Ready = c.runtimeReady(ctx)
	if len(processes) == 1 {
		p := processes[0]
		result.Running, result.PID, result.Version = true, p.PID, p.version()
		result.State = "starting_or_unresponsive"
		if result.Ready {
			result.State = "ready"
		}
		c.watchRuntime(p)
	} else if result.Ready {
		result.State, result.Version = "foreign_listener", ""
	}
	if file, err := os.Open(filepath.Join(c.runtimeDirectory(), "last-exit.json")); err == nil {
		defer file.Close()
		_ = json.NewDecoder(io.LimitReader(file, 4096)).Decode(&result.LastExit)
	}
	if id != "" {
		receipt, err := c.runtimeReceipt(id)
		if err != nil {
			return result, err
		}
		result.Receipt = &receipt
	}
	return result, nil
}
func (c *ControllerImpl) runtimeReady(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.ComfyUI.HealthURL+"system_stats", nil)
	if err != nil {
		return false
	}
	response, err := c.client.Do(r)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusOK
}
func (c *ControllerImpl) ComfyRuntimeCommand(ctx context.Context, request updates.ComfyRuntimeRequest) (updates.ComfyRuntimeStatus, error) {
	if !request.Valid() || !request.ExpiresAt.After(time.Now()) || request.ExpiresAt.After(time.Now().Add(25*time.Second)) {
		return updates.ComfyRuntimeStatus{}, errors.New("invalid runtime request")
	}
	if !c.mu.TryLock() {
		return updates.ComfyRuntimeStatus{}, ErrComfyStorageBusy
	}
	defer c.mu.Unlock()
	if previous, err := c.runtimeReceipt(request.ID); err == nil {
		if previous.Action != request.Action {
			return updates.ComfyRuntimeStatus{}, errors.New("runtime request ID conflict")
		}
		return c.ComfyRuntimeStatus(ctx, request.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return updates.ComfyRuntimeStatus{}, err
	}
	status, err := c.ComfyRuntimeStatus(ctx, "")
	if err != nil {
		return status, err
	}
	if status.Version != request.Version || status.Version == "" {
		return status, errors.New("ComfyUI process identity changed")
	}
	if status.Running {
		busy, queueErr := c.comfyBusy(ctx)
		if busy || queueErr != nil && !request.Force {
			return status, ErrComfyStorageBusy
		}
	}
	if c.storageRuntimeBusy(ctx, status.Ready) {
		return status, ErrComfyStorageBusy
	}
	if err := os.MkdirAll(c.runtimeDirectory(), 0700); err != nil {
		return status, err
	}
	receipt := updates.ComfyRuntimeReceipt{ID: request.ID, Action: request.Action, State: "unconfirmed", Code: "command_accepted"}
	// A receipt is written before mutation. Unknown previous commands are never replayed.
	file, err := os.OpenFile(filepath.Join(c.runtimeDirectory(), request.ID+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return status, err
	}
	err = json.NewEncoder(file).Encode(receipt)
	if err == nil {
		err = file.Sync()
	}
	file.Close()
	if err != nil {
		return status, err
	}
	err = c.runRuntimeCommand(ctx, request, status)
	receipt.State, receipt.Code = "completed", "command_completed"
	if err != nil {
		receipt.State, receipt.Code = "failed", "command_failed"
		log.Printf("ComfyUI runtime %s failed: %v", request.Action, err)
	}
	receipt.FinishedAt = time.Now().UTC()
	if saveErr := c.saveRuntimeJSON(request.ID+".json", receipt); saveErr != nil {
		return status, saveErr
	}
	return c.ComfyRuntimeStatus(ctx, request.ID)
}
func (c *ControllerImpl) storageRuntimeBusy(ctx context.Context, ready bool) bool {
	if c.config.ComfyUI.Storage == nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(c.config.ComfyUI.Storage.JournalDirectory, comfyStoragePendingFile)); !errors.Is(err, os.ErrNotExist) {
		return true
	}
	guard := newComfyStorageGuard(*c.config.ComfyUI.Storage)
	status, err := guard.status(ctx)
	// A stopped server cannot hold an in-process execution window. Its durable
	// cleanup operations are also checked by Gateway before runtime commands.
	return err == nil && status.Held || err != nil && ready
}
func (c *ControllerImpl) runRuntimeCommand(ctx context.Context, request updates.ComfyRuntimeRequest, status updates.ComfyRuntimeStatus) error {
	if status.Running && request.Action != "start" {
		if !request.ExpiresAt.After(time.Now()) {
			return context.DeadlineExceeded
		}
		processes, err := c.runtimeBackend.Inspect(ctx, c.config.ComfyUI)
		if err != nil || len(processes) != 1 || processes[0].version() != request.Version {
			return errors.New("process changed")
		}
		if !request.ExpiresAt.After(time.Now()) || ctx.Err() != nil {
			return context.DeadlineExceeded
		}
		busy, queueErr := c.comfyBusy(ctx)
		if busy || queueErr != nil && !request.Force {
			return ErrComfyStorageBusy
		}
		if !request.ExpiresAt.After(time.Now()) || ctx.Err() != nil {
			return context.DeadlineExceeded
		}
		c.runtimeMonitor.Lock()
		c.runtimeMonitor.requested[request.Version] = true
		c.runtimeMonitor.Unlock()
		if err := c.runtimeBackend.Stop(ctx, c.config.ComfyUI, processes[0]); err != nil {
			c.runtimeMonitor.Lock()
			delete(c.runtimeMonitor.requested, request.Version)
			c.runtimeMonitor.Unlock()
			return err
		}
	}
	if request.Action != "stop" && !status.Running || request.Action == "restart" {
		if !request.ExpiresAt.After(time.Now()) {
			return context.DeadlineExceeded
		}
		processes, err := c.runtimeBackend.Inspect(ctx, c.config.ComfyUI)
		if err != nil || len(processes) != 0 || c.runtimeReady(ctx) {
			return errors.New("another ComfyUI instance is present")
		}
		if !request.ExpiresAt.After(time.Now()) || ctx.Err() != nil {
			return context.DeadlineExceeded
		}
		return c.runtimeBackend.Start(c.config.ComfyUI, c.runtimeDirectory(), c.recordRuntimeExit)
	}
	return nil
}

func (s *Server) handleComfyRuntime(w http.ResponseWriter, r *http.Request) {
	c, ok := s.controller.(*ControllerImpl)
	if !ok {
		http.Error(w, "runtime unavailable", 503)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	var result updates.ComfyRuntimeStatus
	var err error
	if r.Method == http.MethodGet {
		values, queryErr := url.ParseQuery(r.URL.RawQuery)
		id := values.Get("id")
		if queryErr != nil || len(values) > 1 || len(values["id"]) > 1 || len(values) == 1 && id == "" || id != "" && !updates.ComfyStorageHex(id, 16) {
			http.Error(w, "invalid request", 400)
			return
		}
		result, err = c.ComfyRuntimeStatus(ctx, id)
	} else if r.Method == http.MethodPost && r.URL.RawQuery == "" {
		var request updates.ComfyRuntimeRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || !request.Valid() {
			http.Error(w, "invalid request", 400)
			return
		}
		ctx2, cancel2 := context.WithDeadline(ctx, request.ExpiresAt)
		defer cancel2()
		result, err = c.ComfyRuntimeCommand(ctx2, request)
	} else {
		methodNotAllowed(w, "GET, POST")
		return
	}
	code := http.StatusOK
	if err != nil {
		code = http.StatusConflict
		result.Error = "command_rejected"
		if errors.Is(err, ErrComfyStorageBusy) {
			result.Error = "busy"
		}
		if errors.Is(err, os.ErrNotExist) {
			code = http.StatusNotFound
			result.Error = "receipt_not_found"
		}
	}
	writeJSON(w, code, result)
}

// Diagnostic observation never restarts ComfyUI or executes miner commands.
func (c *ControllerImpl) ObserveComfyRuntime(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		_, _ = c.ComfyRuntimeStatus(ctx, "")
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
