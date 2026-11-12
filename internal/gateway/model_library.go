package gateway

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/modelmanager"
	"ai-access-gateway/internal/store"
)

func (a *App) modelManager() *modelmanager.Client {
	return &modelmanager.Client{Base: a.cfg.ComfyUIUpstream, Auth: a.cfg.ComfyUIUpstreamAuthHeader}
}
func (a *App) registerModelLibraryRoutes(mux *http.ServeMux) {
	mux.Handle("/models", a.requireAuth(http.HandlerFunc(a.handleModelLibrary)))
	mux.Handle("/models/api/", a.requireAuth(http.HandlerFunc(a.handleModelLibraryAPI)))
}
func (a *App) handleModelLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Метод не поддерживается.", 405)
		return
	}
	a.render(w, r, "model_library", map[string]any{"Title": "Модели и LoRA", "CanManageModels": a.currentUser(r).Role == "admin", "ModelAdminWorkspace": strings.HasPrefix(r.URL.Path, "/admin/")})
}

type modelLibraryToken struct {
	Scope    string                 `json:"scope"`
	Owner    int64                  `json:"owner"`
	Expires  int64                  `json:"expires"`
	Kind     string                 `json:"kind,omitempty"`
	Path     string                 `json:"path,omitempty"`
	Preview  string                 `json:"preview,omitempty"`
	ModelID  int64                  `json:"model_id,omitempty"`
	SHA256   string                 `json:"sha256,omitempty"`
	Size     int64                  `json:"size,omitempty"`
	Install  *store.ModelInstall    `json:"install,omitempty"`
	Download *modelmanager.Download `json:"download,omitempty"`
}

