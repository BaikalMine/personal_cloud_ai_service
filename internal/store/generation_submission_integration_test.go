package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lib/pq"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func submissionBatch(user int64, suffix string) domain.CreateGenerationBatchParams {
	p := domain.CreateGenerationBatchParams{UserID: user, PublicID: "submission-batch-" + suffix, RequestID: "submission-request-" + suffix, Mode: domain.GenerationBatchSeeds, MaxParallel: 1, ModelName: "synthetic-model", TemplateID: "text-to-image", WorkflowID: "photoflow-krea2"}
	for i := 1; i <= 2; i++ {
		p.Jobs = append(p.Jobs, domain.CreateGenerationBatchJobParams{PublicID: fmt.Sprintf("submission-child-%s-%d", suffix, i), RequestID: fmt.Sprintf("submission-child-request-%s-%d", suffix, i), Position: i,
			Prepared: domain.PreparedGenerationJob{TemplateID: "text-to-image", WorkflowID: "photoflow-krea2", PayloadCipher: []byte("synthetic-payload"), InputNames: []string{}, Seed: int64(i)}})
	}
	return p
}

func TestGenerationSubmissionClosureIntegration(t *testing.T) {
	db, s, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	request := "closed-submission-request"
	for _, kind := range []string{"single", "batch"} {
		for range 2 {
			r, err := s.CloseGenerationSubmission(ctx, user, kind, request)
			if err != nil || !r.Closed || r.PublicID != "" || r.Kind != kind {
				t.Fatal(r, err)
			}
		}
	}
	if _, created, err := s.ClaimGenerationJob(ctx, domain.CreateGenerationJobParams{UserID: user, PublicID: "closed-submission-job", RequestID: request}); !errors.Is(err, store.ErrGenerationSubmissionClosed) || created {
		t.Fatal(created, err)
	}
	p := submissionBatch(user, "closed")
	p.RequestID = request
	if _, created, err := s.CreateGenerationBatch(ctx, p); !errors.Is(err, store.ErrGenerationSubmissionClosed) || created {
		t.Fatal(created, err)
	}
	var count int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM generation_jobs)+(SELECT count(*) FROM generation_batches)+(SELECT count(*) FROM generation_requests)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("closed request created work", count, err)
	}
	var other int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('other-submission-owner','hash','user') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.ClaimGenerationJob(ctx, domain.CreateGenerationJobParams{UserID: other, PublicID: "other-submission-job", RequestID: request}); err != nil || !created {
		t.Fatal("closure crossed user boundary", created, err)
	}
}

