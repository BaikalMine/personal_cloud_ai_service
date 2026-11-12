package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type adminMinerState struct {
	ID                     int64 `json:"id"`
	Label, Tone, Reason    string
	CollectedAt, StartedAt time.Time
	Running                bool `json:"running"`
	CanStart               bool `json:"can_start"`
	CanStop                bool `json:"can_stop"`
	CanUpdate              bool `json:"can_update"`
	CanChange              bool `json:"can_change"`
	CanRestart             bool `json:"can_restart"`
	PIDs                   []int
}

type adminResourceView struct {
	At                                  time.Time
	Service, Label, Tone, Detail, Error string
	Dependency                          DependencyStatus
	GPU                                 []adminGPUWorkView
	GPUAt, ObservedAt                   time.Time
	ObservationLabel, ObservationDetail string
	Pauses                              []adminMiningPauseView
	ProfileVersion                      string
	Profiles                            map[int64]adminMinerState
	Mining                              *MiningOverview
	Analytics                           *domain.ServiceAnalytics
}

type adminMiningPauseView struct {
	Label string
	Count int
	Since time.Time
	Ready int
}

func newAdminResourceView(now time.Time, service string, dependency DependencyStatus, gpu []store.AdminGPUWorkSummary, resource domain.GPUResource, leases []domain.QuickGenerationMiningLease, mining *MiningOverview, fenced bool) adminResourceView {
	view := adminResourceView{At: now, Service: service, Dependency: dependency, GPU: adminGPUWorkViews(gpu), Mining: mining,
		Label: dependency.StateLabel, Tone: string(dependency.State), Detail: dependency.Detail, Profiles: map[int64]adminMinerState{},
		ObservedAt: resource.ObservedAt, ObservationLabel: "Нет подтверждённого наблюдения", ObservationDetail: resource.Message}
	if len(gpu) > 0 {
		view.GPUAt = gpu[0].SnapshotAt
	}
	if !resource.ObservedAt.IsZero() {
		if !resource.ValidUntil.After(now) {
			view.ObservationLabel = "Наблюдение устарело"
		} else {
			switch resource.Observation {
			case "idle":
				view.ObservationLabel = "Исполнители не сообщили об активной работе"
			case "busy":
				view.ObservationLabel = "Исполнители заняты или ожидают задачу"
			default:
				view.ObservationLabel = "Состояние не подтверждено"
			}
		}
	}
	for _, label := range []string{"Генерация", "Обучение LoRA", "Ассистенты и другие задачи"} {
		pause := adminMiningPauseView{Label: label}
		for _, lease := range leases {
			kind := "Ассистенты и другие задачи"
			if lease.LoraTrainingJobID > 0 {
				kind = "Обучение LoRA"
			} else if lease.GenerationJobID > 0 || lease.PromptID != "" {
				kind = "Генерация"
			}
			if kind != label {
				continue
			}
			pause.Count++
			if pause.Since.IsZero() || lease.CreatedAt.Before(pause.Since) {
				pause.Since = lease.CreatedAt
			}
			if lease.ResumeReady {
				pause.Ready++
			}
		}
		if pause.Count > 0 {
			view.Pauses = append(view.Pauses, pause)
		}
	}
	if mining == nil {
		return view
	}
	view.Dependency = mining.Agent
	view.Label, view.Tone, view.Detail = "Остановлен", "neutral", "Майнинг не выполняется."
	if !mining.Available {
		view.Label, view.Tone, view.Detail = "Состояние не подтверждено", "stale", mining.Message
	} else if mining.Running {
		view.Label, view.Tone, view.Detail = "Работает", "online", "Майнинг использует вычислительные ресурсы."
	} else if len(leases) > 0 {
		view.Label, view.Tone, view.Detail = "Пауза для задач", "stale", "Запуск удерживается до подтверждённого завершения связанных задач."
	}
	busy := false
	for _, group := range gpu {
		busy = busy || group.Running+group.Reserved+group.Unconfirmed > 0
	}
	// Configuration identity excludes live state and never exposes paths as an API field.
	configuration := make([]any, 0, len(mining.Miners))
	allFresh := mining.Available
	for _, miner := range mining.Miners {
		configuration = append(configuration, []any{miner.ID, miner.Name, miner.ScriptPath, miner.ProcessName, miner.Enabled, miner.Default, miner.IconMIME})
		fresh := miner.State.Available && !miner.State.CollectedAt.IsZero() && now.Sub(miner.State.CollectedAt) <= 45*time.Second && !miner.State.CollectedAt.After(now.Add(5*time.Second))
		allFresh = allFresh && fresh
		state := adminMinerState{ID: miner.ID, Label: "Остановлен", Tone: "neutral", CollectedAt: miner.State.CollectedAt, StartedAt: miner.State.StartedAt, Running: miner.State.Running, PIDs: miner.State.PIDs,
			CanStop: fresh && miner.State.Running, CanChange: fresh && !miner.State.Running}
		switch {
		case !fresh:
			state.Label, state.Tone, state.Reason = "Не подтверждено", "stale", "Нет свежего ответа агента. Обновите состояние."
		case miner.State.Running:
			state.Label, state.Tone = "Работает", "online"
		case !mining.Available:
			state.Reason = "Состояние остальных профилей не подтверждено. Обновите страницу."
		case !miner.Enabled:
			state.Label, state.Reason = "Профиль отключён", "Включите профиль перед запуском."
		case mining.Active != nil && mining.Active.ID != miner.ID:
			state.Reason = "Сначала остановите работающий профиль."
		case miningUncontrolled(*mining):
			state.Reason = "Работает внешний майнер или несколько майнеров. Новый запуск заблокирован."
		case len(leases) > 0:
			state.Label, state.Tone, state.Reason = "Пауза", "stale", "GPU зарезервирован для связанных задач."
		case busy:
			state.Reason = "Есть выполняемая или неподтверждённая GPU-задача."
		default:
			state.CanStart = true
		}
		state.CanUpdate = fresh && mining.Available && !miningUncontrolled(*mining) && len(leases) == 0 && !busy && (mining.Active == nil || mining.Active.ID == miner.ID)
		state.CanRestart = state.CanUpdate && miner.Enabled && miner.State.Running && mining.Active != nil && mining.Active.ID == miner.ID && sameMiningScript(miner.ScriptPath, miner.State.ScriptPath)
		view.Profiles[miner.ID] = state
	}
	if !allFresh {
		view.Label, view.Tone, view.Detail = "Состояние не подтверждено", "stale", "Нет свежего ответа для всех профилей майнинга."
		for id, state := range view.Profiles {
			state.CanStart, state.CanUpdate = false, false
			if state.Reason == "" && !state.Running {
				state.Reason = "Состояние остальных профилей не подтверждено. Обновите страницу."
			}
			view.Profiles[id] = state
		}
	}
	data, _ := json.Marshal(configuration)
	view.ProfileVersion = fmt.Sprintf("%x", sha256.Sum256(data))
	return view
}