func (a *App) modelToken(v modelLibraryToken) (string, error) {
	if a.contentCipher == nil {
		return "", errors.New("cipher unavailable")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	value, err := a.contentCipher.EncryptBytesWithAAD(raw, []byte("model-library:v1"))
	return base64.RawURLEncoding.EncodeToString(value), err
}
func (a *App) readModelToken(raw, scope string, owner int64) (modelLibraryToken, error) {
	var v modelLibraryToken
	if len(raw) > 16384 || a.contentCipher == nil {
		return v, modelmanager.ErrInvalid
	}
	value, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return v, modelmanager.ErrInvalid
	}
	plain, err := a.contentCipher.DecryptBytesWithAAD(value, []byte("model-library:v1"))
	if err != nil || json.Unmarshal(plain, &v) != nil || v.Scope != scope || v.Owner != owner || v.Expires < time.Now().Unix() {
		return v, modelmanager.ErrInvalid
	}
	return v, nil
}
func modelJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func (a *App) modelBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if r.Method != "POST" {
		http.Error(w, "Метод не поддерживается.", 405)
		return false
	}
	if !a.validCSRFValue(r, r.Header.Get("X-CSRF-Token")) {
		http.Error(w, "Проверка сессии не пройдена.", 403)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "Проверьте параметры запроса.", 400)
		return false
	}
	return true
}
func (a *App) handleModelLibraryAPI(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		http.Error(w, "Войдите в аккаунт.", 401)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	action := strings.TrimPrefix(r.URL.Path, "/models/api/")
	publicRead := action == "catalog" || action == "base-models" || action == "item" || action == "preview"
	if !publicRead && u.Role != "admin" {
		http.Error(w, "Нужны права администратора.", 403)
		return
	}
	if publicRead && r.Method != "GET" {
		http.Error(w, "Каталог доступен только для просмотра.", 405)
		return
	}
	ctx, cancel := modelmanager.ReadContext(r.Context())
	if action == "delete-preview" || action == "delete-lora" || action == "delete-model" {
		cancel()
		ctx, cancel = context.WithTimeout(r.Context(), 24*time.Minute)
	}
	defer cancel()
	client := a.modelManager()
	fail := func() {
		http.Error(w, "LoRA Manager недоступен. Повторите проверку подключения.", 502)
	}
	switch action {
	case "base-models":
		kind := r.URL.Query().Get("kind")
		if !modelmanager.Kind(kind) {
			http.Error(w, "Выберите каталог.", http.StatusBadRequest)
			return
		}
		models, err := client.BaseModels(ctx, kind)
		if err != nil {
			fail()
			return
		}
		modelJSON(w, map[string]any{"base_models": models})
	case "catalog":
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		kind := r.URL.Query().Get("kind")
		if kind == "" {
			kind = "loras"
		}
		if r.URL.Query().Get("grouped") == "true" {
			a.handleGroupedModelCatalog(w, ctx, client, u.ID, kind, r.URL.Query().Get("q"), r.URL.Query().Get("base"), page, r.URL.Query().Get("updates") == "true")
			return
		}
		catalog, err := client.ListFiltered(ctx, kind, r.URL.Query().Get("q"), r.URL.Query().Get("base"), page, r.URL.Query().Get("updates") == "true")
		if err != nil {
			fail()
			return
		}
		items := make([]map[string]any, 0, len(catalog.Items))
		for _, item := range catalog.Items {
			token, err := a.modelToken(modelLibraryToken{Scope: "item", Owner: u.ID, Expires: time.Now().Add(24 * time.Hour).Unix(), Kind: kind, Path: item.Path, Preview: item.Preview, ModelID: item.Civitai.ModelID, SHA256: item.Hash, Size: item.Size})
			if err != nil {
				fail()
				return
			}
			items = append(items, map[string]any{"name": item.Name, "filename": item.Filename, "base": item.Base, "size": item.Size, "hash": item.Hash, "tags": item.Tags, "update": item.Update, "version_id": item.Civitai.ID, "model_id": item.Civitai.ModelID, "words": item.Civitai.Words, "token": token, "preview": item.Preview != "", "mature": item.NSFW > 1})
		}
		modelJSON(w, map[string]any{"items": items, "total": catalog.Total, "page": catalog.Page, "pages": catalog.Pages})
	case "item", "preview":
		token, err := a.readModelToken(r.URL.Query().Get("token"), "item", u.ID)
		if err != nil {
			http.Error(w, "Откройте карточку из каталога ещё раз.", 400)
			return
		}
		if action == "preview" {
			data, typ, err := client.Preview(ctx, token.Preview)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", typ)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(data)
			return
		}
		description, err := client.Description(ctx, token.Kind, token.Path)
		if err != nil {
			fail()
			return
		}
		modelJSON(w, map[string]string{"description": description})
	case "delete-preview", "delete-lora", "delete-model", "refresh-metadata":
		a.handleModelMaintenance(w, r, ctx, action)
	case "check-update":
		var in struct {
			Token string `json:"token"`
		}
		if !a.modelBody(w, r, &in) {
			return
		}
		token, err := a.readModelToken(in.Token, "item", u.ID)
		if err != nil || token.ModelID <= 0 || !modelmanager.Kind(token.Kind) {
			http.Error(w, "Откройте модель с привязкой к CivitAI из каталога ещё раз.", 400)
			return
		}
		available, err := client.CheckUpdate(ctx, token.Kind, token.ModelID)
		if errors.Is(err, modelmanager.ErrRateLimited) {
			http.Error(w, "CivitAI ограничил частоту запросов. Повторите проверку позже.", 429)
			return
		}
		if err != nil {
			http.Error(w, "Проверка обновлений не завершена. Повторите позже; файлы моделей не изменены.", 502)
			return
		}
		modelJSON(w, map[string]any{"update": available, "checked_at": time.Now().UTC()})
	case "settings":
		if r.Method == "POST" {
			var in struct {
				Key string `json:"key"`
			}
			if !a.modelBody(w, r, &in) {
				return
			}
			if err := client.SetKey(ctx, strings.TrimSpace(in.Key)); err != nil {
				fail()
				return
			}
		}
		if r.Method != "GET" && r.Method != "POST" {
			http.Error(w, "Метод не поддерживается.", 405)
			return
		}
		settings, err := client.Settings(ctx)
		if err != nil {
			fail()
			return
		}
		modelJSON(w, settings)
	case "versions":
		if r.Method != "GET" {
			http.Error(w, "Метод не поддерживается.", 405)
			return
		}
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		versions, err := client.Versions(ctx, r.URL.Query().Get("kind"), id)
		if err != nil {
			var mismatch *modelmanager.CatalogMismatchError
			switch {
			case errors.As(err, &mismatch):
				if mismatch.Kind == "checkpoints" {
					http.Error(w, "Это модель, а не LoRA. В поле «Тип каталога» выберите «Модель» и нажмите «Найти версии».", http.StatusUnprocessableEntity)
				} else {
					http.Error(w, "Это LoRA, а не модель. В поле «Тип каталога» выберите «LoRA» и нажмите «Найти версии».", http.StatusUnprocessableEntity)
				}
			case errors.Is(err, modelmanager.ErrUnsupportedType):
				http.Error(w, "Этот тип ресурса CivitAI не поддерживается. Здесь можно установить модели и LoRA.", http.StatusUnprocessableEntity)
			case errors.Is(err, modelmanager.ErrNotFound):
				http.Error(w, "Модель не найдена или недоступна в CivitAI. Проверьте ссылку или ID модели.", http.StatusNotFound)
			case errors.Is(err, modelmanager.ErrRateLimited):
				http.Error(w, "CivitAI ограничил частоту запросов. Повторите поиск версий позже.", http.StatusTooManyRequests)
			case errors.Is(err, modelmanager.ErrInvalid):
				http.Error(w, "Проверьте тип каталога и ID модели CivitAI.", http.StatusBadRequest)
			default:
				fail()
			}
			return
		}
		modelJSON(w, versions)
	case "quote":
		a.handleModelInstallQuote(w, r, ctx)
	case "installs":
		if r.Method == "POST" {
			var in struct {
				ID    string `json:"id"`
				Quote string `json:"quote"`
			}
			if !a.modelBody(w, r, &in) {
				return
			}
			id, err := hex.DecodeString(in.ID)
			if err != nil || len(id) != 16 || strings.ToLower(in.ID) != in.ID {
				http.Error(w, "Некорректный идентификатор установки.", 400)
				return
			}
			token, err := a.readModelToken(in.Quote, "install", u.ID)
			if err != nil || token.Install == nil || token.Download == nil {
				http.Error(w, "Проверьте параметры установки ещё раз.", 400)
				return
			}
			j := *token.Install
			j.ID = in.ID
			j.OwnerID = u.ID
			token.Download.ID = in.ID
			j.Request, _ = json.Marshal(token.Download)
			job, err := a.store.EnqueueModelInstall(ctx, j)
			if err != nil {
				http.Error(w, "Эта версия уже устанавливается либо параметры запроса изменились. Проверьте очередь.", 409)
				return
			}
			modelJSON(w, job)
			return
		}
		if r.Method != "GET" {
			http.Error(w, "Метод не поддерживается.", 405)
			return
		}
		if id := r.URL.Query().Get("id"); id != "" {
			job, err := a.store.ModelInstallReceipt(ctx, id)
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, r)
				return
			}
			if err != nil {
				http.Error(w, "Не удалось проверить приём запроса.", 503)
				return
			}
			modelJSON(w, job)
			return
		}
		jobs, err := a.store.ModelInstalls(ctx, false)
		if err != nil {
			http.Error(w, "Не удалось прочитать очередь.", 503)
			return
		}
		modelJSON(w, map[string]any{"jobs": modelInstallViews(ctx, client, jobs)})
	case "close-request":
		var in struct {
			ID string `json:"id"`
		}
		if !a.modelBody(w, r, &in) {
			return
		}
		id, err := hex.DecodeString(in.ID)
		if err != nil || len(id) != 16 {
			http.Error(w, "Некорректный идентификатор.", 400)
			return
		}
		if err = a.store.CloseModelInstallRequest(ctx, in.ID, u.ID); err != nil {
			http.Error(w, "Установка уже началась. Состояние доступно в загрузках.", 409)
			return
		}
		modelJSON(w, map[string]bool{"closed": true})
	case "cancel":
		var in struct {
			ID string `json:"id"`
		}
		if !a.modelBody(w, r, &in) {
			return
		}
		if err := a.store.CancelQueuedModelInstall(ctx, in.ID); err != nil {
			http.Error(w, "Загрузка уже началась. Отменить ожидание нельзя.", 409)
			return
		}
		modelJSON(w, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}
