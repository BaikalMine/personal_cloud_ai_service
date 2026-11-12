package gateway

import (
	"bytes"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestAccountNavigationAndSessionFilters(t *testing.T) {
	for _, selected := range []string{"profile", "notifications", "access", "password", "sessions"} {
		active := 0
		for _, tab := range AccountTabs(selected) {
			if tab.Active {
				active++
				if tab.Key != selected {
					t.Fatal(tab)
				}
			}
		}
		if active != 1 {
			t.Fatal(selected, active)
		}
	}
	for _, raw := range []string{"tab=oops", "tab=profile&tab=access"} {
		if _, ok := accountProfileTab(httptest.NewRequest("GET", "/account/profile?"+raw, nil)); ok {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{"q=a&q=b", "result=invalid", "result=current&result=others", "sort=bad", "offset=-1", "offset=01", "offset=1.2", "offset=1000000001", "offset=1&offset=2", "q=%00", "q=" + strings.Repeat("a", 121)} {
		v, _ := url.ParseQuery(raw)
		if _, err := ParseAccountSessionFilter(v); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	f, err := ParseAccountSessionFilter(url.Values{"q": {"literal%_ device"}, "result": {"others"}, "sort": {"oldest"}, "offset": {"50"}})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(AccountSessionURL(f, f.Offset))
	if err != nil || u.Path != "/account/sessions" || u.Fragment != "session-results" || u.Query().Get("q") != f.Search {
		t.Fatal(u, err)
	}
	p := BuildAccountSessionPagination(store.UserProfilePage{Filter: f, Total: 102}, 50)
	if p.First != 51 || p.Last != 100 || p.PreviousURL == "" || p.NextURL == "" || strings.Contains(p.NextURL, "/admin") {
		t.Fatal(p)
	}
	f.Offset = 100
	p = BuildAccountSessionPagination(store.UserProfilePage{Filter: f, Total: 102}, 2)
	if p.First != 101 || p.Last != 102 || p.NextURL != "" {
		t.Fatal(p)
	}
}

func TestAccountSessionEffectiveDeadlines(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	items := []domain.AccountSession{{ID: 1, LastSeenAt: now, ExpiresAt: now.Add(24 * time.Hour)}, {ID: 2, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}}
	rows := BuildAccountSessionRows(items, 6*time.Hour, time.Time{})
	if !rows[0].EffectiveExpiresAt.Equal(now.Add(6*time.Hour)) || !rows[1].EffectiveExpiresAt.Equal(items[1].ExpiresAt) {
		t.Fatal(rows)
	}
	rows = BuildAccountSessionRows(items, 6*time.Hour, now.Add(30*time.Minute))
	for _, row := range rows {
		if !row.EffectiveExpiresAt.Equal(now.Add(30 * time.Minute)) {
			t.Fatal(row)
		}
	}
}

func TestAccountPagesTemplates(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	for _, tab := range []string{"profile", "notifications", "access", "password", "sessions"} {
		t.Run(tab, func(t *testing.T) {
			user := &domain.User{ID: 1, Username: "<unsafe>", Role: "user"}
			data := map[string]any{"Title": "Settings", "AssetVersion": tpl.AssetVersion, "CurrentUser": user, "CSRF": "test-csrf", "AccountTab": tab, "AccountTabs": AccountTabs(tab), "ProfileUsername": user.Username, "ProfileEmail": "test@example.com", "ThemePreference": "system", "Access": BuildAdminProfileAccess(*user, store.QuickGenerationQuota{}, time.Now()), "Stats": domain.UserStats{}, "Filter": store.UserProfileFilter{}, "Pagination": BuildAccountSessionPagination(store.UserProfilePage{}, 0), "SnapshotAt": time.Now(), "NotificationPreferences": domain.UserNotificationPreferences{InAppEnabled: true}}
			name := "account_profile"
			if tab == "password" || tab == "sessions" {
				name = "account_" + tab
			}
			var b bytes.Buffer
			if err := tpl.ExecuteTemplate(&b, name, data); err != nil {
				t.Fatal(err)
			}
			body := b.String()
			for _, want := range []string{`data-account-page="` + tab + `"`, `aria-current="page"`, "/static/account-pages.css", "/static/account-pages.js"} {
				if !strings.Contains(body, want) {
					t.Fatal(want)
				}
			}
			if strings.Contains(body, "<unsafe>") || strings.Count(body, `aria-current="page"`) != 1 {
				t.Fatal("unsafe data or ambiguous account navigation")
			}
			if tab != "notifications" && strings.Contains(body, "data-notification-settings") {
				t.Fatal("notification form leaked to another tab")
			}
			if tab == "profile" && (!strings.Contains(body, "readonly") || !strings.Contains(body, "/account/theme")) {
				t.Fatal("profile permissions or no-script theme missing")
			}
			if tab == "access" && (strings.Contains(body, `action="/account/generation-mining"`) || strings.Contains(body, "Без лимита")) {
				t.Fatal("unassigned access shown as unlimited or editable")
			}
		})
	}
}

func TestAccountAssetsServedAndVersioned(t *testing.T) {
	for _, asset := range []string{"account-pages.css", "account-pages.js"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/static/"+asset, nil)
		if strings.HasSuffix(asset, ".css") {
			(&App{}).handleStaticCSS(w, r)
		} else {
			(&App{}).handleStaticJS(w, r)
		}
		if w.Code != 200 || w.Body.Len() < 100 {
			t.Fatal(asset, w.Code)
		}
		found := false
		for _, path := range frontendAssetPaths {
			if path == "static/"+asset {
				found = true
			}
		}
		if !found {
			t.Fatal("asset absent from version inventory", asset)
		}
	}
}
