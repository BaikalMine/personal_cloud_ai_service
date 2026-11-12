package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestTaskPreviewAdapter(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	for _, test := range []struct {
		kind    string
		preview *domain.TaskPreview
		url     string
	}{
		{"generation", &domain.TaskPreview{MediaID: 42, Sensitive: true, ExpiresAt: expires}, "/api/tasks/thumbnail?id=test&kind=generation&ref=media%3A42&sensitive=1"},
		{"lora_caption", &domain.TaskPreview{AssetID: "asset/a", ExpiresAt: expires}, "/api/tasks/thumbnail?id=test&kind=lora_caption&ref=asset%3Aasset%2Fa&sensitive=0"},
		{"lora_training", &domain.TaskPreview{SnapshotID: "version", SnapshotHash: strings.Repeat("a", 64), ExpiresAt: expires}, "/api/tasks/thumbnail?id=test&kind=lora_training&ref=snapshot%3Aversion%3A" + strings.Repeat("a", 64) + "&sensitive=0"},
		{"lora_training", &domain.TaskPreview{MediaID: 42}, ""},
		{"generation", &domain.TaskPreview{AssetID: "wrong-kind"}, ""},
		{"lora_caption", nil, ""},
	} {
		view := makeTaskView(domain.Task{Kind: test.kind, ID: "test", Preview: test.preview})
		if test.url == "" {
			if view.Preview != nil {
				t.Fatalf("unexpected preview: %+v", view)
			}
		} else if view.Preview == nil || view.Preview.URL != test.url || view.Preview.ExpiresUnix != expires.UnixMilli() || view.Preview.Sensitive != test.preview.Sensitive {
			t.Fatalf("preview adapter: %+v", view.Preview)
		}
		data, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"MediaID", "AssetID", "SnapshotID", "SnapshotHash", "wrong-kind"} {
			if strings.Contains(string(data), private) {
				t.Fatalf("internal preview field in response: %s", data)
			}
		}
	}
}
