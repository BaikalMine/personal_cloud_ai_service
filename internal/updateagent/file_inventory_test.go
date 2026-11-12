package updateagent

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-access-gateway/internal/updates"
)

func TestComfyFileInventoryPagesAndReadOnly(t *testing.T) {
	root := t.TempDir()
	var bytes int64
	for i := 0; i < 225; i++ {
		dir := filepath.Join(root, fmt.Sprintf("group-%d", i%3))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		payload := []byte(fmt.Sprintf("synthetic file %d", i))
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%03d.png", i)), payload, 0600); err != nil {
			t.Fatal(err)
		}
		bytes += int64(len(payload))
	}
	target := ComfyTarget{OutputDirectory: root}
	query := updates.ComfyFileInventoryQuery{Kind: "output", Limit: 50}
	seen := map[string]bool{}
	for {
		result, err := ComfyFileInventory(context.Background(), target, query)
		if err != nil || updates.ValidateComfyFileInventory(result, query) != nil || !result.Complete || result.TotalItems != 225 || result.TotalFiles != 225 || result.LogicalBytes != bytes {
			t.Fatal(result, err)
		}
		for _, item := range result.Items {
			if seen[item.ID] || item.EntryType != "file" || !item.SizeKnown {
				t.Fatal("duplicate or invalid observation", item)
			}
			seen[item.ID] = true
		}
		if result.NextCursor == "" {
			break
		}
		query.Cursor, query.Snapshot = result.NextCursor, result.Snapshot
	}
	if len(seen) != 225 {
		t.Fatal("pagination lost files", len(seen))
	}
	query.Cursor, query.Snapshot, query.Search = "", "", "FILE-01"
	result, err := ComfyFileInventory(context.Background(), target, query)
	if err != nil || result.MatchedItems != 10 || len(result.Items) != 10 || result.TotalFiles != 225 {
		t.Fatal(result, err)
	}
	actual, err := os.ReadFile(filepath.Join(root, "group-0", "file-000.png"))
	if err != nil || string(actual) != "synthetic file 0" {
		t.Fatal("inventory changed file bytes", err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 3 {
		t.Fatal("inventory wrote a journal", entries)
	}
}

func TestComfyFileInventoryRejectsChangedPages(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	query := updates.ComfyFileInventoryQuery{Kind: "input", Limit: 1}
	target := ComfyTarget{InputDirectory: root}
	first, err := ComfyFileInventory(context.Background(), target, query)
	if err != nil || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	query.Cursor, query.Snapshot = first.NextCursor, first.Snapshot
	query.Search = "a"
	if _, err := ComfyFileInventory(context.Background(), target, query); !errors.Is(err, updates.ErrComfyInventoryChanged) {
		t.Fatal("accepted changed search with old cursor", err)
	}
	query.Search = ""
	if err := os.WriteFile(filepath.Join(root, "b"), []byte("new bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ComfyFileInventory(context.Background(), target, query); !errors.Is(err, updates.ErrComfyInventoryChanged) {
		t.Fatal(err)
	}
	query.Cursor, query.Snapshot = "", ""
	next, err := ComfyFileInventory(context.Background(), target, query)
	if err != nil || next.Snapshot == first.Snapshot {
		t.Fatal(next, err)
	}
}

func TestComfyFileInventoryRootsLinksAndBounds(t *testing.T) {
	ctx := context.Background()
	query := updates.ComfyFileInventoryQuery{Kind: "output", Limit: 100}
	root, outside := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		target ComfyTarget
		issue  string
	}{
		{ComfyTarget{}, "root_rejected"},
		{ComfyTarget{OutputDirectory: filepath.Join(root, "missing")}, "root_missing"},
		{ComfyTarget{OutputDirectory: root}, ""},
	} {
		result, err := ComfyFileInventory(ctx, tc.target, query)
		if err != nil || result.Issue != tc.issue || updates.ValidateComfyFileInventory(result, query) != nil {
			t.Fatal(result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "private.bin"), []byte("outside-only"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err == nil {
		result, err := ComfyFileInventory(ctx, ComfyTarget{OutputDirectory: root}, query)
		if err != nil || len(result.Items) != 1 || result.Items[0].EntryType != "link" || result.LogicalBytes != 0 {
			t.Fatal("followed link", result, err)
		}
		for _, candidate := range []string{link, link + string(filepath.Separator)} {
			result, err = ComfyFileInventory(ctx, ComfyTarget{OutputDirectory: candidate}, query)
			if err != nil || result.Issue != "root_rejected" {
				t.Fatal(result, err)
			}
		}
	} else {
		t.Log("symlink unavailable; platform junction test covers Windows")
	}
	limited := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(limited, name), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := os.OpenRoot(limited)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	scanner := comfyFileScanner{kind: "output", visited: maxComfyInventoryEntries - 1}
	if err := scanner.walk(ctx, directory, "", 0); err != nil || scanner.issue != "scan_limit" || len(scanner.items) != 1 {
		t.Fatal(scanner, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ComfyFileInventory(cancelled, ComfyTarget{OutputDirectory: root}, query); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestComfyFileInventoryHTTP(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "example.txt"), []byte("kept"), 0600); err != nil {
		t.Fatal(err)
	}
	controller := &fakeController{}
	server, err := NewServer(strings.Repeat("t", 64), controller, ComfyTarget{InputDirectory: root})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	base, _ := url.Parse(host.URL + "/ignored?ignored=1")
	client := updates.NewClient(base, strings.Repeat("t", 64))
	result, err := client.ComfyFileInventory(context.Background(), updates.ComfyFileInventoryQuery{Kind: "input", Limit: 50})
	if err != nil || !result.Complete || len(result.Items) != 1 || result.LogicalBytes != 4 {
		t.Fatal(result, err)
	}
	for _, tc := range []struct {
		method, path, token string
		code                int
	}{
		{"GET", "/v1/comfy-asset/inventory", "", 401},
		{"POST", "/v1/comfy-asset/inventory", strings.Repeat("t", 64), 405},
		{"GET", "/v1/comfy-asset/inventory?root=/tmp", strings.Repeat("t", 64), 400},
		{"GET", "/v1/comfy-asset/inventory?kind=output&kind=input", strings.Repeat("t", 64), 400},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatal(tc, w.Code)
		}
	}
}
