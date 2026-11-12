package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func (f postprocessStoreFixture) accept(t *testing.T) domain.GenerationPostprocessReceipt {
	t.Helper()
	ctx := context.Background()
	if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, f.params); err != nil {
		t.Fatal(err)
	}
	r, _, err := f.repo.ConfirmGenerationPostprocess(ctx, f.user, f.params.RequestID, f.params.ConfirmationSHA256, f.params.RequestID+"-child")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f postprocessStoreFixture) assertUnpublished(t *testing.T, r domain.GenerationPostprocessReceipt) {
	t.Helper()
	job, err := f.repo.GenerationJobByPublicID(context.Background(), f.user, r.JobPublicID)
	if err != nil || job.DispatchQueuedAt != nil || job.QuotaReservedOn != nil {
		t.Fatal("unexpected publication/reservation", job, err)
	}
	q, err := f.repo.QuickGenerationQuota(context.Background(), f.user)
	if err != nil || q.Image.DailyUsed != 0 || q.Image.TotalUsed != 0 || q.Video.DailyUsed != 0 || q.Video.TotalUsed != 0 {
		t.Fatal("quota changed", q, err)
	}
}

func TestPostprocessPublishConcurrentIntegration(t *testing.T) {
	for _, family := range []string{"image", "video"} {
		t.Run(family, func(t *testing.T) {
			f := newPostprocessStoreFixture(t)
			ctx := context.Background()
			if family == "video" {
				f.exec(t, `UPDATE content_media SET media_type='video',mime_type='video/mp4'`)
				f.params.Sources[0].MediaType, f.params.Sources[0].MIMEType = "video", "video/mp4"
			}
			r := f.accept(t)
			f.assertUnpublished(t, r)
			// Expiry limits consent, not recovery after consent was committed.
			f.exec(t, `UPDATE generation_postprocess_receipts SET expires_at=now()-interval '1 hour'`)
			type result struct {
				job       domain.GenerationJob
				published bool
				err       error
			}
			results := make(chan result, 12)
			var wg sync.WaitGroup
			for range 12 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					j, p, e := store.New(f.db).PublishGenerationPostprocess(ctx, f.user, f.params.RequestID)
					results <- result{j, p, e}
				}()
			}
			wg.Wait()
			close(results)
			published := 0
			for item := range results {
				if item.err != nil || item.job.ID != *r.JobID || item.job.DispatchQueuedAt == nil || item.job.QuotaReservedOn == nil {
					t.Fatal(item)
				}
				if item.published {
					published++
				}
			}
			if published != 1 {
				t.Fatal("publication count", published)
			}
			q, err := f.repo.QuickGenerationQuota(ctx, f.user)
			if err != nil || family == "image" && (q.Image.DailyUsed != 1 || q.Image.TotalUsed != 1 || q.Video.TotalUsed != 0) || family == "video" && (q.Video.DailyUsed != 1 || q.Video.TotalUsed != 1 || q.Image.TotalUsed != 0) {
				t.Fatal("wrong quota", q, err)
			}
			claimed, err := f.repo.ClaimNextGenerationDispatch(ctx, "postprocess-publish-claim", 1)
			if err != nil || claimed.ID != *r.JobID {
				t.Fatal("not in common queue", claimed, err)
			}
			if _, err := f.repo.ClaimNextGenerationDispatch(ctx, "postprocess-publish-second", 1); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("duplicate claim", err)
			}
			if j, p, err := f.repo.PublishGenerationPostprocess(ctx, f.user, f.params.RequestID); err != nil || p || j.DispatchToken != claimed.DispatchToken {
				t.Fatal("claim changed", j, p, err)
			}
		})
	}
}

func TestPostprocessPublishRollbackIntegration(t *testing.T) {
	f := newPostprocessStoreFixture(t)
	r := f.accept(t)
	// Fail the queue write after quota accounting has run in the same transaction.
	f.exec(t, `CREATE FUNCTION fail_postprocess_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.dispatch_queued_at IS NOT NULL THEN RAISE EXCEPTION 'injected publication failure'; END IF; RETURN NEW; END $$`)
	t.Cleanup(func() {
		f.exec(t, `DROP TRIGGER IF EXISTS fail_postprocess_publication ON generation_jobs`)
		f.exec(t, `DROP FUNCTION IF EXISTS fail_postprocess_publication()`)
	})
	f.exec(t, `CREATE TRIGGER fail_postprocess_publication BEFORE UPDATE ON generation_jobs FOR EACH ROW EXECUTE FUNCTION fail_postprocess_publication()`)
	if _, p, err := f.repo.PublishGenerationPostprocess(context.Background(), f.user, f.params.RequestID); err == nil || p {
		t.Fatal("failure not propagated", p, err)
	}
	f.assertUnpublished(t, r)
	f.exec(t, `DROP TRIGGER fail_postprocess_publication ON generation_jobs`)
	j, p, err := store.New(f.db).PublishGenerationPostprocess(context.Background(), f.user, f.params.RequestID)
	if err != nil || !p || j.ID != *r.JobID {
		t.Fatal("restart recovery", j, p, err)
	}
}

