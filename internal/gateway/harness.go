package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const harnessMount = "/harness/"
const harnessModelMount = "/internal/harness"
const harnessUploadLimit = 64 << 20
const harnessBinaryUploadLimit = 512 << 20

func harnessBrowserBodyLimit(r *http.Request) int64 {
	if r.Method == http.MethodPost && r.URL.RawPath == "" &&
		(r.URL.Path == harnessMount+"api/session/uploadFileBinary" || r.URL.Path == harnessMount+"api/gateway-files/upload") &&
		strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]), "application/octet-stream") {
		return harnessBinaryUploadLimit
	}
	return harnessUploadLimit
}

func validHarnessOrigin(r *http.Request) bool {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
		return validFormOrigin(r)
	}
	// Opening the authenticated entry page from a bookmark or another site is
	// navigation, not a cross-origin API request. Never extend this to RPC/assets.
	entry := r.URL.Path == "/harness" || r.URL.Path == harnessMount || r.URL.Path == harnessMount+"index.html"
	return r.Method == http.MethodGet && entry && !isWebSocket(r) &&
		strings.EqualFold(r.Header.Get("Sec-Fetch-Mode"), "navigate") &&
		strings.EqualFold(r.Header.Get("Sec-Fetch-Dest"), "document") &&
		strings.TrimSpace(r.Header.Get("Origin")) == ""
}

func (a *App) registerHarnessRoutes(mux *http.ServeMux) {
	mux.Handle("/api/harness/v1/", a.harnessAgentAPI())
	proxy := a.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := a.currentUser(r)
		if user == nil || !user.CanUseService("harness") {
			http.Error(w, "Нет доступа к Harness.", 403)
			return
		}
		if user.Role == "admin" {
			a.harnessBrowserProxy().ServeHTTP(w, r)
			return
		}
		a.harnessTenantProxy(user.ID).ServeHTTP(w, r)
	}))
	entry := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = a.harnessIngressRequest(r)
		if reason := a.harnessSourceRejection(r); reason != "" {
			peer := net.ParseIP(clientIPFromRemote(r.RemoteAddr))
			peerLabel := "nonlocal"
			if peer != nil && (peer.IsPrivate() || peer.IsLoopback()) {
				peerLabel = peer.String()
			}
			log.Printf("Harness source check: reason=%s ingress_host=%s ingress_host_hash=%x ingress_peer=%s",
				reason, harnessIngressHostLabel(r.Host), sha256.Sum256([]byte(strings.ToLower(r.Host))), peerLabel)
		}
		proxy.ServeHTTP(w, r)
	})
	mux.Handle("/harness", entry)
	mux.Handle(harnessMount, entry)
	mux.Handle(harnessModelMount+"/", a.harnessModelProxy())
	mux.Handle("/internal/harness-ultra/", a.harnessUltraBroker())
	mux.Handle("/internal/harness-users/", a.harnessTenantInternal())
}

func harnessIngressHostLabel(host string) string {
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		return "other"
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return "other"
	}
	ip := net.ParseIP(name)
	if name == "localhost" || name == "host.docker.internal" || ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		return net.JoinHostPort(name, strconv.Itoa(value))
	}
	return "other"
}

func (a *App) harnessIngressRequest(r *http.Request) *http.Request {
	h := a.cfg.Harness
	if h.IngressHost == "" || !strings.EqualFold(r.Host, h.IngressHost) || a.cfg.PublicURL == nil {
		return r
	}
	peer := net.ParseIP(clientIPFromRemote(r.RemoteAddr))
	for _, network := range h.IngressPeers {
		if peer != nil && network.Contains(peer) {
			// Canonicalize only the explicitly configured proxy socket and Host pair.
			// Browser-controlled forwarding headers never establish this trust.
			r = r.Clone(r.Context())
			r.Host = a.cfg.PublicURL.Host
			return r
		}
	}
	return r
}

func (a *App) harnessAllowedHost(host string) bool {
	return a.cfg.PublicURL != nil && strings.EqualFold(host, a.cfg.PublicURL.Host) ||
		a.cfg.AdminURL != nil && strings.EqualFold(host, a.cfg.AdminURL.Host)
}

