package gateway

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

const comfyGPUKeyHeader = "X-AI-Gateway-GPU-Key"
const comfyGPUWorkHeader = "X-AI-Gateway-GPU-Work"
const comfyGPULeaseHeader = "X-AI-Gateway-GPU-Lease"

func (a *App) handleComfyGPUPermit(w http.ResponseWriter, r *http.Request) {
	a.handleGPUExecutorPermit(w, r, domain.GPUExecutorComfyUI, a.cfg.ComfyGPUGuardRequired, a.cfg.ComfyGPUGuardToken)
}

func (a *App) handleOllamaGPUPermit(w http.ResponseWriter, r *http.Request) {
	a.handleGPUExecutorPermit(w, r, domain.GPUExecutorOllama, a.cfg.OllamaGPUGuardRequired, a.cfg.OllamaGPUGuardToken)
}

func (a *App) handleGPUExecutorPermit(w http.ResponseWriter, r *http.Request, executor string, required bool, token string) {
	a.handleGPUExecutorPermitWithMining(w, r, executor, required, token, nil)
}

func (a *App) handleGPUExecutorPermitWithMining(w http.ResponseWriter, r *http.Request, executor string, required bool, token string, operator *User) {
	w.Header().Set("Cache-Control", "no-store")
	if !required || len(token) < 32 ||
		subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Action string `json:"action"`
		Code   string `json:"code,omitempty"`
		domain.GPUExecutorPermitRequest
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(&input) != nil || decoder.Decode(&trailing) != io.EOF {
		http.Error(w, "invalid executor permit request", http.StatusBadRequest)
		return
	}
	if input.Executor != "" && input.Executor != executor {
		http.Error(w, "invalid executor scope", http.StatusBadRequest)
		return
	}
	input.Executor = executor
	if a.cfg.RestoreDrillMode || a.gpuCoordinator == nil || !a.cfg.MiningFencedCommands {
		http.Error(w, "executor admission is unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var permit domain.GPUExecutorPermit
	var err error
	started := false
	var miningLease *domain.QuickGenerationMiningLease
	switch input.Action {
	case "reserve":
		err = a.store.WithGPUAdmissionGuard(ctx, func() error {
			var err error
			permit, err = a.store.GPUExecutorPermit(ctx, input.GPUExecutorPermitRequest)
			if err == nil {
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if input.WorkID == "" {
				// Only the configured shared Harness operator may authorize an
				// automatic pause. Completion must remain allowed if it is disabled.
				if operator != nil {
					user, err := a.store.UserByID(ctx, operator.ID)
					if err != nil || user.Role != "admin" || user.Disabled || (user.AccountExpiresAt.Valid && !user.AccountExpiresAt.Time.After(time.Now())) {
						return errors.New("Harness mining operator is unavailable")
					}
					user.PauseMiningForQuickGeneration = true
					operator = &user
				}
				miningUser := operator
				if miningUser == nil {
					miningUser = &User{}
				}
				if err = a.fenceMiningBeforeGPU(ctx, domain.GPUWorkGeneration, miningUser); err != nil {
					return err
				}
				if err = a.gpuCoordinator.observe(ctx); err != nil {
					return err
				}
				if operator != nil && operator.PauseMiningForQuickGeneration {
					miningLease, _, err = a.reserveMiningForGPUWork(ctx, operator, 0, 0, nil, &input.GPUExecutorPermitRequest)
					if err != nil {
						return err
					}
					if miningLease != nil {
						if err = a.gpuCoordinator.observeExceptMiningLease(ctx, miningLease.ID); err != nil {
							return err
						}
					}
				}
			}
			permit, err = a.store.ReserveGPUExecutorPermit(ctx, input.GPUExecutorPermitRequest)
			return err
		})
		if err != nil && miningLease != nil {
			settle, stop := context.WithTimeout(context.Background(), 15*time.Second)
			a.releaseMiningPause(settle, miningLease.ID)
			stop()
		}
	case "start":
		started, err = a.store.StartGPUExecutorPermit(ctx, input.GPUExecutorPermitRequest)
		if err == nil && !started {
			err = store.ErrGPUWorkConflict
		}
	case "finish":
		err = a.store.FinishGPUExecutorPermit(ctx, input.GPUExecutorPermitRequest, input.Code)
	case "status":
		permit, err = a.store.GPUExecutorPermit(ctx, input.GPUExecutorPermitRequest)
	default:
		err = store.ErrGPUWorkInput
	}
	if err != nil {
		log.Printf("GPU executor admission refused: executor=%s action=%s reason=%v", executor, input.Action, err)
		status := http.StatusServiceUnavailable
		var wait *gpuAdmissionWait
		if errors.Is(err, store.ErrGPUExecutorBusy) || errors.As(err, &wait) {
			status = http.StatusConflict
			w.Header().Set("Retry-After", "3")
		}
		if errors.Is(err, store.ErrGPUWorkInput) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, store.ErrGPUWorkConflict) || errors.Is(err, sql.ErrNoRows) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"protocol": 1, "error": "executor_permit_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"protocol": 1, "id": input.ID, "state": permit.State, "started": started})
}

func (a *App) probeComfyGPUGuard(ctx context.Context) error {
	if a.cfg.ComfyUIUpstream == nil || len(a.cfg.ComfyGPUGuardToken) < 32 {
		return errors.New("ComfyUI GPU guard is not configured")
	}
	endpoint := *a.cfg.ComfyUIUpstream
	endpoint.Path = singleJoiningSlash(endpoint.Path, "/ai-gateway/gpu/status")
	endpoint.RawQuery, endpoint.Fragment = "", ""
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(probe, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	r.Header.Set(comfyGPUKeyHeader, a.cfg.ComfyGPUGuardToken)
	if a.cfg.ComfyUIUpstreamAuthHeader != "" {
		r.Header.Set("Authorization", a.cfg.ComfyUIUpstreamAuthHeader)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second, CheckRedirect: rejectUpstreamRedirect}).Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var state struct {
		Protocol int    `json:"protocol"`
		Enforced bool   `json:"enforced"`
		Instance string `json:"instance_id"`
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return errors.New("ComfyUI GPU guard status is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var tail any
	if response.StatusCode != http.StatusOK || decoder.Decode(&state) != nil || decoder.Decode(&tail) != io.EOF || state.Protocol != 1 || !state.Enforced {
		return errors.New("ComfyUI GPU guard is not enforcing admission")
	}
	instance, err := hex.DecodeString(state.Instance)
	if err != nil || len(instance) != 16 || hex.EncodeToString(instance) != state.Instance {
		return errors.New("ComfyUI GPU guard instance is invalid")
	}
	return nil
}

func stripComfyGPUHeaders(header http.Header) {
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "x-ai-gateway-gpu-") {
			delete(header, name)
		}
	}
}

func (a *App) authorizeManagedComfyGPURequest(ctx context.Context, r *http.Request, userID int64, jobID string) error {
	if !a.cfg.ComfyGPUGuardRequired {
		return nil
	}
	work, err := a.store.GPUWorkBySource(ctx, domain.GPUWorkGeneration, jobID)
	if err != nil {
		return err
	}
	if work.UserID == nil || *work.UserID != userID || work.LeaseToken == "" || work.State != domain.GPUWorkHeld || work.Phase != "dispatching" || work.CancellationRequested {
		return store.ErrGPUWorkConflict
	}
	r.Header.Set(comfyGPUKeyHeader, a.cfg.ComfyGPUGuardToken)
	r.Header.Set(comfyGPUWorkHeader, work.ID)
	r.Header.Set(comfyGPULeaseHeader, work.LeaseToken)
	return nil
}
