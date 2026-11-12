package modelmanager

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestVersionsClassifiesCatalogErrorsWithoutLeakingUpstreamText(t *testing.T) {
	for _, tc := range []struct {
		name, body, kind string
		status           int
		want             error
	}{
		{"checkpoint", `{"error":"Model type mismatch. Expected LORA, LoCon, or DORA, got Checkpoint"}`, "checkpoints", 400, ErrInvalid},
		{"lora", `{"error":"Model type mismatch. Expected Checkpoint, got LORA"}`, "loras", 400, ErrInvalid},
		{"locon", `{"error":"Model type mismatch. Expected Checkpoint, got LoCon"}`, "loras", 400, ErrInvalid},
		{"dora", `{"error":"Model type mismatch. Expected Checkpoint, got DoRA"}`, "loras", 400, ErrInvalid},
		{"unsupported", `{"error":"Model type mismatch. Expected LORA, LoCon, or DORA, got TextualInversion"}`, "", 400, ErrUnsupportedType},
		{"not found", `private-token`, "", 404, ErrNotFound},
		{"rate limit", `private-token`, "", 429, ErrRateLimited},
		{"other bad request", `{"error":"private-token"}`, "", 400, ErrUnavailable},
		{"broken JSON", `{"error":`, "", 400, ErrUnavailable},
		{"oversized", `{"error":"Model type mismatch. Expected LORA, got Checkpoint"}` + strings.Repeat(" ", 4096), "", 400, ErrUnavailable},
		{"server failure", `private-token`, "", 500, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			kind := "loras"
			if tc.kind == "loras" {
				kind = "checkpoints"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/api/lm/"+kind+"/civitai/versions/42" {
					t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			_, err := (&Client{Base: base}).Versions(context.Background(), kind, 42)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "private-token") {
				t.Fatalf("error = %v; want %v", err, tc.want)
			}
			var mismatch *CatalogMismatchError
			if errors.As(err, &mismatch) != (tc.kind != "") || (mismatch != nil && mismatch.Kind != tc.kind) {
				t.Fatalf("catalog mismatch = %+v; want %q", mismatch, tc.kind)
			}
			if calls != 1 {
				t.Fatalf("unexpected retry or mutation: %d calls", calls)
			}
		})
	}
}

func TestCatalogErrorClassificationDoesNotApplyToOtherRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Model type mismatch. Expected LORA, got Checkpoint"}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	_, err := (&Client{Base: base}).Settings(context.Background())
	if err != ErrUnavailable {
		t.Fatalf("settings error = %v", err)
	}
}
