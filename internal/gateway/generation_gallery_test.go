package gateway

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestGenerationGalleryTemplateRendersMediaAndRepeatAction(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	items := []generationGalleryItemView{
		{
			VariantID: 42,
			Scenario:  "Текст в изображение",
			ModelName: "Krea2",
			Prompt:    "Портрет в студии",
			Seed:      123,
			CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
			Media:     generationMediaView{ID: 7, URL: "/generate/library/7", Filename: "portrait.png", MediaType: "image", ExpiresUnix: 123456789, Sensitive: true},
		},
		{
			VariantID: 43,
			Scenario:  "Видео",
			ModelName: "MiniMax H3",
			Prompt:    "Медленное движение камеры",
			Seed:      456,
			CreatedAt: time.Date(2026, 8, 31, 12, 5, 0, 0, time.UTC),
			Media:     generationMediaView{ID: 8, URL: "/generate/library/8", Filename: "clip.mp4", MediaType: "video", ExpiresUnix: 123456790},
		},
	}
	var output bytes.Buffer
	if err := templates.ExecuteTemplate(&output, "gallery", map[string]any{
		"Title": "Моя галерея", "CSRF": "csrf", "AssetVersion": "asset", "Items": items, "ImageCount": 1, "VideoCount": 1,
	}); err != nil {
		t.Fatal(err)
	}
	body := output.String()
	for _, wanted := range []string{"data-user-gallery", "/static/gallery.js", "/generate?variant=42", "/generate/library/7", "Контент 18+", "data-media-type=\"video\"", "gallery-lightbox"} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("gallery output does not contain %q", wanted)
		}
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
