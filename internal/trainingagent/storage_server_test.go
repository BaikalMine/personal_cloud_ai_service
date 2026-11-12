package trainingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"ai-access-gateway/internal/loratraining"
)

func TestTrainingStorageHTTPWorkflow(t *testing.T) {
	controller := storageControllerFixture(t)
	token := strings.Repeat("s", 32)
	server, err := NewServer(token, controller)
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	base, _ := url.Parse(host.URL)
	client := loratraining.NewClient(base, token)
	ctx := context.Background()
	page, err := client.StorageInventory(ctx, "", 100)
	if err != nil || !page.Complete {
		t.Fatal(page, err)
	}
	cache := storageItemForTest(t, controller, "training_cache")
	preview, err := client.PreviewStorageCleanup(ctx, []string{cache.ID})
	if err != nil || !preview.Complete || len(preview.Items) != 1 || !preview.Items[0].CanCleanup {
		t.Fatal(preview, err)
	}
	result, err := client.CleanupStorage(ctx, cleanupRequestForTest("7", preview.Items[0]))
	if err != nil || result.State != "completed" || result.Items[0].FreedBytes != cache.SizeBytes {
		t.Fatal(result, err)
	}
	retained, err := client.StorageCleanupResult(ctx, result.ID)
	if err != nil || !reflect.DeepEqual(result, retained) {
		t.Fatal(retained, result, err)
	}
	activity, err := client.Activity(ctx)
	if err != nil || activity.State != "idle" || activity.StorageCleanupActive {
		t.Fatal(activity, err)
	}
}

func TestTrainingStorageHTTPRejectsUnauthorizedOrAmbiguousWrites(t *testing.T) {
	controller := storageControllerFixture(t)
	token := strings.Repeat("s", 32)
	server, err := NewServer(token, controller)
	if err != nil {
		t.Fatal(err)
	}
	cache := storageItemForTest(t, controller, "training_cache")
	valid, _ := json.Marshal(cleanupRequestForTest("8", cache))
	before := storageFileTreeForTest(t, controller.config.RootDir)
	for _, tc := range []struct {
		method, path, body string
		auth               bool
		want               int
	}{
		{"GET", "/v1/storage", "", false, 401},
		{"POST", "/v1/storage/preview", `{"ids":["` + cache.ID + `"]}`, false, 401},
		{"POST", "/v1/storage/cleanup", string(valid), false, 401},
		{"GET", "/v1/storage/operations/" + strings.Repeat("8", 32), "", false, 401},
		{"POST", "/v1/storage/cleanup", string(valid) + ` {}`, true, 400},
		{"POST", "/v1/storage/cleanup", `{"id":"` + strings.Repeat("8", 32) + `","items":[],"path":"C:/Windows"}`, true, 400},
		{"POST", "/v1/storage/preview", `{"ids":["training_cache:../outside"]}`, true, 400},
		{"POST", "/v1/storage/preview", `{"ids":["` + cache.ID + `","` + cache.ID + `"]}`, true, 400},
		{"POST", "/v1/storage/preview", `{"ids":["training_cache:unknown"]}`, true, 400},
		{"POST", "/v1/storage/preview", strings.Repeat(" ", 65<<10) + `{}`, true, 400},
		{"GET", "/v1/storage?limit=101", "", true, 400},
		{"GET", "/v1/storage?limit=1&limit=2", "", true, 400},
		{"GET", "/v1/storage?path=outside", "", true, 400},
		{"GET", "/v1/storage/cleanup", "", true, 405},
		{"DELETE", "/v1/storage/preview", "", true, 405},
		{"GET", "/v1/storage/operations/" + strings.Repeat("8", 32), "", true, 404},
	} {
		t.Run(tc.method+tc.path+tc.body[:min(len(tc.body), 20)], func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
			if tc.auth {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatal(recorder.Code, recorder.Body.String())
			}
		})
	}
	if after := storageFileTreeForTest(t, controller.config.RootDir); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected request changed files")
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/storage", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(recorder.Header())
	}
}
