package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type postprocessStoreFixture struct {
	db     *sql.DB
	repo   *store.Store
	user   int64
	source domain.PostprocessSourceIdentity
	params domain.CreateGenerationPostprocessQuoteParams
}

func newPostprocessStoreFixture(t *testing.T) postprocessStoreFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, err := database.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	f := postprocessStoreFixture{db: db, repo: store.New(db)}
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role,can_use_quick_generation,can_generate_text_to_image,can_generate_video,generation_daily_limit,video_generation_daily_limit) VALUES('postprocess-owner','disabled','user',true,true,true,1,1) RETURNING id`).Scan(&f.user); err != nil {
		t.Fatal(err)
	}
	f.source = domain.PostprocessSourceIdentity{JobPublicID: "postprocess-parent-0001", MediaID: 9007199254741011, MediaType: "image", MIMEType: "image/png", SizeBytes: 100, SHA256: strings.Repeat("a", 64), ArchiveSHA256: strings.Repeat("a", 64)}
	if err := db.QueryRow(`INSERT INTO generation_jobs(user_id,public_id,correlation_id,request_id,state,template_id) VALUES($1,$2,$2,$2,'completed','text-to-image') RETURNING id`, f.user, f.source.JobPublicID).Scan(&f.source.JobID); err != nil {
		t.Fatal(err)
	}
	var event int64
	if err := db.QueryRow(`INSERT INTO content_events(user_id,generation_job_id,service,kind,prompt_cipher,response_cipher,metadata_cipher,is_sensitive) VALUES($1,$2,'comfyui','comfyui_prompt','cipher','cipher','cipher',true) RETURNING id`, f.user, f.source.JobID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO content_media(id,event_id,media_type,mime_type,original_name,payload_cipher,size_bytes,content_hash) VALUES($1,$2,'image','image/png','original.png','original-cipher',100,$3)`, f.source.MediaID, event, f.source.SHA256); err != nil {
		t.Fatal(err)
	}
	f.params = domain.CreateGenerationPostprocessQuoteParams{UserID: f.user, RequestID: "postprocess-request-0001", ConfirmationSHA256: strings.Repeat("b", 64), PayloadCipher: []byte("encrypted-quote-v1"), Sources: []domain.PostprocessSourceIdentity{f.source}, ExpiresAt: time.Now().Add(5 * time.Minute).UTC().Truncate(time.Microsecond)}
	return f
}

