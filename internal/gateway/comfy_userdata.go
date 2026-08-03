package gateway

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

const (
	maxComfyUserDataFile  = 32 << 20
	maxComfyUserDataQuota = 256 << 20
	maxComfySettingsBody  = 2 << 20
)

func (a *App) handleComfyUserState(w http.ResponseWriter, r *http.Request, user *User) bool {
	if user == nil {
		return false
	}
	switch {
	case r.URL.Path == "/api/users":
		a.handleComfyUsers(w, r)
	case r.URL.Path == "/api/settings" || strings.HasPrefix(r.URL.Path, "/api/settings/"):
		a.handleComfySettings(w, r, user.ID)
	case r.URL.Path == "/api/userdata":
		a.handleComfyLegacyUserDataList(w, r, user.ID)
	case r.URL.Path == "/api/v2/userdata":
		a.handleComfyUserDataV2(w, r, user.ID)
	case strings.HasPrefix(r.URL.Path, "/api/userdata/"):
		a.handleComfyUserDataFile(w, r, user.ID)
	default:
		return false
	}
	return true
}

func (a *App) handleComfyUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	writeComfyJSON(w, http.StatusOK, map[string]any{"storage": "server", "migrated": true})
}

func (a *App) handleComfySettings(w http.ResponseWriter, r *http.Request, userID int64) {
	key, hasKey, err := comfyRouteValue(r, "/api/settings/")
	if err != nil || hasKey && (key == "" || len(key) > 512) {
		http.Error(w, "некорректный идентификатор настройки", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		settings, err := a.store.ComfySettings(r.Context(), userID)
		if err != nil {
			http.Error(w, "не удалось загрузить настройки ComfyUI", http.StatusInternalServerError)
			return
		}
		if !hasKey {
			writeComfyRawJSON(w, http.StatusOK, settings)
			return
		}
		var document map[string]json.RawMessage
		if err := json.Unmarshal(settings, &document); err != nil {
			http.Error(w, "повреждены настройки ComfyUI", http.StatusInternalServerError)
			return
		}
		value, ok := document[key]
		if !ok {
			value = json.RawMessage("null")
		}
		writeComfyRawJSON(w, http.StatusOK, value)
	case http.MethodPost:
		body, err := readLimitedBody(w, r, maxComfySettingsBody)
		if err != nil {
			http.Error(w, "настройки ComfyUI слишком велики", http.StatusRequestEntityTooLarge)
			return
		}
		if !json.Valid(body) {
			http.Error(w, "некорректный JSON настроек", http.StatusBadRequest)
			return
		}
		if hasKey {
			err = a.store.SetComfySetting(r.Context(), userID, key, body)
		} else {
			var object map[string]json.RawMessage
			if json.Unmarshal(body, &object) != nil || object == nil {
				http.Error(w, "ожидается объект настроек", http.StatusBadRequest)
				return
			}
			err = a.store.MergeComfySettings(r.Context(), userID, body)
		}
		if err != nil {
			http.Error(w, "не удалось сохранить настройки ComfyUI", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleComfyLegacyUserDataList(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	directory, err := normalizeComfyDataPath(r.URL.Query().Get("dir"), false)
	if err != nil {
		http.Error(w, "некорректный каталог", http.StatusBadRequest)
		return
	}
	entries, err := a.store.ListComfyUserData(r.Context(), userID)
	if err != nil {
		http.Error(w, "не удалось получить список файлов", http.StatusInternalServerError)
		return
	}
	prefix := directory + "/"
	recurse := strings.EqualFold(r.URL.Query().Get("recurse"), "true")
	fullInfo := strings.EqualFold(r.URL.Query().Get("full_info"), "true")
	splitPath := strings.EqualFold(r.URL.Query().Get("split"), "true")
	result := make([]any, 0)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Path, prefix) {
			continue
		}
		relative := strings.TrimPrefix(entry.Path, prefix)
		if !recurse && strings.Contains(relative, "/") {
			continue
		}
		switch {
		case fullInfo:
			result = append(result, comfyFileInfo(relative, entry))
		case splitPath:
			parts := append([]string{relative}, strings.Split(relative, "/")...)
			result = append(result, parts)
		default:
			result = append(result, relative)
		}
	}
	writeComfyJSON(w, http.StatusOK, result)
}

func (a *App) handleComfyUserDataV2(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	requested, err := normalizeComfyDataPath(r.URL.Query().Get("path"), true)
	if err != nil {
		http.Error(w, "некорректный каталог", http.StatusBadRequest)
		return
	}
	entries, err := a.store.ListComfyUserData(r.Context(), userID)
	if err != nil {
		http.Error(w, "не удалось получить список файлов", http.StatusInternalServerError)
		return
	}
	result, foundDirectory, foundFile := comfyV2Entries(entries, requested)
	if foundFile {
		http.Error(w, "запрошенный путь не является каталогом", http.StatusBadRequest)
		return
	}
	if requested != "" && !foundDirectory {
		http.Error(w, "каталог не найден", http.StatusNotFound)
		return
	}
	writeComfyJSON(w, http.StatusOK, result)
}

func (a *App) handleComfyUserDataFile(w http.ResponseWriter, r *http.Request, userID int64) {
	source, destination, isMove, err := comfyUserDataRoute(r)
	if err != nil {
		http.Error(w, "некорректный путь файла", http.StatusBadRequest)
		return
	}
	if isMove {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		overwrite := !strings.EqualFold(r.URL.Query().Get("overwrite"), "false")
		entry, err := a.store.MoveComfyUserData(r.Context(), userID, source, destination, overwrite)
		if errors.Is(err, store.ErrComfyDataNotFound) {
			http.NotFound(w, r)
			return
		}
		if errors.Is(err, store.ErrComfyDataExists) {
			http.Error(w, "файл уже существует", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "не удалось переместить файл", http.StatusInternalServerError)
			return
		}
		writeComfyUserDataResult(w, r, entry)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		payload, entry, err := a.store.ComfyUserData(r.Context(), userID, source)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "не удалось загрузить файл", http.StatusInternalServerError)
			return
		}
		contentType := mime.TypeByExtension(path.Ext(source))
		if contentType == "" || strings.Contains(contentType, "html") || strings.Contains(contentType, "svg") {
			contentType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(source)})
		if disposition == "" {
			disposition = "attachment"
		}
		w.Header().Set("Content-Disposition", disposition)
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, path.Base(source), entry.ModifiedAt, bytes.NewReader(payload))
	case http.MethodPost:
		payload, err := readLimitedBody(w, r, maxComfyUserDataFile)
		if err != nil {
			http.Error(w, "файл ComfyUI слишком велик", http.StatusRequestEntityTooLarge)
			return
		}
		overwrite := !strings.EqualFold(r.URL.Query().Get("overwrite"), "false")
		entry, err := a.store.PutComfyUserData(r.Context(), userID, source, payload, overwrite, maxComfyUserDataQuota)
		if errors.Is(err, store.ErrComfyDataExists) {
			http.Error(w, "файл уже существует", http.StatusConflict)
			return
		}
		if errors.Is(err, store.ErrComfyDataQuota) {
			http.Error(w, "исчерпана квота пользовательских данных ComfyUI", http.StatusInsufficientStorage)
			return
		}
		if err != nil {
			http.Error(w, "не удалось сохранить файл", http.StatusInternalServerError)
			return
		}
		writeComfyUserDataResult(w, r, entry)
	case http.MethodDelete:
		err := a.store.DeleteComfyUserData(r.Context(), userID, source)
		if errors.Is(err, store.ErrComfyDataNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "не удалось удалить файл", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST, DELETE")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
	}
}