func TestPostprocessPublishSensitivityIntegration(t *testing.T) {
	f := newPostprocessStoreFixture(t)
	ctx := context.Background()
	f.exec(t, `UPDATE content_events SET is_sensitive=false`)
	r := f.accept(t)
	if r.Sensitive {
		t.Fatal("fixture already sensitive")
	}
	f.exec(t, `UPDATE content_events SET is_sensitive=true`)
	if _, _, err := f.repo.PublishGenerationPostprocess(ctx, f.user, r.RequestID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE content_events SET is_sensitive=false`)
	if _, p, err := f.repo.PublishGenerationPostprocess(ctx, f.user, r.RequestID); err != nil || p {
		t.Fatal(p, err)
	}
	after, err := f.repo.GenerationPostprocessByRequest(ctx, f.user, r.RequestID)
	if err != nil || !after.Sensitive {
		t.Fatal("publication sensitivity lost", after, err)
	}
}

func TestPostprocessPublishQuotaCompetitionIntegration(t *testing.T) {
	f := newPostprocessStoreFixture(t)
	ctx := context.Background()
	a := f.accept(t)
	f.params.RequestID = "postprocess-competing-0002"
	b := f.accept(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, r := range []domain.GenerationPostprocessReceipt{a, b} {
		go func(r domain.GenerationPostprocessReceipt) {
			<-start
			_, _, err := f.repo.PublishGenerationPostprocess(ctx, f.user, r.RequestID)
			results <- err
		}(r)
	}
	close(start)
	accepted, rejected := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			accepted++
		} else if errors.Is(err, store.ErrQuickGenerationDailyLimit) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatal("quota race", accepted, rejected)
	}
	var published, reserved int
	if err := f.db.QueryRow(`SELECT count(*) FILTER(WHERE dispatch_queued_at IS NOT NULL),count(*) FILTER(WHERE quota_reserved_on IS NOT NULL) FROM generation_jobs WHERE user_id=$1`, f.user).Scan(&published, &reserved); err != nil || published != 1 || reserved != 1 {
		t.Fatal("quota/publication diverged", published, reserved, err)
	}
}

func TestPostprocessPublishCancellationRaceIntegration(t *testing.T) {
	f := newPostprocessStoreFixture(t)
	ctx := context.Background()
	for i := range 12 {
		f.params.RequestID = fmt.Sprintf("postprocess-cancel-race-%04d", i)
		r := f.accept(t)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, _, err := f.repo.PublishGenerationPostprocess(ctx, f.user, r.RequestID)
			results <- err
		}()
		go func() {
			<-start
			_, _, err := f.repo.RequestGenerationJobCancellation(ctx, *r.JobID, f.user)
			results <- err
		}()
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.repo.ClaimNextGenerationDispatch(ctx, "postprocess-cancelled-claim", 1); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("cancelled work claimed", err)
		}
		if _, err := f.repo.ReleaseQuickGenerationForJob(ctx, *r.JobID); err != nil {
			t.Fatal(err)
		}
		if j, p, err := f.repo.PublishGenerationPostprocess(ctx, f.user, r.RequestID); err != nil || p || j.CancellationRequestedAt == nil || j.QuotaReservedOn != nil {
			t.Fatal("cancelled work recharged", j, p, err)
		}
		q, err := f.repo.QuickGenerationQuota(ctx, f.user)
		if err != nil || q.Image.DailyUsed != 0 || q.Image.TotalUsed != 0 {
			t.Fatal("cancellation leaked quota", q, err)
		}
	}
}

func TestPostprocessPublishFencesIntegration(t *testing.T) {
	for name, change := range map[string]string{
		"disabled":        `UPDATE users SET disabled=true`,
		"expired account": `UPDATE users SET account_expires_at=now()-interval '1 second'`,
		"revoked family":  `UPDATE users SET can_generate_text_to_image=false,can_generate_image_to_image=false`,
		"source expired":  `UPDATE content_media SET expires_at=now()-interval '1 second'`,
		"source changed":  `UPDATE content_media SET content_hash=repeat('c',64)`,
		"payload changed": `UPDATE generation_jobs SET payload_cipher='changed' WHERE state='waiting_for_resources'`,
		"wrong workflow":  `UPDATE generation_jobs SET workflow_id='other' WHERE state='waiting_for_resources'`,
		"request unbound": `UPDATE generation_requests SET job_id=NULL`,
		"quota exhausted": `UPDATE users SET generation_total_limit=1,generation_total_used=1`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newPostprocessStoreFixture(t)
			r := f.accept(t)
			f.exec(t, change)
			if _, p, err := f.repo.PublishGenerationPostprocess(context.Background(), f.user, f.params.RequestID); err == nil || p {
				t.Fatal("fence bypassed", p, err)
			}
			if name == "quota exhausted" {
				f.exec(t, `UPDATE users SET generation_total_used=0`)
			}
			f.assertUnpublished(t, r)
		})
	}
	for name, change := range map[string]string{
		"cancelled":        `cancellation_requested_at=now()`,
		"released":         `resources_released_at=now()`,
		"closed":           `dispatch_closed_at=now()`,
		"submission begun": `submission_started_at=now()`,
		"bound":            `prompt_id='existing-prompt'`,
		"failed":           `state='failed'`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newPostprocessStoreFixture(t)
			r := f.accept(t)
			f.exec(t, `UPDATE generation_jobs SET `+change+` WHERE id=$1`, *r.JobID)
			if j, p, err := f.repo.PublishGenerationPostprocess(context.Background(), f.user, f.params.RequestID); err != nil || p || j.ID != *r.JobID {
				t.Fatal("reopened job", j, p, err)
			}
			f.assertUnpublished(t, r)
		})
	}
	t.Run("consent and deletion", func(t *testing.T) {
		f := newPostprocessStoreFixture(t)
		ctx := context.Background()
		if _, _, err := f.repo.CreateGenerationPostprocessQuote(ctx, f.params); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.repo.PublishGenerationPostprocess(ctx, f.user, f.params.RequestID); !errors.Is(err, store.ErrPostprocessConflict) {
			t.Fatal("quote published", err)
		}
		r := f.accept(t)
		if _, _, err := f.repo.PublishGenerationPostprocess(ctx, f.user+1, f.params.RequestID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("foreign publication", err)
		}
		f.exec(t, `DELETE FROM generation_jobs WHERE id=$1`, *r.JobID)
		if _, _, err := f.repo.PublishGenerationPostprocess(ctx, f.user, f.params.RequestID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("deleted child recreated", err)
		}
		after, err := f.repo.GenerationPostprocessByRequest(ctx, f.user, f.params.RequestID)
		if err != nil || after.JobID != nil || after.JobPublicID != r.JobPublicID || after.State != "accepted" {
			t.Fatal("receipt fence lost", after, err)
		}
	})
}

func TestPostprocessPublicationFailureFenceRaceIntegration(t *testing.T) {
	f := newPostprocessStoreFixture(t)
	ctx := context.Background()
	f.exec(t, `UPDATE users SET generation_daily_limit=0,generation_total_limit=0`)
	for i := range 12 {
		f.params.RequestID = fmt.Sprintf("postprocess-failure-fence-%04d", i)
		r := f.accept(t)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, _, err := f.repo.PublishGenerationPostprocess(ctx, f.user, r.RequestID)
			results <- err
		}()
		go func() {
			<-start
			_, _, err := f.repo.FencePostprocessPublicationFailure(ctx, *r.JobID, f.user, r.RequestID, "postprocess_publish_source", "Source unavailable")
			results <- err
		}()
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		job, err := f.repo.GenerationJobByPublicID(ctx, f.user, r.JobPublicID)
		if err != nil || (job.DispatchClosedAt == nil) == (job.DispatchQueuedAt == nil) {
			t.Fatal("failure/publication not exclusive", job, err)
		}
		if (job.QuotaReservedOn != nil) != (job.DispatchQueuedAt != nil) {
			t.Fatal("failure fence leaked quota", job)
		}
		if job.DispatchQueuedAt != nil {
			closed, changed, err := f.repo.FencePostprocessPublicationFailure(ctx, job.ID, f.user, r.RequestID, "postprocess_publish_source", "Late stale rejection")
			if err != nil || changed || closed.DispatchClosedAt != nil || closed.QuotaReservedOn == nil {
				t.Fatal("stale failure closed published child", closed, changed, err)
			}
		} else {
			if after, changed, err := f.repo.PublishGenerationPostprocess(ctx, f.user, r.RequestID); err != nil || changed || after.QuotaReservedOn != nil || after.DispatchClosedAt == nil {
				t.Fatal("failed child reopened", after, changed, err)
			}
		}
	}
}
