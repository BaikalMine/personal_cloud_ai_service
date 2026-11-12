package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"ai-access-gateway/internal/mining"
)

type miningProcessOption struct {
	Name     string
	Selected bool
}

func (a *App) miningEditorTarget(ctx context.Context, r *http.Request) (Miner, []miningProcessOption, error) {
	values := r.URL.Query()
	if r.Method == http.MethodPost {
		values = r.Form
	}
	if values.Get("id") != "" && values.Get("id") != "0" {
		id, err := strconv.ParseInt(values.Get("id"), 10, 64)
		if err != nil || id <= 0 {
			return Miner{}, nil, errors.New("Некорректный профиль")
		}
		miner, err := a.store.MinerByID(ctx, id)
		return miner, nil, err
	}
	catalog, err := a.mining.Files(ctx)
	if err != nil {
		return Miner{}, nil, err
	}
	file, ok := catalogScript(catalog, values.Get("path"))
	if !ok {
		return Miner{}, nil, errors.New("BAT не найден в папке майнинга. Обновите список файлов")
	}
	process := values.Get("process")
	if process == "" {
		process = miningScriptProcess(catalog, file)
	}
	names := map[string]string{}
	local := false
	for _, item := range catalog.Files {
		if item.Kind == "executable" && item.Folder == file.Folder {
			local = true
		}
	}
	for _, item := range catalog.Files {
		if item.Kind == "executable" && (!local || item.Folder == file.Folder) && minerProcessPattern.MatchString(item.Name) {
			names[strings.ToLower(item.Name)] = item.Name
		}
	}
	if process != "" {
		if canonical, ok := names[strings.ToLower(process)]; ok {
			process = canonical
		} else {
			return Miner{}, nil, errors.New("Выбранный EXE не найден для этого BAT")
		}
	}
	if r.Method == http.MethodPost && process == "" {
		return Miner{}, nil, errors.New("Выберите процесс майнера перед сохранением")
	}
	options := make([]miningProcessOption, 0, len(names))
	for _, name := range names {
		options = append(options, miningProcessOption{Name: name, Selected: strings.EqualFold(name, process)})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Name < options[j].Name })
	return Miner{Name: file.Name, ScriptPath: file.Path, ProcessName: process}, options, nil
}

func miningEditorURL(miner Miner) string {
	query := url.Values{}
	if miner.ID > 0 {
		query.Set("id", strconv.FormatInt(miner.ID, 10))
	} else {
		query.Set("path", miner.ScriptPath)
		if miner.ProcessName != "" {
			query.Set("process", miner.ProcessName)
		}
	}
	return "/admin/mining/script?" + query.Encode()
}

func (a *App) handleAdminMiningScript(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil || user.Role != "admin" {
		http.Error(w, "Доступ запрещён", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMiningFormBytes)
	if r.Method == http.MethodPost && !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}
	miner, options, err := a.miningEditorTarget(r.Context(), r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !(MinerView{Miner: miner}).CanEditScript() {
		http.Error(w, "Редактировать можно только BAT и CMD", http.StatusBadRequest)
		return
	}
	var script mining.Script
	var message string
	if r.Method == http.MethodPost {
		script = mining.Script{Path: miner.ScriptPath, Content: r.Form.Get("content"), SHA256: r.Form.Get("sha256")}
		var saved mining.Script
		saved, err = a.mining.SaveScript(r.Context(), mining.SaveScriptRequest{Request: mining.Request{ScriptPath: miner.ScriptPath, ProcessName: miner.ProcessName}, Content: script.Content, ExpectedSHA256: script.SHA256})
		if err == nil {
			var target *int64
			if miner.ID > 0 {
				target = &miner.ID
			}
			a.audit(r.Context(), &user.ID, "miner_script_saved", "mining_script", target, a.clientIP(r), r.UserAgent(), map[string]any{"sha256": saved.SHA256})
			http.Redirect(w, r, miningEditorURL(miner)+"&saved=1", http.StatusSeeOther)
			return
		}
		message = saved.Message
		if message == "" {
			message = err.Error()
		}
	} else {
		script, err = a.mining.Script(r.Context(), miner.ScriptPath)
		if err != nil {
			message = script.Message
			if message == "" {
				message = err.Error()
			}
		}
	}
	a.render(w, r, "admin_mining_script", map[string]any{"Title": "Редактор BAT", "Miner": miner, "Script": script, "Processes": options, "EditorURL": miningEditorURL(miner), "Error": message, "Saved": r.URL.Query().Get("saved") == "1", "CanSave": script.SHA256 != ""})
}

func (a *App) handleAdminMiningFavorite(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil || user.Role != "admin" {
		http.Error(w, "Доступ запрещён", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if !a.validCSRF(r) {
		http.Error(w, "неверный защитный токен", http.StatusForbidden)
		return
	}
	path := r.Form.Get("path")
	enabled := r.Form.Get("enabled") == "true"
	if enabled {
		catalog, err := a.mining.Files(r.Context())
		if err != nil {
			http.Error(w, "Не удалось загрузить список BAT", http.StatusServiceUnavailable)
			return
		}
		file, ok := catalogScript(catalog, path)
		if !ok {
			http.Error(w, "BAT больше не доступен. Обновите список", http.StatusConflict)
			return
		}
		path = file.Path
	}
	if err := a.store.SetMiningScriptFavorite(r.Context(), user.ID, path, enabled); err != nil {
		http.Error(w, "Не удалось сохранить избранное", http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		favorites, err := a.store.MiningScriptFavorites(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "Не удалось получить избранное", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"favorite": enabled, "count": len(favorites)})
		return
	}
	query := url.Values{"folder": {r.Form.Get("folder")}}
	if r.Form.Get("favorites") == "1" {
		query.Set("favorites", "1")
	}
	http.Redirect(w, r, "/admin/mining?"+query.Encode()+"#mining-files", http.StatusSeeOther)
}
