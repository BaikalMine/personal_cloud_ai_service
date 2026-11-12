package modelmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestBaseModelsUsesCompleteInstalledCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/lm/loras/base-models" || r.URL.Query().Get("limit") != "0" {
			t.Errorf("unexpected request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"success":true,"base_models":[{"name":"Krea 2","count":3},{"name":"Flux.1 D","count":1}]}`))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	got, err := (&Client{Base: base}).BaseModels(context.Background(), "loras")
	if err != nil || !reflect.DeepEqual(got, []string{"Krea 2", "Flux.1 D"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := (&Client{}).BaseModels(context.Background(), "../settings"); err != ErrInvalid {
		t.Fatalf("unexpected invalid kind error: %v", err)
	}
}
