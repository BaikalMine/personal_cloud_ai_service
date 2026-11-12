package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/modelmanager"
)

func TestGroupedCatalogAcrossPagesAndFilteredVersions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		filtered := r.URL.Query().Get("search") != ""
		items := []map[string]any{}
		if page == "1" {
			items = append(items, map[string]any{"model_name": "one", "file_path": "one-v1", "civitai": map[string]any{"modelId": 7, "id": 11}})
			if !filtered {
				items = append(items, map[string]any{"model_name": "unknown", "file_path": "unknown", "civitai": map[string]any{}})
			}
		} else if !filtered {
			items = append(items, map[string]any{"model_name": "one", "file_path": "one-v2", "civitai": map[string]any{"modelId": 7, "id": 12}}, map[string]any{"model_name": "other", "file_path": "other", "civitai": map[string]any{"modelId": 8}})
		}
		pages := 2
		if filtered {
			pages = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "page": map[string]int{"1": 1, "2": 2}[page], "total_pages": pages})
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	cipher, _ := contentcrypto.NewCipher(strings.Repeat("s", 32))
	app := &App{contentCipher: cipher}
	client := &modelmanager.Client{Base: base}
	for _, tc := range []struct {
		search string
		total  int
	}{{"", 3}, {"one", 1}} {
		w := httptest.NewRecorder()
		app.handleGroupedModelCatalog(w, context.Background(), client, 42, "loras", tc.search, "", 1, false)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var result struct {
			Total int `json:"total"`
			Items []struct {
				Versions []struct {
					Token string `json:"token"`
				} `json:"versions"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Total != tc.total || len(result.Items[0].Versions) != 2 {
			t.Fatalf("wrong groups: %+v", result)
		}
		first, err1 := app.readModelToken(result.Items[0].Versions[0].Token, "item", 42)
		second, err2 := app.readModelToken(result.Items[0].Versions[1].Token, "item", 42)
		if err1 != nil || err2 != nil || first.Path != "one-v1" || second.Path != "one-v2" {
			t.Fatalf("wrong version tokens: %+v %+v", first, second)
		}
	}
}

func TestGroupedCatalogPaginatesGroupsNotFiles(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		items := make([]map[string]any, 0)
		for i := (page-1)*24 + 1; i <= page*24 && i <= 26; i++ {
			id := i
			if i == 25 {
				id = 1
			}
			items = append(items, map[string]any{"model_name": "same name", "file_path": strconv.Itoa(i), "civitai": map[string]any{"modelId": id}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "page": page, "total_pages": 2})
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	cipher, _ := contentcrypto.NewCipher(strings.Repeat("s", 32))
	app := &App{contentCipher: cipher}
	for _, tc := range []struct{ page, count int }{{1, 24}, {2, 1}} {
		w := httptest.NewRecorder()
		app.handleGroupedModelCatalog(w, context.Background(), &modelmanager.Client{Base: base}, 42, "checkpoints", "", "", tc.page, false)
		var result struct {
			Total int               `json:"total"`
			Pages int               `json:"pages"`
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Total != 25 || result.Pages != 2 || len(result.Items) != tc.count {
			t.Fatalf("incorrect group pagination on page %d: %+v, %v", tc.page, result, err)
		}
	}
}

func TestGroupedCatalogRejectsPartialUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			http.Error(w, "unavailable", 502)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "page": 1, "total_pages": 2})
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	w := httptest.NewRecorder()
	(&App{}).handleGroupedModelCatalog(w, context.Background(), &modelmanager.Client{Base: base}, 42, "loras", "", "", 1, false)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("partial catalog accepted: %d", w.Code)
	}
}
