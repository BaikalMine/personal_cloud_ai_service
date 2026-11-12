package miningagent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"ai-access-gateway/internal/mining"
)

type fakeFileController struct {
	fakeController
	saved int
}

func (f *fakeFileController) Files(context.Context) (mining.FileCatalog, error) {
	return mining.FileCatalog{Files: []mining.LaunchFile{{Path: `C:\Mining\start.bat`, Name: "start.bat", Kind: "script"}}}, nil
}
func (f *fakeFileController) SaveScript(_ context.Context, input mining.SaveScriptRequest) (mining.Script, error) {
	f.saved++
	return mining.Script{Path: input.ScriptPath, Content: input.Content, SHA256: input.ExpectedSHA256}, nil
}

func TestFileAPIAuthenticationAndValidation(t *testing.T) {
	f := &fakeFileController{}
	token := "01234567890123456789012345678901"
	s, _ := NewServer(token, f)
	for _, path := range []string{"/v1/files", "/v1/script/save"} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatal(response.Code)
		}
	}
	body, _ := json.Marshal(mining.SaveScriptRequest{Request: mining.Request{ScriptPath: `C:\Mining\start.bat`, ProcessName: "miner.exe"}, Content: "@echo off", ExpectedSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	for _, suffix := range []string{"", " {}"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/script/save", bytes.NewReader(append(append([]byte{}, body...), []byte(suffix)...)))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, req)
		want := http.StatusOK
		if suffix != "" {
			want = http.StatusBadRequest
		}
		if response.Code != want {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if f.saved != 1 || f.request.ScriptPath != "" {
		t.Fatal("invalid edit or start/stop invoked")
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := mining.NewClient(base, token)
	catalog, err := client.Files(context.Background())
	if err != nil || len(catalog.Files) != 1 {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	var input mining.SaveScriptRequest
	_ = json.Unmarshal(body, &input)
	result, err := client.SaveScript(context.Background(), input)
	if err != nil || result.Content != input.Content {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
