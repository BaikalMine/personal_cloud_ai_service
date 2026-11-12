package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestAdminOperationsOverviewHTTPIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	if _, err := db.ExecContext(ctx, `TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('ops-http','disabled','admin') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store.New(db), tpl: tpl}
	if _, err := a.store.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: "ops-work", ResourceID: domain.GPUPrimaryResource, Kind: domain.GPUWorkAssistant, JobKey: "ops-work", UserID: userID}); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		t.Helper()
		r := httptest.NewRequest("GET", "/admin/operations/overview", nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: userID, Role: "admin"}))
		w := httptest.NewRecorder()
		a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes)).ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
		var result struct {
			HTML   string         `json:"snapshot_html"`
			System SystemOverview `json:"system"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.HTML, `data-ops-gpu-kind="prompt_assistant"`) || result.System.DatabaseBytes == 0 {
			t.Fatal("incomplete response")
		}
		if strings.Contains(w.Body.String(), "private-ops-token") {
			t.Fatal("credential exposed")
		}
		return result.HTML
	}
	if strings.Contains(read(), `data-attention-key="gpu-unconfirmed"`) {
		t.Fatal("waiting work reported as uncertain")
	}
	if _, err := db.ExecContext(ctx, `UPDATE gpu_work SET state='held',lease_token='private-ops-token',lease_until=now()-interval '1 second' WHERE id='ops-work'`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(), `data-attention-key="gpu-unconfirmed"`) {
		t.Fatal("expired holder missing from attention")
	}
	if _, err := db.ExecContext(ctx, `UPDATE gpu_work SET state='released',lease_token='',lease_until=NULL WHERE id='ops-work'`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), `data-attention-key="gpu-unconfirmed"`) {
		t.Fatal("old attention retained")
	}
}

func TestAdminOperationsOverviewAccessAndMethod(t *testing.T) {
	a := &App{}
	for _, tc := range []struct {
		role, method string
		want         int
	}{
		{"", "GET", 302}, {"user", "GET", 403}, {"admin", "POST", 405}, {"admin", "DELETE", 405}, {"admin", "HEAD", 405},
	} {
		t.Run(tc.role+tc.method, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/admin/operations/overview", nil)
			if tc.role != "" {
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: tc.role}))
			}
			w := httptest.NewRecorder()
			a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes)).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			if tc.role == "admin" && (w.Header().Get("Allow") != "GET" || w.Header().Get("Cache-Control") != "no-store") {
				t.Fatal("method/cache contract missing")
			}
		})
	}
}

func TestAdminOperationsSnapshotIsEscapedAndDoesNotSerializeLeases(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	view := adminOperationsView{
		GeneratedAt: time.Now(), OverallState: "warning",
		Attention: []adminAttentionItem{{Key: "test", Title: `<img src=x onerror=alert(1)>`, Detail: "<script>bad()</script>", Href: "#active-jobs"}},
		Leases:    []domain.QuickGenerationMiningLease{{ID: "private-lease-marker", CorrelationID: "private-correlation-marker"}},
		GPU:       adminGPUWorkViews([]store.AdminGPUWorkSummary{{Kind: domain.GPUWorkTraining, Unconfirmed: 1}}),
	}
	var html bytes.Buffer
	if err := tpl.ExecuteTemplate(&html, "admin_operations_snapshot", map[string]any{"Operations": view, "System": SystemOverview{}}); err != nil {
		t.Fatal(err)
	}
	text := html.String()
	for _, forbidden := range []string{"private-lease-marker", "private-correlation-marker", "<script>", "<img src=x", "<html", "data-system-history"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("snapshot includes %q", forbidden)
		}
	}
	for _, expected := range []string{`data-ops-snapshot`, `&lt;img`, "Обучение LoRA", "Не подтверждено завершение: 1"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("snapshot missing %q", expected)
		}
	}
}
