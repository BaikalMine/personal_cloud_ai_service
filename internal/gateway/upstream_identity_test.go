package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenWebUISignInResponseBindsGatewayUser(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901", SessionTTL: time.Hour, CookieSecure: true}}
	user := &User{ID: 42, Username: "alice"}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auths/signin", nil)
	request = request.WithContext(context.WithValue(request.Context(), userCtxKey, user))
	request = request.WithContext(context.WithValue(request.Context(), openWebCookieSecureKey{}, false))
	response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: request}

	app.bindOpenWebUIIdentity(response)

	cookies := (&http.Response{Header: response.Header}).Cookies()
	if len(cookies) != 1 || cookies[0].Name != openWebIdentityCookieName {
		t.Fatalf("identity cookies = %#v", cookies)
	}
	if cookies[0].Secure {
		t.Fatal("local HTTP identity cookie inherited the rewritten upstream host")
	}
	probe := httptest.NewRequest(http.MethodGet, "/api/v1/chats/", nil)
	probe.AddCookie(cookies[0])
	if !app.openWebIdentityMatches(probe, user.ID) || app.openWebIdentityMatches(probe, 7) {
		t.Fatal("sign-in response was not bound to the current Gateway user")
	}
}

func TestPrepareOpenWebUIIdentityOverwritesUntrustedHeaders(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auths/", nil)
	request.Header.Set("X-AI-Gateway-Email", "forged@example.com")
	request.Header.Set("Authorization", "Bearer stale-token")
	request.Header.Set("Cookie", "token=old-openwebui-token; preference=dark")
	user := &User{ID: 42, Username: "alice", Role: "admin"}

	app.prepareUpstreamIdentity(request, "openwebui", user)

	if got, want := request.Header.Get("X-AI-Gateway-Email"), app.openWebUserEmail(user.ID); got != want {
		t.Fatalf("email header = %q, want %q", got, want)
	}
	if got, want := request.Header.Get("X-AI-Gateway-Name"), "alice"; got != want {
		t.Fatalf("name header = %q, want %q", got, want)
	}
	if got, want := request.Header.Get("X-AI-Gateway-Role"), "user"; got != want {
		t.Fatalf("role header = %q, want %q", got, want)
	}
	if strings.Contains(request.Header.Get("Cookie"), "token=") {
		t.Fatal("OpenWebUI token cookie was not stripped")
	}
	if !strings.Contains(request.Header.Get("Cookie"), "preference=dark") {
		t.Fatal("unrelated cookie was removed")
	}
	if request.Header.Get("Authorization") != "" {
		t.Fatal("stale OpenWebUI authorization was not removed")
	}
}

func TestMatchingOpenWebUIIdentityPreservesToken(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	user := &User{ID: 42, Username: "alice"}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/chats/", nil)
	request.Header.Set("Authorization", "Bearer personal-token")
	request.AddCookie(&http.Cookie{Name: openWebIdentityCookieName, Value: app.openWebIdentityValue(user.ID)})

	app.prepareUpstreamIdentity(request, "openwebui", user)

	if got, want := request.Header.Get("Authorization"), "Bearer personal-token"; got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
	if strings.Contains(request.Header.Get("Cookie"), openWebIdentityCookieName+"=") {
		t.Fatal("Gateway identity cookie was forwarded upstream")
	}
}

func TestMismatchedOpenWebUIIdentityStripsTokenOnEveryEndpoint(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	currentUser := &User{ID: 42, Username: "alice"}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/chats/private-chat", nil)
	request.Header.Set("Authorization", "Bearer another-users-token")
	request.Header.Set("Cookie", "token=another-users-cookie")
	request.AddCookie(&http.Cookie{Name: openWebIdentityCookieName, Value: app.openWebIdentityValue(7)})

	app.prepareUpstreamIdentity(request, "openwebui", currentUser)

	if request.Header.Get("Authorization") != "" {
		t.Fatal("mismatched identity preserved an Authorization token")
	}
	if strings.Contains(request.Header.Get("Cookie"), "token=") {
		t.Fatal("mismatched identity preserved an OpenWebUI token cookie")
	}
}

func TestOpenWebUIIdentityIsUserSpecific(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: openWebIdentityCookieName, Value: app.openWebIdentityValue(1)})
	if !app.openWebIdentityMatches(request, 1) {
		t.Fatal("valid identity cookie was rejected")
	}
	if app.openWebIdentityMatches(request, 2) {
		t.Fatal("identity cookie matched a different user")
	}
}

