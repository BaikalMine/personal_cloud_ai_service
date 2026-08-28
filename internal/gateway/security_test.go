package gateway

import (
	"bytes"
	"context"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ai-access-gateway/internal/security"
)

func TestValidCSRFParsesMultipartForm(t *testing.T) {
	const sessionToken = "multipart-session-token"
	app := &App{csrfSigner: security.NewCSRFSigner("multipart-csrf-secret")}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("csrf", app.csrfSigner.Token(sessionToken)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/mining", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	if !app.validCSRF(request) {
		t.Fatal("validCSRF rejected a valid multipart form")
	}
}

func TestClientIPUsesForwardedChainOnlyFromTrustedProxy(t *testing.T) {
	trusted := testNetworks(t, "198.51.100.10/32", "172.16.0.0/12")
	a := &App{cfg: Config{TrustedProxies: trusted}}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.10:443"
	r.Header.Set("X-Forwarded-For", "203.0.113.7, 198.51.100.10")
	if got, want := a.clientIP(r), "203.0.113.7"; got != want {
		t.Fatalf("clientIP() = %q, want %q", got, want)
	}

	r.RemoteAddr = "10.0.0.8:443"
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got, want := a.clientIP(r), "10.0.0.8"; got != want {
		t.Fatalf("untrusted clientIP() = %q, want %q", got, want)
	}
}

func TestRequestProtoIgnoresSpoofedHeader(t *testing.T) {
	trusted := testNetworks(t, "198.51.100.10/32")
	a := &App{cfg: Config{TrustedProxies: trusted}}
	r := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
	r.RemoteAddr = "10.0.0.8:443"
	r.Header.Set("X-Forwarded-Proto", "https")
	if got, want := a.requestProto(r), "http"; got != want {
		t.Fatalf("requestProto() = %q, want %q", got, want)
	}

	r.RemoteAddr = "198.51.100.10:443"
	if got, want := a.requestProto(r), "https"; got != want {
		t.Fatalf("trusted requestProto() = %q, want %q", got, want)
	}
}

func TestRequestProtoUsesCanonicalPublicURLBehindDockerNAT(t *testing.T) {
	publicURL, err := url.Parse("https://ai.example")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{PublicURL: publicURL}}
	request := httptest.NewRequest(http.MethodGet, "http://ai.example/login", nil)
	request.RemoteAddr = "192.168.65.1:50000"
	if got := app.requestProto(request); got != "https" {
		t.Fatalf("requestProto() = %q, want https", got)
	}
	request.Host = "10.0.0.42:8090"
	if got := app.requestProto(request); got != "http" {
		t.Fatalf("local requestProto() = %q, want http", got)
	}
}

func testNetworks(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		networks = append(networks, network)
	}
	return networks
}

