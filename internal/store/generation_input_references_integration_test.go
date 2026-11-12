package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func referenceInput(t *testing.T, db *sql.DB, user int64, id int, name, state string, expires time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,user_id,filename,subfolder,size_bytes,content_hash,state,expires_at)
	 VALUES($1,$2,$3,'owner',42,repeat('a',64),$4,$5)`, fmt.Sprintf("%032d", id), user, name, state, expires); err != nil {
		t.Fatal(err)
	}
}

func referenceJob(t *testing.T, r *store.Store, user int64, suffix string) domain.GenerationJob {
	t.Helper()
	j, _, err := r.CreateGenerationJob(context.Background(), domain.CreateGenerationJobParams{UserID: user, PublicID: "input-reference-job-" + suffix, RequestID: "input-reference-request-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestGenerationInputReferencesLifecycleIntegration(t *testing.T) {
	db, r, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	referenceInput(t, db, user, 1, "photo.png", "stored", time.Now().Add(time.Hour))
	referenceInput(t, db, user, 2, "other.png", "stored", time.Now().Add(-time.Hour))
	j := referenceJob(t, r, user, "lifecycle")
	prepared := domain.PreparedGenerationJob{InputNames: []string{"owner/photo.png", "owner/photo.png"}, InputCount: 2, PayloadCipher: []byte("prepared")}
	if _, err := r.PrepareGenerationJob(ctx, j.ID, prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE comfy_input_assets SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	assertDue := func(want int) {
		t.Helper()
		items, err := r.ExpiredComfyInputAssets(ctx, 100)
		if err != nil || len(items) != want {
			t.Fatalf("due=%v want=%d err=%v", items, want, err)
		}
	}
	assertDue(1)
	view, err := r.ComfyStorageInventory(ctx, store.ComfyStorageQuery{Limit: 50, State: "referenced"})
	if err != nil || view.Total != 1 || view.Items[0].Key != "input:00000000000000000000000000000001" {
		t.Fatal(view, err)
	}
	// The existing queued job can resume after TTL; a new job cannot adopt it.
	if _, err := r.PrepareGenerationJob(ctx, j.ID, prepared); err != nil {
		t.Fatal(err)
	}
	other := referenceJob(t, r, user, "expired")
	if _, err := r.PrepareGenerationJob(ctx, other.ID, prepared); !errors.Is(err, store.ErrGenerationInputUnavailable) {
		t.Fatal(err)
	}
	var count int
	var cipher []byte
	if err := db.QueryRow(`SELECT payload_cipher FROM generation_jobs WHERE id=$1`, other.ID).Scan(&cipher); err != nil || len(cipher) != 0 {
		t.Fatal(cipher, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM generation_input_references WHERE job_id=$1`, other.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	// Unknown historical payload conservatively protects all tracked inputs.
	assertDue(0)
	if _, err := r.PrepareGenerationJob(ctx, other.ID, domain.PreparedGenerationJob{InputNames: []string{}}); err != nil {
		t.Fatal(err)
	}
	assertDue(1)
	if _, err := db.Exec(`UPDATE generation_jobs SET state='failed',submission_started_at=now(),prompt_id='accepted' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	assertDue(1)
	if _, err := db.Exec(`UPDATE generation_jobs SET resources_released_at=now() WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	assertDue(2)
}