func TestGenerationSubmissionRetentionIntegration(t *testing.T) {
	db, s, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	p := domain.CreateGenerationJobParams{UserID: user, PublicID: "retained-submission-job", RequestID: "retained-submission-request"}
	job, created, err := s.ClaimGenerationJob(ctx, p)
	if err != nil || !created {
		t.Fatal(job, created, err)
	}
	r, err := s.CloseGenerationSubmission(ctx, user, "single", p.RequestID)
	if err != nil || r.Closed || r.PublicID != job.PublicID {
		t.Fatal("bound job was reported stopped", r, err)
	}
	if _, created, err := s.ClaimGenerationJob(ctx, p); err != nil || created {
		t.Fatal(created, err)
	}
	if _, err := db.Exec(`DELETE FROM generation_jobs WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.ClaimGenerationJob(ctx, p); !errors.Is(err, store.ErrGenerationSubmissionExpired) || created {
		t.Fatal("history retention reopened request", created, err)
	}
	batchParams := submissionBatch(user, "retained")
	batch, created, err := s.CreateGenerationBatch(ctx, batchParams)
	if err != nil || !created {
		t.Fatal(batch, created, err)
	}
	r, err = s.CloseGenerationSubmission(ctx, user, "batch", batchParams.RequestID)
	if err != nil || r.Closed || r.PublicID != batch.PublicID {
		t.Fatal(r, err)
	}
	if _, created, err := s.CreateGenerationBatch(ctx, batchParams); err != nil || created {
		t.Fatal(created, err)
	}
	if _, err := db.Exec(`DELETE FROM generation_batches WHERE id=$1`, batch.ID); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.CreateGenerationBatch(ctx, batchParams); !errors.Is(err, store.ErrGenerationSubmissionExpired) || created {
		t.Fatal("batch retention reopened request", created, err)
	}
}

func TestGenerationSubmissionConcurrentCloseAndClaimIntegration(t *testing.T) {
	db, s, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	for round := range 12 {
		request := fmt.Sprintf("racing-submission-request-%d", round)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var created atomic.Int32
		for worker := range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if worker%3 == 0 {
					if _, err := s.CloseGenerationSubmission(ctx, user, "single", request); err != nil {
						t.Error(err)
					}
					return
				}
				_, claimed, err := s.ClaimGenerationJob(ctx, domain.CreateGenerationJobParams{UserID: user, PublicID: fmt.Sprintf("racing-submission-job-%d-%d", round, worker), RequestID: request})
				if err != nil && !errors.Is(err, store.ErrGenerationSubmissionClosed) {
					t.Error(err)
				}
				if claimed {
					created.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		r, err := s.GenerationSubmissionByRequest(ctx, user, "single", request)
		var count int32
		if queryErr := db.QueryRow(`SELECT count(*) FROM generation_jobs WHERE user_id=$1 AND request_id=$2`, user, request).Scan(&count); queryErr != nil {
			t.Fatal(queryErr)
		}
		if err != nil || count != created.Load() || count > 1 || r.Closed && count != 0 || !r.Closed && (count != 1 || r.PublicID == "") {
			t.Fatal("creation/closure did not serialize", r, count, created.Load(), err)
		}
	}
}

func TestGenerationSubmissionLegacyOrphanCannotBeClosedIntegration(t *testing.T) {
	db, s, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	request := "legacy-submission-request"
	if _, err := db.Exec(`INSERT INTO generation_requests(user_id,request_id,prompt_id) VALUES($1,$2,'legacy-prompt-id')`, user, request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseGenerationSubmission(ctx, user, "single", request); !errors.Is(err, store.ErrGenerationSubmissionUncertain) {
		t.Fatal(err)
	}
	job, created, err := s.ClaimGenerationJob(ctx, domain.CreateGenerationJobParams{UserID: user, PublicID: "legacy-submission-adopted", RequestID: request})
	if err != nil || created || job.PromptID != "legacy-prompt-id" {
		t.Fatal(job, created, err)
	}
	r, err := s.CloseGenerationSubmission(ctx, user, "single", request)
	if err != nil || r.Closed || r.PublicID != job.PublicID {
		t.Fatal(r, err)
	}
}

func TestGenerationSubmissionConcurrentBatchCloseIntegration(t *testing.T) {
	db, s, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	for round := range 10 {
		request := fmt.Sprintf("racing-batch-request-%d", round)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var created atomic.Int32
		for worker := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if worker%3 == 0 {
					if _, err := s.CloseGenerationSubmission(ctx, user, "batch", request); err != nil {
						t.Error(err)
					}
					return
				}
				params := submissionBatch(user, fmt.Sprintf("race-%d-%d", round, worker))
				params.RequestID = request
				_, claimed, err := s.CreateGenerationBatch(ctx, params)
				var pgErr *pq.Error
				serializationConflict := errors.As(err, &pgErr) && pgErr.Code == "40001"
				if err != nil && !errors.Is(err, store.ErrGenerationSubmissionClosed) && !serializationConflict {
					t.Error(err)
				}
				if claimed {
					created.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		r, err := s.GenerationSubmissionByRequest(ctx, user, "batch", request)
		var batches, children int32
		if queryErr := db.QueryRow(`SELECT count(DISTINCT b.id),count(j.id) FROM generation_batches b
			LEFT JOIN generation_jobs j ON j.batch_id=b.id WHERE b.user_id=$1 AND b.request_id=$2`, user, request).Scan(&batches, &children); queryErr != nil {
			t.Fatal(queryErr)
		}
		if err != nil || batches != created.Load() || batches > 1 || children != batches*2 || r.Closed && batches != 0 || !r.Closed && (batches != 1 || r.PublicID == "") {
			t.Fatal("batch creation and closure diverged", r, batches, children, created.Load(), err)
		}
	}
}

func TestGenerationSubmissionBatchRollbackIntegration(t *testing.T) {
	db, s, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	params := submissionBatch(user, "rollback")
	params.Jobs[1].PublicID = params.Jobs[0].PublicID
	if _, created, err := s.CreateGenerationBatch(ctx, params); err == nil || created {
		t.Fatal("duplicate child did not abort batch", created, err)
	}
	var count int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM generation_submissions)+(SELECT count(*) FROM generation_batches)+(SELECT count(*) FROM generation_jobs)+(SELECT count(*) FROM generation_requests)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("rollback left a binding or partial work", count, err)
	}
	receipt, err := s.CloseGenerationSubmission(ctx, user, "batch", params.RequestID)
	if err != nil || !receipt.Closed || receipt.PublicID != "" {
		t.Fatal(receipt, err)
	}
	params.Jobs[1].PublicID += "-unique"
	if _, created, err := s.CreateGenerationBatch(ctx, params); !errors.Is(err, store.ErrGenerationSubmissionClosed) || created {
		t.Fatal("rollback closure did not fence a delayed batch", created, err)
	}
}
