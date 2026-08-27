package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const openWebUIHTMLLimit = 4 << 20

const aiGatewayReturnStyle = `<style id="ai-gateway-return-style">.ai-gateway-return-bar{position:fixed;top:50%;right:0;z-index:99999;display:block;width:34px;height:44px;margin:0;padding:0;box-sizing:border-box;pointer-events:none;transform:translateY(-50%);font-family:system-ui,sans-serif}.ai-gateway-return{all:initial;box-sizing:border-box;display:flex;align-items:center;justify-content:center;width:34px;height:44px;padding:0;border:1px solid rgba(98,221,181,.72);border-right:0;border-radius:8px 0 0 8px;background:#183c31;box-shadow:0 3px 10px rgba(0,0,0,.24);color:#b6f2dc;cursor:pointer;font:700 18px/1 system-ui,sans-serif;text-decoration:none;pointer-events:auto;transition:width .16s ease,background .16s ease,border-color .16s ease}.ai-gateway-return:hover{width:40px;border-color:#83e7c6;background:#1b4335}.ai-gateway-return:focus-visible{outline:2px solid #55c7d8;outline-offset:-3px}.ai-gateway-return-label{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}.ai-gateway-comfy-return-bar{position:fixed;top:10px;right:10px;z-index:99999;display:block;width:36px;height:36px;margin:0;padding:0;box-sizing:border-box;pointer-events:none}.ai-gateway-comfy-return-link{all:initial;box-sizing:border-box;display:flex;align-items:center;justify-content:center;width:36px;height:36px;padding:0;border:1px solid rgba(98,221,181,.72);border-radius:8px;background:#183c31;box-shadow:0 3px 10px rgba(0,0,0,.24);color:#b6f2dc;cursor:pointer;font:700 18px/1 system-ui,sans-serif;text-decoration:none;pointer-events:auto;transition:background .16s ease,border-color .16s ease,transform .16s ease}.ai-gateway-comfy-return-link:hover{border-color:#83e7c6;background:#1b4335;transform:translateY(-1px)}.ai-gateway-comfy-return-link:focus-visible{outline:2px solid #55c7d8;outline-offset:2px}@media (max-width:600px){.ai-gateway-return-bar{width:30px;height:40px}.ai-gateway-return{width:30px;height:40px}.ai-gateway-return:hover{width:34px}.ai-gateway-comfy-return-bar{top:8px;right:8px}}</style>`

const openWebUIReturnLink = `<div class="ai-gateway-return-bar" role="navigation" aria-label="Навигация AI Gateway"><a class="ai-gateway-return" href="/app" aria-label="Вернуться в AI Gateway" title="Вернуться в AI Gateway">&larr;<span class="ai-gateway-return-label">AI Gateway</span></a></div>`
const comfyUIReturnLink = `<div class="ai-gateway-comfy-return-bar" role="navigation" aria-label="Навигация AI Gateway"><a class="ai-gateway-comfy-return-link" href="/app" aria-label="Вернуться в AI Gateway" title="Вернуться в AI Gateway">&larr;</a></div>`

func injectAIReturnLink(resp *http.Response, link string) error {
	if resp == nil || resp.Body == nil || !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, openWebUIHTMLLimit+1))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if len(body) > openWebUIHTMLLimit || bytes.Contains(body, []byte("ai-gateway-return")) {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}

	lower := bytes.ToLower(body)
	if index := bytes.Index(lower, []byte("</head>")); index >= 0 {
		body = insertBytes(body, index, []byte(aiGatewayReturnStyle))
	}
	lower = bytes.ToLower(body)
	if start := bytes.Index(lower, []byte("<body")); start >= 0 {
		if end := bytes.IndexByte(body[start:], '>'); end >= 0 {
			body = insertBytes(body, start+end+1, []byte(link))
		}
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Del("Content-Encoding")
	return nil
}

func injectOpenWebUIReturnLink(resp *http.Response) error {
	return injectAIReturnLink(resp, openWebUIReturnLink)
}