func (a *App) handleModelInstallQuote(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	var in struct {
		Kind      string `json:"kind"`
		ModelID   int64  `json:"model_id"`
		VersionID int64  `json:"version_id"`
		FileID    int64  `json:"file_id"`
		Target    string `json:"target"`
	}
	if !a.modelBody(w, r, &in) {
		return
	}
	client := a.modelManager()
	versions, err := client.Versions(ctx, in.Kind, in.ModelID)
	if err != nil {
		http.Error(w, "Не удалось проверить версию CivitAI.", 502)
		return
	}
	var version modelmanager.Version
	var file modelmanager.File
	for _, v := range versions.Versions {
		if v.ID == in.VersionID {
			version = v
			for _, f := range v.Files {
				if f.ID == in.FileID {
					file = f
				}
			}
		}
	}
	if version.ID == 0 || file.ID == 0 || version.Exists {
		http.Error(w, "Версия уже установлена или файл не найден.", 409)
		return
	}
	version.Model.Type = versions.Type
	settings, err := client.Settings(ctx)
	if err != nil || !settings.KeySet {
		http.Error(w, "Сначала настройте ключ CivitAI.", 409)
		return
	}
	download, size, err := modelmanager.Prepare(version, file, settings, in.Target, "")
	if err != nil {
		http.Error(w, "Файл не прошёл проверку: проверьте формат SafeTensor, контрольную сумму и тип установки.", 400)
		return
	}
	capacity, err := a.updates.ModelCapacity(ctx, download.Root)
	if err != nil {
		http.Error(w, "Windows-агент не подтвердил свободное место. Требуется агент с поддержкой установки моделей.", 409)
		return
	}
	if capacity.FreeBytes < modelmanager.RequiredFree(size) {
		http.Error(w, "Недостаточно свободного места с учётом резерва 5 ГБ.", 409)
		return
	}
	j := store.ModelInstall{ModelID: in.ModelID, VersionID: version.ID, Kind: in.Kind, Name: versions.Name + " · " + version.Name, Filename: file.Name, SHA256: strings.ToLower(file.Hashes.SHA256), Size: size}
	token, err := a.modelToken(modelLibraryToken{Scope: "install", Owner: a.currentUser(r).ID, Expires: time.Now().Add(15 * time.Minute).Unix(), Install: &j, Download: &download})
	if err != nil {
		http.Error(w, "Не удалось подготовить установку.", 500)
		return
	}
	modelJSON(w, map[string]any{"quote": token, "destination": strings.TrimRight(download.Root, "/\\") + "/" + download.RelativePath, "filename": file.Name, "size": size, "free_bytes": capacity.FreeBytes})
}
func (a *App) runModelInstall(ctx context.Context) (int64, error) {
	j, release, err := a.store.ClaimModelInstall(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer release()
	finish := func(state, message string, progress float64) (int64, error) {
		save, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return 1, a.store.UpdateModelInstall(save, j.ID, state, message, progress)
	}
	var d modelmanager.Download
	if json.Unmarshal(j.Request, &d) != nil {
		return finish("failed", "Параметры установки повреждены.", 0)
	}
	capacity, err := a.updates.ModelCapacity(ctx, d.Root)
	if err != nil || capacity.FreeBytes < modelmanager.RequiredFree(j.Size) {
		return finish("failed", "Не удалось подтвердить свободное место перед установкой.", 0)
	}
	result, err := a.modelManager().Start(ctx, d)
	if err != nil {
		return finish("reconciling", "Связь с LoRA Manager прервалась. Проверяем состояние; повторная загрузка не запускается.", 0)
	}
	if result.Cancelled {
		return finish("cancelled", "Загрузка отменена в LoRA Manager.", 0)
	}
	if !result.Success {
		return finish("failed", "LoRA Manager не завершил установку. Проверьте ключ, доступ к файлу и свободное место.", 0)
	}
	return finish("reconciling", "Загрузка завершена. Проверяем файл.", 100)
}
func (a *App) refreshModelInstallProgress(ctx context.Context) (int64, error) {
	jobs, err := a.store.ModelInstalls(ctx, true)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, j := range jobs {
		result, err := a.modelManager().Progress(ctx, j.ID)
		if err != nil {
			if errors.Is(err, modelmanager.ErrNotFound) && j.State == "running" {
				if err = a.store.UpdateModelInstall(ctx, j.ID, "reconciling", "Проверяем файл после потери состояния загрузки.", j.Progress); err != nil {
					return count, err
				}
			}
			continue
		}
		state, message := j.State, j.Message
		if result.Success && result.Status == "" && result.Progress >= 100 && !math.IsInf(result.Progress, 0) {
			state, message = "reconciling", "Передача завершена. Проверяем файл."
		}
		switch result.Status {
		case "completed":
			state, message = "reconciling", "Загрузка завершена. Проверяем файл."
		case "error", "failed":
			state, message = "failed", "Загрузка завершилась ошибкой в LoRA Manager."
		case "cancelled":
			state, message = "cancelled", "Загрузка отменена в LoRA Manager."
		}
		progress := math.Max(0, math.Min(100, result.Progress))
		if math.IsNaN(progress) {
			progress = 0
		}
		if state == "completed" {
			progress = 100
		}
		if err = a.store.UpdateModelInstall(ctx, j.ID, state, message, progress); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