func comfyRouteValue(r *http.Request, prefix string) (string, bool, error) {
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, prefix) {
		return "", false, nil
	}
	value, err := url.PathUnescape(strings.TrimPrefix(escaped, prefix))
	return value, true, err
}

func comfyUserDataRoute(r *http.Request) (string, string, bool, error) {
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), "/api/userdata/")
	if split := strings.LastIndex(escaped, "/move/"); split >= 0 {
		source, err := url.PathUnescape(escaped[:split])
		if err != nil {
			return "", "", false, err
		}
		destination, err := url.PathUnescape(escaped[split+len("/move/"):])
		if err != nil {
			return "", "", false, err
		}
		source, err = normalizeComfyDataPath(source, false)
		if err != nil {
			return "", "", false, err
		}
		destination, err = normalizeComfyDataPath(destination, false)
		return source, destination, true, err
	}
	value, err := url.PathUnescape(escaped)
	if err != nil {
		return "", "", false, err
	}
	value, err = normalizeComfyDataPath(value, false)
	return value, "", false, err
}

func normalizeComfyDataPath(value string, allowEmpty bool) (string, error) {
	if value == "" && allowEmpty {
		return "", nil
	}
	if value == "" || len(value) > 1024 || strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") {
		return "", errors.New("invalid ComfyUI userdata path")
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "", errors.New("invalid ComfyUI userdata path")
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != value {
		return "", errors.New("invalid ComfyUI userdata path")
	}
	return cleaned, nil
}

