package gateway

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

type generationGalleryItemView struct {
	VariantID    int64
	JobID        string
	RequestID    string
	TemplateID   string
	WorkflowID   string
	WorkflowName string
	Scenario     string
	ModelName    string
	Prompt       string
	Seed         int64
	State        string
	ErrorMessage string
	CreatedAt    time.Time
	HasMedia     bool
	CompareCount int
	Media        generationMediaView
}

type generationPickerImageView struct {
	ID          int64  `json:"id"`
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	ModelName   string `json:"model_name"`
	CreatedUnix int64  `json:"created_unix"`
	ExpiresUnix int64  `json:"expires_unix"`
	Sensitive   bool   `json:"sensitive"`
	Pinned      bool   `json:"pinned"`
	Favorite    bool   `json:"favorite"`
}

func (a *App) handleGenerationGalleryPage(w http.ResponseWriter, r *http.Request) {
	if (r.URL.Path != "/gallery" && r.URL.Path != "/gallery/") || r.Method != http.MethodGet {
		if r.Method != http.MethodGet && (r.URL.Path == "/gallery" || r.URL.Path == "/gallery/") {
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		http.NotFound(w, r)
		return
	}
	user := a.currentUser(r)
	a.classifyPendingSensitiveContent(r.Context())
	a.queueSensitiveMediaClassification()
	variants, err := a.generationLibraryVariantViews(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "не удалось загрузить галерею", http.StatusInternalServerError)
		return
	}
	items := generationGalleryItems(variants)
	collections, err := a.store.ListGenerationMediaCollections(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "не удалось загрузить коллекции", http.StatusInternalServerError)
		return
	}
	a.render(w, r, "gallery", map[string]any{
		"Title":              "Моя галерея",
		"Items":              items,
		"Collections":        collections,
		"ImageCount":         generationGalleryMediaCount(items, "image"),
		"VideoCount":         generationGalleryMediaCount(items, "video"),
		"PinnedCount":        generationGalleryFlagCount(items, "pinned"),
		"FavoriteCount":      generationGalleryFlagCount(items, "favorite"),
		"ErrorCount":         generationGalleryFlagCount(items, "error"),
		"CanUseImageToImage": user.CanUseQuickGenerationType("image-to-image"),
		"CanUseMiniMaxVideo": user.CanUseQuickGenerationType("minimax-h3-video"),
		"CanReuseImages":     user.CanUseQuickGenerationType("image-to-image") || user.CanUseQuickGenerationType("minimax-h3-video"),
	})
}

func (a *App) handleReuseGenerationLibraryImage(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	var operationErr error
	var mediaSize int64
	defer func() { a.observeMediaOperation("gallery_reuse", mediaSize, started, operationErr) }()
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	mediaID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("media_id")), 10, 64)
	if err != nil || mediaID <= 0 || a.store == nil || a.contentCipher == nil {
		http.NotFound(w, r)
		return
	}
	user := a.currentUser(r)
	media, err := a.store.ContentMediaByIDForUser(r.Context(), mediaID, user.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if media.MediaType != "image" {
		writeGenerationError(w, http.StatusBadRequest, "в генерации можно использовать только изображение")
		return
	}
	mediaSize = media.SizeBytes
	if media.SizeBytes <= 0 || media.SizeBytes > maxComfyUploadBody-(1<<20) {
		writeGenerationError(w, http.StatusRequestEntityTooLarge, "изображение из галереи слишком большое")
		return
	}
	payload, err := a.materializeContentMedia(r.Context(), media)
	if err != nil {
		operationErr = err
		writeGenerationError(w, http.StatusInternalServerError, "не удалось подготовить изображение из галереи")
		return
	}
	defer payload.Close()
	uploadRequest, err := newGenerationLibraryImageUploadRequestFromReader(
		r.Context(), media.OriginalName, payload, a.mediaSpoolDir(),
	)
	if err != nil {
		operationErr = err
		writeGenerationError(w, http.StatusInternalServerError, "не удалось подготовить изображение из галереи")
		return
	}
	defer uploadRequest.Body.Close()
	uploadRequest.RemoteAddr = r.RemoteAddr
	uploadRequest.Host = r.Host
	uploadRequest.Header.Set("User-Agent", r.UserAgent())
	a.quickGenerationUploadHandler().ServeHTTP(w, uploadRequest)
}

