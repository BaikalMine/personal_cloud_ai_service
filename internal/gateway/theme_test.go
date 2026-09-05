package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThemePreferenceCookie(t *testing.T) {
	for _, value := range []string{"", "light", "dark", "system", "LIGHT", "invalid"} {
		r := httptest.NewRequest("GET", "/login", nil)
		r.AddCookie(&http.Cookie{Name: "ai_gateway_theme", Value: value})
		want := value
		if value != "light" && value != "dark" {
			want = "system"
		}
		if got := ThemePreference(r); got != want {
			t.Fatalf("%q: got %q want %q", value, got, want)
		}
	}
}

func TestThemeAssetsAndPrepaintMarkup(t *testing.T) {
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, preference := range []string{"light", "dark", "system"} {
		var output bytes.Buffer
		if err := templates.ExecuteTemplate(&output, "login", map[string]any{"ThemePreference": preference, "AssetVersion": templates.AssetVersion}); err != nil {
			t.Fatal(err)
		}
		html := output.String()
		if !strings.Contains(html, `data-theme-preference="`+preference+`"`) {
			t.Fatal("preference missing")
		}
		if preference != "system" && !strings.Contains(html, `data-theme="`+preference+`"`) {
			t.Fatal("server theme missing")
		}
		script, css := strings.Index(html, "/static/theme.js"), strings.Index(html, "/static/theme.css")
		if script < 0 || css < script || strings.Contains(html[script:css], "defer") {
			t.Fatal("theme must resolve before styles load")
		}
		if strings.Count(html, `data-theme-preference-control`) != 1 {
			t.Fatal("login theme control missing or duplicated")
		}
	}
	if _, ok := staticCSSAssets["/static/theme.css"]; !ok {
		t.Fatal("theme stylesheet not served")
	}
	if _, ok := staticJavaScriptAssets["/static/theme.js"]; !ok {
		t.Fatal("theme script not served")
	}
}
