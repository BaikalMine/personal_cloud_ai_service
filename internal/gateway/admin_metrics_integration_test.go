package gateway

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestAdminMetricsIntegration(t *testing.T) {
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
	// This destructive fixture is restricted to the explicitly marked disposable database.
	var name, marker string
	if err := db.QueryRowContext(ctx, `SELECT current_database(),COALESCE(current_setting('ai_gateway.disposable_test',true),'')`).Scan(&name, &marker); err != nil {
		t.Fatal(err)
	}
	if name != "metrics_d09_ephemeral" || marker != "metrics-d09-20260914" {
		t.Skip("requires the marked disposable metrics test database")
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetGatewayIntegrationDatabase(t, db)
	if _, err := db.ExecContext(ctx, `TRUNCATE generation_jobs,gateway_observations,host_metrics,service_observations CASCADE`); err != nil {
		t.Fatal(err)
	}
	s := store.New(db)
	now := time.Now().UTC().Truncate(time.Second)
	from := now.Add(-time.Hour)
	for _, tc := range []struct {
		at     time.Time
		active int64
	}{{from.Add(-time.Microsecond), 99}, {from, 0}, {from.Add(2 * time.Minute), 4}, {from.Add(5 * time.Minute), 2}, {now, 99}} {
		if err := s.RecordGatewayObservation(ctx, domain.GatewayObservation{RecordedAt: tc.at, ActiveJobs: tc.active}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.MetricsGatewayHistory(ctx, from, now, 5*time.Minute)
	if err != nil || len(rows) != 12 || rows[0].Samples != 2 || rows[0].ActivePeak != 4 || rows[1].Samples != 1 || rows[1].ActivePeak != 2 || rows[2].Samples != 0 || rows[2].FirstAt != nil {
		t.Fatal(rows, err)
	}
	if _, err := s.MetricsGatewayHistory(ctx, from, now, time.Second); err == nil {
		t.Fatal("unbounded buckets")
	}
	if _, err := s.MetricsGeneration(ctx, now.Add(-8*24*time.Hour), now, time.Minute); err == nil {
		t.Fatal("unbounded period")
	}
	for i, tc := range []struct {
		state  string
		finish time.Time
	}{{"completed", from}, {"failed", from.Add(time.Minute)}, {"cancelled", from.Add(2 * time.Minute)}, {"expired", from.Add(3 * time.Minute)}, {"completed", now}, {"failed", from.Add(-time.Second)}} {
		_, err := db.ExecContext(ctx, `INSERT INTO generation_jobs(public_id,request_id,state,created_at,started_at,finished_at,workflow_id,model_name,error_message) VALUES($1,$1,$2,$3,$4,$5,'workflow','model','<script>private()</script>')`, fmt.Sprintf("metrics-job-%06d", i), tc.state, tc.finish.Add(-2*time.Minute), tc.finish.Add(-time.Minute), tc.finish)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO generation_jobs(public_id,request_id,state,state_changed_at,created_at) VALUES('metrics-active-0001','active','running',$1,$1)`, from.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	g, err := s.MetricsGeneration(ctx, from, now, 45*time.Minute)
	if err != nil || g.ActiveJobs != 1 || g.OverdueJobs != 1 || g.Completed != 1 || g.Failed != 1 || g.Cancelled != 1 || g.Expired != 1 || g.SuccessRate != 50 || g.ExecutionSamples != 4 || g.QueueSamples != 4 || g.ExecutionP50MS != 60000 {
		t.Fatal(g, err)
	}
	outcomes, err := s.GenerationOutcomeGroupsBetween(ctx, from, 31, now)
	if err != nil || len(outcomes) != 1 || outcomes[0].Total != 4 || outcomes[0].Cancelled != 2 {
		t.Fatal(outcomes, err)
	}
	failures, err := s.GenerationFailureSummariesBetween(ctx, from, 21, now)
	if err != nil || len(failures) != 1 {
		t.Fatal(failures, err)
	}
	for _, at := range []time.Time{from.Add(-time.Second), from, now.Add(-time.Second), now} {
		if err := s.RecordServiceObservation(ctx, domain.ServiceObservationRecord{Component: "comfyui", Operation: "probe", Outcome: "ok", ObservedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	latencies, err := s.ServiceLatencySummariesBetween(ctx, from, now)
	if err != nil || len(latencies) != 1 || latencies[0].Samples != 2 {
		t.Fatal(latencies, err)
	}
	for _, at := range []time.Time{now.Add(-time.Hour), now.Add(time.Minute)} {
		if err := s.RecordHostMetric(ctx, domain.HostMetric{RecordedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	host, err := s.MetricsLatestHost(ctx, now)
	if err != nil || !host.RecordedAt.Equal(now.Add(-time.Hour)) {
		t.Fatal(host, err)
	}
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{tpl: tpl, store: s}
	for _, method := range []string{"GET", "HEAD"} {
		r := httptest.NewRequest(method, "/admin/metrics?period=1h", nil)
		r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{ID: 1, Role: "admin"}))
		w := httptest.NewRecorder()
		a.handleAdminMetricsPage(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Устаревший снимок") || strings.Contains(w.Body.String(), "<script>private()") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var gatewayCount, serviceCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM gateway_observations`).Scan(&gatewayCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM service_observations`).Scan(&serviceCount); err != nil {
		t.Fatal(err)
	}
	if gatewayCount != 5 || serviceCount != 4 {
		t.Fatal("page read wrote observations", gatewayCount, serviceCount)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE generation_jobs,gateway_observations,host_metrics,service_observations CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/admin/metrics", nil)
	r = r.WithContext(context.WithValue(ctx, userCtxKey, &User{Role: "admin"}))
	w := httptest.NewRecorder()
	a.handleAdminMetricsPage(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "Метрики не загружены") || strings.Contains(w.Body.String(), "Результаты генераций") {
		t.Fatal(w.Code, w.Body.String())
	}
}
