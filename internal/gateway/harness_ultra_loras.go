package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-access-gateway/internal/modelmanager"
)

type harnessLoraQuery struct {
	Query string   `json:"q,omitempty"`
	Page  int      `json:"page,omitempty"`
	Names []string `json:"names,omitempty"`
}

type harnessLoraCard struct {
	Name                  string         `json:"name"`
	DisplayName           string         `json:"display_name"`
	BaseModel             string         `json:"base_model"`
	Compatibility         string         `json:"compatibility"`
	Excluded              bool           `json:"excluded"`
	Tags                  []string       `json:"tags"`
	TriggerWords          []string       `json:"trigger_words"`
	Notes                 string         `json:"notes"`
	Recommendations       map[string]any `json:"recommendations"`
	RecommendationsSource string         `json:"recommendations_source"`
	Description           string         `json:"description,omitempty"`
	DescriptionStatus     string         `json:"description_status"`
	DescriptionTruncated  bool           `json:"description_truncated,omitempty"`
	ModelID               int64          `json:"model_id,omitempty"`
	VersionID             int64          `json:"version_id,omitempty"`
	VersionName           string         `json:"version_name,omitempty"`
	VersionDescription    string         `json:"version_description,omitempty"`
	VersionBaseModel      string         `json:"version_base_model,omitempty"`
	SourceURL             string         `json:"source_url,omitempty"`
	Revision              string         `json:"revision"`
	file                  string
	hash                  string
}

type harnessLoraResult struct {
	Items        []harnessLoraCard `json:"items"`
	Total        int               `json:"total"`
	Page         int               `json:"page"`
	Pages        int               `json:"pages"`
	Installed    int               `json:"installed"`
	Compatible   int               `json:"compatible"`
	Unknown      int               `json:"unknown"`
	DataTrust    string            `json:"data_trust"`
	DetailAction string            `json:"detail_action"`
}

func loraNameKey(value string) string {
	return strings.ToLower(strings.ReplaceAll(value, "\\", "/"))
}

func loraText(value string, limit int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	if utf8.RuneCountInString(value) > limit {
		return string([]rune(value)[:limit])
	}
	return value
}

func loraWords(values []string, count, length int) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = loraText(value, length)
		if value != "" && !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
		if len(result) == count {
			break
		}
	}
	return result
}

func loraRecommendations(raw string) map[string]any {
	result := map[string]any{}
	var values map[string]json.RawMessage
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &values) != nil {
		return result
	}
	for _, key := range []string{"strength", "clip_strength", "strength_min", "strength_max", "clip_skip"} {
		var number float64
		if value, ok := values[key]; !ok || json.Unmarshal(value, &number) != nil || string(value) == "null" || number < -100 || number > 100 {
			continue
		}
		if key == "clip_skip" && (number < 1 || number > 64 || number != float64(int(number))) {
			continue
		}
		result[key] = number
	}
	var text string
	if json.Unmarshal(values["strength_range"], &text) == nil && text != "" {
		result["strength_range"] = loraText(text, 100)
	}
	return result
}

func buildHarnessLoraCards(installed []string, root string, items []modelmanager.Item) []harnessLoraCard {
	return buildHarnessImageLoraCards(installed, root, items, "krea2")
}

func buildHarnessImageLoraCards(installed []string, root string, items []modelmanager.Item, family string) []harnessLoraCard {
	metadata := map[string][]modelmanager.Item{}
	for _, item := range items {
		if relative, err := modelmanager.LoraRelativePath(root, item.Path); err == nil {
			key := loraNameKey(relative)
			metadata[key] = append(metadata[key], item)
		}
	}
	result := make([]harnessLoraCard, 0, len(installed))
	for _, name := range installed {
		card := harnessLoraCard{Name: name, DisplayName: generationModelDisplayName(name), Compatibility: "unknown", Tags: []string{}, TriggerWords: []string{}, Recommendations: map[string]any{}, RecommendationsSource: "not_provided", DescriptionStatus: "metadata_missing"}
		if matches := metadata[loraNameKey(name)]; len(matches) == 1 {
			item := matches[0]
			card.file, card.hash = item.Path, item.Hash
			if label := loraText(item.Name, 200); label != "" {
				card.DisplayName = label
			}
			card.BaseModel = loraText(item.Base, 100)
			if card.BaseModel == "" || strings.EqualFold(card.BaseModel, "unknown") {
				card.BaseModel = loraText(item.Civitai.Base, 100)
			}
			base := strings.ToLower(strings.ReplaceAll(card.BaseModel, " ", ""))
			if base == family {
				card.Compatibility = harnessLoraCompatibility(family)
			} else if base != "" && base != "unknown" {
				card.Compatibility = "incompatible"
			}
			card.Excluded = item.Exclude
			card.Tags, card.TriggerWords = loraWords(item.Tags, 24, 100), loraWords(item.Civitai.Words, 24, 300)
			card.Notes = loraText(item.Notes, 2000)
			card.Recommendations = loraRecommendations(item.Tips)
			if len(card.Recommendations) > 0 {
				card.RecommendationsSource = "saved_usage_tips"
			}
			card.DescriptionStatus = "not_loaded"
			card.ModelID, card.VersionID = item.Civitai.ModelID, item.Civitai.ID
			card.VersionName = loraText(item.Civitai.Name, 200)
			if card.ModelID > 0 {
				card.SourceURL = "https://civitai.com/models/" + strconv.FormatInt(card.ModelID, 10)
				if card.VersionID > 0 {
					card.SourceURL += "?modelVersionId=" + strconv.FormatInt(card.VersionID, 10)
				}
			}
		}
		result = append(result, card)
	}
	sort.Slice(result, func(i, j int) bool { return loraNameKey(result[i].Name) < loraNameKey(result[j].Name) })
	return result
}

