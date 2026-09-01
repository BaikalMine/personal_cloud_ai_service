package gateway

import (
	"encoding/json"
	"testing"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
)

func TestParseSuggestionLinksRejectsUnsafeTargets(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
		bad   bool
	}{
		{name: "public links", input: "https://huggingface.co/model\nhttps://github.com/org/project", want: 2},
		{name: "deduplicates", input: "https://example.com\nhttps://example.com", want: 1},
		{name: "loopback", input: "http://127.0.0.1/model", bad: true},
		{name: "private lan", input: "http://192.168.1.10/file", bad: true},
		{name: "credentials", input: "https://user:pass@example.com/file", bad: true},
		{name: "too many", input: "https://a.example\nhttps://b.example\nhttps://c.example\nhttps://d.example", bad: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			links, err := parseSuggestionLinks(test.input)
			if (err != nil) != test.bad {
				t.Fatalf("parseSuggestionLinks() error = %v, want bad=%v", err, test.bad)
			}
			if !test.bad && len(links) != test.want {
				t.Fatalf("links = %#v, want %d", links, test.want)
			}
		})
	}
}

func TestVirusTotalStatus(t *testing.T) {
	if got := virusTotalStatus("in_progress"); got != "in-progress" {
		t.Fatalf("in_progress = %q", got)
	}
	if got := virusTotalStatus("other"); got != "queued" {
		t.Fatalf("other = %q", got)
	}
}

func TestSuggestionScanRecordsKeepsStableSourceIndexes(t *testing.T) {
	if scans := suggestionScanRecords(nil, ""); len(scans) != 0 {
		t.Fatalf("text-only suggestion scans=%+v, want none", scans)
	}
	scans := suggestionScanRecords([]string{"https://one.example", "https://two.example"}, "workflow.json")
	if len(scans) != 3 || scans[0].Kind != "url" || scans[0].SourceIndex != 0 || scans[1].SourceIndex != 1 || scans[2].Kind != "json" || scans[2].SourceIndex != 0 {
		t.Fatalf("unexpected suggestion scans: %+v", scans)
	}
}

func TestFeatureSuggestionViewSeparatesLifecycleFromScanDetails(t *testing.T) {
	cipher, err := contentcrypto.NewCipher("feature-suggestion-view-test")
	if err != nil {
		t.Fatal(err)
	}
	description, _ := cipher.Encrypt("Добавить workflow для нескольких референсов")
	linksRaw, _ := json.Marshal([]string{"https://example.com/workflow"})
	links, _ := cipher.Encrypt(string(linksRaw))
	comment, _ := cipher.Encrypt("Проверим совместимость вручную.")
	app := &App{contentCipher: cipher}
	view, err := app.featureSuggestionView(domain.FeatureSuggestionRow{
		ID: 7, Kind: "workflow", Title: "Новый workflow", DescriptionCipher: description, LinksCipher: links,
		JSONName: "workflow.json", JSONSizeBytes: 128, Status: "review", ScanStatus: "flagged", ReviewCommentCipher: comment,
		Scans: []domain.FeatureSuggestionScan{
			{Kind: "url", SourceName: "Ссылка 1", SourceIndex: 0, Status: "completed", Harmless: 70},
			{Kind: "json", SourceName: "workflow.json", SourceIndex: 0, Status: "completed", Malicious: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.KindLabel != "Workflow" || view.StatusLabel != "На рассмотрении" || len(view.Links) != 1 || !view.Links[0].Safe {
		t.Fatalf("unexpected suggestion view: %+v", view)
	}
	if view.CanAccept || !view.CanReject || !view.CanRetry || view.CanDownloadJSON {
		t.Fatalf("unsafe review actions are wrong: accept=%v reject=%v retry=%v download=%v", view.CanAccept, view.CanReject, view.CanRetry, view.CanDownloadJSON)
	}
	if view.ReviewComment != "Проверим совместимость вручную." {
		t.Fatalf("review comment=%q", view.ReviewComment)
	}
}

func TestFeatureSuggestionWorkerReadsOnlyTheIndexedSavedSource(t *testing.T) {
	cipher, err := contentcrypto.NewCipher("feature-suggestion-source-test")
	if err != nil {
		t.Fatal(err)
	}
	linksRaw, _ := json.Marshal([]string{"https://one.example/model", "https://two.example/model"})
	linksCipher, _ := cipher.Encrypt(string(linksRaw))
	jsonPayload := []byte(`{"workflow":{"1":{"class_type":"LoadImage"}}}`)
	jsonCipher, _ := cipher.EncryptBytes(jsonPayload)
	app := &App{contentCipher: cipher}
	row := domain.FeatureSuggestionRow{LinksCipher: linksCipher, JSONName: "workflow.json", JSONCipher: jsonCipher, JSONSizeBytes: int64(len(jsonPayload))}
	target, err := app.featureSuggestionURL(row, 1)
	if err != nil || target != "https://two.example/model" {
		t.Fatalf("indexed URL=%q err=%v", target, err)
	}
	payload, err := app.featureSuggestionJSON(row, 0)
	if err != nil || string(payload) != string(jsonPayload) {
		t.Fatalf("saved JSON=%q err=%v", payload, err)
	}
	row.JSONSizeBytes++
	if _, err := app.featureSuggestionJSON(row, 0); err == nil {
		t.Fatal("JSON with mismatched stored size must be rejected")
	}
}
