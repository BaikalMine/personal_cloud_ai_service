package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

func (a *App) generationJobSourceAssets(ctx context.Context, userID int64, values map[string]string) ([]generationDraftAssetView, bool, error) {
	assets := []generationDraftAssetView{}
	missing := false
	for index, field := range generationDraftAssetFields {
		value := strings.TrimSpace(values[field])
		delete(values, field)
		if value == "" {
			continue
		}
		item := generationDraftAssetView{generationDraftAsset: generationDraftAsset{Field: field, Name: "Исходный материал"}}
		if index < maxGenerationReferenceSlots {
			item.Name = strings.TrimSpace(values[fmt.Sprintf("image_source_name_%d", index+1)])
		}
		if err := a.validateGenerationAsset(value, userID, "исходный материал"); err == nil {
			normalized, _ := normalizeComfyDataPath(value, false)
			if item.Name == "" || item.Name == "Исходный материал" {
				item.Name = path.Base(normalized)
			}
			asset, err := a.store.ComfyInputAssetByPathForUser(ctx, userID, path.Base(normalized), path.Dir(normalized))
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, false, err
			}
			if err == nil {
				item.ID, item.Available = asset.ID, true
				item.Value = path.Join(asset.Subfolder, asset.Filename)
				item.URL = "/generate/draft/asset?id=" + url.QueryEscape(asset.ID)
				item.ExpiresAt = &asset.ExpiresAt
			}
		}
		if item.Name == "" {
			item.Name = "Исходный материал"
		}
		missing = missing || !item.Available
		assets = append(assets, item)
	}
	return assets, missing, nil
}
