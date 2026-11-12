package trainingagent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/componentinspection"
	"ai-access-gateway/internal/loratraining"
)

func TestGenerationComponentRouteReadOnly(t *testing.T) {
	root := t.TempDir()
	file := touchTestFile(t, root, "model.safetensors")
	inspector, err := componentinspection.New(map[string][]string{"diffusion_model": {root}})
	if err != nil {
		t.Fatal(err)
	}
	defer inspector.Close()
	controller := &Controller{components: inspector, jobs: map[string]*jobRecord{}}
	token := strings.Repeat("t", 32)
	server, err := NewServer(token, controller)
	if err != nil {
		t.Fatal(err)
	}
	request := componentinspection.Request{Version: 1, RequestID: "generation-request-001", References: []componentinspection.Reference{{Kind: "diffusion_model", Name: "model.safetensors"}}}
	encoded, _ := json.Marshal(request)
	for _, tc := range []struct {
		method, query, body, token string
		code                       int
	}{
		{"POST", "", string(encoded), "", 401},
		{"GET", "", "", token, 405},
		{"POST", "?root=/", string(encoded), token, 400},
		{"POST", "", string(encoded) + " {}", token, 400},
		{"POST", "", strings.Replace(string(encoded), "model.safetensors", "../model.safetensors", 1), token, 400},
		{"POST", "", strings.Replace(string(encoded), `"version":1`, `"root":"/","version":1`, 1), token, 400},
		{"POST", "", strings.Repeat(" ", 128<<10) + string(encoded), token, 400},
		{"POST", "", string(encoded), token, 200},
	} {
		r := httptest.NewRequest(tc.method, "/v1/generation/components/inspect"+tc.query, strings.NewReader(tc.body))
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.query, w.Code, w.Body.String())
		}
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	base, _ := url.Parse(httpServer.URL)
	client := loratraining.NewClient(base, token)
	var result componentinspection.Response
	for deadline := time.Now().Add(5 * time.Second); ; {
		result, err = client.InspectGenerationComponents(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if result.State == "complete" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inspection never completed")
		}
		time.Sleep(time.Millisecond)
	}
	if result.Items[0].State != "verified" || result.Items[0].Bytes != 4 || result.Items[0].SHA256 != "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08" {
		t.Fatalf("real file not observed: %+v", result)
	}
	if len(controller.jobs) != 0 {
		t.Fatal("inspection created training work")
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	result, err = client.InspectGenerationComponents(context.Background(), request)
	if err != nil || result.Items[0].State != "missing" {
		t.Fatalf("cached removed file: %+v %v", result, err)
	}
	inspector.Close()
	if _, err = client.InspectGenerationComponents(context.Background(), request); err == nil {
		t.Fatal("closed inspector accepted request")
	}
}

func TestGenerationComponentDirectoryConfig(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		dirs  map[string][]string
		valid bool
	}{
		{nil, true}, {map[string][]string{"lora": {root}}, true},
		{map[string][]string{"arbitrary": {root}}, false},
		{map[string][]string{"lora": {}}, false},
		{map[string][]string{"lora": {"relative"}}, false},
		{map[string][]string{"lora": {root + string(filepath.Separator) + ".."}}, false},
		{map[string][]string{"lora": {root, root, root, root, root, root, root, root, root}}, false},
	} {
		config := Config{GenerationComponentDirs: tc.dirs, Profiles: []ProfileConfig{{ID: "test", Family: "krea2", Name: "Test", BaseModel: "model.safetensors"}}}
		if err := config.Validate(); (err == nil) != tc.valid {
			t.Fatalf("%v: %v", tc.dirs, err)
		}
	}
}