func (f postprocessStoreFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestPostprocessPendingQuotesBoundIntegration(t *testing.T) {
	f := newPostprocessStoreFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := f.params
			p.RequestID = fmt.Sprintf("pending-postprocess-%04d", i)
			_, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, p)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, store.ErrPostprocessQuoteLimit) {
			t.Fatal(err)
		}
	}
	if accepted != 4 {
		t.Fatal("concurrent limit bypassed", accepted)
	}
	var oldID string
	if err := f.db.QueryRow(`SELECT request_id FROM generation_postprocess_receipts LIMIT 1`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE generation_postprocess_receipts SET expires_at=now()-interval '1 second' WHERE request_id=$1`, oldID)
	p := f.params
	p.RequestID = "pending-postprocess-replacement"
	if _, created, err := f.repo.CreateGenerationPostprocessQuote(ctx, p); err != nil || !created {
		t.Fatal("expired plan did not free capacity", err)
	}
	old, err := f.repo.GenerationPostprocessByRequest(ctx, f.user, oldID)
	if err != nil || old.State != "cancelled" || len(old.PayloadCipher) != 0 || len(old.Sources) != 0 || old.ConfirmationSHA256 != "" {
		t.Fatal("expired payload retained", old, err)
	}
	p.RequestID = oldID
	if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, p); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal("expired ID resurrected", err)
	}
	if _, _, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, oldID, f.params.ConfirmationSHA256, "postprocess-expired-child"); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal("expired plan confirmed", err)
	}
}

func TestPostprocessReceiptLifecycleIntegration(t *testing.T) {
	f := newPostprocessStoreFixture(t)
	ctx := context.Background()
	q, created, err := f.repo.CreateGenerationPostprocessQuote(ctx, f.params)
	if err != nil || !created || q.State != "quoted" || !q.Sensitive || q.Sources[0] != f.source || !bytes.Equal(q.PayloadCipher, f.params.PayloadCipher) || !q.ExpiresAt.Equal(f.params.ExpiresAt) {
		t.Fatal(q, created, err)
	}
	var persisted string
	if err := f.db.QueryRow(`SELECT sources::text FROM generation_postprocess_receipts`).Scan(&persisted); err != nil || !strings.Contains(persisted, `"9007199254741011"`) {
		t.Fatal(persisted, err)
	}
	p := f.params
	p.PayloadCipher = []byte("different-encryption-nonce-same-plan")
	q2, created, err := f.repo.CreateGenerationPostprocessQuote(ctx, p)
	if err != nil || created || !q2.CreatedAt.Equal(q.CreatedAt) || !bytes.Equal(q2.PayloadCipher, q.PayloadCipher) {
		t.Fatal("quote was replaced", q2, created, err)
	}
	p.ConfirmationSHA256 = strings.Repeat("c", 64)
	if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, p); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal(err)
	}
	if _, _, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, f.params.RequestID, p.ConfirmationSHA256, "postprocess-child-0001"); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal(err)
	}
	if _, err := f.repo.GenerationPostprocessByRequest(ctx, f.user+1, f.params.RequestID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign read", err)
	}
	f.exec(t, `UPDATE content_events SET is_sensitive=false`)
	type result struct {
		r       domain.GenerationPostprocessReceipt
		created bool
		err     error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, c, e := f.repo.ConfirmGenerationPostprocess(ctx, f.user, f.params.RequestID, f.params.ConfirmationSHA256, fmt.Sprintf("postprocess-child-%04d", i))
			results <- result{r, c, e}
		}(i)
	}
	wg.Wait()
	close(results)
	var accepted domain.GenerationPostprocessReceipt
	count := 0
	for r := range results {
		if r.err != nil || r.r.JobID == nil || r.r.State != "accepted" || !r.r.Sensitive {
			t.Fatal(r)
		}
		if accepted.JobID != nil && *accepted.JobID != *r.r.JobID {
			t.Fatal("duplicate child", r)
		}
		accepted = r.r
		if r.created {
			count++
		}
	}
	if count != 1 {
		t.Fatal("creation count", count)
	}
	job, err := f.repo.GenerationJobByPublicID(ctx, f.user, accepted.JobPublicID)
	if err != nil || job.ParentJobID == nil || *job.ParentJobID != f.source.JobID || job.TemplateID != domain.PostprocessImageTemplate || job.WorkflowID != domain.PostprocessWorkflow || job.QuotaReservedOn != nil || !bytes.Equal(job.PayloadCipher, q.PayloadCipher) || job.Seed != 0 {
		t.Fatal(job, err)
	}
	for _, table := range []string{"generation_job_transitions", "generation_requests"} {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE job_id=$1`, job.ID).Scan(&n); err != nil || n != 1 {
			t.Fatal(table, n, err)
		}
	}
	if r, err := f.repo.DiscardGenerationPostprocess(ctx, f.user, f.params.RequestID); err != nil || r.State != "accepted" {
		t.Fatal("discard hid accepted child", r, err)
	}
	f.exec(t, `UPDATE generation_postprocess_receipts SET expires_at=now()-interval '1 hour'`)
	f.exec(t, `DELETE FROM generation_jobs WHERE id IN ($1,$2)`, f.source.JobID, job.ID)
	r, created, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, f.params.RequestID, f.params.ConfirmationSHA256, "postprocess-child-late")
	if err != nil || created || r.JobID != nil || r.JobPublicID != job.PublicID || r.Sources[0] != f.source {
		t.Fatal("deleted child recreated", r, created, err)
	}
}