func comfyV2Entries(entries []domain.ComfyUserDataEntry, requested string) ([]map[string]any, bool, bool) {
	prefix := ""
	if requested != "" {
		prefix = requested + "/"
	}
	foundDirectory := requested == ""
	foundFile := false
	directories := make(map[string]struct{})
	result := make([]map[string]any, 0)
	for _, entry := range entries {
		if entry.Path == requested {
			foundFile = true
			continue
		}
		if !strings.HasPrefix(entry.Path, prefix) {
			continue
		}
		foundDirectory = true
		directory := path.Dir(entry.Path)
		for directory != "." && directory != requested && strings.HasPrefix(directory+"/", prefix) {
			directories[directory] = struct{}{}
			directory = path.Dir(directory)
		}
		result = append(result, map[string]any{
			"name": path.Base(entry.Path), "path": entry.Path, "type": "file",
			"size": entry.Size, "modified": entry.ModifiedAt.Unix(),
		})
	}
	for directory := range directories {
		result = append(result, map[string]any{"name": path.Base(directory), "path": directory, "type": "directory"})
	}
	sort.Slice(result, func(i, j int) bool {
		leftDir := result[i]["type"] == "directory"
		rightDir := result[j]["type"] == "directory"
		if leftDir != rightDir {
			return leftDir
		}
		return strings.ToLower(result[i]["name"].(string)) < strings.ToLower(result[j]["name"].(string))
	})
	return result, foundDirectory, foundFile
}

func comfyFileInfo(dataPath string, entry domain.ComfyUserDataEntry) map[string]any {
	return map[string]any{
		"path": dataPath, "size": entry.Size,
		"modified": entry.ModifiedAt.UnixMilli(), "created": entry.CreatedAt.UnixMilli(),
	}
}

func writeComfyUserDataResult(w http.ResponseWriter, r *http.Request, entry domain.ComfyUserDataEntry) {
	if strings.EqualFold(r.URL.Query().Get("full_info"), "true") {
		writeComfyJSON(w, http.StatusOK, comfyFileInfo(entry.Path, entry))
		return
	}
	writeComfyJSON(w, http.StatusOK, entry.Path)
}

func readLimitedBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func writeComfyRawJSON(w http.ResponseWriter, status int, body json.RawMessage) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeComfyJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "не удалось сформировать JSON", http.StatusInternalServerError)
		return
	}
	writeComfyRawJSON(w, status, body)
}
