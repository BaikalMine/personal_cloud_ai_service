package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

const loraDatasetManifestBytes = 512 << 10

var loraDatasetIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)

type loraDatasetRequest struct {
	Revision int64                      `json:"revision"`
	Manifest domain.LoraDatasetManifest `json:"manifest"`
	MediaID  int64                      `json:"media_id"`
}

type loraDatasetWarning struct {
	ImageID string `json:"image_id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type loraDatasetView struct {
	Dataset  domain.LoraDatasetRow              `json:"dataset"`
	Manifest domain.LoraDatasetManifest         `json:"manifest"`
	Assets   map[string]domain.LoraDatasetAsset `json:"assets"`
	Warnings []loraDatasetWarning               `json:"warnings"`
}

func validateLoraDatasetManifest(manifest domain.LoraDatasetManifest) error {
	if manifest.Version != 1 {
		return datasetInputError("Неизвестная версия датасета.")
	}
	if len(manifest.Images) > domain.LoraDatasetMaxImages {
		return store.ErrLoraDatasetQuota
	}
	settings := manifest.Settings
	for _, field := range []struct {
		value string
		limit int
	}{
		{settings.Name, 80}, {settings.OutputName, 64}, {settings.TriggerWord, 80}, {settings.ConceptType, 40},
		{settings.ProfileID, 200}, {settings.Preset, 40}, {settings.GlobalCaption, 1000},
	} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.limit || strings.ContainsRune(field.value, 0) {
			return datasetInputError("Одно из полей датасета слишком длинное или содержит недопустимые символы.")
		}
	}
	seen := make(map[string]bool)
	for _, item := range manifest.Images {
		if !loraDatasetIDPattern.MatchString(item.ID) || !loraDatasetIDPattern.MatchString(item.AssetID) || seen[item.ID] {
			return datasetInputError("Некорректный или повторяющийся идентификатор изображения.")
		}
		seen[item.ID] = true
		if !utf8.ValidString(item.Caption) || utf8.RuneCountInString(item.Caption) > 1000 || strings.ContainsRune(item.Caption, 0) {
			return datasetInputError("Описание изображения должно содержать не больше 1000 символов.")
		}
	}
	return nil
}

func (a *App) decodeLoraDatasetManifest(cipher []byte) (domain.LoraDatasetManifest, error) {
	var manifest domain.LoraDatasetManifest
	plain, err := a.contentCipher.DecryptBytes(cipher)
	if err != nil {
		return manifest, err
	}
	defer clear(plain)
	if err = json.Unmarshal(plain, &manifest); err != nil {
		return manifest, err
	}
	return manifest, validateLoraDatasetManifest(manifest)
}

func (a *App) encryptLoraDatasetManifest(manifest domain.LoraDatasetManifest) ([]byte, string, error) {
	if err := validateLoraDatasetManifest(manifest); err != nil {
		return nil, "", err
	}
	plain, err := json.Marshal(manifest)
	if err != nil {
		return nil, "", err
	}
	defer clear(plain)
	if len(plain) > loraDatasetManifestBytes {
		return nil, "", datasetInputError("Описания датасета занимают слишком много места.")
	}
	hash := sha256.Sum256(plain)
	cipher, err := a.contentCipher.EncryptBytes(plain)
	return cipher, hex.EncodeToString(hash[:]), err
}

func (a *App) loraDatasetView(ctx context.Context, row domain.LoraDatasetRow) (loraDatasetView, error) {
	manifest, err := a.decodeLoraDatasetManifest(row.ManifestCipher)
	view := loraDatasetView{Dataset: row, Manifest: manifest, Assets: map[string]domain.LoraDatasetAsset{}, Warnings: []loraDatasetWarning{}}
	if err != nil {
		return view, err
	}
	seen := map[string]bool{}
	for _, item := range manifest.Images {
		asset, ok := view.Assets[item.AssetID]
		if !ok {
			asset, err = a.store.LoraDatasetAsset(ctx, row.UserID, item.AssetID)
			if err != nil {
				return view, err
			}
			view.Assets[item.AssetID] = asset
		}
		if seen[asset.Hash] {
			view.Warnings = append(view.Warnings, loraDatasetWarning{item.ID, "duplicate", "В наборе уже есть точно такое же изображение."})
		}
		seen[asset.Hash] = true
		if !item.Excluded && strings.TrimSpace(item.Caption) == "" && strings.TrimSpace(manifest.Settings.GlobalCaption) == "" {
			view.Warnings = append(view.Warnings, loraDatasetWarning{item.ID, "empty_caption", "Перед обучением добавьте описание."})
		}
		if asset.Width < manifest.Settings.Resolution || asset.Height < manifest.Settings.Resolution {
			view.Warnings = append(view.Warnings, loraDatasetWarning{item.ID, "small_image", "Изображение меньше рабочего разрешения обучения."})
		}
	}
	return view, nil
}

func writeLoraDatasetError(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "Не удалось выполнить действие с датасетом."
	var input *loraDatasetInputError
	switch {
	case errors.As(err, &input):
		status, message = http.StatusBadRequest, input.Error()
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, store.ErrLoraDatasetAsset):
		status, message = http.StatusNotFound, "Датасет или изображение больше недоступны."
	case errors.Is(err, store.ErrLoraDatasetConflict):
		status, message = http.StatusConflict, "Датасет изменён в другой вкладке. Сначала загрузите актуальную версию."
	case errors.Is(err, store.ErrLoraDatasetQuota):
		status, message = http.StatusRequestEntityTooLarge, "Достигнут лимит: 20 датасетов, 100 версий, 100 изображений и 512 МБ на набор, 2 ГБ на пользователя."
	case errors.Is(err, store.ErrLoraDatasetInUse):
		status, message = http.StatusConflict, "Эта версия используется активным обучением."
	case errors.Is(err, store.ErrLoraTrainingAlreadyActive):
		status, message = http.StatusConflict, "У вас уже есть активное обучение."
	case errors.Is(err, errMediaMemoryBudget):
		status, message = http.StatusTooManyRequests, "Обработчик изображений занят. Повторите попытку через несколько секунд."
	}
	writeGenerationError(w, status, message)
}

func (a *App) writeLoraDatasetView(w http.ResponseWriter, r *http.Request, row domain.LoraDatasetRow, status int) {
	view, err := a.loraDatasetView(r.Context(), row)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	writeJSON(w, status, view)
}

func (a *App) handleLoraDatasets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPost && !a.validCSRFValue(r, r.Header.Get("X-CSRF-Token")) {
		writeGenerationError(w, http.StatusForbidden, "Проверка безопасности не пройдена.")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/lora-datasets"), "/")
	if parts[0] != "" {
		http.NotFound(w, r)
		return
	}
	parts = parts[1:]
	userID := a.currentUser(r).ID
	if len(parts) == 0 {
		if r.Method == http.MethodGet {
			rows, err := a.store.ListLoraDatasets(r.Context(), userID)
			if err != nil {
				writeLoraDatasetError(w, err)
				return
			}
			used, err := a.store.LoraDatasetStorageBytes(r.Context(), userID)
			if err != nil {
				writeLoraDatasetError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"datasets": rows, "storage_bytes": used, "storage_limit_bytes": domain.LoraDatasetUserMaxBytes, "retention_days": 30})
			return
		}
		var request loraDatasetRequest
		if !readLoraDatasetJSON(w, r, &request) {
			return
		}
		if len(request.Manifest.Images) != 0 {
			writeLoraDatasetError(w, datasetInputError("Создайте пустой набор, затем добавьте изображения."))
			return
		}
		cipher, _, err := a.encryptLoraDatasetManifest(request.Manifest)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		row, err := a.store.CreateLoraDataset(r.Context(), userID, newRequestID(), request.Manifest.Settings.Name, cipher)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		a.writeLoraDatasetView(w, r, row, http.StatusCreated)
		return
	}
	if parts[0] == "assets" && len(parts) == 2 && r.Method == http.MethodGet {
		asset, err := a.store.LoraDatasetAsset(r.Context(), userID, parts[1])
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		file, err := a.materializeLoraDatasetAsset(r.Context(), asset)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", asset.MIMEType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, asset.Name, asset.CreatedAt, file)
		return
	}
	if parts[0] == "versions" {
		a.handleLoraDatasetVersions(w, r, parts[1:])
		return
	}
	if len(parts) > 2 || !loraDatasetIDPattern.MatchString(parts[0]) {
		http.NotFound(w, r)
		return
	}
	row, err := a.store.LoraDataset(r.Context(), userID, parts[0])
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		a.writeLoraDatasetView(w, r, row, http.StatusOK)
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if parts[1] == "assets" {
		a.handleLoraDatasetUpload(w, r)
		return
	}
	var request loraDatasetRequest
	if !readLoraDatasetJSON(w, r, &request) {
		return
	}
	switch parts[1] {
	case "train":
		a.handleLoraDatasetTraining(w, r, row, request.Revision)
	case "save":
		cipher, _, err := a.encryptLoraDatasetManifest(request.Manifest)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		ids := make([]string, 0, len(request.Manifest.Images))
		for _, item := range request.Manifest.Images {
			ids = append(ids, item.AssetID)
		}
		row, err = a.store.SaveLoraDataset(r.Context(), userID, row.ID, request.Revision, request.Manifest.Settings.Name, cipher, ids)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		a.writeLoraDatasetView(w, r, row, http.StatusOK)
	case "delete":
		if err := a.store.DeleteLoraDataset(r.Context(), userID, row.ID, request.Revision); err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	case "versions":
		manifest, err := a.decodeLoraDatasetManifest(row.ManifestCipher)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		_, hash, err := a.encryptLoraDatasetManifest(manifest)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		snapshot, err := a.store.CreateLoraDatasetSnapshot(r.Context(), userID, row.ID, request.Revision, newRequestID(), hash)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"version": snapshot})
	case "reuse":
		media, err := a.store.ContentMediaByIDForUser(r.Context(), request.MediaID, userID)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		if media.MediaType != "image" || media.SizeBytes > maxLoraTrainingImageBytes {
			writeLoraDatasetError(w, datasetInputError("Выберите своё изображение размером до 24 МБ."))
			return
		}
		file, err := a.materializeContentMedia(r.Context(), media)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		defer file.Close()
		asset, err := a.persistLoraDatasetImage(r.Context(), userID, media.OriginalName, file)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"asset": asset})
	default:
		http.NotFound(w, r)
	}
}

func readLoraDatasetJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, loraDatasetManifestBytes+4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeLoraDatasetError(w, datasetInputError("Не удалось прочитать данные датасета."))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeLoraDatasetError(w, datasetInputError("Некорректное тело запроса."))
		return false
	}
	return true
}

func (a *App) handleLoraDatasetUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLoraTrainingImageBytes+64<<10)
	reader, err := r.MultipartReader()
	if err != nil {
		writeLoraDatasetError(w, datasetInputError("Не удалось прочитать загрузку."))
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "image" || part.FileName() == "" {
		writeLoraDatasetError(w, datasetInputError("Загружайте по одному изображению."))
		return
	}
	defer part.Close()
	asset, err := a.persistLoraDatasetImage(r.Context(), a.currentUser(r).ID, part.FileName(), part)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	if _, err := reader.NextPart(); !errors.Is(err, io.EOF) {
		writeLoraDatasetError(w, datasetInputError("Загружайте по одному изображению."))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"asset": asset})
}

func (a *App) handleLoraDatasetVersions(w http.ResponseWriter, r *http.Request, parts []string) {
	userID := a.currentUser(r).ID
	if len(parts) == 0 && r.Method == http.MethodGet {
		rows, err := a.store.ListLoraDatasetSnapshots(r.Context(), userID, r.URL.Query().Get("dataset_id"))
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"versions": rows})
		return
	}
	if len(parts) == 0 || len(parts) > 2 || !loraDatasetIDPattern.MatchString(parts[0]) {
		http.NotFound(w, r)
		return
	}
	snapshot, err := a.store.LoraDatasetSnapshot(r.Context(), userID, parts[0])
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		manifest, err := a.decodeLoraDatasetManifest(snapshot.ManifestCipher)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"version": snapshot, "manifest": manifest})
		return
	}
	if len(parts) == 2 && parts[1] == "delete" && r.Method == http.MethodPost {
		if err := a.store.DeleteLoraDatasetSnapshot(r.Context(), userID, snapshot.ID); err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
		return
	}
	http.NotFound(w, r)
}