func TestPostprocessSourceAndExpiryIntegration(t *testing.T) {
	for name, query := range map[string]string{
		"hidden":            `UPDATE content_media SET profile_hidden_at=now()`,
		"deleted":           `DELETE FROM content_media`,
		"media expired":     `UPDATE content_media SET expires_at=now()-interval '1 hour'`,
		"event expired":     `UPDATE content_events SET expires_at=now()-interval '1 hour'`,
		"hash":              `UPDATE content_media SET content_hash=repeat('c',64)`,
		"size":              `UPDATE content_media SET size_bytes=101`,
		"mime":              `UPDATE content_media SET mime_type='image/jpeg'`,
		"unfinished":        `UPDATE generation_jobs SET state='failed'`,
		"parent permission": `UPDATE users SET can_generate_text_to_image=false,can_generate_image_to_image=true`,
		"account disabled":  `UPDATE users SET disabled=true`,
		"account expired":   `UPDATE users SET account_expires_at=now()-interval '1 hour'`,
		"service revoked":   `UPDATE users SET can_use_quick_generation=false`,
		"quote expired":     `UPDATE generation_postprocess_receipts SET expires_at=now()-interval '1 hour'`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newPostprocessStoreFixture(t)
			ctx := context.Background()
			if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, f.params); err != nil {
				t.Fatal(err)
			}
			f.exec(t, query)
			if _, _, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, f.params.RequestID, f.params.ConfirmationSHA256, "postprocess-child-0001"); err == nil {
				t.Fatal("changed source accepted")
			}
			var n int
			if err := f.db.QueryRow(`SELECT count(*) FROM generation_requests`).Scan(&n); err != nil || n != 0 {
				t.Fatal("partial acceptance", n, err)
			}
		})
	}
}

