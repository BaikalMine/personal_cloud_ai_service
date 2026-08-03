package gateway

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestInjectOpenWebUIReturnLink(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:   io.NopCloser(strings.NewReader(`<!doctype html><html><head></head><body data-sveltekit-preload-data="hover"><main>OpenWebUI</main></body></html>`)),
	}

	if err := injectOpenWebUIReturnLink(resp); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	content := string(body)
	if !strings.Contains(content, `class="ai-gateway-return"`) || !strings.Contains(content, `href="/app"`) {
		t.Fatalf("return link was not injected: %s", content)
	}
	if got, want := resp.ContentLength, int64(len(body)); got != want {
		t.Fatalf("ContentLength = %d, want %d", got, want)
	}
}

func TestInjectComfyUIReturnLink(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:   io.NopCloser(strings.NewReader(`<!doctype html><html><head></head><body><main>ComfyUI</main></body></html>`)),
	}

	if err := injectComfyUIReturnLink(resp); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	content := string(body)
	if !strings.Contains(content, `class="ai-gateway-comfy-return-link"`) || !strings.Contains(content, `href="/app"`) {
		t.Fatalf("return link was not injected: %s", content)
	}
}

func TestInjectOpenWebUIReturnLinkSkipsNonHTML(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}

	if err := injectOpenWebUIReturnLink(resp); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != `{"ok":true}` {
		t.Fatalf("non-HTML response changed: %s", got)
	}
}

func TestRewriteProxyURLPreservesEncodedWorkflowSlash(t *testing.T) {
	requestURL, err := url.Parse("/comfyui/api/userdata/workflows%2Fexample.json?full_info=true")
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := url.Parse("http://host.docker.internal:8088")
	if err != nil {
		t.Fatal(err)
	}

	rewriteProxyURL(requestURL, upstream, "/comfyui/", true)

	if got, want := requestURL.Path, "/api/userdata/workflows/example.json"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	if got, want := requestURL.RawPath, "/api/userdata/workflows%2Fexample.json"; got != want {
		t.Fatalf("RawPath = %q, want %q", got, want)
	}
	if got, want := requestURL.EscapedPath(), "/api/userdata/workflows%2Fexample.json"; got != want {
		t.Fatalf("EscapedPath = %q, want %q", got, want)
	}
	if got, want := requestURL.RawQuery, "full_info=true"; got != want {
		t.Fatalf("RawQuery = %q, want %q", got, want)
	}
}

func TestCaptureWriterRecordsWebSocketUpgrade(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})
	underlying := &hijackResponseWriter{conn: serverConn}
	w := &captureWriter{ResponseWriter: underlying}

	if _, _, err := w.Hijack(); err != nil {
		t.Fatal(err)
	}
	if got, want := w.status, http.StatusSwitchingProtocols; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

type hijackResponseWriter struct {
	conn net.Conn
}

func (w *hijackResponseWriter) Header() http.Header {
	return make(http.Header)
}

func (w *hijackResponseWriter) Write(body []byte) (int, error) {
	return len(body), nil
}

func (w *hijackResponseWriter) WriteHeader(int) {}

func (w *hijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}