func (a *App) harnessSourceRejection(r *http.Request) string {
	if !a.harnessAllowedHost(r.Host) {
		return "host_mismatch"
	}
	if !validHarnessOrigin(r) {
		return "origin_rejected"
	}
	if r.URL.RawPath != "" || strings.Contains(r.URL.Path, "\\") {
		return "noncanonical_path"
	}
	if r.URL.Query().Has("token") {
		return "token_in_query"
	}
	return ""
}

func (a *App) handleHarnessGPUPermit(w http.ResponseWriter, r *http.Request) {
	var operator *User
	if a.cfg.Harness.PauseMining {
		// The shared admin-only workspace has an explicitly configured operator.
		// This does not alter generation settings for any account.
		operator = &User{ID: a.cfg.Harness.MiningUserID}
	}
	a.handleGPUExecutorPermitWithMining(w, r, domain.GPUExecutorOllama, a.cfg.Harness.Enabled(), a.cfg.Harness.AdmissionKey, operator)
}

func harnessCookieHeader(r *http.Request) string {
	var values []string
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, "dsh-auth-") {
			values = append(values, c.Name+"="+c.Value)
		}
	}
	return strings.Join(values, "; ")
}

func (a *App) harnessSession(ctx context.Context, host string) (*http.Cookie, error) {
	u := *a.cfg.Harness.BootstrapURL
	u.Path = "/session"
	u.RawQuery = url.Values{"host": {host}}.Encode()
	r, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+a.cfg.Harness.BootstrapKey)
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: rejectUpstreamRedirect}
	response, err := client.Do(r)
	if err != nil {
		return nil, errors.New("Harness bootstrap unavailable")
	}
	defer response.Body.Close()
	var result struct {
		Cookie string `json:"cookie"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8193))
	if response.StatusCode != 200 || decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("Harness bootstrap invalid")
	}
	cookies := (&http.Response{Header: http.Header{"Set-Cookie": {result.Cookie}}}).Cookies()
	if len(cookies) != 1 || !strings.HasPrefix(cookies[0].Name, "dsh-auth-") || cookies[0].Value == "" || cookies[0].Domain != "" {
		return nil, errors.New("Harness session invalid")
	}
	return cookies[0], nil
}

func (a *App) harnessBrowserProxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !a.cfg.Harness.Enabled() || a.cfg.RestoreDrillMode {
			http.Error(w, "Harness пока недоступен. Вернитесь в сервис и повторите позже.", 503)
			return
		}
		if a.harnessSourceRejection(r) != "" {
			http.Error(w, "Источник запроса Harness не подтверждён.", 403)
			return
		}
		if r.URL.Path == "/harness" {
			http.Redirect(w, r, harnessMount, http.StatusTemporaryRedirect)
			return
		}
		bodyLimit := harnessBrowserBodyLimit(r)
		limitMessage := fmt.Sprintf("Размер загрузки не должен превышать %d МБ.", bodyLimit>>20)
		if r.ContentLength > bodyLimit {
			http.Error(w, limitMessage, 413)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
		var session *http.Cookie
		if (r.URL.Path == harnessMount || r.URL.Path == harnessMount+"index.html") && r.Method == "GET" {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			var err error
			session, err = a.harnessSession(ctx, r.Host)
			cancel()
			if err != nil {
				http.Error(w, "Harness запускается или временно недоступен. Повторите открытие позже.", 503)
				return
			}
		}
		upstream := a.cfg.Harness.Upstream
		proxy := &httputil.ReverseProxy{
			Rewrite: func(p *httputil.ProxyRequest) {
				p.SetURL(upstream)
				p.Out.URL.Path = strings.TrimPrefix(p.In.URL.Path, "/harness")
				p.Out.Host = p.In.Host
				p.Out.Header.Del("Authorization")
				p.Out.Header.Del("Idempotency-Key")
				p.Out.Header.Set("Accept-Encoding", "")
				p.Out.Header.Del("Cookie")
				if session != nil {
					p.Out.AddCookie(&http.Cookie{Name: session.Name, Value: session.Value})
				} else if cookies := harnessCookieHeader(p.In); cookies != "" {
					p.Out.Header.Set("Cookie", cookies)
				}
			},
			ModifyResponse: func(response *http.Response) error {
				if session != nil {
					session.Path, session.Secure, session.HttpOnly = harnessMount, a.cookieSecure(r), true
					response.Header.Add("Set-Cookie", session.String())
				}
				cookies := response.Cookies()
				response.Header.Del("Set-Cookie")
				for _, cookie := range cookies {
					if strings.HasPrefix(cookie.Name, "dsh-auth-") && cookie.Domain == "" {
						cookie.Path, cookie.Secure, cookie.HttpOnly = harnessMount, a.cookieSecure(r), true
						response.Header.Add("Set-Cookie", cookie.String())
					}
				}
				response.Header.Set("Cache-Control", "no-store")
				response.Header.Set("Referrer-Policy", "no-referrer")
				rewriteLocation(response, "/harness", upstream)
				return injectAIReturnLink(response, openWebUIReturnLink)
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					http.Error(w, limitMessage, 413)
					return
				}
				http.Error(w, "Соединение с Harness потеряно. Откройте сохранённую сессию заново.", 502)
			},
			FlushInterval: -1,
		}
		proxy.ServeHTTP(w, r)
	})
}

func harnessModelRoute(r *http.Request) bool {
	if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || path.Clean(r.URL.Path) != r.URL.Path {
		return false
	}
	return r.URL.Path == harnessModelMount+"/v1/models" && r.Method == "GET" ||
		r.URL.Path == harnessModelMount+"/v1/chat/completions" && r.Method == "POST" ||
		r.URL.Path == harnessModelMount+"/harness/task" && r.Method == "POST"
}

func (a *App) harnessModelProxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !a.cfg.Harness.Enabled() || a.cfg.RestoreDrillMode || len(a.cfg.Harness.ModelKey) < 32 ||
			len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+a.cfg.Harness.ModelKey)) != 1 {
			http.NotFound(w, r)
			return
		}
		if !harnessModelRoute(r) {
			http.NotFound(w, r)
			return
		}
		requestLimit := int64(domain.MaxGPUInferenceRequestBytes)
		if r.URL.Path == harnessModelMount+"/harness/task" {
			requestLimit = 1024
		}
		if r.ContentLength > requestLimit || r.Header.Get("Content-Encoding") != "" {
			http.Error(w, "invalid request size or encoding", 413)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, requestLimit)
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "invalid request body", 413)
				return
			}
			defer clear(body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		}
		proxy := &httputil.ReverseProxy{
			Transport: harnessAdmissionTransport{base: http.DefaultTransport, wait: 4 * time.Minute, interval: time.Second},
			Rewrite: func(p *httputil.ProxyRequest) {
				p.SetURL(a.cfg.Harness.ModelUpstream)
				p.Out.URL.Path = strings.TrimPrefix(p.In.URL.Path, harnessModelMount)
				p.Out.Header.Set("Authorization", "Bearer "+a.cfg.Harness.ClientKey)
				p.Out.Header.Del("Cookie")
				p.Out.Header.Del("Idempotency-Key")
				stripComfyGPUHeaders(p.Out.Header)
			},
			ModifyResponse: func(response *http.Response) error {
				response.Header.Del("Set-Cookie")
				response.Header.Set("Cache-Control", "no-store")
				if r.URL.Path == harnessModelMount+"/v1/chat/completions" && response.StatusCode == http.StatusOK && strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
					response.Body = observeHarnessStream(r.Context(), response.Body, logHarnessStream)
				}
				if response.StatusCode == http.StatusConflict {
					_ = response.Body.Close()
					data, _ := json.Marshal(map[string]any{"error": map[string]string{
						"message": "Допуск GPU пока не получен: выполняется другая задача или состояние исполнителей ещё не подтверждено. Повторите запрос позже.",
						"type":    "gateway_error", "code": "gpu_busy",
					}})
					response.Body = io.NopCloser(strings.NewReader(string(data)))
					response.ContentLength = int64(len(data))
					response.Header.Set("Content-Length", strconv.Itoa(len(data)))
					response.Header.Del("Content-Encoding")
					response.Header.Set("Content-Type", "application/json")
				}
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				writeJSON(w, 503, map[string]any{"error": map[string]string{"message": "Исполнитель Ollama недоступен. Повторите запрос позже.", "code": "executor_unavailable"}})
			},
			FlushInterval: -1,
		}
		proxy.ServeHTTP(w, r)
	})
}
