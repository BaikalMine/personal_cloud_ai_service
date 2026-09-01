package gateway

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestGenerationLibraryImageUploadRequest(t *testing.T) {
	payload := []byte("stored-image")
	directory := t.TempDir()
	request, err := newGenerationLibraryImageUploadRequestFromReader(
		context.Background(), `..\portrait.png`, bytes.NewReader(payload), directory,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer request.Body.Close()
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	file, header, err := request.FormFile("image")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	actual, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if header.Filename != "portrait.png" || !bytes.Equal(actual, payload) {
		t.Fatalf("image part = (%q, %q)", header.Filename, actual)
	}
	if request.FormValue("type") != "input" || request.FormValue("overwrite") != "true" {
		t.Fatal("ComfyUI upload fields are missing")
	}
	if err := request.Body.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("gallery reuse left spool files=%v err=%v", entries, err)
	}
}

func TestGenerationLibraryArchiveNameIsFlatAndUnique(t *testing.T) {
	used := make(map[string]int)
	if got := generationLibraryArchiveName(`..\portrait.png`, 7, used); got != "portrait.png" {
		t.Fatalf("sanitized archive name = %q", got)
	}
	if got := generationLibraryArchiveName("../portrait.png", 8, used); got != "portrait-2.png" {
		t.Fatalf("duplicate archive name = %q", got)
	}
	if got := generationLibraryArchiveName("..", 9, used); got != "result-9" {
		t.Fatalf("parent archive name = %q", got)
	}
}

func TestGenerationGalleryTemplateRendersMediaAndRepeatAction(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	items := []generationGalleryItemView{
		{
			VariantID: 42, TemplateID: "text-to-image", WorkflowID: "photoflow-krea2", WorkflowName: "Krea2 · текст в изображение",
			Scenario: "Текст в изображение", ModelName: "Krea2", Prompt: "Портрет в студии", Seed: 123,
			CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC), HasMedia: true, CompareCount: 1,
			Media: generationMediaView{ID: 7, URL: "/generate/library/7", Filename: "portrait.png", MediaType: "image", ExpiresUnix: 123456789, Sensitive: true},
		},
		{
			VariantID: 43, TemplateID: "minimax-h3-video", WorkflowID: "minimax-h3-video", WorkflowName: "MiniMax H3 · видео",
			Scenario: "Видео", ModelName: "MiniMax H3", Prompt: "Медленное движение камеры", Seed: 456,
			CreatedAt: time.Date(2026, 8, 31, 12, 5, 0, 0, time.UTC), HasMedia: true, CompareCount: 1,
			Media: generationMediaView{ID: 8, URL: "/generate/library/8", Filename: "clip.mp4", MediaType: "video", ExpiresUnix: 123456790},
		},
	}
	var output bytes.Buffer
	if err := templates.ExecuteTemplate(&output, "gallery", map[string]any{
		"Title": "Моя галерея", "CSRF": "csrf", "AssetVersion": "asset", "Items": items, "ImageCount": 1, "VideoCount": 1,
		"Collections": []domain.GenerationMediaCollection{}, "PinnedCount": 0, "FavoriteCount": 0, "ErrorCount": 0,
		"CanUseImageToImage": true, "CanUseMiniMaxVideo": true, "CanReuseImages": true,
		"Retention": newRetentionPolicyView(Config{}.Retention),
	}); err != nil {
		t.Fatal(err)
	}
	body := output.String()
	for _, wanted := range []string{"data-user-gallery", "/static/gallery.js", "/generate?variant=43", "/generate/library/7", "Контент 18+", "data-media-type=\"video\"", "data-gallery-use-open", "photoflow-flux2-edit", "gallery-lightbox"} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("gallery output does not contain %q", wanted)
		}
	}
}

func TestGenerationGalleryTemplateShowsOnlyAllowedReuseWorkflows(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	item := generationGalleryItemView{
		VariantID: 42, TemplateID: "text-to-image", WorkflowID: "photoflow-krea2", WorkflowName: "Krea2 · текст в изображение",
		Scenario: "Текст в изображение", ModelName: "Krea2", Prompt: "Портрет", CreatedAt: time.Now(), HasMedia: true,
		Media: generationMediaView{ID: 7, URL: "/generate/library/7", Filename: "portrait.png", MediaType: "image", ExpiresUnix: time.Now().Add(time.Hour).UnixMilli()},
	}
	data := map[string]any{
		"Title": "Моя галерея", "CSRF": "csrf", "AssetVersion": "asset", "Items": []generationGalleryItemView{item},
		"Collections": []domain.GenerationMediaCollection{}, "CanUseImageToImage": false, "CanUseMiniMaxVideo": true, "CanReuseImages": true,
		"Retention": newRetentionPolicyView(Config{}.Retention),
	}
	var output bytes.Buffer
	if err := templates.ExecuteTemplate(&output, "gallery", data); err != nil {
		t.Fatal(err)
	}
	body := output.String()
	if !strings.Contains(body, "minimax-h3-video") || strings.Contains(body, "photoflow-krea2-edit") || strings.Contains(body, "photoflow-flux2-edit") {
		t.Fatalf("reuse workflow permissions were not applied: %s", body)
	}

	data["CanUseMiniMaxVideo"] = false
	data["CanReuseImages"] = false
	output.Reset()
	if err := templates.ExecuteTemplate(&output, "gallery", data); err != nil {
		t.Fatal(err)
	}
	body = output.String()
	if strings.Contains(body, "data-gallery-use-open") || strings.Contains(body, "id=\"gallery-use-dialog\"") {
		t.Fatal("reuse controls rendered without an available image workflow")
	}
}

func TestGenerationGalleryItemsFlattensMedia(t *testing.T) {
	variants := []generationVariantView{
		{ID: 1, TemplateID: "image-to-image", ModelName: `MiniMaxH3\\model.safetensors`, Seed: 11, Values: map[string]string{"positive_prompt": "  Изменить фон  "}, Media: []generationMediaView{{ID: 1, MediaType: "image"}, {ID: 2, MediaType: "image"}}},
		{ID: 2, TemplateID: "minimax-h3-video", ModelName: "", Seed: 12, Values: map[string]string{}, Media: []generationMediaView{{ID: 3, MediaType: "video"}}},
	}
	items := generationGalleryItems(variants)
	if len(items) != 3 {
		t.Fatalf("gallery item count = %d, want 3", len(items))
	}
	if items[0].Scenario != "Фото и промт" || items[0].ModelName != "model" || items[0].Prompt != "Изменить фон" {
		t.Fatalf("first gallery item = %#v", items[0])
	}
	if items[2].Scenario != "Видео" || items[2].ModelName != "Модель не указана" || items[2].Prompt != "Промт не сохранён" {
		t.Fatalf("video gallery item = %#v", items[2])
	}
	if generationGalleryMediaCount(items, "image") != 2 || generationGalleryMediaCount(items, "video") != 1 {
		t.Fatal("gallery media counts are incorrect")
	}
}
