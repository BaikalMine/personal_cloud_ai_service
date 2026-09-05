package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

const maxGenerationDraftBody = 192 << 10
const maxGenerationDraftPayload = 128 << 10

var generationDraftAssetFields = []string{"input_image", "input_image_2", "input_image_3", "input_image_4", "input_audio", "input_video"}

type generationDraftAsset struct {
	Field string `json:"field"`
	ID    string `json:"id"`
	Name  string `json:"name"`
}

type generationDraftPayload struct {
	Version int                    `json:"version"`
	Values  map[string]string      `json:"values"`
	Assets  []generationDraftAsset `json:"assets"`
}

type generationDraftAssetView struct {
	generationDraftAsset
	Available bool       `json:"available"`
	Value     string     `json:"value,omitempty"`
	URL       string     `json:"url,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type generationDraftView struct {
	Revision  int64                      `json:"revision"`
	Values    map[string]string          `json:"values"`
	Assets    []generationDraftAssetView `json:"assets"`
	UpdatedAt time.Time                  `json:"updated_at"`
	ExpiresAt time.Time                  `json:"expires_at"`
}

func (a *App) registerGenerationDraftRoutes(mux *http.ServeMux, quick func(http.Handler) http.Handler) {
	mux.Handle("/generate/draft", quick(http.HandlerFunc(a.handleGenerationDraft)))
	mux.Handle("/generate/draft/delete", quick(http.HandlerFunc(a.handleDeleteGenerationDraft)))
	mux.Handle("/generate/draft/asset", quick(http.HandlerFunc(a.handleGenerationDraftAsset)))
}

func generationDraftValues(form url.Values) (map[string]string, error) {
	values := make(map[string]string)
	for name, entries := range form {
		allowed := allowedGenerationRecipeField(name) || allowedGenerationReferenceJobField(name)
		switch name {
		case "assistant_requested", "assistant_applied", "assistant_action", "assistant_template_used", "assistant_think_used", "assistant_original_prompt", "assistant_suggestion", "assistant_enabled", "assistant_draft", "assistant_references", "correlation_id", "quality_preset", "draft_step", "draft_advanced", "batch_enabled", "batch_mode", "batch_count", "batch_parameter", "batch_from", "batch_to":
			allowed = true
		}
		if !allowed || len(entries) == 0 {
			continue
		}
		// Drafts preserve unfinished text verbatim, including whitespace and
		// temporarily invalid numeric values. Submission validates separately.
		if len(name) > 100 || len(entries[0]) > 64<<10 {
			return nil, errors.New("поле черновика слишком большое")
		}
		values[name] = entries[0]
	}
	return values, nil
}

func (a *App) generationDraftPayloadFromForm(ctx context.Context, userID int64, form url.Values) (generationDraftPayload, error) {
	values, err := generationDraftValues(form)
	if err != nil {
		return generationDraftPayload{}, err
	}
	payload := generationDraftPayload{Version: 1, Values: values, Assets: []generationDraftAsset{}}
	for index, field := range generationDraftAssetFields {
		id := strings.TrimSpace(form.Get("draft_asset_" + field))
		value := strings.TrimSpace(form.Get(field))
		pendingName := strings.TrimSpace(form.Get("draft_pending_" + field))
		if id == "" && value == "" && pendingName == "" {
			continue
		}
		if pendingName != "" && id == "" && value == "" {
			if len(pendingName) > 1024 {
				return generationDraftPayload{}, errors.New("название материала слишком длинное")
			}
			payload.Assets = append(payload.Assets, generationDraftAsset{Field: field, Name: pendingName})
			continue
		}
		var asset domain.OwnedComfyInputAsset
		if id != "" {
			if len(id) > 96 {
				return generationDraftPayload{}, errors.New("некорректный ID материала")
			}
			asset, err = a.store.ComfyInputAssetForUser(ctx, userID, id)
		} else {
			if err := a.validateGenerationAsset(value, userID, "материал черновика"); err != nil {
				return generationDraftPayload{}, err
			}
			normalized, _ := normalizeComfyDataPath(value, false)
			asset, err = a.store.ComfyInputAssetByPathForUser(ctx, userID, path.Base(normalized), path.Dir(normalized))
			if errors.Is(err, sql.ErrNoRows) {
				return generationDraftPayload{}, fmt.Errorf("материал %s больше недоступен: загрузите его заново", field)
			}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return generationDraftPayload{}, err
		}
		if asset.ID != "" {
			id = asset.ID
		}
		name := strings.TrimSpace(form.Get("draft_name_" + field))
		if name == "" && index < 4 {
			name = form.Get(fmt.Sprintf("image_source_name_%d", index+1))
		}
		if name == "" {
			name = asset.Filename
		}
		if len(name) > 1024 {
			return generationDraftPayload{}, errors.New("название материала слишком длинное")
		}
		payload.Assets = append(payload.Assets, generationDraftAsset{Field: field, ID: id, Name: name})
	}
	return payload, nil
}

func (a *App) generationDraftView(ctx context.Context, row domain.GenerationDraftRow) (generationDraftView, error) {
	plain, err := a.contentCipher.Decrypt(row.PayloadCipher)
	if err != nil {
		return generationDraftView{}, err
	}
	var payload generationDraftPayload
	if err := json.Unmarshal([]byte(plain), &payload); err != nil || payload.Version != 1 || payload.Values == nil {
		return generationDraftView{}, errors.New("неизвестный формат черновика")
	}
	view := generationDraftView{Revision: row.Revision, Values: payload.Values, Assets: []generationDraftAssetView{}, UpdatedAt: row.UpdatedAt, ExpiresAt: row.ExpiresAt}
	for _, reference := range payload.Assets {
		item := generationDraftAssetView{generationDraftAsset: reference}
		asset, err := a.store.ComfyInputAssetForUser(ctx, row.UserID, reference.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return generationDraftView{}, err
		}
		if err == nil {
			item.Available = true
			item.Value = path.Join(asset.Subfolder, asset.Filename)
			item.URL = "/generate/draft/asset?id=" + url.QueryEscape(asset.ID)
			item.ExpiresAt = &asset.ExpiresAt
		}
		view.Assets = append(view.Assets, item)
	}
	return view, nil
}

func (a *App) writeCurrentGenerationDraft(w http.ResponseWriter, r *http.Request, status int) {
	row, err := a.store.GenerationDraft(r.Context(), a.currentUser(r).ID)
	response := map[string]any{"draft": nil, "retention_days": 30}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось прочитать черновик")
		return
	}
	if err == nil {
		view, err := a.generationDraftView(r.Context(), row)
		if err != nil {
			writeGenerationError(w, http.StatusInternalServerError, "не удалось восстановить черновик")
			return
		}
		response["draft"] = view
	}
	if status == http.StatusConflict {
		response["error"] = "черновик изменён на другом устройстве или в другой вкладке"
		response["code"] = "draft_conflict"
	}
	writeJSON(w, status, response)
}

func (a *App) handleGenerationDraft(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == http.MethodGet {
		a.writeCurrentGenerationDraft(w, r, http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGenerationDraftBody)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeGenerationError(w, http.StatusBadRequest, "не удалось прочитать черновик")
		return
	}
	revision, err := strconv.ParseInt(r.Form.Get("draft_revision"), 10, 64)
	if err != nil || revision < 0 {
		writeGenerationError(w, http.StatusBadRequest, "некорректная версия черновика")
		return
	}
	payload, err := a.generationDraftPayloadFromForm(r.Context(), a.currentUser(r).ID, r.Form)
	if err != nil {
		writeGenerationError(w, http.StatusBadRequest, err.Error())
		return
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > maxGenerationDraftPayload {
		writeGenerationError(w, http.StatusBadRequest, "черновик слишком большой")
		return
	}
	cipher, err := a.contentCipher.Encrypt(string(encoded))
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось защитить черновик")
		return
	}
	row, err := a.store.SaveGenerationDraft(r.Context(), a.currentUser(r).ID, revision, cipher)
	if errors.Is(err, store.ErrGenerationDraftConflict) {
		a.writeCurrentGenerationDraft(w, r, http.StatusConflict)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось сохранить черновик")
		return
	}
	view, err := a.generationDraftView(r.Context(), row)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "черновик записан, но не удалось прочитать его материалы")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": view})
}

func (a *App) handleDeleteGenerationDraft(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeGenerationError(w, http.StatusBadRequest, "не удалось прочитать запрос")
		return
	}
	revision, err := strconv.ParseInt(r.Form.Get("draft_revision"), 10, 64)
	if err != nil || revision <= 0 {
		writeGenerationError(w, http.StatusBadRequest, "некорректная версия черновика")
		return
	}
	err = a.store.DeleteGenerationDraft(r.Context(), a.currentUser(r).ID, revision)
	if errors.Is(err, store.ErrGenerationDraftConflict) {
		a.writeCurrentGenerationDraft(w, r, http.StatusConflict)
		return
	}
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось удалить черновик")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (a *App) handleGenerationDraftAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	asset, err := a.store.ComfyInputAssetForUser(r.Context(), a.currentUser(r).ID, r.URL.Query().Get("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "не удалось прочитать материал", http.StatusInternalServerError)
		return
	}
	if a.cfg.ComfyUIUpstream == nil {
		http.Error(w, "хранилище материалов временно недоступно", http.StatusServiceUnavailable)
		return
	}
	streamed, err := a.streamGenerationOutput(w, r, generationOutput{Filename: asset.Filename, Subfolder: asset.Subfolder, Type: "input", MediaType: "image"})
	if err != nil && !streamed {
		http.Error(w, "материал временно недоступен", http.StatusBadGateway)
	}
}
