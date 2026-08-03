package gateway

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxComfyPromptBody = 32 << 20

type openWebCookieSecureKey struct{}

var openWebUIIdentityHeaders = []string{
	"X-AI-Gateway-Email",
	"X-AI-Gateway-Name",
	"X-AI-Gateway-Role",
}

var comfyNamespacePattern = regexp.MustCompile(`gateway/gateway-[0-9a-f]{24}`)

func (a *App) prepareUpstreamIdentity(req *http.Request, service string, user *User) {
	for _, name := range openWebUIIdentityHeaders {
		req.Header.Del(name)
	}
	if user == nil {
		return
	}

	switch service {
	case "openwebui":
		identityMatches := a.openWebIdentityMatches(req, user.ID)
		stripCookie(req, openWebIdentityCookieName)
		if req.URL.Path == "/api/v1/auths/signin" || !identityMatches {
			req.Header.Del("Authorization")
			stripCookie(req, "token")
		}
		req.Header.Set("X-AI-Gateway-Email", a.openWebUserEmail(user.ID))
		req.Header.Set("X-AI-Gateway-Name", user.Username)
		req.Header.Set("X-AI-Gateway-Role", "user")
	case "comfyui":
		if isWebSocket(req) {
			query := req.URL.Query()
			query.Set("clientId", a.comfyClientID(user.ID))
			req.URL.RawQuery = query.Encode()
		}
	}
}

func (a *App) openWebUserEmail(userID int64) string {
	mac := hmac.New(sha256.New, []byte(a.cfg.SessionSecret))
	_, _ = mac.Write([]byte("openwebui-user:" + strconv.FormatInt(userID, 10)))
	return "gateway-" + hex.EncodeToString(mac.Sum(nil))[:24] + "@local.invalid"
}

func (a *App) bindOpenWebUIIdentity(resp *http.Response) {
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/api/v1/auths/signin" {
		return
	}
	user := a.currentUser(resp.Request)
	if user == nil {
		return
	}
	secure := a.cookieSecure(resp.Request)
	if originalSecure, ok := resp.Request.Context().Value(openWebCookieSecureKey{}).(bool); ok {
		secure = originalSecure
	}
	cookie := &http.Cookie{
		Name:     openWebIdentityCookieName,
		Value:    a.openWebIdentityValue(user.ID),
		Path:     "/",
		Expires:  time.Now().Add(a.cfg.SessionTTL),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	}
	resp.Header.Add("Set-Cookie", cookie.String())
}

func (a *App) openWebIdentityValue(userID int64) string {
	id := strconv.FormatInt(userID, 10)
	mac := hmac.New(sha256.New, []byte(a.cfg.SessionSecret))
	_, _ = mac.Write([]byte("openwebui:" + id))
	return id + "." + hex.EncodeToString(mac.Sum(nil))
}

func (a *App) openWebIdentityMatches(req *http.Request, userID int64) bool {
	cookie, err := req.Cookie(openWebIdentityCookieName)
	if err != nil {
		return false
	}
	want := a.openWebIdentityValue(userID)
	if len(cookie.Value) != len(want) || !strings.Contains(cookie.Value, ".") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(want)) == 1
}

func (a *App) comfyClientID(userID int64) string {
	mac := hmac.New(sha256.New, []byte(a.cfg.SessionSecret))
	_, _ = mac.Write([]byte(strconv.FormatInt(userID, 10)))
	return "gateway-" + hex.EncodeToString(mac.Sum(nil))[:24]
}

func (a *App) enforceComfyPromptIdentity(req *http.Request, user *User) error {
	if user == nil || req.Method != http.MethodPost || req.URL.Path != "/prompt" || req.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxComfyPromptBody+1))
	if err != nil {
		return fmt.Errorf("read ComfyUI prompt: %w", err)
	}
	_ = req.Body.Close()
	if len(body) > maxComfyPromptBody {
		return fmt.Errorf("ComfyUI prompt exceeds %d bytes", maxComfyPromptBody)
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		return nil
	}
	if prompt, ok := document["prompt"]; ok && containsForeignComfyNamespace(prompt, comfyUploadNamespace(a.comfyClientID(user.ID))) {
		return errForeignComfyAsset
	}
	clientID, _ := json.Marshal(a.comfyClientID(user.ID))
	document["client_id"] = clientID
	rewritten, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode ComfyUI prompt: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(rewritten))
	req.ContentLength = int64(len(rewritten))
	req.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	return nil
}

func containsForeignComfyNamespace(raw json.RawMessage, ownNamespace string) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	var inspect func(any) bool
	inspect = func(current any) bool {
		switch typed := current.(type) {
		case string:
			for _, namespace := range comfyNamespacePattern.FindAllString(typed, -1) {
				if namespace != ownNamespace {
					return true
				}
			}
		case []any:
			for _, item := range typed {
				if inspect(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range typed {
				if inspect(item) {
					return true
				}
			}
		}
		return false
	}
	return inspect(value)
}
