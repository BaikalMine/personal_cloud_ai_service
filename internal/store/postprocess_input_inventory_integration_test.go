package store_test

import (
	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/store"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPostprocessInputInventoryIntegration(t *testing.T) {
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
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	for n := 1; n <= 3; n++ {
		id := fmt.Sprintf("inventory-%032d", n)
		if _, err := db.ExecContext(ctx, `INSERT INTO comfy_input_assets(id,filename,subfolder,size_bytes,content_hash,state,expires_at) VALUES($1,'source.png','private',42,$2,'reserved',now())`, id, strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO generation_postprocess_inputs(input_id,source_position,dispatch_token,cleanup_blocked) VALUES($1,0,$2,$3)`, id, strings.Repeat("b", 32), n != 2); err != nil {
			t.Fatal(err)
		}
	}
	r := store.New(db)
	first, err := r.AdminUnresolvedPostprocessInputs(ctx, "", 1)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	if first[0].JobID != nil || first[0].UserID != nil || first[0].SizeBytes != 42 {
		t.Fatal(first[0])
	}
	next, err := r.AdminUnresolvedPostprocessInputs(ctx, first[0].ID, 100)
	if err != nil || len(next) != 1 || !strings.HasSuffix(next[0].ID, "3") {
		t.Fatal(next, err)
	}
	end, err := r.AdminUnresolvedPostprocessInputs(ctx, next[0].ID, 100)
	if err != nil || len(end) != 0 {
		t.Fatal(end, err)
	}
	for _, limit := range []int{0, -1, 101} {
		if _, err := r.AdminUnresolvedPostprocessInputs(ctx, "", limit); err == nil {
			t.Fatal("invalid limit accepted", limit)
		}
	}
	var blocked int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM generation_postprocess_inputs WHERE cleanup_blocked`).Scan(&blocked); err != nil || blocked != 2 {
		t.Fatal("inventory mutated holds", blocked, err)
	}
}