func TestComfyIdentityIsStableAndUserSpecific(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	first := app.comfyClientID(1)
	if first != app.comfyClientID(1) {
		t.Fatal("ComfyUI client ID is not stable")
	}
	if first == app.comfyClientID(2) {
		t.Fatal("different users received the same ComfyUI client ID")
	}
}

func TestEnforceComfyPromptIdentity(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	request := httptest.NewRequest(http.MethodPost, "/prompt", strings.NewReader(`{"client_id":"forged","prompt":{"1":{"class_type":"KSampler"}}}`))
	user := &User{ID: 7, Username: "bob"}

	if err := app.enforceComfyPromptIdentity(request, user); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	var clientID string
	if err := json.Unmarshal(document["client_id"], &clientID); err != nil {
		t.Fatal(err)
	}
	if got, want := clientID, app.comfyClientID(user.ID); got != want {
		t.Fatalf("client_id = %q, want %q", got, want)
	}
}

func TestEnforceComfyPromptRejectsForeignUploadNamespace(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	user := &User{ID: 7, Username: "bob"}
	request := httptest.NewRequest(http.MethodPost, "/prompt", strings.NewReader(
		`{"prompt":{"1":{"class_type":"LoadImage","inputs":{"image":"gateway/gateway-aaaaaaaaaaaaaaaaaaaaaaaa/private.png"}}}}`,
	))
	if err := app.enforceComfyPromptIdentity(request, user); !errors.Is(err, errForeignComfyAsset) {
		t.Fatalf("foreign namespace error = %v", err)
	}

	own := comfyUploadNamespace(app.comfyClientID(user.ID))
	request = httptest.NewRequest(http.MethodPost, "/prompt", strings.NewReader(
		`{"prompt":{"1":{"class_type":"LoadImage","inputs":{"image":"`+own+`/mine.png"}}}}`,
	))
	if err := app.enforceComfyPromptIdentity(request, user); err != nil {
		t.Fatalf("own namespace was rejected: %v", err)
	}
}

func TestComfyNamespaceIsolationNormalizesWindowsPathsAndCase(t *testing.T) {
	own := "gateway/gateway-bbbbbbbbbbbbbbbbbbbbbbbb"
	foreign := json.RawMessage(`{"image":"GATEWAY\\GATEWAY-AAAAAAAAAAAAAAAAAAAAAAAA\\private.png"}`)
	if !containsForeignComfyNamespace(foreign, own) {
		t.Fatal("foreign Windows namespace escaped isolation")
	}

	ownWindowsPath := json.RawMessage(`{"image":"GATEWAY\\GATEWAY-BBBBBBBBBBBBBBBBBBBBBBBB\\mine.png"}`)
	if containsForeignComfyNamespace(ownWindowsPath, own) {
		t.Fatal("normalized own Windows namespace was rejected")
	}
}

func TestEnforceComfyPromptRejectsLegacyAndNoncanonicalAssetPaths(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	user := &User{ID: 7, Username: "bob"}
	for _, asset := range []string{
		"legacy.png",
		"gateway/gateway-aaaaaaaaaaaaaaaaaaaaaaaa/../gateway-bbbbbbbbbbbbbbbbbbbbbbbb/private.png",
	} {
		request := httptest.NewRequest(http.MethodPost, "/prompt", strings.NewReader(
			`{"prompt":{"1":{"class_type":"LoadImage","inputs":{"image":"`+asset+`"}}}}`,
		))
		if err := app.enforceComfyPromptIdentity(request, user); err == nil {
			t.Fatalf("asset %q was accepted", asset)
		}
	}
}

func TestEnforceComfyPromptAllowsNodeLinkedMediaInputs(t *testing.T) {
	app := &App{cfg: Config{SessionSecret: "01234567890123456789012345678901"}}
	user := &User{ID: 7, Username: "bob"}
	own := comfyUploadNamespace(app.comfyClientID(user.ID))
	request := httptest.NewRequest(http.MethodPost, "/prompt", strings.NewReader(
		`{"prompt":{"1":{"class_type":"LoadImage","inputs":{"image":"`+own+`/mine.png"}},"2":{"class_type":"ImageScale","inputs":{"image":["1",0]}}}}`,
	))
	if err := app.enforceComfyPromptIdentity(request, user); err != nil {
		t.Fatalf("node-linked image was rejected: %v", err)
	}
}