func TestPostprocessDiscardAndClaimRacesIntegration(t *testing.T) {
	f := newPostprocessStoreFixture(t)
	ctx := context.Background()
	r, err := f.repo.DiscardGenerationPostprocess(ctx, f.user, f.params.RequestID)
	if err != nil || r.State != "cancelled" {
		t.Fatal(r, err)
	}
	if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, f.params); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal("late quote", err)
	}
	if _, _, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, f.params.RequestID, f.params.ConfirmationSHA256, "postprocess-child-0001"); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal("late confirm", err)
	}
	for i := 0; i < 12; i++ {
		p := f.params
		p.RequestID = fmt.Sprintf("postprocess-race-%04d", i)
		if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, p); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, e := f.repo.DiscardGenerationPostprocess(ctx, f.user, p.RequestID)
			errs <- e
		}()
		go func() {
			defer wg.Done()
			_, _, e := f.repo.ConfirmGenerationPostprocess(ctx, f.user, p.RequestID, p.ConfirmationSHA256, fmt.Sprintf("postprocess-race-child-%04d", i))
			if errors.Is(e, store.ErrPostprocessConflict) {
				e = nil
			}
			errs <- e
		}()
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		r, e := f.repo.GenerationPostprocessByRequest(ctx, f.user, p.RequestID)
		if e != nil || r.State != "accepted" && r.State != "cancelled" {
			t.Fatal(r, e)
		}
		var n int
		if e := f.db.QueryRow(`SELECT count(*) FROM generation_jobs WHERE request_id=$1`, p.RequestID).Scan(&n); e != nil || r.State == "accepted" && n != 1 || r.State == "cancelled" && n != 0 {
			t.Fatal("race outcome", n, r, e)
		}
	}
	p := f.params
	p.RequestID = "postprocess-conflict-claim"
	if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, p); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO generation_requests(user_id,request_id,correlation_id) VALUES($1,$2,'unrelated-purpose')`, f.user, p.RequestID)
	if _, _, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, p.RequestID, p.ConfirmationSHA256, "postprocess-child-conflict"); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal(err)
	}
}

func TestPostprocessIndependentQuotaIntegration(t *testing.T) {
	for _, family := range []string{"image", "video"} {
		t.Run(family, func(t *testing.T) {
			f := newPostprocessStoreFixture(t)
			ctx := context.Background()
			kind := store.QuickGenerationImageQuota
			if family == "video" {
				kind = store.QuickGenerationVideoQuota
				f.exec(t, `UPDATE content_media SET media_type='video',mime_type='video/mp4'`)
				f.params.Sources[0].MediaType = "video"
				f.params.Sources[0].MIMEType = "video/mp4"
			}
			create := func(id string) domain.GenerationPostprocessReceipt {
				t.Helper()
				p := f.params
				p.RequestID = id
				if _, _, e := f.repo.CreateGenerationPostprocessQuote(ctx, p); e != nil {
					t.Fatal(e)
				}
				r, _, e := f.repo.ConfirmGenerationPostprocess(ctx, f.user, id, p.ConfirmationSHA256, id+"-child")
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			a := create("postprocess-quota-0001")
			b := create("postprocess-quota-0002")
			for i := 0; i < 2; i++ {
				r, c, e := f.repo.ReserveQuickGenerationForJob(ctx, *a.JobID, f.user)
				if e != nil || r.Kind != kind || c != (i == 0) {
					t.Fatal(r, c, e)
				}
			}
			quota, e := f.repo.QuickGenerationQuota(ctx, f.user)
			if e != nil || family == "video" && (quota.Video.DailyUsed != 1 || quota.Image.DailyUsed != 0 || quota.Video.TotalUsed != 1) || family == "image" && (quota.Image.DailyUsed != 1 || quota.Video.DailyUsed != 0 || quota.Image.TotalUsed != 1) {
				t.Fatal(quota, e)
			}
			if _, _, e := f.repo.ReserveQuickGenerationForJob(ctx, *b.JobID, f.user); !errors.Is(e, store.ErrQuickGenerationDailyLimit) {
				t.Fatal(e)
			}
			if released, e := f.repo.ReleaseQuickGenerationForJob(ctx, *a.JobID); e != nil || !released {
				t.Fatal(released, e)
			}
			if released, e := f.repo.ReleaseQuickGenerationForJob(ctx, *a.JobID); e != nil || released {
				t.Fatal(released, e)
			}
			if _, _, e := f.repo.ReserveQuickGenerationForJob(ctx, *b.JobID, f.user); e != nil {
				t.Fatal(e)
			}
			f.exec(t, `UPDATE generation_jobs SET prompt_id='acknowledged-postprocess-prompt' WHERE id=$1`, *b.JobID)
			if c, e := f.repo.CommitQuickGenerationForJob(ctx, *b.JobID); e != nil || !c {
				t.Fatal(c, e)
			}
			if c, e := f.repo.ReleaseQuickGenerationForJob(ctx, *b.JobID); e != nil || c {
				t.Fatal(c, e)
			}
			f.exec(t, `UPDATE users SET can_generate_text_to_image=false,can_generate_image_to_image=false,can_generate_video=false`)
			if _, _, e := f.repo.ReserveQuickGenerationForJob(ctx, *a.JobID, f.user); !errors.Is(e, store.ErrQuickGenerationForbidden) {
				t.Fatal("revoked family reserved", e)
			}
		})
	}
}

func TestPostprocessReferenceLegacyAndSensitivityIntegration(t *testing.T) {
	f := newPostprocessStoreFixture(t)
	ctx := context.Background()
	ref := f.source
	ref.MediaID++
	f.exec(t, `INSERT INTO content_media(id,event_id,media_type,mime_type,original_name,payload_cipher,size_bytes,content_hash)
	SELECT $1,event_id,media_type,mime_type,'reference.png',payload_cipher,size_bytes,'' FROM content_media WHERE id=$2`, ref.MediaID, f.source.MediaID)
	ref.ArchiveSHA256 = ""
	f.params.Sources = append(f.params.Sources, ref)
	f.exec(t, `UPDATE content_events SET is_sensitive=false`)
	q, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, f.params)
	if err != nil || q.Sensitive || q.Sources[1].SHA256 != ref.SHA256 {
		t.Fatal(q, err)
	}
	f.exec(t, `UPDATE content_media SET profile_hidden_at=now() WHERE id=$1`, ref.MediaID)
	if _, _, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, f.params.RequestID, f.params.ConfirmationSHA256, "postprocess-reference-child"); !errors.Is(err, store.ErrPostprocessSource) {
		t.Fatal("hidden reference accepted", err)
	}
	f.exec(t, `UPDATE content_media SET profile_hidden_at=NULL WHERE id=$1`, ref.MediaID)
	f.exec(t, `UPDATE content_events SET is_sensitive=true`)
	r, _, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, f.params.RequestID, f.params.ConfirmationSHA256, "postprocess-reference-child")
	if err != nil || !r.Sensitive || len(r.Sources) != 2 {
		t.Fatal(r, err)
	}
	job, err := f.repo.GenerationJobByPublicID(ctx, f.user, r.JobPublicID)
	if err != nil || job.InputCount != 2 {
		t.Fatal(job, err)
	}
	var original string
	if err := f.db.QueryRow(`SELECT convert_from(payload_cipher,'UTF8') FROM content_media WHERE id=$1`, ref.MediaID).Scan(&original); err != nil || original != "original-cipher" {
		t.Fatal("original modified", original, err)
	}
	// A fresh source identifier cannot borrow another owner's event.
	f.params.RequestID = "postprocess-foreign-source"
	var stranger int64
	if err := f.db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('postprocess-stranger','disabled','user') RETURNING id`).Scan(&stranger); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE content_events SET user_id=$1`, stranger)
	if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, f.params); !errors.Is(err, store.ErrPostprocessSource) {
		t.Fatal("foreign source accepted", err)
	}
}

func TestPostprocessExpiryWhileWaitingForSourceLockIntegration(t *testing.T) {
	for _, target := range []string{"quote", "source", "account"} {
		t.Run(target, func(t *testing.T) {
			f := newPostprocessStoreFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, f.params); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE generation_postprocess_receipts SET expires_at=clock_timestamp()+interval '2 seconds'`
			if target == "source" {
				query = `UPDATE content_media SET expires_at=clock_timestamp()+interval '2 seconds'`
			}
			if target == "account" {
				query = `UPDATE users SET account_expires_at=clock_timestamp()+interval '2 seconds'`
			}
			f.exec(t, query)
			tx, err := f.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, `SELECT id FROM content_media WHERE id=$1 FOR UPDATE`, f.source.MediaID); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, _, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, f.params.RequestID, f.params.ConfirmationSHA256, "postprocess-expiry-child")
				result <- err
			}()
			blocked := false
			for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
				if err := f.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE OF j,e,m%')`).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !blocked {
				t.Fatal("confirmation never waited on source lock")
			}
			if _, err := f.db.ExecContext(ctx, `SELECT pg_sleep(2.05)`); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-result; !errors.Is(err, store.ErrPostprocessExpired) && !errors.Is(err, store.ErrPostprocessSource) {
				t.Fatal("expiry lost during lock wait", err)
			}
		})
	}
}

