package store

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
)

func TestComfyStorageQueryValidation(t *testing.T) {
	for _, key := range []string{"output:1", "cleanup:99", "input:" + strings.Repeat("a", 32)} {
		if !ValidComfyStorageKey(key) {
			t.Fatal(key)
		}
	}
	for _, key := range []string{"output:0", "output:01", "output:-1", "cleanup:9223372036854775808", "input:short", "input:" + strings.Repeat("a", 129), "input:" + strings.Repeat("a", 32) + "/../other", "filename:C:/input/file.png"} {
		if ValidComfyStorageKey(key) {
			t.Fatal(key)
		}
	}
	for _, state := range []string{"", "pending", "retained", "retry", "mismatched", "unconfirmed", "referenced"} {
		if err := (ComfyStorageQuery{Limit: 50, State: state}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []ComfyStorageQuery{{Limit: 0}, {Limit: 101}, {Limit: 1, Offset: -1}, {Limit: 1, Offset: 100001}, {Limit: 1, Kind: "all"}, {Limit: 1, State: "deleted"}, {Limit: 1, Search: strings.Repeat("x", 201)}, {Limit: 1, Search: "a\x00b"}, {Limit: 50, Keys: []string{"output:1", "output:1"}}} {
		if q.Validate() == nil {
			t.Fatal(q)
		}
	}
}

func TestComfyStorageVersionAndProtection(t *testing.T) {
	item := domain.ComfyStorageItem{Key: "input:" + strings.Repeat("a", 32), Kind: "input", Filename: "input.png", State: "stored", SizeBytes: 42, SizeKnown: true, ContentHash: strings.Repeat("b", 64), Pending: true, CreatedAt: time.Now().UTC()}
	decorateComfyStorageItem(&item, domain.ComfyStorageSummary{})
	if !item.CanInspect || !reflect.DeepEqual(item.Protection, []string{"executor_not_fenced"}) || len(item.Version) != 64 {
		t.Fatal(item)
	}
	version := item.Version
	decorateComfyStorageItem(&item, domain.ComfyStorageSummary{})
	if item.Version != version {
		t.Fatal("read-only version changed")
	}
	item.ContentHash = strings.Repeat("c", 64)
	decorateComfyStorageItem(&item, domain.ComfyStorageSummary{})
	if item.Version == version {
		t.Fatal("fingerprint change omitted from version")
	}
	item.State = "reserved"
	item.ReferenceProtected = true
	decorateComfyStorageItem(&item, domain.ComfyStorageSummary{GenerationActive: true, ExecutionUnconfirmed: true})
	if item.CanInspect || !reflect.DeepEqual(item.Protection, []string{"upload_unconfirmed", "generation_input_reference", "generation_active", "execution_unconfirmed", "executor_not_fenced"}) {
		t.Fatal(item)
	}
	jobID := int64(1)
	item.PostprocessJobID = &jobID
	decorateComfyStorageItem(&item, domain.ComfyStorageSummary{})
	if !reflect.DeepEqual(item.Protection, []string{"upload_unconfirmed", "postprocess_reference", "executor_not_fenced"}) {
		t.Fatal(item)
	}
}

func TestComfyStoragePathIndexPlan(t *testing.T) {
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.QueryContext(ctx, `EXPLAIN (COSTS OFF) `+comfyStorageCTE+`SELECT key,size_bytes,content_hash FROM inventory`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "content_media_output_path_idx") {
		t.Fatal("registry cannot use the partial output-path index", plan)
	}
}
