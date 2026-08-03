package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestNormalizeComfyDataPath(t *testing.T) {
	for _, value := range []string{"workflows/example.json", "templates/team/demo.json", "a-b_c.1"} {
		if got, err := normalizeComfyDataPath(value, false); err != nil || got != value {
			t.Fatalf("normalizeComfyDataPath(%q) = %q, %v", value, got, err)
		}
	}
	for _, value := range []string{"", "/absolute", "../secret", "workflows/../secret", "workflows//file", "a\\b", "line\r\nbreak"} {
		if _, err := normalizeComfyDataPath(value, false); err == nil {
			t.Fatalf("unsafe path %q was accepted", value)
		}
	}
	if got, err := normalizeComfyDataPath("", true); err != nil || got != "" {
		t.Fatalf("empty root path = %q, %v", got, err)
	}
}

func TestComfyUserDataRoutePreservesEncodedSubpaths(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/userdata/workflows%2Fteam%2Fdemo.json", nil)
	source, destination, move, err := comfyUserDataRoute(request)
	if err != nil || move || source != "workflows/team/demo.json" || destination != "" {
		t.Fatalf("file route = (%q,%q,%v,%v)", source, destination, move, err)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/userdata/workflows%2Fold.json/move/workflows%2Fnew.json", nil)
	source, destination, move, err = comfyUserDataRoute(request)
	if err != nil || !move || source != "workflows/old.json" || destination != "workflows/new.json" {
		t.Fatalf("move route = (%q,%q,%v,%v)", source, destination, move, err)
	}
}

func TestComfyV2EntriesScopesRequestedDirectory(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	entries := []domain.ComfyUserDataEntry{
		{Path: "workflows/root.json", Size: 10, ModifiedAt: now},
		{Path: "workflows/team/nested.json", Size: 20, ModifiedAt: now},
		{Path: "templates/hidden.json", Size: 30, ModifiedAt: now},
	}
	result, foundDirectory, foundFile := comfyV2Entries(entries, "workflows")
	if !foundDirectory || foundFile {
		t.Fatalf("directory flags = directory:%v file:%v", foundDirectory, foundFile)
	}
	if len(result) != 3 {
		t.Fatalf("result count = %d, want directory plus two files", len(result))
	}
	for _, entry := range result {
		if entry["path"] == "templates/hidden.json" {
			t.Fatal("entry outside requested directory leaked")
		}
	}

	_, foundDirectory, foundFile = comfyV2Entries(entries, "workflows/root.json")
	if foundDirectory || !foundFile {
		t.Fatalf("file flags = directory:%v file:%v", foundDirectory, foundFile)
	}
}
