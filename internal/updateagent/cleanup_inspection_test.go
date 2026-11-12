package updateagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/updates"
)

func TestCleanupInspectionNeverModifiesFiles(t *testing.T) {
	directory := t.TempDir()
	payload := []byte("original bytes")
	digest := sha256.Sum256(payload)
	item := updates.ComfyAssetFile{Filename: "matched.png", StorageType: "output", SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	path := filepath.Join(directory, item.Filename)
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	missing, mismatch, rejected := item, item, item
	missing.Filename, mismatch.SizeBytes, rejected.Filename = "missing.png", item.SizeBytes+1, "file:stream"
	result, err := InspectComfyAssetFiles(context.Background(), ComfyTarget{OutputDirectory: directory}, []updates.ComfyAssetFile{item, missing, mismatch, rejected})
	if err != nil || !result.Complete || result.Protocol != 1 || result.CheckedAt.IsZero() || len(result.Items) != 4 {
		t.Fatal(result, err)
	}
	for i, status := range []string{"matched", "missing", "mismatched", "rejected"} {
		if result.Items[i].Status != status {
			t.Fatal(result.Items)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("inspection changed bytes", string(got), err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("inspection replaced/modified file", err)
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 1 {
		t.Fatal("inspection created files", entries, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := InspectComfyAssetFiles(ctx, ComfyTarget{OutputDirectory: directory}, []updates.ComfyAssetFile{item}); err == nil || result.Complete || len(result.Items) != 0 {
		t.Fatal("cancelled inspection reported complete", result, err)
	}
}

func TestCleanupInspectionEndpointIsSeparateAndAuthenticated(t *testing.T) {
	controller := &fakeController{}
	token := strings.Repeat("x", 32)
	directory := t.TempDir()
	payload := []byte("keep this file")
	digest := sha256.Sum256(payload)
	if err := os.WriteFile(filepath.Join(directory, "input.png"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	item := updates.ComfyAssetFile{Filename: "input.png", StorageType: "input", SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}
	server, err := NewServer(token, controller, ComfyTarget{InputDirectory: directory})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(updates.ComfyAssetDeleteRequest{Files: []updates.ComfyAssetFile{item}})
	for _, tc := range []struct {
		method, token, body string
		want                int
	}{
		{http.MethodPost, "", string(body), 401},
		{http.MethodGet, token, string(body), 405},
		{http.MethodPost, token, string(body) + ` {}`, 400},
		{http.MethodPost, token, `{"files":[],"dry_run":true}`, 400},
		{http.MethodPost, token, string(body), 200},
	} {
		r := httptest.NewRequest(tc.method, "/v1/comfy-asset/inspect", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(w.Code, tc.want, w.Body.String())
		}
		if tc.want == 200 {
			var result updates.ComfyAssetInspectResult
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Complete || len(result.Items) != 1 || result.Items[0].Status != "matched" {
				t.Fatal(result, err)
			}
		}
		if got, err := os.ReadFile(filepath.Join(directory, "input.png")); err != nil || !bytes.Equal(got, payload) {
			t.Fatal("HTTP inspection modified file", err)
		}
	}
	if len(controller.last.Components) != 0 {
		t.Fatal("inspection invoked update controller")
	}
}

type cancellingCleanupReader struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancellingCleanupReader) Read(p []byte) (int, error) {
	r.reads++
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = 'x'
	r.cancel()
	return 1, nil
}

func TestCleanupInspectionHashCancellationAndChangedEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &cancellingCleanupReader{cancel: cancel}
	n, err := io.Copy(io.Discard, comfyCleanupReader{ctx: ctx, reader: source})
	if !errors.Is(err, context.Canceled) || n != 1 || source.reads != 1 {
		t.Fatal(n, err, source.reads)
	}
	directory := t.TempDir()
	filename := filepath.Join(directory, "file.png")
	payload := []byte("unchanged bytes")
	if err := os.WriteFile(filename, payload, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	if matched, err := hasExpectedSHA256(context.Background(), root, "file.png", hash, before); err != nil || !matched {
		t.Fatal(matched, err)
	}
	if matched, err := hasExpectedSHA256(ctx, root, "file.png", hash, before); !errors.Is(err, context.Canceled) || matched {
		t.Fatal(matched, err)
	}
	changedTime := before.ModTime().Add(-time.Hour)
	if err := os.Chtimes(filename, changedTime, changedTime); err != nil {
		t.Fatal(err)
	}
	if matched, err := hasExpectedSHA256(context.Background(), root, "file.png", hash, before); err != nil || matched {
		t.Fatal("changed identity accepted despite matching content", matched, err)
	}
}