func TestPostprocessQuoteDeadlineAndAdminQuotaIntegration(t *testing.T) {
	f := newPostprocessStoreFixture(t)
	ctx := context.Background()
	for _, deadline := range []time.Time{time.Now().Add(-time.Second), time.Now().Add(time.Hour)} {
		p := f.params
		p.ExpiresAt = deadline.UTC().Truncate(time.Microsecond)
		if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, p); !errors.Is(err, store.ErrPostprocessExpired) {
			t.Fatal("invalid lifetime accepted", err)
		}
	}
	if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, f.params); err != nil {
		t.Fatal(err)
	}
	p := f.params
	p.ExpiresAt = p.ExpiresAt.Add(time.Minute)
	if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, p); !errors.Is(err, store.ErrPostprocessConflict) {
		t.Fatal("deadline drift accepted", err)
	}
	f.exec(t, `UPDATE users SET role='admin',can_use_quick_generation=false,can_generate_text_to_image=false,can_generate_video=false`)
	r, _, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, f.params.RequestID, f.params.ConfirmationSHA256, "postprocess-admin-child")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.repo.ReserveQuickGenerationForJob(ctx, *r.JobID, f.user); err != nil {
		t.Fatal("admin permission semantics diverged", err)
	}
	if _, err := f.repo.ReleaseQuickGenerationForJob(ctx, *r.JobID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE users SET disabled=true`)
	if _, _, err := f.repo.ReserveQuickGenerationForJob(ctx, *r.JobID, f.user); !errors.Is(err, store.ErrQuickGenerationForbidden) {
		t.Fatal("disabled admin bypass", err)
	}
}
