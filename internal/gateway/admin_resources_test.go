package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
	"ai-access-gateway/internal/store"
)

func TestAdminResourceObservationAndMiningAdmission(t *testing.T) {
	now := time.Now().UTC()
	miner := MinerView{Miner: domain.Miner{ID: 1, Name: "Fixture", Enabled: true, Default: true}, State: mining.State{Available: true, CollectedAt: now}}
	m := MiningOverview{Available: true, Miners: []MinerView{miner}}
	resource := domain.GPUResource{Observation: "idle", ObservedAt: now.Add(-time.Second), ValidUntil: now.Add(time.Second)}
	gpu := []store.AdminGPUWorkSummary{{Kind: domain.GPUWorkGeneration, SnapshotAt: now}}
	view := newAdminResourceView(now, "mining", DependencyStatus{}, gpu, resource, nil, &m, true)
	if !view.Profiles[1].CanStart || view.Profiles[1].CanStop || view.ObservationLabel != "Исполнители не сообщили об активной работе" {
		t.Fatal(view)
	}
	version := view.ProfileVersion
	resource.ValidUntil = now
	view = newAdminResourceView(now, "mining", DependencyStatus{}, gpu, resource, nil, &m, true)
	if view.ObservationLabel != "Наблюдение устарело" {
		t.Fatal(view.ObservationLabel)
	}
	if got := newAdminResourceView(now, "comfyui", DependencyStatus{}, nil, domain.GPUResource{}, nil, nil, false); strings.Contains(got.ObservationLabel, "Свобод") {
		t.Fatal(got)
	}
	for _, state := range []string{"reserved", "running", "unconfirmed"} {
		group := gpu[0]
		switch state {
		case "reserved":
			group.Reserved = 1
		case "running":
			group.Running = 1
		case "unconfirmed":
			group.Unconfirmed = 1
		}
		v := newAdminResourceView(now, "mining", DependencyStatus{}, []store.AdminGPUWorkSummary{group}, resource, nil, &m, true)
		if v.Profiles[1].CanStart || v.Profiles[1].CanUpdate {
			t.Fatal(state, v.Profiles[1])
		}
	}
	leases := []domain.QuickGenerationMiningLease{{GenerationJobID: 1, CreatedAt: now.Add(-time.Minute)}, {LoraTrainingJobID: 2, CreatedAt: now}, {GPUWorkID: "private-work", ResumeReady: true, CreatedAt: now}}
	view = newAdminResourceView(now, "mining", DependencyStatus{}, gpu, resource, leases, &m, false)
	if len(view.Pauses) != 3 || view.Pauses[2].Ready != 1 || view.Profiles[1].CanStart || view.Label != "Пауза для задач" {
		t.Fatal(view)
	}
	m.Miners[0].State.Running = true
	view = newAdminResourceView(now, "mining", DependencyStatus{}, gpu, resource, leases, &m, true)
	if !view.Profiles[1].CanStop || view.Profiles[1].CanChange || view.ProfileVersion != version {
		t.Fatal(view.Profiles[1])
	}
	m.Miners[0].Name = "Changed"
	if newAdminResourceView(now, "mining", DependencyStatus{}, gpu, resource, nil, &m, true).ProfileVersion == version {
		t.Fatal("configuration change was not detected")
	}
	for _, collected := range []time.Time{{}, now.Add(-46 * time.Second), now.Add(time.Minute)} {
		m.Miners[0].State.CollectedAt = collected
		s := newAdminResourceView(now, "mining", DependencyStatus{}, gpu, resource, nil, &m, true).Profiles[1]
		if s.CanStart || s.CanStop || s.CanUpdate || s.CanChange || s.Label != "Не подтверждено" {
			t.Fatal(s)
		}
	}
}

func TestAdminResourceReadGuardsAndEscaping(t *testing.T) {
	a := &App{}
	for _, tc := range []struct {
		role, method, query string
		code                int
	}{{"", "GET", "mining", 403}, {"user", "GET", "mining", 403}, {"admin", "POST", "mining", 405}, {"admin", "HEAD", "mining", 405}, {"admin", "GET", "../private", 400}} {
		r := httptest.NewRequest(tc.method, "/admin/resources/overview?service="+tc.query, nil)
		if tc.role != "" {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{ID: 1, Role: tc.role}))
		}
		w := httptest.NewRecorder()
		a.handleAdminRoutes(w, r)
		if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(tc, w.Code)
		}
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	v := newAdminResourceView(time.Now().UTC(), "comfyui", DependencyStatus{Detail: `<script>private</script>`}, nil, domain.GPUResource{Message: `<img src=x onerror=bad()>`}, []domain.QuickGenerationMiningLease{{GPUWorkID: "private-work", ScriptPath: "private-script", CorrelationID: "private-correlation", ID: "private-lease"}}, nil, true)
	var b bytes.Buffer
	if err = tpl.ExecuteTemplate(&b, "admin_resource_snapshot", v); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"<script>", "<img src=x", "private-work", "private-script", "private-correlation", "private-lease", "<form"} {
		if strings.Contains(b.String(), marker) {
			t.Fatal("leaked", marker)
		}
	}
	if !strings.Contains(b.String(), "&lt;script&gt;") {
		t.Fatal("missing escaped diagnostic")
	}
}

