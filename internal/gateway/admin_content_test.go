package gateway

import (
	"bytes"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestAdminContentRendersEveryMediaItem(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Title": "AI content",
		"Events": []ContentEventView{{
			ID: 1, Username: "alice", Prompt: "prompt", MediaCount: 2,
			Media: []domain.ContentMediaSummary{
				{ID: 11, EventID: 1, MediaType: "image"},
				{ID: 12, EventID: 1, MediaType: "video"},
			},
		}},
	}
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "admin_content", data); err != nil {
		t.Fatal(err)
	}
	html := rendered.String()
	for _, expected := range []string{
		`/admin/content/media/11`,
		`/admin/content/media/12`,
		`preload="none"`,
		`rel="noopener"`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("rendered gallery does not contain %q", expected)
		}
	}
}
