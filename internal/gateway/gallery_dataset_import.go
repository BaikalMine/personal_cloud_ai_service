package gateway

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type galleryDatasetImportRequest struct {
	RequestID string `json:"request_id"`
	MediaID   string `json:"media_id"`
	DatasetID string `json:"dataset_id"`
	Revision  int64  `json:"revision"`
	Name      string `json:"name"`
}

func (a *App) writeGalleryDatasetImport(w http.ResponseWriter, r *http.Request, receipt domain.GalleryDatasetImportReceipt) {
	available := false
	if !receipt.Cancelled {
		_, err := a.store.LoraDataset(r.Context(), receipt.UserID, receipt.DatasetID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeLoraDatasetError(w, err)
			return
		}
		available = err == nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": receipt, "dataset_available": available})
}

func (a *App) handleGalleryDatasetImport(w http.ResponseWriter, r *http.Request, discard bool) {
	if r.Method == http.MethodGet && !discard {
		id := r.URL.Query().Get("request")
		if !validGenerationRequestID(id) || len(r.URL.Query()) != 1 || len(r.URL.Query()["request"]) != 1 {
			writeLoraDatasetError(w, datasetInputError("Некорректный запрос."))
			return
		}
		receipt, err := a.store.GalleryDatasetImport(r.Context(), a.currentUser(r).ID, id)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		a.writeGalleryDatasetImport(w, r, receipt)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	var request galleryDatasetImportRequest
	if !readLoraDatasetJSON(w, r, &request) {
		return
	}
	mediaID, err := strconv.ParseInt(request.MediaID, 10, 64)
	if !validGenerationRequestID(request.RequestID) || err != nil || mediaID <= 0 || strconv.FormatInt(mediaID, 10) != request.MediaID {
		writeLoraDatasetError(w, datasetInputError("Некорректный результат или запрос."))
		return
	}
	userID := a.currentUser(r).ID
	if discard {
		receipt, err := a.store.DiscardGalleryDatasetImport(r.Context(), userID, request.RequestID, mediaID)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		a.writeGalleryDatasetImport(w, r, receipt)
		return
	}
	if request.DatasetID != "" && (!loraDatasetIDPattern.MatchString(request.DatasetID) || request.Revision <= 0) || request.DatasetID == "" && request.Revision != 0 || !utf8.ValidString(request.Name) || utf8.RuneCountInString(request.Name) > 80 || strings.ContainsRune(request.Name, 0) || request.DatasetID == "" && strings.TrimSpace(request.Name) == "" {
		writeLoraDatasetError(w, datasetInputError("Проверьте название и версию датасета."))
		return
	}
	canonical, _ := json.Marshal(request)
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	previous, err := a.store.GalleryDatasetImport(r.Context(), userID, request.RequestID)
	if err == nil {
		if !previous.Cancelled && (previous.RequestSHA256 != digest || previous.MediaID != mediaID) {
			writeGenerationError(w, http.StatusConflict, "Этот запрос уже связан с другим переносом.")
			return
		}
		a.writeGalleryDatasetImport(w, r, previous)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeLoraDatasetError(w, err)
		return
	}
	if request.DatasetID != "" {
		row, err := a.store.LoraDataset(r.Context(), userID, request.DatasetID)
		if err != nil {
			writeLoraDatasetError(w, err)
			return
		}
		if row.Revision != request.Revision {
			writeLoraDatasetError(w, store.ErrLoraDatasetConflict)
			return
		}
	}
	media, err := a.store.ContentMediaByIDForUser(r.Context(), mediaID, userID)
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	if media.MediaType != "image" || media.SizeBytes <= 0 || media.SizeBytes > maxLoraTrainingImageBytes {
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
	p := domain.GalleryDatasetImportParams{UserID: userID, RequestID: request.RequestID, RequestSHA256: digest, MediaID: mediaID, MediaSize: media.SizeBytes, DatasetID: request.DatasetID, ExpectedRevision: request.Revision, Create: request.DatasetID == "", AssetID: asset.ID}
	if p.Create {
		p.DatasetID = newRequestID()
	}
	imageID := newRequestID()
	receipt, err := a.store.ImportGalleryDataset(r.Context(), p, func(row domain.LoraDatasetRow) (string, []byte, []string, string, bool, error) {
		manifest := domain.LoraDatasetManifest{Version: 1, Settings: domain.LoraDatasetSettings{Name: strings.TrimSpace(request.Name), Resolution: 1024, Preset: "balanced"}, Images: []domain.LoraDatasetImage{}}
		if !p.Create {
			var err error
			manifest, err = a.decodeLoraDatasetManifest(row.ManifestCipher)
			if err != nil {
				return "", nil, nil, "", false, err
			}
		}
		for _, item := range manifest.Images {
			if item.AssetID == asset.ID {
				return row.Name, row.ManifestCipher, nil, item.ID, true, nil
			}
		}
		manifest.Images = append(manifest.Images, domain.LoraDatasetImage{ID: imageID, AssetID: asset.ID})
		cipher, _, err := a.encryptLoraDatasetManifest(manifest)
		ids := make([]string, 0, len(manifest.Images))
		for _, item := range manifest.Images {
			ids = append(ids, item.AssetID)
		}
		return manifest.Settings.Name, cipher, ids, imageID, false, err
	})
	if errors.Is(err, store.ErrQuickGenerationForbidden) {
		writeGenerationError(w, http.StatusForbidden, "Доступ к обучению отключён.")
		return
	}
	if errors.Is(err, store.ErrGalleryImportConflict) {
		writeGenerationError(w, http.StatusConflict, "Этот запрос уже связан с другим переносом.")
		return
	}
	if err != nil {
		writeLoraDatasetError(w, err)
		return
	}
	a.writeGalleryDatasetImport(w, r, receipt)
}
