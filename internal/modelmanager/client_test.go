package modelmanager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestModelManagerProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer internal-token" {
			t.Error("missing internal authorization")
		}
		switch r.URL.Path {
		case "/api/lm/loras/civitai/versions/42":
			_, _ = w.Write([]byte(`[{"id":51,"modelId":42,"name":"v1","description":"<p>Version text</p>","files":[]}]`))
		case "/api/lm/loras/civitai/model/version/51":
			_, _ = w.Write([]byte(`{"id":51,"modelId":42,"model":{"name":"Studio light","type":"LORA","description":"<p>Model description</p>"}}`))
		case "/api/lm/settings":
			if r.Method == "POST" {
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				if len(body) != 1 || body["civitai_api_key"] != "new-secret-key" {
					t.Errorf("unexpected key payload fields")
				}
				_, _ = w.Write([]byte(`{"success":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"settings":{"civitai_api_key":"must-not-leak","civitai_api_key_set":true,"proxy_password":"also-private","default_lora_root":"C:/ComfyUI/models/loras"}}`))
		case "/api/lm/loras/list":
			if r.URL.Query().Get("page_size") != "24" {
				t.Error("unbounded list")
			}
			_, _ = w.Write([]byte(`{"items":[],"total":0,"page":1,"total_pages":0}`))
		default:
			http.Error(w, "private upstream error with key", 500)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	c := Client{Base: base, Auth: "Bearer internal-token"}
	v, err := c.Versions(context.Background(), "loras", 42)
	if err != nil || v.Type != "LORA" || v.Name != "Studio light" || v.Description != "Model description" || v.Versions[0].Description != "Version text" {
		t.Fatalf("versions = %+v %v", v, err)
	}
	s, err := c.Settings(context.Background())
	if err != nil || !s.KeySet {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "must-not-leak") || strings.Contains(string(raw), "proxy_password") {
		t.Fatal("settings leaked secrets")
	}
	if err = c.SetKey(context.Background(), "new-secret-key"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.List(context.Background(), "loras", "", "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = c.List(context.Background(), "../settings", "", "", 1); err != ErrInvalid {
		t.Fatal("unsafe path accepted")
	}
	if _, _, err = c.Preview(context.Background(), "http://evil.invalid/api/lm/previews?path=x"); err != ErrInvalid {
		t.Fatal("remote preview accepted")
	}
	if _, err = c.Progress(context.Background(), "id"); err != ErrUnavailable || strings.Contains(err.Error(), "private") {
		t.Fatal("upstream error leaked")
	}
}
func TestModelManagerNoRedirect(t *testing.T) {
	var calls int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	u, _ := url.Parse(source.URL)
	_, err := (&Client{Base: u, Auth: "Bearer secret"}).Settings(context.Background())
	if err == nil || calls != 0 {
		t.Fatal("followed credential-bearing redirect")
	}
}
func TestModelManagerPlainText(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"<p>Model &amp; LoRA</p>", "Model & LoRA"}, {"<script>alert(1)</script><b>safe</b>", "safe"}, {"plain description", "plain description"}, {"<style>body{}</style>Text", "Text"}} {
		if got := PlainText(tc.in); got != tc.want {
			t.Errorf("%q: %q", tc.in, got)
		}
	}
}
