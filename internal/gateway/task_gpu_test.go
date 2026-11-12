package gateway

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestTaskGPUSnapshotRequestValidation(t *testing.T) {
	app := &App{}
	admin := &User{ID: 1, Role: "admin"}
	for _, tc := range []struct {
		method, query string
		user          *User
		status        int
	}{
		{"POST", "", admin, 405}, {"GET", "", nil, 401},
		{"GET", "?task=generation:a", &User{ID: 1}, 403},
		{"GET", "", admin, 400},
		{"GET", "?task=", admin, 400},
		{"GET", "?task=generation:", admin, 400},
		{"GET", "?task=generation:a&task=generation:a", admin, 400},
		{"GET", "?task=generation:a&user_id=2", admin, 400},
		{"GET", "?kind=generation&id=a", admin, 400},
		{"GET", "?task=generation_batch:a", admin, 400},
		{"GET", "?task=lora_caption_series:a", admin, 400},
		{"GET", "?task=generation:a:b", admin, 400},
		{"GET", "?task=generation:a/b", admin, 400},
		{"GET", "?task=generation:a+b", admin, 400},
		{"GET", "?task=generation:%00", admin, 400},
		{"GET", "?task=generation:%FF", admin, 400},
		{"GET", "?task=generation:%ZZ", admin, 400},
		{"GET", "?task=generation:%D0%B0", admin, 400},
		{"GET", "?task=generation:a;task=generation:b", admin, 400},
		{"GET", "?task=generation:" + strings.Repeat("a", 201), admin, 400},
	} {
		r := httptest.NewRequest(tc.method, "/api/tasks/gpu-snapshots"+tc.query, nil)
		if tc.user != nil {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, tc.user))
		}
		w := httptest.NewRecorder()
		app.handleTaskGPUSnapshots(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s %s: %d %s", tc.method, tc.query, w.Code, w.Body.String())
		}
		if tc.status == 405 && w.Header().Get("Allow") != "GET" {
			t.Fatal("missing allowed method")
		}
	}
	values := url.Values{}
	for i := 0; i < domain.TaskGPUSnapshotLimit; i++ {
		values.Add("task", fmt.Sprintf("generation:ID_%d-%s", i, strings.Repeat("a", 192)))
	}
	keys, err := parseTaskGPUKeys(values)
	if err != nil || len(keys) != domain.TaskGPUSnapshotLimit || keys[0].Kind != "generation" {
		t.Fatalf("valid boundary: %+v %v", keys, err)
	}
	values.Add("task", "generation:one-too-many")
	if _, err := parseTaskGPUKeys(values); err == nil {
		t.Fatal("unbounded read accepted")
	}
	keys, err = parseTaskGPUKeys(url.Values{"task": {"generation:same", "lora_training:same", "lora_caption:same"}})
	if err != nil || len(keys) != 3 || keys[2].Kind != "lora_caption" {
		t.Fatal("distinct families treated as duplicate identities")
	}
}

func TestTaskGPUSnapshotPublicRouteAuthentication(t *testing.T) {
	app := &App{}
	w := httptest.NewRecorder()
	app.publicMux().ServeHTTP(w, httptest.NewRequest("GET", "/api/tasks/gpu-snapshots?task=generation:a", nil))
	if w.Code != 401 || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Location") != "" {
		t.Fatalf("snapshot route must own its JSON auth error: %d %s", w.Code, w.Body.String())
	}
}
