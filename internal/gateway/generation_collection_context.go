package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type generationCollectionContextView struct {
	ID         int64                      `json:"id"`
	Name       string                     `json:"name"`
	Revision   int64                      `json:"revision"`
	HasContext bool                       `json:"has_context"`
	SavedAt    *time.Time                 `json:"saved_at,omitempty"`
	Values     map[string]string          `json:"values"`
	Assets     []generationDraftAssetView `json:"assets"`
}

func generationCollectionID(values url.Values) (int64, error) {
	entries := values["collection_id"]
	if len(entries) == 0 || len(entries) == 1 && strings.TrimSpace(entries[0]) == "" {
		return 0, nil
	}
	if len(entries) != 1 {
		return 0, store.ErrGenerationCollectionInvalid
	}
	id, err := strconv.ParseInt(strings.TrimSpace(entries[0]), 10, 64)
	if err != nil || id < 0 {
		return 0, store.ErrGenerationCollectionInvalid
	}
	return id, nil
}

// Collection context is reusable input, not a replay of a prior submission.
func (a *App) generationCollectionPayload(cipher []byte) (generationDraftPayload, error) {
	plain, err := a.contentCipher.Decrypt(cipher)
	if err != nil {
		return generationDraftPayload{}, err
	}
	var source generationDraftPayload
	if len(plain) > maxGenerationDraftPayload || json.Unmarshal([]byte(plain), &source) != nil || source.Version != 1 || source.Values == nil {
		return generationDraftPayload{}, errors.New("invalid saved collection format")
	}
	values := make(url.Values)
	for name, value := range source.Values {
		allowed := allowedGenerationRecipeField(name) || allowedGenerationReferenceJobField(name)
		switch name {
		case "quality_preset", "draft_step", "draft_advanced", "batch_enabled", "batch_mode", "batch_count", "batch_parameter", "batch_from", "batch_to":
			allowed = true
		}
		if allowed && !strings.HasPrefix(name, "lora_verification_") {
			values.Set(name, value)
		}
	}
	filtered, err := generationDraftValues(values)
	if err != nil {
		return generationDraftPayload{}, err
	}
	if len(source.Assets) > len(generationDraftAssetFields) {
		return generationDraftPayload{}, errors.New("too many collection assets")
	}
	seen := make(map[string]bool)
	for _, asset := range source.Assets {
		valid := false
		for _, field := range generationDraftAssetFields {
			valid = valid || asset.Field == field
		}
		if !valid || seen[asset.Field] || len(asset.ID) > 96 || len(asset.Name) > 1024 {
			return generationDraftPayload{}, errors.New("invalid collection asset")
		}
		seen[asset.Field] = true
	}
	return generationDraftPayload{Version: 1, Values: filtered, Assets: source.Assets}, nil
}

func (a *App) generationCollectionView(ctx context.Context, row domain.GenerationCollectionContext) (generationCollectionContextView, error) {
	view := generationCollectionContextView{ID: row.Collection.ID, Name: row.Collection.Name, Revision: row.Collection.Revision,
		HasContext: len(row.PayloadCipher) > 0, SavedAt: row.ContextSavedAt, Values: map[string]string{}, Assets: []generationDraftAssetView{}}
	if !view.HasContext {
		return view, nil
	}
	draft, err := a.generationDraftView(ctx, domain.GenerationDraftRow{UserID: row.UserID, PayloadCipher: row.PayloadCipher})
	if err != nil {
		return generationCollectionContextView{}, err
	}
	view.Values, view.Assets = draft.Values, draft.Assets
	return view, nil
}

func writeGenerationCollectionError(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "collection_failed", "Не удалось обработать коллекцию. Обновите её состояние перед повтором."
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, store.ErrGenerationCollectionMissing):
		status, code, message = http.StatusNotFound, "collection_missing", "Коллекция недоступна."
	case errors.Is(err, store.ErrGenerationCollectionConflict):
		status, code, message = http.StatusConflict, "collection_conflict", "Коллекция изменена в другой вкладке. Обновите её перед сохранением."
	case errors.Is(err, store.ErrGenerationDraftConflict):
		status, code, message = http.StatusConflict, "draft_conflict", "Черновик изменён. Проверьте актуальную версию перед продолжением."
	case errors.Is(err, store.ErrGenerationCollectionName):
		status, code, message = http.StatusConflict, "collection_name_conflict", "Коллекция с таким названием уже существует."
	case errors.Is(err, store.ErrGenerationCollectionInvalid):
		status, code, message = http.StatusBadRequest, "collection_invalid", "Проверьте название и версию коллекции."
	}
	writeJSON(w, status, map[string]any{"error": message, "code": code})
}