func TestGenerationInputReferencesValidationIntegration(t *testing.T) {
	db, r, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	var stranger int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('other-input-owner','hash','user') RETURNING id`).Scan(&stranger); err != nil {
		t.Fatal(err)
	}
	referenceInput(t, db, user, 1, "stored.png", "stored", time.Now().Add(time.Hour))
	referenceInput(t, db, user, 2, "reserved.png", "reserved", time.Now().Add(time.Hour))
	referenceInput(t, db, stranger, 3, "foreign.png", "stored", time.Now().Add(time.Hour))
	referenceInput(t, db, user, 4, "duplicate.png", "stored", time.Now().Add(time.Hour))
	referenceInput(t, db, user, 5, "duplicate.png", "stored", time.Now().Add(time.Hour))
	j := referenceJob(t, r, user, "validation")
	for _, names := range [][]string{{"owner/missing.png"}, {"owner/reserved.png"}, {"owner/foreign.png"}, {"owner/duplicate.png"}, {"owner/stored.png", "owner/missing.png"}} {
		if _, err := r.PrepareGenerationJob(ctx, j.ID, domain.PreparedGenerationJob{InputNames: names}); !errors.Is(err, store.ErrGenerationInputUnavailable) {
			t.Fatalf("%v: %v", names, err)
		}
	}
	if _, err := r.PrepareGenerationJob(ctx, j.ID, domain.PreparedGenerationJob{InputNames: []string{"owner/stored.png"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.PrepareGenerationJob(ctx, j.ID, domain.PreparedGenerationJob{}); err != nil {
		t.Fatal(err)
	}
	var verified bool
	if err := db.QueryRow(`SELECT input_references_verified FROM generation_jobs WHERE id=$1`, j.ID).Scan(&verified); err != nil || verified {
		t.Fatal(verified, err)
	}
	if _, err := r.PrepareGenerationJob(ctx, j.ID, domain.PreparedGenerationJob{InputNames: []string{}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM generation_input_references WHERE job_id=$1`, j.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestGenerationInputReferencesScanWaitsForCommitIntegration(t *testing.T) {
	db, r, user := comfyStorageDBFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	referenceInput(t, db, user, 1, "race.png", "stored", time.Now().Add(time.Hour))
	j := referenceJob(t, r, user, "race")
	// Pause publication after rows were bound, with the file becoming expired in
	// that transaction. The cleanup scan must not read the pre-publication state.
	if _, err := db.Exec(`CREATE FUNCTION test_input_reference_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF NEW.input_references_verified THEN
	 UPDATE comfy_input_assets SET expires_at=now()-interval '1 second';
	 PERFORM pg_advisory_xact_lock(743629836);
	 END IF; RETURN NEW; END $$;
	 CREATE TRIGGER test_input_reference_pause BEFORE UPDATE OF input_references_verified ON generation_jobs
	 FOR EACH ROW EXECUTE FUNCTION test_input_reference_pause()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DROP TRIGGER IF EXISTS test_input_reference_pause ON generation_jobs; DROP FUNCTION IF EXISTS test_input_reference_pause()`)
	})
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, `SELECT pg_advisory_xact_lock(743629836)`); err != nil {
		t.Fatal(err)
	}
	prepared := make(chan error, 1)
	go func() {
		_, err := r.PrepareGenerationJob(ctx, j.ID, domain.PreparedGenerationJob{InputNames: []string{"owner/race.png"}})
		prepared <- err
	}()
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=743629836 AND NOT granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	scanned := make(chan error, 1)
	go func() {
		items, err := r.ExpiredComfyInputAssets(ctx, 100)
		if err == nil && len(items) != 0 {
			err = fmt.Errorf("live input selected: %v", items)
		}
		scanned <- err
	}()
	select {
	case err := <-scanned:
		t.Fatalf("scan escaped publication lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-prepared; err != nil {
		t.Fatal(err)
	}
	if err := <-scanned; err != nil {
		t.Fatal(err)
	}
}

