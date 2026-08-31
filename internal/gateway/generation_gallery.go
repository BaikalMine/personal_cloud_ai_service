package gateway

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
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

type generationPickerImageView struct {
	ID          int64  `json:"id"`
	URL         string `json:"url"`
	Filename    string `json:"filename"`
	ModelName   string `json:"model_name"`
	CreatedUnix int64  `json:"created_unix"`
	ExpiresUnix int64  `json:"expires_unix"`
	Sensitive   bool   `json:"sensitive"`
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

func (a *App) handleReuseGenerationLibraryImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if !a.validCSRF(r) {
		writeGenerationError(w, http.StatusForbidden, "проверка безопасности не пройдена")
		return
	}
	mediaID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("media_id")), 10, 64)
	if err != nil || mediaID <= 0 || a.store == nil || a.contentCipher == nil {
		http.NotFound(w, r)
		return
	}
	user := a.currentUser(r)
	media, err := a.store.ContentMediaByIDForUser(r.Context(), mediaID, user.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if media.MediaType != "image" {
		writeGenerationError(w, http.StatusBadRequest, "для видеореференса можно выбрать только изображение")
		return
	}
	payload, err := a.contentCipher.DecryptBytes(media.PayloadCipher)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось подготовить изображение из галереи")
		return
	}
	if len(payload) == 0 || int64(len(payload)) > maxComfyUploadBody-(1<<20) {
		writeGenerationError(w, http.StatusRequestEntityTooLarge, "изображение из галереи слишком большое")
		return
	}
	uploadRequest, err := newGenerationLibraryImageUploadRequest(r.Context(), media.OriginalName, payload)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось подготовить изображение из галереи")
		return
	}
	uploadRequest.RemoteAddr = r.RemoteAddr
	uploadRequest.Host = r.Host
	uploadRequest.Header.Set("User-Agent", r.UserAgent())
	a.quickGenerationUploadHandler().ServeHTTP(w, uploadRequest)
}

func (a *App) handleGenerationLibraryImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	if a.store == nil {
		writeGenerationError(w, http.StatusServiceUnavailable, "галерея временно недоступна")
		return
	}
	user := a.currentUser(r)
	media, err := a.store.ListUserGenerationImages(r.Context(), user.ID, 100)
	if err != nil {
		writeGenerationError(w, http.StatusInternalServerError, "не удалось загрузить изображения")
		return
	}
	items := make([]generationPickerImageView, 0, len(media))
	for _, item := range media {
		items = append(items, generationPickerImageView{
			ID: item.ID, URL: "/generate/library/" + strconv.FormatInt(item.ID, 10), Filename: item.OriginalName,
			ModelName: generationModelLabel(item.ModelName), CreatedUnix: item.CreatedAt.UnixMilli(),
			ExpiresUnix: item.ExpiresAt.UnixMilli(), Sensitive: item.Sensitive || item.VisualPending,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"images": items})
}

func newGenerationLibraryImageUploadRequest(ctx context.Context, filename string, payload []byte) (*http.Request, error) {
	filename = filepath.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/"))
	if filename == "" || filename == "." {
		filename = "gallery-image.png"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", filename)
	if err != nil {
		return nil, fmt.Errorf("create image part: %w", err)
	}
	if _, err := part.Write(payload); err != nil {
		return nil, fmt.Errorf("write image part: %w", err)
	}
	if err := writer.WriteField("type", "input"); err != nil {
		return nil, fmt.Errorf("write upload type: %w", err)
	}
	if err := writer.WriteField("overwrite", "true"); err != nil {
		return nil, fmt.Errorf("write overwrite flag: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish image upload: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/generate/upload/image", bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.ContentLength = int64(body.Len())
	return request, nil
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