func (a *App) handleGenerationCollectionContext(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	values := r.URL.Query()
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		if !a.validCSRF(r) {
			writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
			return
		}
		if err := r.ParseForm(); err != nil {
			writeGenerationCollectionError(w, store.ErrGenerationCollectionInvalid)
			return
		}
		values = r.PostForm
	}
	id, err := generationCollectionID(values)
	if err != nil || id == 0 {
		writeGenerationCollectionError(w, store.ErrGenerationCollectionInvalid)
		return
	}
	userID := a.currentUser(r).ID
	row, err := a.store.GenerationCollectionContext(r.Context(), userID, id)
	if err != nil {
		writeGenerationCollectionError(w, err)
		return
	}
	if r.Method == http.MethodPost {
		revision, err := strconv.ParseInt(values.Get("collection_revision"), 10, 64)
		if err != nil || revision <= 0 {
			writeGenerationCollectionError(w, store.ErrGenerationCollectionInvalid)
			return
		}
		if row.Collection.Revision != revision {
			writeGenerationCollectionError(w, store.ErrGenerationCollectionConflict)
			return
		}
		action := values.Get("action")
		if action != "save_draft" && action != "open" && action != "rename" {
			writeGenerationCollectionError(w, store.ErrGenerationCollectionInvalid)
			return
		}
		params := domain.SaveGenerationCollectionContextParams{UserID: userID, CollectionID: id, ExpectedRevision: revision, Name: values.Get("name")}
		if action != "rename" {
			draftRevision, err := strconv.ParseInt(values.Get("draft_revision"), 10, 64)
			if err != nil || draftRevision < 0 || action == "save_draft" && draftRevision == 0 {
				writeGenerationCollectionError(w, store.ErrGenerationCollectionInvalid)
				return
			}
			cipher := row.PayloadCipher
			if action == "save_draft" {
				draft, err := a.store.GenerationDraft(r.Context(), userID)
				if errors.Is(err, sql.ErrNoRows) || err == nil && draft.Revision != draftRevision {
					writeGenerationCollectionError(w, store.ErrGenerationDraftConflict)
					return
				}
				if err != nil {
					writeGenerationCollectionError(w, err)
					return
				}
				cipher = draft.PayloadCipher
				params.ExpectedDraftRevision = draftRevision
			} else if len(cipher) == 0 {
				writeJSON(w, http.StatusConflict, map[string]any{"code": "collection_empty", "error": "В коллекции ещё нет сохранённых настроек."})
				return
			}
			payload, err := a.generationCollectionPayload(cipher)
			if err != nil {
				writeGenerationCollectionError(w, err)
				return
			}
			if action == "open" {
				payload.Values["collection_id"] = strconv.FormatInt(id, 10)
			}
			encoded, err := json.Marshal(payload)
			if err != nil || len(encoded) > maxGenerationDraftPayload {
				writeGenerationCollectionError(w, store.ErrGenerationCollectionInvalid)
				return
			}
			cipher, err = a.contentCipher.Encrypt(string(encoded))
			if err != nil {
				writeGenerationCollectionError(w, err)
				return
			}
			if action == "open" {
				draft, err := a.store.OpenGenerationCollectionDraft(r.Context(), userID, id, revision, draftRevision, cipher)
				if err != nil {
					writeGenerationCollectionError(w, err)
					return
				}
				view, err := a.generationDraftView(r.Context(), draft)
				if err != nil {
					writeGenerationCollectionError(w, err)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"draft": view})
				return
			}
			params.PayloadCipher, params.ReplaceContext = cipher, true
		}
		row, err = a.store.SaveGenerationCollectionContext(r.Context(), params)
		if err != nil {
			writeGenerationCollectionError(w, err)
			return
		}
	}
	view, err := a.generationCollectionView(r.Context(), row)
	if err != nil {
		writeGenerationCollectionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"collection": view})
}
