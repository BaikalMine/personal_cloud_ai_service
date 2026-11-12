package gateway

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestAdminMetricsWindowAndEvidence(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		period         string
		hours, buckets int
	}{{"", 24, 48}, {"1h", 1, 12}, {"24h", 24, 48}, {"7d", 168, 56}} {
		v, err := newAdminMetricsView(now, tc.period)
		if err != nil || v.At.Sub(v.From) != time.Duration(tc.hours)*time.Hour || int(v.At.Sub(v.From)/v.Step) != tc.buckets {
			t.Fatal(v, err)
		}
	}
	for _, value := range []string{"30d", "-1", "24H", "<script>"} {
		if _, err := newAdminMetricsView(now, value); err == nil {
			t.Fatal(value)
		}
	}
	for _, tc := range []struct {
		at   time.Time
		want string
	}{{time.Time{}, "Нет снимка"}, {now, "Свежий снимок"}, {now.Add(-time.Minute), "Свежий снимок"}, {now.Add(-61 * time.Second), "Устаревший снимок"}, {now.Add(time.Minute), "Время не подтверждено"}} {
		if got := metricsSnapshotState(tc.at, now, time.Minute); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
	v, _ := newAdminMetricsView(now, "1h")
	v.setHistory([]store.MetricsBucket{{Samples: 0, ActivePeak: 99}, {Samples: 2, ActivePeak: 0}, {Samples: 1, ActivePeak: 5, OverduePeak: 1}})
	if v.Samples != 3 || v.MissingBuckets != 1 || v.ChartMax != 5 || v.Buckets[0].ActiveHeight != 0 || v.Buckets[2].ActiveHeight != 100 || v.Buckets[2].OverdueHeight != 20 {
		t.Fatal(v)
	}
	v.setHistory(nil)
	if v.Samples != 0 || v.ChartMax != 0 || v.MissingBuckets != 0 {
		t.Fatal(v)
	}
}

func TestAdminMetricsAccessValidationAndTemplate(t *testing.T) {
	tpl, err := ParseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	a := &App{tpl: tpl}
	for _, tc := range []struct {
		role, method, query string
		status              int
	}{{"", "GET", "", 403}, {"user", "GET", "", 403}, {"admin", "POST", "", 405}, {"admin", "DELETE", "", 405}, {"admin", "GET", "?period=all", 400}} {
		r := httptest.NewRequest(tc.method, "/admin/metrics"+tc.query, nil)
		if tc.role != "" {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, &User{Role: tc.role}))
		}
		w := httptest.NewRecorder()
		a.handleAdminMetricsPage(w, r)
		if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	v, _ := newAdminMetricsView(time.Now(), "1h")
	v.Generation.Cancelled = 3
	v.Outcomes = []domain.GenerationOutcomeGroup{{WorkflowID: "<script>bad()</script>", Cancelled: 3}}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "admin_metrics", map[string]any{"Metrics": v, "User": &User{Role: "admin"}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Нет завершений", "Нет измерений", "Нет снимков нагрузки", "&lt;script&gt;", "admin-metrics.css"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("missing", want)
		}
	}
	if strings.Contains(out.String(), "<script>bad()") || strings.Contains(out.String(), "Все 0 фоновых") {
		t.Fatal("invalid empty or escaped state")
	}
}
