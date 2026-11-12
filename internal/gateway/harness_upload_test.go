package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-access-gateway/internal/config"
)

func TestHarnessBinaryUploadLimitIsRouteScoped(t *testing.T) {
	for _, tc := range []struct {
		method, path, media string
		want                int64
	}{
		{"POST", "/harness/api/session/uploadFileBinary", "application/octet-stream", harnessBinaryUploadLimit},
		{"POST", "/harness/api/gateway-files/upload", "application/octet-stream", harnessBinaryUploadLimit},
		{"POST", "/harness/api/session/uploadFileBinary", "application/json", harnessUploadLimit},
		{"GET", "/harness/api/session/uploadFileBinary", "application/octet-stream", harnessUploadLimit},
		{"POST", "/harness/api/gateway-files/manage", "application/octet-stream", harnessUploadLimit},
		{"POST", "/harness/api/session/uploadFileBinary/extra", "application/octet-stream", harnessUploadLimit},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Content-Type", tc.media)
		if got := harnessBrowserBodyLimit(r); got != tc.want {
			t.Fatalf("%s %s: %d != %d", tc.method, tc.path, got, tc.want)
		}
	}
}

type harnessZeroReader struct{}

func (harnessZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestHarnessStreams500MiBWithoutBuffering(t *testing.T) {
	var received int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		received, err = io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Error(err)
		}
		w.WriteHeader(201)
	}))
	defer upstream.Close()
	a := &App{cfg: Config{PublicURL: harnessURL(t, "https://public.example"), Harness: config.HarnessConfig{Upstream: harnessURL(t, upstream.URL), BootstrapURL: harnessURL(t, upstream.URL), BootstrapKey: strings.Repeat("b", 64)}}}
	for _, route := range []string{"api/session/uploadFileBinary", "api/gateway-files/upload"} {
		r := httptest.NewRequest("POST", "https://public.example/harness/"+route, io.LimitReader(harnessZeroReader{}, 500<<20))
		r.ContentLength = 500 << 20
		r.Header.Set("Content-Type", "application/octet-stream")
		w := httptest.NewRecorder()
		a.harnessBrowserProxy().ServeHTTP(w, harnessAdmin(r))
		if w.Code != 201 || received != 500<<20 {
			t.Fatalf("upload: HTTP %d, bytes %d", w.Code, received)
		}
		r = httptest.NewRequest("POST", "https://public.example/harness/"+route, nil)
		r.ContentLength = harnessBinaryUploadLimit + 1
		r.Header.Set("Content-Type", "application/octet-stream")
		w = httptest.NewRecorder()
		a.harnessBrowserProxy().ServeHTTP(w, harnessAdmin(r))
		if w.Code != 413 {
			t.Fatalf("oversized binary upload: %d", w.Code)
		}
	}
}
