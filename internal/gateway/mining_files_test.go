package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMiningScriptEditorRequiresAdmin(t *testing.T) {
	app := &App{}
	for _, role := range []string{"", "user"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(method, "/admin/mining/script?id=1", nil)
			if role != "" {
				req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &User{Role: role}))
			}
			response := httptest.NewRecorder()
			app.handleAdminMiningScript(response, req)
			if response.Code != http.StatusForbidden {
				t.Fatalf("role=%s method=%s status=%d", role, method, response.Code)
			}
		}
	}
}