func injectComfyUIReturnLink(resp *http.Response) error {
	return injectAIReturnLink(resp, comfyUIReturnLink)
}

func insertBytes(body []byte, index int, insertion []byte) []byte {
	result := make([]byte, 0, len(body)+len(insertion))
	result = append(result, body[:index]...)
	result = append(result, insertion...)
	return append(result, body[index:]...)
}

func (a *App) proxyRootHandler(service string, upstream *url.URL, authHeader string) http.Handler {
	return a.proxyHandlerWithPath(service, "", upstream, authHeader, false)
}

func (a *App) proxyHandlerWithPath(service, prefix string, upstream *url.URL, authHeader string, stripPrefix bool) http.Handler {
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			user := a.currentUser(req)
			originalHost := req.Host
			req.URL.Scheme = upstream.Scheme
			req.URL.Host = upstream.Host
			rewriteProxyURL(req.URL, upstream, prefix, stripPrefix)
			req.Host = upstream.Host
			req.Header.Set("X-Forwarded-Host", originalHost)
			req.Header.Set("X-Forwarded-Proto", a.requestProto(req))
			req.Header.Set("X-Forwarded-For", a.clientIP(req))
			req.Header.Set("X-Real-IP", a.clientIP(req))
			if prefix != "" {
				req.Header.Set("X-Forwarded-Prefix", strings.TrimRight(prefix, "/"))
			}
			if service != "openwebui" && service != "comfyui" {
				req.Header.Del("Authorization")
			} else {
				// The gateway injects a return link into the initial HTML page.
				// Avoid upstream compression so the response body can be rewritten.
				req.Header.Set("Accept-Encoding", "")
			}
			stripCookie(req, sessionCookieName)
			stripCookie(req, serviceCookieName)
			a.prepareUpstreamIdentity(req, service, user)
			if authHeader != "" {
				req.Header.Set("Authorization", authHeader)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if service == "comfyui" {
				if err := a.filterComfyResponse(resp); err != nil {
					return err
				}
				if err := injectComfyUIReturnLink(resp); err != nil {
					return err
				}
			}
			attachResponseCapture(resp)
			if service == "openwebui" {
				if err := injectOpenWebUIReturnLink(resp); err != nil {
					return err
				}
				a.bindOpenWebUIIdentity(resp)
			}
			if prefix != "" {
				rewriteSetCookiePath(resp, prefix)
				rewriteLocation(resp, prefix, upstream)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.WriteHeader(http.StatusBadGateway)
			_ = a.tpl.ExecuteTemplate(w, "bad_gateway", map[string]any{
				"Title": "Сервис недоступен", "Service": service,
				"AssetVersion": a.tpl.AssetVersion,
			})
		},
		FlushInterval: -1,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := a.currentUser(r)
		if service == "openwebui" {
			r = r.WithContext(context.WithValue(r.Context(), openWebCookieSecureKey{}, a.cookieSecure(r)))
		}
		if service == "comfyui" {
			stateWriter := &captureWriter{ResponseWriter: w}
			started := time.Now()
			if a.handleComfyUserState(stateWriter, r, user) {
				status := stateWriter.status
				if status == 0 {
					status = http.StatusOK
				}
				bytesIn := r.ContentLength
				if bytesIn < 0 {
					bytesIn = 0
				}
				a.recordProxyRequest(r.Context(), user.ID, service, r.Method, sanitizedPath(r.URL), status,
					time.Since(started), bytesIn, stateWriter.bytes, false, a.clientIP(r), r.UserAgent())
				a.incProxyCount(service, status)
				return
			}
			if isComfyUploadRequest(r) {
				if a.comfyUploadSlots != nil {
					select {
					case a.comfyUploadSlots <- struct{}{}:
						defer func() { <-a.comfyUploadSlots }()
					default:
						http.Error(w, "слишком много одновременных загрузок ComfyUI", http.StatusTooManyRequests)
						return
					}
				}
				if err := a.rewriteComfyUpload(w, r, user); err != nil {
					status := http.StatusBadRequest
					var tooLarge *http.MaxBytesError
					if errors.As(err, &tooLarge) {
						status = http.StatusRequestEntityTooLarge
					} else if errors.Is(err, errForeignComfyAsset) {
						status = http.StatusForbidden
					}
					http.Error(w, "некорректная загрузка ComfyUI", status)
					return
				}
			}
		}
		allowed, err := a.authorizeComfyMediaRequest(r, user)
		if err != nil {
			http.Error(w, "ошибка проверки доступа", http.StatusInternalServerError)
			return
		}
		if !allowed {
			http.NotFound(w, r)
			return
		}
		allowed, err = a.enforceComfyControlIsolation(r, user, service)
		if err != nil {
			http.Error(w, "не удалось проверить владельца задания ComfyUI", http.StatusBadGateway)
			return
		}
		if !allowed {
			http.Error(w, "это действие доступно только для ваших заданий ComfyUI", http.StatusForbidden)
			return
		}
		if err := a.enforceComfyPromptIdentity(r, user); err != nil {
			status := http.StatusRequestEntityTooLarge
			if errors.Is(err, errForeignComfyAsset) {
				status = http.StatusForbidden
			}
			http.Error(w, "некорректный запрос ComfyUI", status)
			return
		}
		contentCapture, err := a.beginContentCapture(r, user, service)
		if err != nil {
			http.Error(w, "некорректный запрос к сервису", http.StatusBadRequest)
			return
		}
		if contentCapture != nil {
			defer contentCapture.Release()
			r = r.WithContext(context.WithValue(r.Context(), contentCaptureKey{}, contentCapture))
		}
		started := time.Now()
		isWS := isWebSocket(r)
		clientIP := a.clientIP(r)
		userAgent := r.UserAgent()
		path := sanitizedPath(r.URL)
		bytesIn := r.ContentLength
		if bytesIn < 0 {
			bytesIn = 0
		}

		var wsID int64
		if isWS {
			a.activeWS.Add(1)
			defer a.activeWS.Add(-1)
			wsID = a.openWebSocketSession(r.Context(), user.ID, service, clientIP, userAgent)
		}

		cw := &captureWriter{ResponseWriter: w}
		proxy.ServeHTTP(cw, r)
		persistCtx, cancelPersist := contentPersistenceContext(r.Context())
		err = a.persistContentCapture(persistCtx, contentCapture)
		cancelPersist()
		if err != nil {
			log.Printf("capture %s content: %v", service, err)
		}
		status := cw.status
		if status == 0 {
			status = http.StatusOK
		}
		duration := time.Since(started)

		a.recordProxyRequest(r.Context(), user.ID, service, r.Method, path, status, duration, bytesIn, cw.bytes, isWS, clientIP, userAgent)
		if isWS && wsID > 0 {
			a.closeWebSocketSession(r.Context(), wsID, duration)
		}
		a.incProxyCount(service, status)
	})
}

func rewriteProxyURL(requestURL, upstream *url.URL, prefix string, stripPrefix bool) {
	targetQuery := upstream.RawQuery
	targetPath := requestURL.Path
	targetRawPath := requestURL.EscapedPath()
	if stripPrefix {
		targetPath = strings.TrimPrefix(targetPath, prefix)
		targetRawPath = strings.TrimPrefix(targetRawPath, prefix)
	}

	requestURL.Path = singleJoiningSlash(upstream.Path, targetPath)
	requestURL.RawPath = ""
	if targetRawPath != "" && targetRawPath != targetPath {
		requestURL.RawPath = singleJoiningSlash(upstream.EscapedPath(), targetRawPath)
	}
	if requestURL.Path == "" {
		requestURL.Path = "/"
	}
	if targetQuery == "" || requestURL.RawQuery == "" {
		requestURL.RawQuery = targetQuery + requestURL.RawQuery
	} else {
		requestURL.RawQuery = targetQuery + "&" + requestURL.RawQuery
	}
}