func TestValidateCredentials(t *testing.T) {
	tests := []struct {
		name  string
		user  string
		email string
		pass  string
		want  bool
	}{
		{name: "valid", user: "alice.admin", email: "alice@example.com", pass: strings.Repeat("x", 12), want: true},
		{name: "bad username", user: "alice name", pass: strings.Repeat("x", 12)},
		{name: "bad email", user: "alice", email: "not-an-email", pass: strings.Repeat("x", 12)},
		{name: "short password", user: "alice", pass: "short"},
		{name: "long password", user: "alice", pass: strings.Repeat("x", 257)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateCredentials(tt.user, tt.email, tt.pass) == ""
			if got != tt.want {
				t.Fatalf("valid = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidFormOrigin(t *testing.T) {
	tests := []struct {
		name      string
		origin    string
		fetchSite string
		want      bool
	}{
		{name: "same origin", origin: "https://ai.example", fetchSite: "same-origin", want: true},
		{name: "local same origin", origin: "http://10.0.0.42:8090", fetchSite: "same-origin", want: true},
		{name: "cross origin", origin: "https://evil.example", fetchSite: "cross-site"},
		{name: "spoofed fetch metadata", origin: "https://ai.example", fetchSite: "cross-site"},
		{name: "non browser client", want: true},
		{name: "null origin", origin: "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := "https://ai.example/login"
			if tt.name == "local same origin" {
				target = "http://10.0.0.42:8090/login"
			}
			request := httptest.NewRequest(http.MethodPost, target, nil)
			request.Header.Set("Origin", tt.origin)
			request.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			if got := validFormOrigin(request); got != tt.want {
				t.Fatalf("validFormOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoginRateLimitKeyIsAccountSpecific(t *testing.T) {
	alice := loginRateLimitKey("192.0.2.10", " Alice ")
	if alice != loginRateLimitKey("192.0.2.10", "alice") {
		t.Fatal("username case or whitespace changed the limiter key")
	}
	if alice == loginRateLimitKey("192.0.2.10", "bob") {
		t.Fatal("different usernames shared a limiter key")
	}
	if alice == loginRateLimitKey("192.0.2.11", "alice") {
		t.Fatal("different IP addresses shared a limiter key")
	}
}

func TestRewriteLocationAddsGatewayPrefix(t *testing.T) {
	upstream, err := url.Parse("http://upstream:8188")
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("Location", "http://upstream:8188/api/user")
	rewriteLocation(resp, "/comfyui/", upstream)
	if got, want := resp.Header.Get("Location"), "/comfyui/api/user"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestCookieSecureUsesPublicDomainOnly(t *testing.T) {
	publicURL, err := url.Parse("https://ai.example")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: Config{CookieSecure: true, PublicURL: publicURL}}
	local := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8090/login", nil)
	if got := a.sessionCookie(local, "token").Secure; got {
		t.Fatal("loopback LAN cookie must allow direct HTTP access")
	}
	lan := httptest.NewRequest(http.MethodGet, "http://10.0.0.42:8090/login", nil)
	if got := a.sessionCookie(lan, "token").Secure; got {
		t.Fatal("private LAN cookie must allow direct HTTP access")
	}
	public := httptest.NewRequest(http.MethodGet, "http://ai.example/login", nil)
	if got := a.sessionCookie(public, "token").Secure; !got {
		t.Fatal("public-domain cookie must remain Secure")
	}
	publicWithPort := httptest.NewRequest(http.MethodGet, "http://ai.example:8090/login", nil)
	if got := a.sessionCookie(publicWithPort, "token").Secure; !got {
		t.Fatal("public-domain cookie with port must remain Secure")
	}
}

func TestEmbeddedTemplatesParse(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates.AssetVersion) != 12 {
		t.Fatalf("asset version length = %d, want 12", len(templates.AssetVersion))
	}
}

func TestLogoutRequiresPost(t *testing.T) {
	a := &App{}
	request := httptest.NewRequest(http.MethodGet, "/logout", nil)
	response := httptest.NewRecorder()

	a.handleLogout(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if got := response.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", got)
	}
}

func TestAccountQuickGenerationPriorityRequiresAdminPostAndCSRF(t *testing.T) {
	app := &App{csrfSigner: security.NewCSRFSigner("priority-csrf-secret")}

	t.Run("GET is rejected", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/account/quick-generation-priority", nil)
		response := httptest.NewRecorder()
		app.handleAccountQuickGenerationPriority(response, request)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("regular user is rejected", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/account/quick-generation-priority", nil)
		request = request.WithContext(context.WithValue(request.Context(), userCtxKey, &User{ID: 7, Role: "user"}))
		response := httptest.NewRecorder()
		app.handleAccountQuickGenerationPriority(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
		}
	})

	t.Run("administrator without CSRF is rejected", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/account/quick-generation-priority", nil)
		request = request.WithContext(context.WithValue(request.Context(), userCtxKey, &User{ID: 1, Role: "admin"}))
		response := httptest.NewRecorder()
		app.handleAccountQuickGenerationPriority(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
		}
	})
}

func TestSafeAdminMediaType(t *testing.T) {
	tests := []struct {
		name      string
		mediaType string
		mimeType  string
		wantType  string
		inline    bool
	}{
		{name: "png", mediaType: "image", mimeType: "image/png", wantType: "image/png", inline: true},
		{name: "mp4 with parameter", mediaType: "video", mimeType: "video/mp4; codecs=avc1", wantType: "video/mp4", inline: true},
		{name: "svg is never inline", mediaType: "image", mimeType: "image/svg+xml", wantType: "application/octet-stream"},
		{name: "html is never inline", mediaType: "image", mimeType: "text/html", wantType: "application/octet-stream"},
		{name: "mismatched media type", mediaType: "video", mimeType: "image/png", wantType: "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotInline := safeAdminMediaType(tt.mediaType, tt.mimeType)
			if gotType != tt.wantType || gotInline != tt.inline {
				t.Fatalf("safeAdminMediaType() = (%q,%v), want (%q,%v)", gotType, gotInline, tt.wantType, tt.inline)
			}
		})
	}
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	app := &App{}
	request := httptest.NewRequest(http.MethodGet, "https://ai.example/app", nil)
	response := httptest.NewRecorder()
	app.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestID(r) == "" {
			t.Fatal("request ID was not added to the request context")
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	for name, want := range map[string]string{
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "same-origin",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	} {
		if got := response.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if len(response.Header().Get("X-Request-ID")) != 24 {
		t.Fatalf("invalid request ID %q", response.Header().Get("X-Request-ID"))
	}
	if got := app.requestsTotal.Load(); got != 1 {
		t.Fatalf("requests total = %d, want 1", got)
	}
}
