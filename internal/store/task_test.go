package store

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestTaskFilterAndCursorValidation(t *testing.T) {
	for _, filter := range []domain.TaskFilter{{Kind: "other"}, {State: "unknown"}, {Search: strings.Repeat("a", 201)}, {Search: "a\x00b"}} {
		if _, err := normalizeTaskFilter(filter); !errors.Is(err, ErrTaskQuery) {
			t.Fatalf("accepted invalid filter: %+v, %v", filter, err)
		}
	}
	from := time.Date(2026, 9, 6, 12, 0, 0, 0, time.FixedZone("offset", 3*3600))
	utc := from.UTC()
	scope := domain.TaskScope{UserID: 7, Generation: true, Training: true}
	first, _ := normalizeTaskFilter(domain.TaskFilter{Search: "  model  ", From: &from})
	second, _ := normalizeTaskFilter(domain.TaskFilter{Search: "model", From: &utc})
	binding := taskQueryBinding(scope, first)
	if binding != taskQueryBinding(scope, second) {
		t.Fatal("equivalent filters bind differently")
	}
	data, _ := json.Marshal(taskCursor{1, binding, from, "generation", "job-123"})
	raw := base64.RawURLEncoding.EncodeToString(data)
	if _, err := decodeTaskCursor(raw, binding); err != nil {
		t.Fatal(err)
	}
	for _, other := range []domain.TaskScope{{UserID: 8, Generation: true, Training: true}, {UserID: 7, Generation: true}} {
		if _, err := decodeTaskCursor(raw, taskQueryBinding(other, first)); !errors.Is(err, ErrTaskQuery) {
			t.Fatal("cursor accepted for changed owner or permissions")
		}
	}
	for _, raw := range []string{"%%%", strings.Repeat("x", 2049), base64.RawURLEncoding.EncodeToString([]byte(`{}`))} {
		if _, err := decodeTaskCursor(raw, binding); !errors.Is(err, ErrTaskQuery) {
			t.Fatal("malformed cursor accepted")
		}
	}
	if _, err := normalizeTaskFilter(domain.TaskFilter{From: &from, Before: &from}); !errors.Is(err, ErrTaskQuery) {
		t.Fatal("empty date interval accepted")
	}
}
