package gateway

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/store"
)

const (
	maxMiningFormBytes = 1 << 20
	maxMinerIconBytes  = 256 << 10
)

var (
	windowsScriptPattern = regexp.MustCompile(`(?i)^[a-z]:\\.+\.(bat|cmd|ps1|exe)$`)
	minerProcessPattern  = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9_.-]{0,126}\.exe$`)
	allowedIconMIMEs     = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/x-icon": true, "image/vnd.microsoft.icon": true}
)

func (a *App) miningOverview(ctx context.Context, includeDisabled, includeScript bool) MiningOverview {
	miners, err := a.store.ListMiners(ctx, includeDisabled)
	if err != nil {
		return MiningOverview{Message: "Не удалось загрузить настройки майнинга."}
	}
	overview := MiningOverview{Available: a.mining != nil && a.mining.Configured(), Miners: make([]MinerView, 0, len(miners))}
	if len(miners) == 0 {
		overview.Message = "Профиль майнинга ещё не настроен."
		return overview
	}
	if !overview.Available {
		overview.Message = "Windows-agent управления майнингом не настроен."
	}
	for _, miner := range miners {
		view := MinerView{Miner: miner}
		if overview.Available {
			state, stateErr := a.mining.State(ctx, miner.ProcessName)
			view.State = state
			if stateErr != nil {
				overview.Available = false
				overview.Message = state.Message
			}
		}
		overview.Miners = append(overview.Miners, view)
		current := &overview.Miners[len(overview.Miners)-1]
		if miner.Default {
			overview.Default = current
		}
		if current.State.Running && overview.Active == nil {
			overview.Active = current
			overview.Running = true
		}
	}
	if overview.Available && overview.Message == "" {
		if overview.Running {
			overview.Message = "Майнинг работает и использует вычислительные ресурсы."
		} else {
			overview.Message = "Майнинг остановлен, ресурсы доступны нейросетям."
		}
	}
	selected := overview.Active
	if selected == nil {
		selected = overview.Default
	}
	if includeScript && overview.Available && selected != nil {
		script, scriptErr := a.mining.Script(ctx, selected.ScriptPath)
		if scriptErr != nil && script.Message == "" {
			script.Message = "Не удалось прочитать содержимое скрипта."
		}
		overview.Script = script
	}
	return overview
}

func (a *App) handleMiningToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMiningFormBytes)
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}
	user := a.currentUser(r)
	if user == nil || !user.CanAccessMining() {
		http.Error(w, "доступ к майнингу не разрешён", http.StatusForbidden)
		return
	}
	overview := a.miningOverview(r.Context(), true, false)
	if !overview.Available {
		http.Redirect(w, r, "/app?mining=unavailable", http.StatusSeeOther)
		return
	}
	var target *MinerView
	var action string
	var state mining.State
	var err error
	if overview.Active != nil {
		target = overview.Active
		action = "mining_stopped"
		state, err = a.mining.Stop(r.Context(), mining.Request{ScriptPath: target.ScriptPath, ProcessName: target.ProcessName})
	} else if overview.Default != nil {
		if _, leaseErr := a.store.ActiveQuickGenerationMiningLease(r.Context()); leaseErr == nil {
			http.Redirect(w, r, "/app?mining=priority_busy", http.StatusSeeOther)
			return
		}
		target = overview.Default
		action = "mining_started"
		state, err = a.mining.Start(r.Context(), mining.Request{ScriptPath: target.ScriptPath, ProcessName: target.ProcessName})
	} else {
		err = errors.New("default miner is not configured")
	}
	if err != nil || target == nil {
		http.Redirect(w, r, "/app?mining=error", http.StatusSeeOther)
		return
	}
	a.audit(r.Context(), &user.ID, action, "miner", &target.ID, a.clientIP(r), r.UserAgent(), map[string]any{
		"name": target.Name, "process_name": target.ProcessName, "running": state.Running,
	})
	status := "stopped"
	if state.Running {
		status = "started"
	}
	http.Redirect(w, r, "/app?mining="+status, http.StatusSeeOther)
}

func (a *App) handleAdminMining(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		a.renderAdminMining(w, r, "", r.URL.Query().Get("status"))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMiningFormBytes)
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}
	action := strings.TrimSpace(r.Form.Get("action"))
	if action == "create" {
		a.handleCreateMiner(w, r)
		return
	}
	id, err := strconv.ParseInt(r.Form.Get("id"), 10, 64)
	if err != nil || id <= 0 {
		a.renderAdminMining(w, r, "Некорректный профиль майнинга.", "")
		return
	}
	miner, err := a.store.MinerByID(r.Context(), id)
	if err != nil {
		a.renderAdminMining(w, r, "Профиль майнинга не найден.", "")
		return
	}
	user := a.currentUser(r)
	status := "updated"
	switch action {
	case "update":
		archiveURL := strings.TrimSpace(r.FormValue("archive_url"))
		archiveSHA256 := strings.ToLower(strings.TrimSpace(r.FormValue("archive_sha256")))
		if len(archiveURL) == 0 || len(archiveURL) > 2048 {
			err = errors.New("укажите HTTPS-ссылку на ZIP-архив обновления")
			break
		}
		if !validArchiveSHA256(archiveSHA256) {
			err = errors.New("укажите SHA-256 ZIP-архива: 64 шестнадцатеричных символа")
			break
		}
		if a.mining == nil || !a.mining.Configured() {
			err = errors.New("агент управления майнингом недоступен")
			break
		}
		updateResult, updateErr := a.mining.Update(r.Context(), mining.UpdateRequest{
			ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName, MinerName: miner.Name, ArchiveURL: archiveURL, ArchiveSHA256: archiveSHA256,
		})
		err = updateErr
		if err != nil && updateResult.Message != "" {
			err = errors.New(updateResult.Message)
		}
		if err == nil {
			a.audit(r.Context(), &user.ID, "miner_updated", "miner", &id, a.clientIP(r), r.UserAgent(), map[string]any{
				"name": miner.Name, "process_name": miner.ProcessName, "archive_url": archiveURL, "archive_sha256": archiveSHA256,
			})
			status = "miner-updated"
		}
	case "start":
		if _, leaseErr := a.store.ActiveQuickGenerationMiningLease(r.Context()); leaseErr == nil {
			a.renderAdminMining(w, r, "Майнинг зарезервирован для приоритетной быстрой генерации. Дождитесь её завершения.", "")
			return
		}
		overview := a.miningOverview(r.Context(), true, false)
		if !overview.Available {
			a.renderAdminMining(w, r, overview.Message, "")
			return
		}
		if overview.Active != nil && overview.Active.ID != miner.ID {
			a.renderAdminMining(w, r, "Сначала остановите активный профиль "+overview.Active.Name+".", "")
			return
		}
		if _, err = a.mining.Start(r.Context(), mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}); err == nil {
			a.audit(r.Context(), &user.ID, "mining_started", "miner", &id, a.clientIP(r), r.UserAgent(), map[string]any{"name": miner.Name})
			status = "started"
		}
	case "stop":
		if _, err = a.mining.Stop(r.Context(), mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}); err == nil {
			a.audit(r.Context(), &user.ID, "mining_stopped", "miner", &id, a.clientIP(r), r.UserAgent(), map[string]any{"name": miner.Name})
			status = "stopped"
		}
	case "default":
		_, err = a.store.SetDefaultMiner(r.Context(), id)
	case "enable":
		_, err = a.store.SetMinerEnabled(r.Context(), id, true)
	case "disable":
		if state, stateErr := a.mining.State(r.Context(), miner.ProcessName); stateErr == nil && state.Running {
			err = errors.New("нельзя отключить работающий профиль")
		} else {
			_, err = a.store.SetMinerEnabled(r.Context(), id, false)
		}
	case "delete":
		if state, stateErr := a.mining.State(r.Context(), miner.ProcessName); stateErr == nil && state.Running {
			err = errors.New("нельзя удалить работающий профиль")
		} else {
			_, err = a.store.DeleteMiner(r.Context(), id)
		}
	default:
		err = errors.New("неизвестное действие")
	}
	if err != nil {
		a.renderAdminMining(w, r, miningActionError(err), "")
		return
	}
	if action != "start" && action != "stop" {
		a.audit(r.Context(), &user.ID, "miner_profile_"+action, "miner", &id, a.clientIP(r), r.UserAgent(), map[string]any{"name": miner.Name})
	}
	http.Redirect(w, r, "/admin/mining?status="+status, http.StatusSeeOther)
}

func validArchiveSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (a *App) handleCreateMiner(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxMiningFormBytes); err != nil {
		a.renderAdminMining(w, r, "Форма слишком большая или повреждена.", "")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	scriptPath := strings.TrimSpace(r.FormValue("script_path"))
	processName := strings.TrimSpace(r.FormValue("process_name"))
	if len(name) == 0 || len(name) > 80 || len(scriptPath) > 1024 || !windowsScriptPattern.MatchString(scriptPath) || !minerProcessPattern.MatchString(processName) {
		a.renderAdminMining(w, r, "Проверьте название, путь к скрипту и имя процесса .exe.", "")
		return
	}
	iconMIME, iconData, err := readMinerIcon(r, "icon")
	if err != nil {
		a.renderAdminMining(w, r, err.Error(), "")
		return
	}
	user := a.currentUser(r)
	id, err := a.store.CreateMiner(r.Context(), store.CreateMinerParams{
		Name: name, ScriptPath: scriptPath, ProcessName: processName, IconMIME: iconMIME, IconData: iconData,
		Enabled: true, Default: r.FormValue("is_default") == "on", CreatedByUserID: user.ID,
	})
	if err != nil {
		a.renderAdminMining(w, r, "Не удалось добавить профиль. Проверьте, не используется ли уже этот путь.", "")
		return
	}
	a.audit(r.Context(), &user.ID, "miner_profile_created", "miner", &id, a.clientIP(r), r.UserAgent(), map[string]any{
		"name": name, "script_path": scriptPath, "process_name": processName,
	})
	http.Redirect(w, r, "/admin/mining?status=created", http.StatusSeeOther)
}

func (a *App) renderAdminMining(w http.ResponseWriter, r *http.Request, errorMessage, status string) {
	a.render(w, r, "admin_mining", map[string]any{
		"Title": "Управление майнингом", "Mining": a.miningOverview(r.Context(), true, true), "Error": errorMessage, "Status": status,
	})
}

func (a *App) handleMinerIcon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	if user == nil || !user.CanAccessMining() {
		http.Error(w, "доступ к майнингу не разрешён", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/mining/icon/"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	icon, err := a.store.MinerIcon(r.Context(), id)
	if err != nil || len(icon.Data) == 0 || !allowedIconMIMEs[icon.MIME] {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", icon.MIME)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Length", strconv.Itoa(len(icon.Data)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(icon.Data)
	}
}

func readMinerIcon(r *http.Request, field string) (string, []byte, error) {
	file, _, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, errors.New("не удалось прочитать иконку")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMinerIconBytes+1))
	if err != nil || len(data) > maxMinerIconBytes {
		return "", nil, errors.New("иконка должна быть не больше 256 КБ")
	}
	mimeType := http.DetectContentType(data)
	if !allowedIconMIMEs[mimeType] {
		return "", nil, errors.New("поддерживаются PNG, JPEG, WebP и ICO")
	}
	return mimeType, data, nil
}

func miningActionError(err error) string {
	if errors.Is(err, store.ErrDefaultMinerRequired) {
		return "Сначала назначьте другой профиль по умолчанию."
	}
	return fmt.Sprintf("Действие не выполнено: %v", err)
}