func TestGenerationInputReferencesBatchAtomicIntegration(t *testing.T) {
	db, r, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	referenceInput(t, db, user, 1, "batch.png", "stored", time.Now().Add(time.Hour))
	p := domain.CreateGenerationBatchParams{UserID: user, PublicID: "reference-batch-public", RequestID: "reference-batch-request", TemplateID: "text-to-image", WorkflowID: "krea2", ModelName: "Krea2", Mode: domain.GenerationBatchSeeds, MaxParallel: 1}
	for i := 1; i <= 2; i++ {
		p.Jobs = append(p.Jobs, domain.CreateGenerationBatchJobParams{PublicID: fmt.Sprintf("reference-batch-child-%d", i), RequestID: fmt.Sprintf("reference-child-request-%d", i), CorrelationID: "reference-batch-correlation", Position: i,
			Prepared: domain.PreparedGenerationJob{TemplateID: p.TemplateID, WorkflowID: p.WorkflowID, ModelName: p.ModelName, Seed: int64(i), PayloadCipher: []byte("batch-payload"), InputCount: 1, InputNames: []string{"owner/batch.png"}}})
	}
	p.Jobs[1].Prepared.InputNames = []string{"owner/missing.png"}
	if _, _, err := r.CreateGenerationBatch(ctx, p); !errors.Is(err, store.ErrGenerationInputUnavailable) {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM generation_batches)+(SELECT count(*) FROM generation_jobs)+(SELECT count(*) FROM generation_input_references)+(SELECT count(*) FROM generation_requests)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed batch left partial admission", count, err)
	}
	quota, err := r.QuickGenerationQuota(ctx, user)
	if err != nil || quota.Image.TotalUsed != 0 || quota.Image.DailyUsed != 0 {
		t.Fatal("failed batch consumed quota", quota, err)
	}
	p.Jobs[1].Prepared.InputNames = []string{"owner/batch.png"}
	batch, created, err := r.CreateGenerationBatch(ctx, p)
	if err != nil || !created {
		t.Fatal(batch, created, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM generation_input_references`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if _, err := db.Exec(`UPDATE comfy_input_assets SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if replay, created, err := r.CreateGenerationBatch(ctx, p); err != nil || created || replay.ID != batch.ID {
		t.Fatal("replay should read accepted batch", replay, created, err)
	}
	if items, err := r.ExpiredComfyInputAssets(ctx, 100); err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
}

func TestGenerationInputReferencesRepeatAtomicIntegration(t *testing.T) {
	db, r, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	referenceInput(t, db, user, 1, "repeat.png", "stored", time.Now().Add(time.Hour))
	source := referenceJob(t, r, user, "source")
	if _, err := db.Exec(`UPDATE generation_jobs SET state='completed',payload_cipher='source' WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	p := domain.CreateGenerationRepeatParams{Job: domain.CreateGenerationJobParams{UserID: user, PublicID: "reference-repeat-child", RequestID: "reference-repeat-request"},
		SourcePublicID: source.PublicID, SourcePayloadCipher: []byte("source"), ConfirmationSHA256: strings.Repeat("a", 64),
		Prepared: domain.PreparedGenerationJob{TemplateID: "text-to-image", WorkflowID: "krea2", PayloadCipher: []byte("repeat"), InputCount: 1, InputNames: []string{"owner/missing.png"}}}
	if _, _, err := r.CreateGenerationRepeat(ctx, p); !errors.Is(err, store.ErrGenerationInputUnavailable) {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM generation_jobs)+(SELECT count(*) FROM generation_repeats)+(SELECT count(*) FROM generation_requests)+(SELECT count(*) FROM generation_input_references)`).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed repeat published partial rows", count, err)
	}
	p.Prepared.InputNames = []string{"owner/repeat.png"}
	receipt, created, err := r.CreateGenerationRepeat(ctx, p)
	if err != nil || !created || receipt.JobID == nil {
		t.Fatal(receipt, created, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM generation_input_references WHERE job_id=$1`, *receipt.JobID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err := db.Exec(`UPDATE comfy_input_assets SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if replay, created, err := r.CreateGenerationRepeat(ctx, p); err != nil || created || replay.JobPublicID != receipt.JobPublicID {
		t.Fatal(replay, created, err)
	}
	if items, err := r.ExpiredComfyInputAssets(ctx, 100); err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
}