func TestAdminResourcePartiallyStaleProfilesCannotStart(t *testing.T) {
	now := time.Now().UTC()
	m := MiningOverview{Available: true, Miners: []MinerView{
		{Miner: domain.Miner{ID: 1, Enabled: true}, State: mining.State{Available: true, CollectedAt: now}},
		{Miner: domain.Miner{ID: 2, Enabled: true}, State: mining.State{Available: true, CollectedAt: now.Add(-time.Minute)}},
	}}
	v := newAdminResourceView(now, "mining", DependencyStatus{}, nil, domain.GPUResource{}, nil, &m, true)
	for _, state := range v.Profiles {
		if state.CanStart || state.CanUpdate {
			t.Fatal(state)
		}
	}
	if v.Label != "Состояние не подтверждено" {
		t.Fatal(v.Label)
	}
}

func TestAdminResourceHTTPIntegration(t *testing.T) {
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
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: store.New(db), tpl: tpl}
	if err = a.store.EnsurePrimaryGPUResource(ctx); err != nil {
		t.Fatal(err)
	}
	resource, err := a.store.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.ObserveGPUResource(ctx, resource.ID, resource.Revision, "unknown", "resource test observation", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"comfyui", "openwebui", "mining"} {
		r := httptest.NewRequest("GET", "/admin/resources/overview?service="+service, nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: 1, Role: "admin"}))
		w := httptest.NewRecorder()
		a.requireAdmin(http.HandlerFunc(a.handleAdminRoutes)).ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(service, w.Code, w.Body.String())
		}
		var result struct {
			HTML     string         `json:"snapshot_html"`
			GPU      string         `json:"gpu_html"`
			Profiles map[string]any `json:"profiles"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.HTML+result.GPU, "Состояние не подтверждено") || !strings.Contains(result.HTML+result.GPU, "Промпт-ассистент") || len(result.Profiles) != 0 {
			t.Fatal(service, result)
		}
	}
	var userID int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('resource-http','disabled','admin') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	minerID, err := a.store.CreateMiner(ctx, store.CreateMinerParams{Name: "HTTP miner", ScriptPath: `C:\Mining\private-script.bat`, ProcessName: "fixture.exe", Enabled: true, Default: true, CreatedByUserID: userID})
	if err != nil {
		t.Fatal(err)
	}
	var commands atomic.Int32
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			commands.Add(1)
			http.Error(w, "unexpected command", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(mining.State{Available: true, CollectedAt: time.Now().UTC()})
	}))
	defer agent.Close()
	base, _ := url.Parse(agent.URL)
	a.mining = mining.NewClient(base, "fixture-token")
	a.cfg.MiningAgentURL = base
	if err = a.store.CreateQuickGenerationMiningLease(ctx, domain.QuickGenerationMiningLease{ID: "private-http-lease", UserID: userID, MinerID: minerID, ScriptPath: `C:\Mining\private-script.bat`, ProcessName: "fixture.exe"}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/admin/resources/overview?service=mining", nil)
	r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: userID, Role: "admin"}))
	w := httptest.NewRecorder()
	a.handleAdminRoutes(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		HTML     string `json:"snapshot_html"`
		Profiles map[string]struct {
			Start bool   `json:"start"`
			HTML  string `json:"html"`
		} `json:"profiles"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Profiles) != 1 || !strings.Contains(result.HTML, "Пауза для задач") {
		t.Fatal(result)
	}
	for _, state := range result.Profiles {
		if state.Start || !strings.Contains(state.HTML, "зарезервирован") {
			t.Fatal(state)
		}
	}
	if strings.Contains(w.Body.String(), "private-http-lease") || strings.Contains(w.Body.String(), "private-script") || commands.Load() != 0 {
		t.Fatal("read leaked private data or issued a command")
	}
}

func TestAdminResourceMiningFormRecovery(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	m := MiningOverview{Available: true, Miners: []MinerView{{Miner: domain.Miner{ID: 2, Name: "Profile", Enabled: true}}}}
	v := newAdminResourceView(time.Now().UTC(), "mining", DependencyStatus{}, nil, domain.GPUResource{}, nil, &m, true)
	form := url.Values{"name": {`<new-profile>`}, "script_path": {`C:\Mining\start.bat`}, "process_name": {"miner.exe"}, "is_default": {"on"}, "id": {"2"}, "archive_url": {"https://example.com/release.zip"}, "archive_sha256": {"bad checksum"}}
	var html bytes.Buffer
	if err = tpl.ExecuteTemplate(&html, "admin_mining", map[string]any{"Resource": v, "Mining": m, "MiningForm": form, "CSRF": "fixture"}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"&lt;new-profile&gt;", "https://example.com/release.zip", "bad checksum", `value="miner.exe"`, "checked"} {
		if !strings.Contains(html.String(), text) {
			t.Fatal("form value lost", text)
		}
	}
}
