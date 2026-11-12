package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/thumbnail"
)

type taskThumbnailIdentity struct {
	kind, id, reference string
	sensitive           bool
}

type taskThumbnailSource struct {
	preview domain.TaskPreview
	asset   *domain.LoraDatasetAsset
	key     string
}

func taskPreviewReference(kind string, p *domain.TaskPreview) string {
	if p == nil {
		return ""
	}
	switch kind {
	case "generation":
		if p.MediaID > 0 {
			return "media:" + strconv.FormatInt(p.MediaID, 10)
		}
	case "lora_caption":
		if p.AssetID != "" {
			return "asset:" + p.AssetID
		}
	case "lora_training":
		if p.SnapshotID != "" && len(p.SnapshotHash) == 64 {
			return "snapshot:" + p.SnapshotID + ":" + p.SnapshotHash
		}
	}
	return ""
}

func taskThumbnailURL(task domain.Task) string {
	ref := taskPreviewReference(task.Kind, task.Preview)
	if ref == "" {
		return ""
	}
	sensitive := "0"
	if task.Preview.Sensitive {
		sensitive = "1"
	}
	return "/api/tasks/thumbnail?" + url.Values{"kind": {task.Kind}, "id": {task.ID}, "ref": {ref}, "sensitive": {sensitive}}.Encode()
}

func parseTaskThumbnailIdentity(raw string) (taskThumbnailIdentity, bool) {
	var identity taskThumbnailIdentity
	q, err := url.ParseQuery(raw)
	if err != nil || len(q) != 4 {
		return identity, false
	}
	for _, key := range []string{"kind", "id", "ref", "sensitive"} {
		if len(q[key]) != 1 || q.Get(key) == "" {
			return identity, false
		}
	}
	identity.kind, identity.id, identity.reference = q.Get("kind"), q.Get("id"), q.Get("ref")
	if (identity.kind != "generation" && identity.kind != "lora_training" && identity.kind != "lora_caption") || len(identity.id) > 200 || len(identity.reference) > 512 {
		return identity, false
	}
	for _, ch := range identity.id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return identity, false
		}
	}
	if q.Get("sensitive") != "0" && q.Get("sensitive") != "1" {
		return identity, false
	}
	identity.sensitive = q.Get("sensitive") == "1"
	return identity, true
}

func (a *App) taskThumbnailCache() *thumbnail.Cache {
	a.taskThumbnailsOnce.Do(func() { a.taskThumbnails = thumbnail.NewCache() })
	return a.taskThumbnails
}

// Re-read the account and source on every call. Cached pixels are never evidence
// of ownership, current source-family access, classification or retention.
func (a *App) loadTaskThumbnailSource(ctx context.Context, userID int64, identity taskThumbnailIdentity) (taskThumbnailSource, error) {
	var source taskThumbnailSource
	user, err := a.store.UserByID(ctx, userID)
	if err != nil {
		return source, err
	}
	if user.Disabled || user.AccountExpiresAt.Valid && !user.AccountExpiresAt.Time.After(time.Now()) {
		return source, sql.ErrNoRows
	}
	p, err := a.store.TaskPreview(ctx, taskAccessScope(&user), identity.kind, identity.id)
	if err != nil {
		return source, err
	}
	if taskPreviewReference(identity.kind, p) != identity.reference || p.Sensitive != identity.sensitive || !p.ExpiresAt.After(time.Now()) {
		return source, sql.ErrNoRows
	}
	if identity.kind == "generation" && !user.CanUseQuickGenerationType(p.TemplateID) {
		return source, sql.ErrNoRows
	}
	source.preview = *p
	source.key = fmt.Sprintf("v1:%d:%s:%s:%s:%t", userID, identity.kind, identity.id, identity.reference, p.Sensitive)
	switch identity.kind {
	case "lora_caption":
		asset, err := a.store.LoraDatasetAsset(ctx, userID, p.AssetID)
		if err != nil {
			return source, err
		}
		source.asset = &asset
	case "lora_training":
		snapshot, err := a.store.LoraDatasetSnapshot(ctx, userID, p.SnapshotID)
		if err != nil {
			return source, err
		}
		if snapshot.Hash != p.SnapshotHash {
			return source, sql.ErrNoRows
		}
		manifest, err := a.decodeLoraDatasetManifest(snapshot.ManifestCipher)
		if err != nil {
			return source, sql.ErrNoRows
		}
		encoded, err := json.Marshal(manifest)
		if err != nil {
			return source, err
		}
		hash := sha256.Sum256(encoded)
		clear(encoded)
		if hex.EncodeToString(hash[:]) != snapshot.Hash {
			return source, sql.ErrNoRows
		}
		ids := make([]string, 0, len(manifest.Images))
		for _, item := range manifest.Images {
			if !item.Excluded {
				ids = append(ids, item.AssetID)
			}
		}
		asset, err := a.store.TaskTrainingPreviewAsset(ctx, userID, snapshot.ID, ids)
		if err != nil {
			return source, err
		}
		source.asset = &asset
	}
	if source.asset != nil {
		if source.asset.SizeBytes <= 0 || source.asset.SizeBytes > maxLoraTrainingImageBytes {
			return source, sql.ErrNoRows
		}
		source.key += ":" + source.asset.ID + ":" + source.asset.Hash
	}
	return source, nil
}

