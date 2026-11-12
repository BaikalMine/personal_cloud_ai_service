package gateway

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	archiveSHA256Pattern = regexp.MustCompile(`[0-9a-f]{64}`)
	allowedIconMIMEs     = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/x-icon": true, "image/vnd.microsoft.icon": true}
)

func (a *App) miningOverview(ctx context.Context, includeDisabled, includeScript bool) MiningOverview {
	miners, err := a.store.ListMiners(ctx, includeDisabled)
	if err != nil {
		return MiningOverview{LoadError: true, Message: "Не удалось загрузить настройки майнинга."}
	}
	overview := MiningOverview{Available: a.mining != nil && a.mining.Configured(), Miners: make([]MinerView, 0, len(miners))}
	overview.Agent = a.dependencyStatus(dependencyMiningAgent)
	var inventory mining.Inventory
	inventoryKnown := false
	if overview.Available {
		started := time.Now()
		var inventoryErr error
		inventory, inventoryErr = a.mining.Inventory(ctx)
		if inventoryErr == nil {
			inventoryKnown = true
			a.dependencyMonitor().success(dependencyMiningAgent, "Каталог работающих майнеров получен.", &inventory.CollectedAt, time.Since(started))
		} else if !errors.Is(inventoryErr, mining.ErrInventoryUnsupported) {
			overview.Available = false
			overview.Message = "Не удалось проверить работающие майнеры. Команды запуска недоступны."
			a.dependencyMonitor().failure(dependencyMiningAgent, overview.Message, false, time.Since(started))
		}
	}
	if len(miners) == 0 && !inventoryKnown {
		if overview.Message == "" {
			overview.Message = "Профиль майнинга ещё не настроен. Для обнаружения внешнего запуска нужен обновлённый агент."
		}
		overview.Agent = a.dependencyStatus(dependencyMiningAgent)
		return overview
	}
	if !overview.Available && overview.Message == "" {
		overview.Message = "Windows-agent управления майнингом не настроен."
	}
	for _, miner := range miners {
		view := MinerView{Miner: miner}
		if overview.Available && !inventoryKnown {
			started := time.Now()
			state, stateErr := a.mining.State(ctx, miner.ProcessName)
			view.State = state
			if stateErr != nil {
				a.dependencyMonitor().failure(dependencyMiningAgent, dependencyCallError(state.Message, stateErr), false, time.Since(started))
				overview.Available = false
				overview.Message = dependencyCallError(state.Message, stateErr)
			} else {
				a.dependencyMonitor().success(dependencyMiningAgent, "Состояние майнера получено.", &state.CollectedAt, time.Since(started))
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
	normalizeMiningProfiles(&overview)
	if inventoryKnown {
		applyMiningInventory(&overview, inventory)
	}
	if overview.Available && overview.Message == "" {
		if miningUncontrolled(overview) {
			overview.Message = "Работает внешний майнер или несколько майнеров. Добавьте точный BAT в профиль управления."
		} else if overview.Running {
			overview.Message = "Майнинг работает и использует вычислительные ресурсы."
		} else {
			overview.Message = "Майнинг остановлен."
		}
	}
	selected := overview.Active
	if selected == nil && !overview.Running {
		selected = overview.Default
	}
	if includeScript && overview.Available && selected != nil {
		script, scriptErr := a.mining.Script(ctx, selected.ScriptPath)
		if scriptErr != nil && script.Message == "" {
			script.Message = "Не удалось прочитать содержимое скрипта."
		}
		overview.Script = script
	}
	overview.Agent = a.dependencyStatus(dependencyMiningAgent)
	if !overview.Available && overview.Agent.State == DependencyOnline {
		overview.Agent.State = DependencyStale
		overview.Agent.StateLabel = "Проверяем связь"
		overview.Agent.Detail = overview.Message
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
		state, err = a.stopMiningManually(r.Context(), target.Miner)
	} else if miningUncontrolled(overview) {
		http.Redirect(w, r, "/app?mining=external", http.StatusSeeOther)
		return
	} else if overview.Default != nil {
		if _, leaseErr := a.store.ActiveQuickGenerationMiningLease(r.Context()); leaseErr == nil {
			http.Redirect(w, r, "/app?mining=priority_busy", http.StatusSeeOther)
			return
		}
		target = overview.Default
		action = "mining_started"
		var result mining.CommandResult
		result, err = a.manualMiningCommand(r.Context(), "start", mining.Request{ScriptPath: target.ScriptPath, ProcessName: target.ProcessName}, nil)
		state = result.State
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
	case "restart":
		overview := a.miningOverview(r.Context(), true, false)
		if !overview.Available || overview.Active == nil || overview.Active.ID != miner.ID || !sameMiningScript(miner.ScriptPath, overview.Active.State.ScriptPath) || !miner.Enabled {
			err = errors.New("точный работающий BAT не подтверждён; перезапуск не выполнялся")
			break
		}
		_, err = a.manualMiningCommand(r.Context(), "restart", mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}, nil)
		if err == nil {
			status = "restarted"
		}
	case "update":
		overview := a.miningOverview(r.Context(), true, false)
		if !overview.Available || (overview.Active != nil && overview.Active.ID != miner.ID) || (overview.Running && (overview.Active == nil || !sameMiningScript(miner.ScriptPath, overview.Active.State.ScriptPath))) {
			err = errors.New("состояние профиля не подтверждено или работает другой профиль; обновление не запускалось")
			break
		}
		archiveURL := strings.TrimSpace(r.FormValue("archive_url"))
		archiveSHA256, sha256OK := normalizeArchiveSHA256(r.FormValue("archive_sha256"))
		if len(archiveURL) == 0 || len(archiveURL) > 2048 {
			err = errors.New("укажите HTTPS-ссылку на ZIP-архив обновления")
			break
		}
		if !sha256OK {
			err = errors.New("укажите полный SHA-256 ZIP-архива или вставьте строку с контрольной суммой")
			break
		}
		if a.mining == nil || !a.mining.Configured() {
			err = errors.New("агент управления майнингом недоступен")
			break
		}
		updateRequest := mining.UpdateRequest{
			ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName, MinerName: miner.Name, ArchiveURL: archiveURL, ArchiveSHA256: archiveSHA256,
		}
		commandResult, updateErr := a.manualMiningCommand(r.Context(), "update", mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}, &updateRequest)
		updateResult := mining.UpdateResult{}
		if commandResult.Update != nil {
			updateResult = *commandResult.Update
		}
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
		if !miner.Enabled {
			a.renderAdminMining(w, r, "Включите профиль перед запуском.", "")
			return
		}
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
		if overview.Running && (overview.Active == nil || !sameMiningScript(miner.ScriptPath, overview.Active.State.ScriptPath)) {
			a.renderAdminMining(w, r, "Майнер уже работает, но выбранный BAT не подтверждён. Новый профиль не запускался.", "")
			return
		}
		if _, err = a.manualMiningCommand(r.Context(), "start", mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}, nil); err == nil {
			a.audit(r.Context(), &user.ID, "mining_started", "miner", &id, a.clientIP(r), r.UserAgent(), map[string]any{"name": miner.Name})
			status = "started"
		}
	case "stop":
		if _, err = a.stopMiningManually(r.Context(), miner); err == nil {
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
			err = a.changeStoppedMiner(r.Context(), mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}, func() error {
				_, changeErr := a.store.SetMinerEnabled(r.Context(), id, false)
				return changeErr
			})
		}
	case "delete":
		if state, stateErr := a.mining.State(r.Context(), miner.ProcessName); stateErr == nil && state.Running {
			err = errors.New("нельзя удалить работающий профиль")
		} else {
			err = a.changeStoppedMiner(r.Context(), mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}, func() error {
				_, changeErr := a.store.DeleteMiner(r.Context(), id)
				return changeErr
			})
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

func normalizeArchiveSHA256(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 {
		return "", false
	}

	lower := strings.ToLower(value)
	if validArchiveSHA256(lower) {
		return lower, true
	}

	// CertUtil may group a checksum with spaces. Only compact an input that
	// consists entirely of hexadecimal characters and whitespace.
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		default:
			return r
		}
	}, lower)
	if validArchiveSHA256(compact) {
		return compact, true
	}

	candidates := make(map[string]struct{})
	for _, match := range archiveSHA256Pattern.FindAllStringIndex(lower, -1) {
		if match[0] > 0 && isHexByte(lower[match[0]-1]) {
			continue
		}
		if match[1] < len(lower) && isHexByte(lower[match[1]]) {
			continue
		}
		candidates[lower[match[0]:match[1]]] = struct{}{}
	}
	if len(candidates) != 1 {
		return "", false
	}
	for candidate := range candidates {
		return candidate, true
	}
	return "", false
}

