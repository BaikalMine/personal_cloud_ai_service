package updates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestModelVerificationRejectsRedirectAndUnknownState(t *testing.T) {
	var requests int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer target.Close()
	for _, body := range []string{"redirect", `{"protocol":1,"state":"unexpected"}`, `{"protocol":2,"state":"verified"}`} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if body == "redirect" {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				return
			}
			_, _ = w.Write([]byte(body))
		}))
		base, _ := url.Parse(s.URL)
		_, err := NewClient(base, strings.Repeat("x", 32)).VerifyModel(context.Background(), ModelVerifyRequest{})
		s.Close()
		if err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
	if requests != 0 {
		t.Fatal("forwarded private token to redirect target")
	}
}