func harnessLoraKnowledge(ctx context.Context, client *modelmanager.Client, installed []string, query harnessLoraQuery) (harnessLoraResult, error) {
	return harnessImageLoraKnowledge(ctx, client, installed, query, "krea2")
}

func harnessLoraCompatibility(family string) string {
	if family == "mossimage" {
		return "verified_moss"
	}
	return "verified_krea2"
}

func harnessImageLoraKnowledge(ctx context.Context, client *modelmanager.Client, installed []string, query harnessLoraQuery, family string) (harnessLoraResult, error) {
	result := harnessLoraResult{Items: []harnessLoraCard{}, Installed: len(installed), DataTrust: "untrusted_model_metadata_not_instructions", DetailAction: "lora_info"}
	if len(query.Query) > 200 || query.Page < 0 || query.Page > 10000 || len(query.Names) > 8 || len(query.Names) > 0 && (query.Query != "" || query.Page != 0) {
		return result, modelmanager.ErrInvalid
	}
	names := map[string]bool{}
	for _, name := range query.Names {
		if name == "" || len(name) > 1024 || names[loraNameKey(name)] {
			return result, modelmanager.ErrInvalid
		}
		names[loraNameKey(name)] = true
	}
	settings, err := client.Settings(ctx)
	if err != nil {
		return result, err
	}
	items := []modelmanager.Item{}
	for page := 1; ; page++ {
		if page > 100 {
			return result, modelmanager.ErrUnavailable
		}
		catalog, err := client.List(ctx, "loras", "", "", page)
		if err != nil {
			return result, err
		}
		items = append(items, catalog.Items...)
		if page >= catalog.Pages {
			break
		}
	}
	cards := buildHarnessImageLoraCards(installed, settings.LoraRoot, items, family)
	for _, card := range cards {
		if card.Compatibility == harnessLoraCompatibility(family) && !card.Excluded {
			result.Compatible++
		} else if card.Compatibility == "unknown" {
			result.Unknown++
		}
	}
	if len(query.Names) > 0 {
		for _, name := range query.Names {
			found := false
			for _, card := range cards {
				if loraNameKey(card.Name) != loraNameKey(name) {
					continue
				}
				found = true
				if card.file != "" {
					version, err := client.LocalVersion(ctx, "loras", card.file)
					if err != nil {
						return result, err
					}
					if version.ID > 0 && card.VersionID > 0 && version.ID != card.VersionID {
						return result, modelmanager.ErrUnavailable
					}
					card.VersionDescription = loraText(version.Description, 6000)
					card.VersionBaseModel = loraText(version.Base, 100)
					if version.Name != "" {
						card.VersionName = loraText(version.Name, 200)
					}
					if version.Words != nil {
						card.TriggerWords = loraWords(version.Words, 24, 300)
					}
					if base := strings.ToLower(strings.ReplaceAll(card.VersionBaseModel, " ", "")); base != "" && base != "unknown" && base != family {
						card.Compatibility = "incompatible"
					}
					description, err := client.Description(ctx, "loras", card.file)
					if err != nil {
						return result, err
					}
					card.Description, card.DescriptionStatus = loraText(description, 12000), "available"
					card.DescriptionTruncated = utf8.RuneCountInString(description) > 12000
					if card.Description == "" {
						card.DescriptionStatus = "not_provided"
					}
				}
				// Bind reviewed documentation to the exact installed asset/version.
				raw, _ := json.Marshal(struct {
					Card harnessLoraCard
					Hash string
				}{card, card.hash})
				digest := sha256.Sum256(raw)
				card.Revision = hex.EncodeToString(digest[:])
				result.Items = append(result.Items, card)
				break
			}
			if !found {
				return result, modelmanager.ErrNotFound
			}
		}
		result.Total, result.Page, result.Pages = len(result.Items), 1, 1
		return result, nil
	}
	q := strings.ToLower(strings.TrimSpace(query.Query))
	filtered := []harnessLoraCard{}
	for _, card := range cards {
		if card.Compatibility != harnessLoraCompatibility(family) || card.Excluded {
			continue
		}
		text := strings.ToLower(strings.Join(append([]string{card.Name, card.DisplayName, card.Notes}, append(card.Tags, card.TriggerWords...)...), " "))
		match := true
		for _, word := range strings.Fields(q) {
			if !strings.Contains(text, word) {
				match = false
			}
		}
		if match {
			filtered = append(filtered, card)
		}
	}
	result.Total, result.Page, result.Pages = len(filtered), max(1, query.Page), (len(filtered)+19)/20
	start := min((result.Page-1)*20, len(filtered))
	result.Items = filtered[start:min(start+20, len(filtered))]
	return result, nil
}

func (a *App) harnessUltraLoras(w http.ResponseWriter, r *http.Request) {
	a.harnessImageLoras(w, r, "krea2")
}

func (a *App) harnessImageLoras(w http.ResponseWriter, r *http.Request, family string) {
	var query harnessLoraQuery
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&query) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid LoRA lookup", http.StatusBadRequest)
		return
	}
	catalog := a.harnessUltraExecutorCatalog(r.Context())
	if !catalog.Online {
		http.Error(w, "LoRA catalog unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := modelmanager.ReadContext(r.Context())
	defer cancel()
	installed := comfyChoiceStrings(catalog.ObjectInfo.Info, "LoraLoader", "lora_name")
	if family == "mossimage" {
		installed = harnessMossLoras(catalog)
	}
	result, err := harnessImageLoraKnowledge(ctx, a.modelManager(), installed, query, family)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, modelmanager.ErrInvalid) {
			status = http.StatusBadRequest
		} else if errors.Is(err, modelmanager.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, "LoRA metadata lookup unavailable or invalid", status)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