func isHexByte(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f'
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
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	view, err := a.loadAdminResource(ctx, "mining", true)
	if err != nil {
		http.Error(w, "Не удалось загрузить состояние майнинга. Обновите страницу.", http.StatusServiceUnavailable)
		return
	}
	catalog := view.Mining.Catalog
	var catalogErr error
	if !view.Mining.InventoryKnown {
		catalog, catalogErr = a.mining.Files(ctx)
	}
	if catalogErr != nil {
		if catalog.Message == "" {
			catalog.Message = catalogErr.Error()
		}
		catalog.Files = nil
	}
	favorites, favoritesErr := a.store.MiningScriptFavorites(ctx, a.currentUser(r).ID)
	if favoritesErr != nil {
		catalog.Message = "Не удалось загрузить избранное. Попробуйте обновить список."
	}
	browser := BuildMiningBrowser(catalog, favorites, r.URL.Query())
	form := r.Form
	if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Query().Get("pick") != "" {
		if file, ok := catalogScript(catalog, r.URL.Query().Get("pick")); ok {
			process := miningScriptProcess(catalog, file)
			for _, state := range view.Mining.External {
				if sameMiningScript(state.ScriptPath, file.Path) {
					process = state.ProcessName
					break
				}
			}
			form = url.Values{"name": {file.Name}, "script_path": {file.Path}, "process_name": {process}}
		}
	}
	a.render(w, r, "admin_mining", map[string]any{
		"Title": "Управление майнингом", "Mining": view.Mining, "Resource": view, "Error": errorMessage, "Status": status,
		"MiningForm":          form,
		"MiningFiles":         catalog,
		"MiningBrowser":       browser,
		"MiningAutoResume":    a.manualMiningResumeEnabled(),
		"MiningResumePending": a.manualMiningResumePending(*view.Mining),
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
