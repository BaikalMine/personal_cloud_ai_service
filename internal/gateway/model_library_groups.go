package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"ai-access-gateway/internal/modelmanager"
)

const modelCatalogPageSize = 24
const modelCatalogMaxPages = 100

// Read every upstream page before grouping: versions may fall on different pages.
func allModelCatalogItems(ctx context.Context, client *modelmanager.Client, kind, search, base string, updates bool) ([]modelmanager.Item, error) {
	var items []modelmanager.Item
	for page := 1; page <= modelCatalogMaxPages; page++ {
		result, err := client.ListFiltered(ctx, kind, search, base, page, updates)
		if err != nil {
			return nil, err
		}
		if result.Pages < 0 || result.Pages > modelCatalogMaxPages || (result.Page != 0 && result.Page != page) {
			return nil, errors.New("invalid catalog pagination")
		}
		if result.Pages == 0 && result.Total > len(result.Items) {
			return nil, errors.New("incomplete catalog pagination")
		}
		items = append(items, result.Items...)
		if result.Pages <= page {
			return items, nil
		}
	}
	return nil, errors.New("model catalog exceeds page limit")
}

func (a *App) handleGroupedModelCatalog(w http.ResponseWriter, ctx context.Context, client *modelmanager.Client, owner int64, kind, search, base string, page int, updates bool) {
	if page < 1 || page > 10000 {
		http.Error(w, "Проверьте номер страницы.", http.StatusBadRequest)
		return
	}
	items, err := allModelCatalogItems(ctx, client, kind, "", "", false)
	if err != nil {
		http.Error(w, "Не удалось загрузить полный каталог моделей.", http.StatusBadGateway)
		return
	}
	var matches map[string]bool
	var matchedModels map[int64]bool
	if search != "" || base != "" || updates {
		filtered, filterErr := allModelCatalogItems(ctx, client, kind, search, base, updates)
		if filterErr != nil {
			http.Error(w, "Не удалось применить фильтры каталога.", http.StatusBadGateway)
			return
		}
		matches, matchedModels = make(map[string]bool), make(map[int64]bool)
		for _, item := range filtered {
			matches[item.Path] = true
			if item.Civitai.ModelID > 0 {
				matchedModels[item.Civitai.ModelID] = true
			}
		}
	}
	groups := make([][]modelmanager.Item, 0)
	byID := make(map[int64]int)
	seen := make(map[string]bool)
	for _, item := range items {
		if item.Path == "" || seen[item.Path] {
			continue
		}
		seen[item.Path] = true
		id := item.Civitai.ModelID
		if matches != nil && !matches[item.Path] && (id <= 0 || !matchedModels[id]) {
			continue
		}
		if id > 0 {
			if i, ok := byID[id]; ok {
				groups[i] = append(groups[i], item)
				continue
			}
			byID[id] = len(groups)
		}
		groups = append(groups, []modelmanager.Item{item})
	}
	total := len(groups)
	pages := (total + modelCatalogPageSize - 1) / modelCatalogPageSize
	if pages == 0 {
		pages = 1
	}
	start := (page - 1) * modelCatalogPageSize
	if start > total {
		start = total
	}
	end := start + modelCatalogPageSize
	if end > total {
		end = total
	}
	output := make([]map[string]any, 0, end-start)
	for _, group := range groups[start:end] {
		versions := make([]map[string]any, 0, len(group))
		for _, item := range group {
			token, tokenErr := a.modelToken(modelLibraryToken{Scope: "item", Owner: owner, Expires: time.Now().Add(24 * time.Hour).Unix(), Kind: kind, Path: item.Path, Preview: item.Preview, ModelID: item.Civitai.ModelID, SHA256: item.Hash, Size: item.Size})
			if tokenErr != nil {
				http.Error(w, "Не удалось подготовить каталог.", http.StatusInternalServerError)
				return
			}
			versions = append(versions, map[string]any{"name": item.Name, "filename": item.Filename, "base": item.Base, "size": item.Size, "hash": item.Hash, "tags": item.Tags, "update": item.Update, "version_id": item.Civitai.ID, "version_name": item.Civitai.Name, "model_id": item.Civitai.ModelID, "words": item.Civitai.Words, "token": token, "preview": item.Preview != "", "mature": item.NSFW > 1})
		}
		card := make(map[string]any, len(versions[0])+1)
		for k, v := range versions[0] {
			card[k] = v
		}
		for _, item := range group {
			if item.Update {
				card["update"] = true
				break
			}
		}
		card["versions"] = versions
		output = append(output, card)
	}
	modelJSON(w, map[string]any{"items": output, "total": total, "page": page, "pages": pages})
}