func (a *App) handleGenerationLibraryImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if a.store == nil {
		writeGenerationError(w, http.StatusServiceUnavailable, "галерея временно недоступна")
		return
	}
	user := a.currentUser(r)
	items, err := a.generationLibraryImageViews(r.Context(), user.ID, "/generate/library/")
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить изображения")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": items})
}

func (a *App) generationLibraryImageViews(ctx context.Context, userID int64, prefix string) ([]generationPickerImageView, error) {
	media, err := a.store.ListUserGenerationImages(ctx, userID, 100)
	if err != nil {
		return nil, err
	}
	items := make([]generationPickerImageView, 0, len(media))
	for _, item := range media {
		items = append(items, generationPickerImageView{
			ID: item.ID, URL: prefix + strconv.FormatInt(item.ID, 10), Filename: item.OriginalName,
			ModelName: generationModelLabel(item.ModelName), CreatedUnix: item.CreatedAt.UnixMilli(),
			ExpiresUnix: item.ExpiresAt.UnixMilli(), Sensitive: item.Sensitive || item.VisualPending,
			Pinned: item.Pinned, Favorite: item.Favorite,
		})
	}
	return items, nil
}

func (a *App) handlePinGenerationLibraryMedia(w http.ResponseWriter, r *http.Request) {
	mediaID, enabled, ok := a.generationLibraryToggleRequest(w, r)
	if !ok {
		return
	}
	policy := a.retentionPolicy()
	now := time.Now()
	expiresAt, changed, err := a.store.SetGenerationMediaPinned(
		r.Context(), a.currentUser(r).ID, mediaID, enabled,
		now.Add(policy.GenerationMedia), now.Add(policy.PinnedMedia),
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "не удалось изменить срок хранения", http.StatusInternalServerError)
		return
	}
	if !changed {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"updated": true, "media_id": mediaID, "pinned": enabled, "expires_unix": expiresAt.UnixMilli(),
	})
}

func (a *App) handleFavoriteGenerationLibraryMedia(w http.ResponseWriter, r *http.Request) {
	mediaID, enabled, ok := a.generationLibraryToggleRequest(w, r)
	if !ok {
		return
	}
	changed, err := a.store.SetGenerationMediaFavorite(r.Context(), a.currentUser(r).ID, mediaID, enabled)
	if err != nil {
		http.Error(w, "не удалось обновить избранное", http.StatusInternalServerError)
		return
	}
	if !changed {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": true, "media_id": mediaID, "favorite": enabled})
}

func (a *App) generationLibraryToggleRequest(w http.ResponseWriter, r *http.Request) (int64, bool, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return 0, false, false
	}
	if !a.validCSRF(r) {
		http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
		return 0, false, false
	}
	mediaID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("media_id")), 10, 64)
	if err != nil || mediaID <= 0 {
		http.NotFound(w, r)
		return 0, false, false
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(r.FormValue("enabled")))
	if err != nil {
		http.Error(w, "некорректное состояние", http.StatusBadRequest)
		return 0, false, false
	}
	return mediaID, enabled, true
}

func (a *App) handleGenerationLibraryMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось прочитать данные", http.StatusBadRequest)
		return
	}
	mediaID, err := strconv.ParseInt(strings.TrimSpace(r.Form.Get("media_id")), 10, 64)
	if err != nil || mediaID <= 0 {
		http.NotFound(w, r)
		return
	}
	tags := strings.FieldsFunc(r.Form.Get("tags"), func(character rune) bool { return character == ',' || character == ';' || character == '\n' })
	collectionIDs, err := generationLibraryFormIDs(r.Form["collection_id"], 12)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.store.UpdateGenerationMediaMetadata(r.Context(), a.currentUser(r).ID, mediaID, tags, collectionIDs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": true, "media_id": mediaID})
}

