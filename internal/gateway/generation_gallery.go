package gateway

import (
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type generationGalleryItemView struct {
	VariantID int64
	Scenario  string
	ModelName string
	Prompt    string
	Seed      int64
	CreatedAt time.Time
	Media     generationMediaView
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
	variants, err := a.generationVariantViews(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "не удалось загрузить галерею", http.StatusInternalServerError)
		return
	}
	items := generationGalleryItems(variants)
	a.render(w, r, "gallery", map[string]any{
		"Title":      "Моя галерея",
		"Items":      items,
		"ImageCount": generationGalleryMediaCount(items, "image"),
		"VideoCount": generationGalleryMediaCount(items, "video"),
	})
}

func generationGalleryItems(variants []generationVariantView) []generationGalleryItemView {
	items := make([]generationGalleryItemView, 0)
	for _, variant := range variants {
		prompt := strings.TrimSpace(variant.Values["positive_prompt"])
		if prompt == "" {
			prompt = "Промт не сохранён"
		}
		for _, media := range variant.Media {
			items = append(items, generationGalleryItemView{
				VariantID: variant.ID,
				Scenario:  generationScenarioLabel(variant.TemplateID),
				ModelName: generationModelLabel(variant.ModelName),
				Prompt:    prompt,
				Seed:      variant.Seed,
				CreatedAt: variant.CreatedAt,
				Media:     media,
			})
		}
	}
	return items
}

func generationGalleryMediaCount(items []generationGalleryItemView, mediaType string) int {
	count := 0
	for _, item := range items {
		if item.Media.MediaType == mediaType {
			count++
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
