package gateway

import "net/http"

// ThemePreference is shared by real page rendering and the isolated UI preview.
// Cookie input is constrained before being emitted into document attributes.
func ThemePreference(r *http.Request) string {
	if cookie, err := r.Cookie("ai_gateway_theme"); err == nil {
		switch cookie.Value {
		case "light", "dark", "system":
			return cookie.Value
		}
	}
	return "system"
}
