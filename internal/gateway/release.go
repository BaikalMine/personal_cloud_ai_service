package gateway

import (
	"ai-access-gateway/internal/buildinfo"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type ReleasePeer struct {
	Name      string          `json:"name"`
	Component string          `json:"component"`
	Check     buildinfo.Check `json:"check"`
}

type ReleaseSnapshot struct {
	SchemaVersion int              `json:"schema_version"`
	CheckedAt     time.Time        `json:"checked_at"`
	Gateway       buildinfo.Report `json:"gateway"`
	Peers         []ReleasePeer    `json:"peers"`
}

func (peer ReleasePeer) StatusLabel() string {
	switch peer.Check.Status {
	case "compatible":
		return "API совместим"
	case "incompatible":
		return "API несовместим"
	case "not_configured":
		return "Не настроен"
	case "invalid_configuration":
		return "Ошибка настройки"
	case "unauthorized":
		return "Доступ отклонён"
	case "unsupported":
		return "Версия не подтверждена"
	case "invalid":
		return "Некорректный ответ"
	default:
		return "Нет ответа"
	}
}

func (peer ReleasePeer) Guidance() string {
	switch peer.Check.Status {
	case "compatible":
		if peer.Check.Report != nil && peer.Check.Report.Build.ReleaseIdentified() {
			return ""
		}
		return "Сборка не привязана к выпуску. Сверьте установленный файл с манифестом."
	case "incompatible":
		return "Сверьте версии компонента и Gateway перед обновлением."
	case "not_configured", "invalid_configuration":
		return "Проверьте подключение компонента в конфигурации Gateway."
	case "unauthorized":
		return "Сверьте токен Gateway с настройками агента."
	case "unsupported":
		return "Компонент не сообщает версию. Проверьте установленный файл."
	case "invalid":
		return "Сверьте компонент с манифестом выпуска и повторите проверку."
	default:
		return "Проверьте процесс и соединение, затем обновите данные."
	}
}

func (a *App) releaseSnapshot(ctx context.Context) ReleaseSnapshot {
	snapshot := ReleaseSnapshot{SchemaVersion: 1, Gateway: buildinfo.NewReport(buildinfo.Gateway)}
	peers := []struct {
		name, component string
		base            *url.URL
		token           string
	}{
		{"Агент обновлений", buildinfo.UpdateAgent, a.cfg.UpdateAgentURL, a.cfg.UpdateAgentToken},
		{"Агент обучения", buildinfo.TrainingAgent, a.cfg.LoraTrainingAgentURL, a.cfg.LoraTrainingAgentToken},
		{"Агент майнинга", buildinfo.MiningAgent, a.cfg.MiningAgentURL, a.cfg.MiningAgentToken},
		{"Монитор системы", buildinfo.SystemMonitor, a.cfg.SystemMonitorAgentURL, a.cfg.SystemMonitorAgentToken},
		{"Обработчик моделей", buildinfo.InferenceWorker, a.cfg.InferenceWorkerURL, ""},
	}
	snapshot.Peers = make([]ReleasePeer, len(peers))
	var workers sync.WaitGroup
	for index, peer := range peers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			snapshot.Peers[index] = ReleasePeer{Name: peer.name, Component: peer.component, Check: buildinfo.Probe(ctx, peer.component, peer.base, peer.token)}
		}()
	}
	workers.Wait()
	snapshot.CheckedAt = time.Now().UTC()
	return snapshot
}

func (a *App) handleAdminRelease(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	actor := a.currentUser(r)
	if actor == nil || actor.Role != "admin" {
		http.Error(w, "доступ запрещён", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "параметры не поддерживаются", http.StatusBadRequest)
		return
	}
	snapshot := a.releaseSnapshot(r.Context())
	if r.URL.Path == "/admin/release/status" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="gateway-builds.json"`)
		_ = json.NewEncoder(w).Encode(snapshot)
		return
	}
	a.render(w, r, "admin_release", map[string]any{"Title": "Версии и совместимость", "Release": snapshot})
}