func (a *App) handleGenerationLibraryCollections(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	switch r.Method {
	case http.MethodGet:
		collections, err := a.store.ListGenerationMediaCollections(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "не удалось загрузить коллекции", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"collections": collections})
	case http.MethodPost:
		if !a.validCSRF(r) {
			http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
			return
		}
		collection, err := a.store.CreateGenerationMediaCollection(r.Context(), user.ID, r.FormValue("name"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"collection": collection})
	default:
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleDeleteGenerationLibraryCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
		return
	}
	collectionID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("collection_id")), 10, 64)
	if err != nil || collectionID <= 0 {
		http.NotFound(w, r)
		return
	}
	deleted, err := a.store.DeleteGenerationMediaCollection(r.Context(), a.currentUser(r).ID, collectionID)
	if err != nil {
		http.Error(w, "не удалось удалить коллекцию", http.StatusInternalServerError)
		return
	}
	if !deleted {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "collection_id": collectionID})
}

func (a *App) handleBulkHideGenerationLibraryMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось прочитать данные", http.StatusBadRequest)
		return
	}
	mediaIDs, err := generationLibraryFormIDs(r.Form["media_id"], 100)
	if err != nil || len(mediaIDs) == 0 {
		http.Error(w, "выберите результаты", http.StatusBadRequest)
		return
	}
	removed, err := a.store.HideGenerationMediaForUserBulk(r.Context(), a.currentUser(r).ID, mediaIDs)
	if err != nil {
		http.Error(w, "не удалось обновить галерею", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "media_ids": mediaIDs})
}

func (a *App) handleExportGenerationLibraryMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !a.validCSRF(r) {
		http.Error(w, "проверка безопасности не пройдена", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "не удалось прочитать данные", http.StatusBadRequest)
		return
	}
	mediaIDs, err := generationLibraryFormIDs(r.Form["media_id"], 20)
	if err != nil || len(mediaIDs) == 0 {
		http.Error(w, "для выгрузки выберите от 1 до 20 результатов", http.StatusBadRequest)
		return
	}
	media, err := a.store.GenerationMediaByIDsForUser(r.Context(), a.currentUser(r).ID, mediaIDs)
	if err != nil || len(media) != len(mediaIDs) {
		http.Error(w, "один из выбранных результатов недоступен", http.StatusBadRequest)
		return
	}
	var totalBytes int64
	for _, item := range media {
		totalBytes += item.SizeBytes
	}
	if totalBytes <= 0 || totalBytes > maxGenerationOutputFingerprint {
		http.Error(w, "общий размер выгрузки превышает 2 ГБ", http.StatusRequestEntityTooLarge)
		return
	}
	archive, err := a.buildGenerationLibraryArchive(r.Context(), media)
	if err != nil {
		http.Error(w, "не удалось подготовить выгрузку", http.StatusInternalServerError)
		return
	}
	defer archive.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": "generation-library-" + time.Now().Format("20060102-1504") + ".zip",
	}))
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, filepath.Base(archive.File.Name()), time.Now(), archive.File)
}

