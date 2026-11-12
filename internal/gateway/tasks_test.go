package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestTaskQueryParser(t *testing.T) {
	for _, raw := range []string{"limit=0", "limit=101", "limit=wat", "from=yesterday", "user_id=2", "kind=generation&kind=lora_training"} {
		values, _ := url.ParseQuery(raw)
		if _, _, _, err := parseTaskQuery(values); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	values, _ := url.ParseQuery("kind=generation&from=2026-09-06T12:00:00Z&search=model&cursor=cursor")
	filter, limit, cursor, err := parseTaskQuery(values)
	if err != nil || limit != 40 || filter.Kind != "generation" || filter.From == nil || cursor != "cursor" {
		t.Fatalf("query: %+v %d %q %v", filter, limit, cursor, err)
	}
}

func TestTaskViewsOnlyLinkToExistingDetails(t *testing.T) {
	for _, tc := range []struct{ kind, detail, open string }{
		{"generation", "/generate/jobs/detail?job_id=job-1", "/generate?job=job-1"},
		{"lora_training", "/api/lora-training/jobs/job-1", ""},
		{"lora_caption", "/api/lora-training/caption/job-1", ""},
		{"lora_caption_series", "/api/tasks/detail?kind=lora_caption_series&id=job-1", ""},
	} {
		view := makeTaskView(domain.Task{Kind: tc.kind, ID: "job-1"})
		if view.DetailURL != tc.detail || view.OpenURL != tc.open {
			t.Fatalf("incorrect link: %+v", view)
		}
	}
}

func TestTasksAccessAndMethods(t *testing.T) {
	app := &App{}
	for _, tc := range []struct {
		method string
		user   *User
		status int
	}{
		{http.MethodGet, nil, http.StatusUnauthorized},
		{http.MethodGet, &User{ID: 1}, http.StatusForbidden},
		{http.MethodPost, &User{ID: 1, Role: "admin"}, http.StatusMethodNotAllowed},
	} {
		r := httptest.NewRequest(tc.method, "/api/tasks", nil)
		if tc.user != nil {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, tc.user))
		}
		w := httptest.NewRecorder()
		app.handleTasks(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
}

func TestTasksAPIIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	app := &App{store: store.New(db)}
	var owner, other int64
	for name, dest := range map[string]*int64{"tasks-api-owner": &owner, "tasks-api-other": &other} {
		if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES($1,'disabled','user') RETURNING id`, name).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []struct {
		id   string
		user int64
	}{{"task-api-owner-001", owner}, {"task-api-other-001", other}} {
		if _, _, err := app.store.CreateGenerationJob(ctx, domain.CreateGenerationJobParams{PublicID: entry.id, UserID: entry.user, RequestID: entry.id}); err != nil {
			t.Fatal(err)
		}
	}
	request := func(user User, query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/tasks"+query, nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &user))
		w := httptest.NewRecorder()
		app.handleTasks(w, r)
		return w
	}
	for _, user := range []User{{ID: owner, Role: "admin"}, {ID: owner, CanUseQuickGeneration: true}} {
		w := request(user, "")
		var page struct {
			Tasks           []taskView
			Total, Revision int64
			SchemaVersion   int `json:"schema_version"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("API: %d %s", w.Code, w.Body.String())
		}
		if page.Total != 1 || page.SchemaVersion != 1 || len(page.Tasks) != 1 || page.Tasks[0].ID != "task-api-owner-001" || page.Revision <= 0 {
			t.Fatalf("API scope: %+v", page)
		}
		for _, private := range []string{"cipher", "run_token", "task-api-other-001", "username", "password", "user_id"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("private source data exposed: %s", private)
			}
		}
	}
	for _, query := range []string{"?user_id=2", "?kind=unknown", "?state=unknown", "?cursor=invalid", "?search=%00", "?search=%XX"} {
		if w := request(User{ID: owner, Role: "admin"}, query); w.Code != 400 {
			t.Fatalf("query %s: %d %s", query, w.Code, w.Body.String())
		}
	}
	if w := request(User{ID: owner, CanTrainImageLora: true}, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("training-only access: %d %s", w.Code, w.Body.String())
	}
	for _, userID := range []int64{owner, other} {
		if _, err := db.Exec(`INSERT INTO lora_caption_series(id,user_id,name,total_count,completed_count,failed_count,state)
			VALUES('api-series-'||$1::text||'-history',$1::bigint,'History',2,1,1,'partial')`, userID); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		user  User
		total int64
	}{
		{User{ID: owner, Role: "admin"}, 1},
		{User{ID: owner, CanTrainImageLora: true}, 1},
		{User{ID: owner, CanUseQuickGeneration: true}, 0},
	} {
		w := request(tc.user, "?kind=lora_caption_series&state=partial")
		var page struct {
			Tasks []taskView
			Total int64
		}
		if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Total != tc.total {
			t.Fatalf("history scope: %d %s", w.Code, w.Body.String())
		}
		if tc.total == 1 && (len(page.Tasks) != 1 || page.Tasks[0].Series == nil || page.Tasks[0].Series.Total != 2 || page.Tasks[0].NextAction != "inspect" || !strings.HasPrefix(page.Tasks[0].DetailURL, "/api/tasks/detail?kind=lora_caption_series&")) {
			t.Fatalf("history envelope: %+v", page)
		}
	}
}
