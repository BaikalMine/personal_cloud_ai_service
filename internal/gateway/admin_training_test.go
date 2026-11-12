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

func TestAdminTrainingFiltersAndReturnContext(t *testing.T) {
	for _, raw := range []string{"q=a&q=b", "offset=-1", "offset=wat", "offset=999999999999999999999", "q=%zz", "state=bad", "family=minimax", "sort=DROP", "q=" + strings.Repeat("я", 121)} {
		if _, err := parseAdminTrainingFilter(raw); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	f, err := parseAdminTrainingFilter("q=+Anna+&family=krea2&state=active&sort=oldest&offset=25")
	if err != nil || f.Search != "Anna" || f.Offset != 25 {
		t.Fatal(f, err)
	}
	r := httptest.NewRequest("POST", "/admin/lora-training/run/cancel"+adminTrainingQuery(f)+"&return_to=https://example.com", nil)
	redirect := adminTrainingRedirect(r, "cancelled")
	if !strings.HasPrefix(redirect, "/admin/lora-training?") || !strings.Contains(redirect, "offset=25") || !strings.Contains(redirect, "cancelled=1") || strings.Contains(redirect, "example.com") {
		t.Fatal(redirect)
	}
}

func TestAdminTrainingReadAccessAndMethods(t *testing.T) {
	a := &App{}
	for _, path := range []string{"/admin/lora-training", "/admin/lora-training/overview", "/admin/lora-training/gpu"} {
		for _, tc := range []struct {
			role, method string
			want         int
		}{{"", "GET", 302}, {"user", "GET", 403}, {"admin", "POST", 405}, {"admin", "HEAD", 405}} {
			r := httptest.NewRequest(tc.method, path, nil)
			if tc.role != "" {
				r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: tc.role}))
			}
			w := httptest.NewRecorder()
			a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes)).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("%s %s %s: %d", path, tc.role, tc.method, w.Code)
			}
		}
	}
	for _, q := range []string{"task=generation:foreign-source", "task=lora_caption:foreign-source", "task=lora_training:same&task=lora_training:same", "task=lora_training:bad.id", "q=any"} {
		r := httptest.NewRequest("GET", "/admin/lora-training/gpu?"+q, nil)
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: "admin"}))
		w := httptest.NewRecorder()
		a.handleAdminRoutes(w, r)
		if w.Code != 400 {
			t.Fatal("GPU filter", q, w.Code)
		}
	}
}

func TestAdminTrainingSnapshotEscapingAndPrivateFields(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	job := domain.LoraTrainingJob{PublicID: "training-private-test", Name: `<img src=x onerror=bad()>`, UsernameSnapshot: "<script>owner</script>", State: domain.LoraTrainingFailed, ErrorMessage: "<script>error</script>", DatasetPath: "private-dataset-path", DispatchToken: "private-dispatch-token", AgentJobID: "private-agent-job"}
	v := adminTrainingView{Jobs: []adminTrainingRow{{loraTrainingJobView: newLoraTrainingJobView(job)}}}
	var b bytes.Buffer
	if err := tpl.ExecuteTemplate(&b, "admin_training_snapshot", map[string]any{"View": v, "CSRF": "fixture-csrf"}); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-dataset-path", "private-dispatch-token", "private-agent-job", "<script>", "<img src=x"} {
		if strings.Contains(b.String(), secret) {
			t.Fatal("unsafe fragment", secret)
		}
	}
	if !strings.Contains(b.String(), "&lt;img") || !strings.Contains(b.String(), "fixture-csrf") {
		t.Fatal("escaping/actions missing")
	}
}

func TestAdminTrainingOverviewHTTPIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	var owner int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('training-http','disabled','user') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store.New(db), tpl: tpl}
	_, err = a.store.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{PublicID: "admin-training-http", UserID: owner, UsernameSnapshot: "training-http", RequestID: "training-http-request", ProfileID: "profile", Family: "krea2", BaseModel: "base.safetensors", Name: "HTTP training", OutputName: "http_training", TriggerWord: "subject", ConceptType: "object", Preset: "quick", Resolution: 768, MaxTrainSteps: 800, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.0001, Seed: 1, SampleCount: 10, DatasetBytes: 1024, DatasetPath: "private-http-dataset"})
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: owner, Role: "admin"}))
		w := httptest.NewRecorder()
		a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes)).ServeHTTP(w, r)
		return w
	}
	w := read("/admin/lora-training/overview?state=active&q=HTTP")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		HTML string `json:"snapshot_html"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"admin-training-http", "HTTP training", "1–1 из 1", "Локальный агент обучения ещё не настроен"} {
		if !strings.Contains(result.HTML, value) {
			t.Fatal("missing", value)
		}
	}
	if strings.Contains(result.HTML, "private-http-dataset") {
		t.Fatal("private source path leaked")
	}
	if w = read("/admin/lora-training/overview?state=completed"); w.Code != 200 || !strings.Contains(w.Body.String(), "Нет запусков по выбранным фильтрам") {
		t.Fatal("empty", w.Code, w.Body.String())
	}
	if w = read("/admin/lora-training/gpu?task=lora_training:admin-training-http"); w.Code != 200 {
		t.Fatal("GPU", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("POST", "/admin/lora-training/admin-training-http/cancel", strings.NewReader("csrf=wrong"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: owner, Role: "admin"}))
	w = httptest.NewRecorder()
	a.handleAdminRoutes(w, r)
	if w.Code != 403 {
		t.Fatal("CSRF write accepted", w.Code)
	}
	job, err := a.store.LoraTrainingJobByPublicID(ctx, "admin-training-http", owner, true)
	if err != nil || job.CancellationRequestedAt != nil {
		t.Fatal("denied write changed job", job, err)
	}
}
