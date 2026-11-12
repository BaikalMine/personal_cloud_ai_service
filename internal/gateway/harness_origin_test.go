package gateway

import (
	"net/http/httptest"
	"testing"
)

func TestHarnessNavigationOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, site, mode, dest, origin, upgrade string
		valid                                                 bool
	}{
		{"external link", "GET", "/harness/", "cross-site", "navigate", "document", "", "", true},
		{"external bare link", "GET", "/harness", "cross-site", "navigate", "document", "", "", true},
		{"external index link", "GET", "/harness/index.html", "cross-site", "navigate", "document", "", "", true},
		{"same site fetch", "POST", "/harness/upload", "same-origin", "cors", "empty", "https://public.example", "", true},
		{"cross site upload", "POST", "/harness/upload", "cross-site", "navigate", "document", "", "", false},
		{"cross site fetch", "GET", "/harness/", "cross-site", "cors", "empty", "", "", false},
		{"cross site iframe", "GET", "/harness/", "cross-site", "navigate", "iframe", "", "", false},
		{"cross site RPC", "GET", "/harness/api", "cross-site", "navigate", "document", "", "", false},
		{"cross site origin", "GET", "/harness/", "cross-site", "navigate", "document", "https://evil.example", "", false},
		{"cross site websocket", "GET", "/harness/", "cross-site", "navigate", "document", "", "websocket", false},
		{"same site bad origin", "POST", "/harness/upload", "same-origin", "cors", "empty", "https://evil.example", "", false},
		{"direct opening", "GET", "/harness/", "none", "navigate", "document", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://public.example"+tc.path, nil)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.Header.Set("Sec-Fetch-Mode", tc.mode)
			r.Header.Set("Sec-Fetch-Dest", tc.dest)
			r.Header.Set("Origin", tc.origin)
			if tc.upgrade != "" {
				r.Header.Set("Upgrade", tc.upgrade)
				r.Header.Set("Connection", "Upgrade")
			}
			if got := validHarnessOrigin(r); got != tc.valid {
				t.Fatalf("origin valid=%v, want %v", got, tc.valid)
			}
			if tc.site == "cross-site" && validFormOrigin(r) {
				t.Fatal("general form origin policy changed")
			}
		})
	}
}