func (a *App) produceTaskThumbnail(ctx context.Context, userID int64, source taskThumbnailSource) ([]byte, error) {
	var materialized *materializedContentMedia
	var mime string
	var err error
	if source.asset != nil {
		mime = source.asset.MIMEType
		materialized, err = a.materializeLoraDatasetAsset(ctx, *source.asset)
	} else {
		media, loadErr := a.store.ContentMediaByIDForUser(ctx, source.preview.MediaID, userID)
		if loadErr != nil {
			return nil, loadErr
		}
		if media.SizeBytes <= 0 || media.SizeBytes > thumbnail.MaxInputBytes || len(media.PayloadCipher) > thumbnail.MaxInputBytes+1024 {
			return nil, thumbnail.ErrLimit
		}
		var inline bool
		mime, inline = safeAdminMediaType(media.MediaType, media.MIMEType)
		if !inline {
			return nil, thumbnail.ErrUnsupported
		}
		materialized, err = a.materializeContentMedia(ctx, media)
	}
	if err != nil {
		return nil, err
	}
	defer materialized.Close()
	return thumbnail.Render(ctx, materialized.path, mime)
}

func (a *App) handleTaskThumbnail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	user := a.currentUser(r)
	if user == nil {
		http.Error(w, "authentication_required", http.StatusUnauthorized)
		return
	}
	identity, valid := parseTaskThumbnailIdentity(r.URL.RawQuery)
	if !valid || a.store == nil || a.contentCipher == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	source, err := a.loadTaskThumbnailSource(ctx, user.ID, identity)
	if err != nil {
		a.taskThumbnailError(w, r, err)
		return
	}
	data, err := a.taskThumbnailCache().Get(ctx, source.key, source.preview.ExpiresAt, func(ctx context.Context) ([]byte, error) { return a.produceTaskThumbnail(ctx, user.ID, source) })
	if err != nil {
		a.taskThumbnailError(w, r, err)
		return
	}
	// A deletion, permission change or classification update during native work
	// must not leak a now-inaccessible derivative, including after a cache hit.
	current, err := a.loadTaskThumbnailSource(ctx, user.ID, identity)
	if err != nil {
		a.taskThumbnailError(w, r, err)
		return
	}
	if current.key != source.key || ctx.Err() != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *App) taskThumbnailError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, thumbnail.ErrUnsupported) || errors.Is(err, thumbnail.ErrInvalid) || errors.Is(err, thumbnail.ErrLimit) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, thumbnail.ErrBusy) || errors.Is(err, errMediaMemoryBudget) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "thumbnail_busy", http.StatusTooManyRequests)
		return
	}
	http.Error(w, "thumbnail_unavailable", http.StatusServiceUnavailable)
}
