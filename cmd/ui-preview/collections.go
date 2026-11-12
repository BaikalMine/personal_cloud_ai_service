package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"ai-access-gateway/internal/domain"
)

type previewWorkingCollection struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Revision   int64             `json:"revision"`
	HasContext bool              `json:"has_context"`
	SavedAt    *time.Time        `json:"saved_at,omitempty"`
	Values     map[string]string `json:"values"`
	Assets     []map[string]any  `json:"assets"`
}

func newPreviewGenerationState(now time.Time) *previewGenerationState {
	asset := map[string]any{"field": "input_image", "id": "preview-collection-source", "name": "Портрет для серии.png", "available": true, "value": "preview/portrait.png", "url": "/generate/draft/asset?id=preview-collection-source", "expires_at": now.Add(72 * time.Hour)}
	return &previewGenerationState{assets: map[string]map[string]any{"preview-collection-source": asset}, nextCollection: 13, collections: map[int64]*previewWorkingCollection{
		11: {ID: 11, Name: "Портреты", Revision: 1, HasContext: true, SavedAt: &now, Values: map[string]string{"template_id": "text-to-image", "generation_workflow": "photoflow-krea2", "positive_prompt": "Кинематографичный портрет в тихом ночном городе, естественная кожа и мягкий свет витрин.", "lora_1": "Krea2-realism-V2.safetensors", "lora_model_strength_1": "0.7"}, Assets: []map[string]any{asset}},
		12: {ID: 12, Name: "Для видео", Revision: 2, Values: map[string]string{}, Assets: []map[string]any{}},
	}}
}

func (s *previewGenerationStore) collectionList(w http.ResponseWriter, r *http.Request) []domain.GenerationMediaCollection {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.session(w, r)
	items := []domain.GenerationMediaCollection{}
	for _, row := range state.collections {
		count := int64(0)
		if row.ID == 11 {
			count = 3
		}
		if row.ID == 12 {
			count = 2
		}
		items = append(items, domain.GenerationMediaCollection{ID: row.ID, Name: row.Name, Revision: row.Revision, ItemCount: count})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func servePreviewCollection(w http.ResponseWriter, r *http.Request, state *previewGenerationState, revision *int64, now time.Time) {
	respond := func(status int, body any) { w.WriteHeader(status); _ = json.NewEncoder(w).Encode(body) }
	fail := func(status int, code, message string) {
		respond(status, map[string]any{"code": code, "error": message})
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		fail(405, "method", "Метод недоступен")
		return
	}
	if err := r.ParseForm(); err != nil {
		fail(400, "invalid", "Некорректный запрос")
		return
	}
	if r.URL.Path == "/generate/library/collections" {
		if r.Method == http.MethodGet {
			items := []*previewWorkingCollection{}
			for _, value := range state.collections {
				items = append(items, value)
			}
			sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
			respond(200, map[string]any{"collections": items})
			return
		}
		name := strings.TrimSpace(r.Form.Get("name"))
		if name == "" || utf8.RuneCountInString(name) > 80 {
			fail(400, "collection_invalid", "Проверьте название коллекции")
			return
		}
		for _, value := range state.collections {
			if strings.EqualFold(value.Name, name) {
				respond(201, map[string]any{"collection": value})
				return
			}
		}
		if len(state.collections) >= 40 {
			fail(400, "collection_limit", "Достигнут лимит коллекций")
			return
		}
		*revision++
		value := &previewWorkingCollection{ID: state.nextCollection, Name: name, Revision: *revision, Values: map[string]string{}, Assets: []map[string]any{}}
		state.nextCollection++
		state.collections[value.ID] = value
		respond(201, map[string]any{"collection": value})
		return
	}
	id, err := strconv.ParseInt(r.Form.Get("collection_id"), 10, 64)
	row := state.collections[id]
	if err != nil || row == nil {
		fail(404, "collection_missing", "Коллекция недоступна.")
		return
	}
	if r.URL.Path == "/generate/library/collections/delete" {
		if r.Method != http.MethodPost {
			fail(405, "method", "Метод недоступен")
			return
		}
		delete(state.collections, id)
		respond(200, map[string]any{"deleted": true})
		return
	}
	if r.Method == http.MethodGet {
		respond(200, map[string]any{"collection": row})
		return
	}
	expected, _ := strconv.ParseInt(r.Form.Get("collection_revision"), 10, 64)
	if expected != row.Revision {
		fail(409, "collection_conflict", "Коллекция изменена. Обновите её состояние.")
		return
	}
	action := r.Form.Get("action")
	name := strings.TrimSpace(r.Form.Get("name"))
	if action == "save_draft" || action == "rename" {
		if name == "" || utf8.RuneCountInString(name) > 80 {
			fail(400, "collection_invalid", "Проверьте название коллекции")
			return
		}
		for _, other := range state.collections {
			if other.ID != id && strings.EqualFold(other.Name, name) {
				fail(409, "collection_name_conflict", "Коллекция с таким названием уже существует.")
				return
			}
		}
	}
	if action == "open" || action == "save_draft" {
		current := int64(0)
		if state.draft != nil {
			current = state.draft["revision"].(int64)
		}
		draftRevision, parseErr := strconv.ParseInt(r.Form.Get("draft_revision"), 10, 64)
		if parseErr != nil || draftRevision != current || action == "save_draft" && current == 0 {
			fail(409, "draft_conflict", "Черновик изменён. Проверьте актуальную версию.")
			return
		}
		if action == "open" {
			if !row.HasContext {
				fail(409, "collection_empty", "В коллекции ещё нет сохранённых настроек.")
				return
			}
			values := map[string]string{}
			for name, value := range row.Values {
				values[name] = value
			}
			values["collection_id"] = strconv.FormatInt(id, 10)
			*revision++
			state.draft = map[string]any{"revision": *revision, "values": values, "assets": row.Assets, "updated_at": now, "expires_at": now.Add(30 * 24 * time.Hour)}
			respond(200, map[string]any{"draft": state.draft})
			return
		}
		row.Values = map[string]string{}
		for name, value := range state.draft["values"].(map[string]string) {
			if name == "collection_id" || name == "parent_job_id" || name == "client_request_id" || name == "correlation_id" || strings.HasPrefix(name, "assistant_") || strings.HasPrefix(name, "lora_verification_") {
				continue
			}
			row.Values[name] = value
		}
		row.Assets = []map[string]any{}
		for _, source := range state.draft["assets"].([]map[string]any) {
			copy := map[string]any{}
			for name, value := range source {
				copy[name] = value
			}
			if source["available"] == true {
				copy["url"] = "/generate/draft/asset?id=" + url.QueryEscape(source["id"].(string))
			}
			row.Assets = append(row.Assets, copy)
		}
		row.HasContext = true
		row.SavedAt = &now
	} else if action != "rename" {
		fail(400, "collection_invalid", "Действие недоступно")
		return
	}
	row.Name = name
	*revision++
	row.Revision = *revision
	respond(200, map[string]any{"collection": row})
}
