package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestCaptionSeriesTaskHistoryIntegration(t *testing.T) {
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
	resetIntegrationDatabase(t, db)
	repo := store.New(db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var owner, other int64
	for name, dest := range map[string]*int64{"series-history-owner": &owner, "series-history-other": &other} {
		if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES($1,'disabled','user') RETURNING id`, name).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO lora_datasets(id,user_id,name,manifest_cipher) VALUES('history-dataset',$1,'Renamed working set','x')`, owner)
	created := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	for _, id := range []string{"history-series-001", "history-series-002", "history-series-003"} {
		exec(`INSERT INTO lora_caption_series(id,user_id,dataset_id,name,total_count,completed_count,failed_count,cancelled_count,unavailable_count,state,created_at,updated_at)
			VALUES($1,$2,'history-dataset','Original 100%_name',4,1,1,1,1,'partial',$3,$3)`, id, owner, created)
	}
	exec(`INSERT INTO lora_caption_series(id,user_id,name,total_count,state,created_at) VALUES('foreign-series-001',$1,'Private series',1,'running',$2)`, other, created)
	scope := domain.TaskScope{UserID: owner, Generation: true, Training: true}
	filter := domain.TaskFilter{Kind: "lora_caption_series"}
	list := func(scope domain.TaskScope, f domain.TaskFilter, limit int, cursor string) domain.TaskPage {
		t.Helper()
		page, err := repo.ListTasks(ctx, scope, f, limit, cursor)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := list(scope, filter, 1, "")
	if page.Total != 3 || len(page.Tasks) != 1 || page.NextCursor == "" || page.Tasks[0].ID != "history-series-003" {
		t.Fatalf("first series page: %+v", page)
	}
	summary := page.Tasks[0].Series
	if summary == nil || summary.Total != 4 || summary.Completed != 1 || summary.Failed != 1 || summary.Unavailable != 1 || summary.Cancelled != 1 || page.Tasks[0].Progress != nil || page.Tasks[0].Preview != nil {
		t.Fatalf("series summary became source detail: %+v", page.Tasks[0])
	}
	if page.Tasks[0].Name != "Original 100%_name" || page.Tasks[0].DatasetID != "history-dataset" {
		t.Fatal("series lost its recorded identity")
	}
	if list(scope, domain.TaskFilter{}, 10, "").Total != 0 || list(scope, domain.TaskFilter{Kind: "lora_caption"}, 10, "").Total != 0 {
		t.Fatal("series were counted as individual jobs")
	}
	if list(domain.TaskScope{UserID: owner, Generation: true}, filter, 10, "").Total != 0 || list(domain.TaskScope{UserID: owner}, filter, 10, "").Total != 0 {
		t.Fatal("series bypassed training permission")
	}
	if p := list(domain.TaskScope{UserID: other, Training: true}, filter, 10, ""); p.Total != 1 || p.Tasks[0].ID != "foreign-series-001" {
		t.Fatal("series bypassed owner scope")
	}

	// Updating an old series must not move it across a creation-order cursor.
	exec(`UPDATE lora_caption_series SET updated_at=now() WHERE id='history-series-001'`)
	second := list(scope, filter, 1, page.NextCursor)
	third := list(scope, filter, 1, second.NextCursor)
	if second.Tasks[0].ID != "history-series-002" || third.Tasks[0].ID != "history-series-001" || third.NextCursor != "" || second.Revision <= page.Revision {
		t.Fatal("history cursor/revision ordering failed")
	}
	for _, invalid := range []struct {
		scope  domain.TaskScope
		filter domain.TaskFilter
	}{
		{domain.TaskScope{UserID: other, Training: true}, filter},
		{scope, domain.TaskFilter{Kind: "lora_caption"}},
		{scope, domain.TaskFilter{Kind: "lora_caption_series", State: "partial"}},
	} {
		if _, err := repo.ListTasks(ctx, invalid.scope, invalid.filter, 10, page.NextCursor); !errors.Is(err, store.ErrTaskQuery) {
			t.Fatalf("cursor accepted another scope/filter: %v", err)
		}
	}
	before, from := created.Add(time.Second), created
	for _, f := range []domain.TaskFilter{
		{Kind: "lora_caption_series", Search: "100%_"},
		{Kind: "lora_caption_series", State: "partial"},
		{Kind: "lora_caption_series", From: &from, Before: &before},
	} {
		if list(scope, f, 10, "").Total != 3 {
			t.Fatalf("series filter failed: %+v", f)
		}
	}
	if list(scope, domain.TaskFilter{Kind: "lora_caption_series", Before: &created}, 10, "").Total != 0 {
		t.Fatal("end boundary was not exclusive")
	}
	if list(scope, domain.TaskFilter{Kind: "lora_caption_series", Search: "Renamed working set"}, 10, "").Total != 0 {
		t.Fatal("series search used mutable dataset name")
	}
	if _, err := repo.ListTasks(ctx, scope, domain.TaskFilter{Kind: "generation", State: "partial"}, 10, ""); !errors.Is(err, store.ErrTaskQuery) {
		t.Fatal("series state leaked into source filters")
	}

	exec(`DELETE FROM lora_datasets WHERE id='history-dataset'`)
	retained := list(scope, filter, 10, "")
	if retained.Total != 3 || retained.Tasks[0].DatasetID != "" || retained.Tasks[0].Name != "Original 100%_name" {
		t.Fatal("deleting working dataset destroyed historical series")
	}
	revision, err := repo.TaskRevision(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	list(scope, filter, 10, "")
	after, err := repo.TaskRevision(ctx, owner)
	if err != nil || after != revision {
		t.Fatal("reading history mutated source state")
	}
}
