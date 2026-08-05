package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestUpdateRequestFromFormDirectInstallIgnoresBatchSelections(t *testing.T) {
	form := url.Values{
		"action":     {"install:openwebui"},
		"components": {"gateway", "comfyui", "openwebui"},
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/updates", nil)
	request.Form = form

	action, update := updateRequestFromForm(request)
	if action != "install" {
		t.Fatalf("action = %q, want install", action)
	}
	if len(update.Components) != 1 || update.Components[0] != "openwebui" {
		t.Fatalf("components = %#v, want only openwebui", update.Components)
	}
}