func (a *App) loadAdminResource(ctx context.Context, service string, script bool) (adminResourceView, error) {
	groups, err := a.store.AdminGPUOverview(ctx)
	if err != nil {
		return adminResourceView{}, err
	}
	resource, err := a.store.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return adminResourceView{}, err
	}
	leases, err := a.store.ListQuickGenerationMiningLeases(ctx)
	if err != nil {
		return adminResourceView{}, err
	}
	var mining *MiningOverview
	if service == "mining" {
		value := a.miningOverview(ctx, true, script)
		if value.LoadError {
			return adminResourceView{}, errors.New("mining configuration unavailable")
		}
		mining = &value
	}
	view := newAdminResourceView(time.Now().UTC(), service, a.dependencyStatus(service), groups, resource, leases, mining, a.cfg.MiningFencedCommands)
	if service != "mining" {
		analytics, err := a.store.ServiceAnalytics(ctx, service, 30)
		if err != nil {
			return view, err
		}
		analytics.DisplayName = serviceDisplayName(service)
		view.Analytics = &analytics
	}
	return view, ctx.Err()
}

func (a *App) handleAdminResourceOverview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if user := a.currentUser(r); user == nil || user.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "access_denied"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	service := r.URL.Query().Get("service")
	if service != "comfyui" && service != "mining" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_service"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	view, err := a.loadAdminResource(ctx, service, false)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "overview_unavailable"})
		return
	}
	var fragment bytes.Buffer
	if err = a.tpl.ExecuteTemplate(&fragment, "admin_resource_snapshot", view); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "render_failed"})
		return
	}
	states := make(map[int64]map[string]any, len(view.Profiles))
	for id, state := range view.Profiles {
		var html bytes.Buffer
		if err = a.tpl.ExecuteTemplate(&html, "admin_miner_state", state); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "render_failed"})
			return
		}
		states[id] = map[string]any{"html": html.String(), "running": state.Running, "start": state.CanStart, "stop": state.CanStop, "update": state.CanUpdate, "change": state.CanChange, "restart": state.CanRestart}
	}
	var gpu bytes.Buffer
	if view.Mining != nil {
		if err = a.tpl.ExecuteTemplate(&gpu, "admin_resource_gpu", view); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "render_failed"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshot_html": fragment.String(), "gpu_html": gpu.String(), "profile_version": view.ProfileVersion, "profiles": states})
}