func (a *App) buildGenerationLibraryArchive(ctx context.Context, media []domain.ContentMediaRow) (*generationOutputArchive, error) {
	file, err := os.CreateTemp(a.mediaSpoolDir(), "generation-library-*.zip")
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	archive := zip.NewWriter(file)
	usedNames := make(map[string]int)
	for _, item := range media {
		payload, err := a.materializeContentMedia(ctx, item)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		name := generationLibraryArchiveName(item.OriginalName, item.ID, usedNames)
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetModTime(time.Now())
		entry, err := archive.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(entry, payload)
		}
		closeErr := payload.Close()
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		if closeErr != nil {
			_ = archive.Close()
			return nil, closeErr
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	cleanup = false
	return &generationOutputArchive{File: file, path: file.Name(), ContentType: "application/zip"}, nil
}

func generationLibraryArchiveName(original string, mediaID int64, used map[string]int) string {
	name := filepath.Base(strings.ReplaceAll(strings.TrimSpace(original), "\\", "/"))
	if name == "" || name == "." || name == ".." {
		name = "result-" + strconv.FormatInt(mediaID, 10)
	}
	count := used[strings.ToLower(name)]
	used[strings.ToLower(name)] = count + 1
	if count == 0 {
		return name
	}
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	return fmt.Sprintf("%s-%d%s", base, count+1, extension)
}

func generationLibraryFormIDs(values []string, maximum int) ([]int64, error) {
	if len(values) > maximum {
		return nil, fmt.Errorf("можно выбрать не более %d результатов", maximum)
	}
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("некорректный идентификатор результата")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func newGenerationLibraryImageUploadRequestFromReader(ctx context.Context, filename string, payload io.Reader, directory string) (request *http.Request, operationErr error) {
	filename = filepath.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/"))
	if filename == "" || filename == "." || filename == ".." {
		filename = "gallery-image.png"
	}
	file, err := os.CreateTemp(directory, "gateway-media-reuse-*")
	if err != nil {
		return nil, fmt.Errorf("create gallery upload spool: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	writer := multipart.NewWriter(file)
	part, err := writer.CreateFormFile("image", filename)
	if err != nil {
		return nil, fmt.Errorf("create image part: %w", err)
	}
	if _, err := io.Copy(part, payload); err != nil {
		return nil, fmt.Errorf("write image part: %w", err)
	}
	if err := writer.WriteField("type", "input"); err != nil {
		return nil, fmt.Errorf("write upload type: %w", err)
	}
	if err := writer.WriteField("overwrite", "true"); err != nil {
		return nil, fmt.Errorf("write overwrite flag: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish image upload: %w", err)
	}
	length, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("measure gallery upload spool: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind gallery upload spool: %w", err)
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodPost, "/generate/upload/image", &removableFileBody{file: file, path: file.Name()})
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.ContentLength = length
	cleanup = false
	return request, nil
}

func generationGalleryItems(variants []generationVariantView) []generationGalleryItemView {
	items := make([]generationGalleryItemView, 0)
	for _, variant := range variants {
		prompt := strings.TrimSpace(variant.Values["positive_prompt"])
		if prompt == "" {
			prompt = "Промт не сохранён"
		}
		base := generationGalleryItemView{
			VariantID: variant.ID, JobID: variant.JobID, RequestID: variant.RequestID,
			TemplateID: variant.TemplateID, WorkflowID: variant.WorkflowID, WorkflowName: generationWorkflowLabel(variant.WorkflowID),
			Scenario: generationScenarioLabel(variant.TemplateID), ModelName: generationModelLabel(variant.ModelName),
			Prompt: prompt, Seed: variant.Seed, State: variant.State, ErrorMessage: variant.ErrorMessage,
			CreatedAt: variant.CreatedAt, CompareCount: len(variant.Media),
		}
		if len(variant.Media) == 0 && (variant.State == "error" || variant.State == "failed") {
			items = append(items, base)
			continue
		}
		for _, media := range variant.Media {
			item := base
			item.HasMedia = true
			item.Media = media
			items = append(items, item)
		}
	}
	return items
}

func generationGalleryMediaCount(items []generationGalleryItemView, mediaType string) int {
	count := 0
	for _, item := range items {
		if item.HasMedia && item.Media.MediaType == mediaType {
			count++
		}
	}
	return count
}

func generationGalleryFlagCount(items []generationGalleryItemView, flag string) int {
	count := 0
	for _, item := range items {
		switch flag {
		case "pinned":
			if item.HasMedia && item.Media.Pinned {
				count++
			}
		case "favorite":
			if item.HasMedia && item.Media.Favorite {
				count++
			}
		case "error":
			if item.State == "error" || item.State == "failed" {
				count++
			}
		}
	}
	return count
}

func generationScenarioLabel(templateID string) string {
	switch templateID {
	case "text-to-image":
		return "Текст в изображение"
	case "image-to-image":
		return "Фото и промт"
	case "minimax-h3-video":
		return "Видео"
	default:
		return "Быстрая генерация"
	}
}

func generationModelLabel(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" {
		return "Модель не указана"
	}
	name = filepath.Base(name)
	for _, suffix := range []string{".safetensors", ".ckpt", ".gguf"} {
		if strings.HasSuffix(strings.ToLower(name), suffix) {
			return name[:len(name)-len(suffix)]
		}
	}
	return name
}

func generationWorkflowLabel(workflowID string) string {
	switch strings.TrimSpace(workflowID) {
	case "photoflow-krea2":
		return "Krea2 · текст в изображение"
	case "photoflow-krea2-edit":
		return "Krea2 · фото и промт"
	case "photoflow-flux2-edit":
		return "Flux2 · фото и промт"
	case "minimax-h3-video":
		return "MiniMax H3 · видео"
	default:
		if strings.TrimSpace(workflowID) == "" {
			return "Workflow не указан"
		}
		return workflowID
	}
}
