package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestGPUAdmissionIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	if _, err = db.ExecContext(ctx, `TRUNCATE gpu_resources CASCADE`); err != nil {
		t.Fatal(err)
	}
	repository := store.New(db)
	var normal, priority int64
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('gpu-normal','disabled','user') RETURNING id`).Scan(&normal); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('gpu-priority','disabled','user') RETURNING id`).Scan(&priority); err != nil {
		t.Fatal(err)
	}
	enqueue := func(id string, kind domain.GPUWorkKind, high bool) domain.GPUWork {
		t.Helper()
		user := normal
		if high {
			user = priority
		}
		work, err := repository.EnqueueGPUWork(ctx, domain.GPUWorkRequest{ID: id, Kind: kind, JobKey: id, ResourceID: domain.GPUPrimaryResource, UserID: user, Priority: high})
		if err != nil {
			t.Fatal(err)
		}
		return work
	}
	resource := func() domain.GPUResource {
		t.Helper()
		item, err := repository.GPUResource(ctx, domain.GPUPrimaryResource)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	observe := func(state string) domain.GPUResource {
		t.Helper()
		current := resource()
		item, err := repository.ObserveGPUResource(ctx, current.ID, current.Revision, state, "executor fixture", 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	acquire := func(work domain.GPUWork, token string) domain.GPUAdmission {
		t.Helper()
		admission, err := repository.AcquireGPUWork(ctx, work.ID, token, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return admission
	}
	generation := enqueue("normal-generation", domain.GPUWorkGeneration, false)
	if got := acquire(generation, "first"); got.Granted || got.WaitCode != "executor_unavailable" {
		t.Fatalf("unobserved executor allowed work: %+v", got)
	}
	old := observe("busy")
	if got := acquire(generation, "first"); got.Granted || got.WaitCode != "external_work" {
		t.Fatalf("unknown external ComfyUI job ignored: %+v", got)
	}
	observe("unknown")
	if _, err = repository.ObserveGPUResource(ctx, old.ID, old.Revision, "idle", "late idle result", time.Second); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("late observation accepted: %v", err)
	}
	observe("idle")
	if _, err = db.ExecContext(ctx, `UPDATE gpu_resources SET valid_until=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if got := acquire(generation, "first"); got.Granted || got.WaitCode != "executor_unavailable" {
		t.Fatalf("stale idle observation accepted: %+v", got)
	}
	training := enqueue("priority-training", domain.GPUWorkTraining, true)
	caption := enqueue("normal-caption", domain.GPUWorkCaption, false)
	assistant := enqueue("priority-assistant", domain.GPUWorkAssistant, true)
	observe("idle")
	if got := acquire(generation, "first"); got.Granted || got.WaitCode != "waiting_turn" || got.Position != 3 {
		t.Fatalf("priority order: %+v", got)
	}
	if _, err = db.ExecContext(ctx, `UPDATE gpu_work SET queued_at=now()-interval '11 minutes' WHERE id=$1`, generation.ID); err != nil {
		t.Fatal(err)
	}
	if got := acquire(training, "first"); got.Granted || got.Position != 2 {
		t.Fatalf("aging did not protect normal generation: %+v", got)
	}

	// Every type competes concurrently through separate Store instances, as independent workers do.
	var wg sync.WaitGroup
	results := make(chan domain.GPUAdmission, 32)
	failures := make(chan error, 32)
	for worker := range 8 {
		for _, work := range []domain.GPUWork{generation, training, caption, assistant} {
			wg.Add(1)
			go func(worker int, work domain.GPUWork) {
				defer wg.Done()
				got, err := store.New(db).AcquireGPUWork(ctx, work.ID, fmt.Sprintf("worker-%d-%s", worker, work.ID), time.Minute)
				if err != nil {
					failures <- err
				} else {
					results <- got
				}
			}(worker, work)
		}
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	winners := []domain.GPUAdmission{}
	for result := range results {
		if result.Granted {
			winners = append(winners, result)
		}
	}
	if len(winners) != 1 || winners[0].Work.ID != generation.ID {
		t.Fatalf("concurrent GPU holders: %+v", winners)
	}
	holder := winners[0].Work
	if got := acquire(generation, holder.LeaseToken); !got.Granted {
		t.Fatal("same lease retry lost its grant")
	}
	if _, err = repository.SetGPUWorkPhase(ctx, holder.ID, holder.LeaseToken, "running", "prompt-1"); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("running skipped dispatch marker: %v", err)
	}
	beforeDispatch := resource()
	if _, err = repository.SetGPUWorkPhase(ctx, holder.ID, holder.LeaseToken, "dispatching", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ObserveGPUResource(ctx, beforeDispatch.ID, beforeDispatch.Revision, "idle", "probe before submission", time.Second); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("in-flight handoff overwritten by idle: %v", err)
	}
	if _, err = repository.ReleaseGPUWork(ctx, holder.ID, holder.LeaseToken, domain.GPUReleaseEvidence{Code: "not_dispatched", ResourceRevision: resource().Revision}); !errors.Is(err, store.ErrGPUWorkInput) {
		t.Fatalf("dispatching request treated as not sent: %v", err)
	}
	if _, err = repository.SetGPUWorkPhase(ctx, holder.ID, holder.LeaseToken, "running", "prompt-1"); err != nil {
		t.Fatal(err)
	}
	if renewed, err := repository.HeartbeatGPUWork(ctx, holder.ID, "wrong-token", time.Minute); err != nil || renewed {
		t.Fatalf("wrong worker renewed GPU: %v %v", renewed, err)
	}
	if renewed, err := repository.HeartbeatGPUWork(ctx, holder.ID, holder.LeaseToken, time.Minute); err != nil || !renewed {
		t.Fatalf("live heartbeat rejected: %v %v", renewed, err)
	}
	if _, err = repository.RequestGPUWorkCancellation(ctx, holder.ID); err != nil {
		t.Fatal(err)
	}
	if got := acquire(training, "trainer"); got.Granted || got.WaitCode != "gpu_in_use" {
		t.Fatalf("cancellation released running GPU early: %+v", got)
	}
	if _, err = db.ExecContext(ctx, `UPDATE gpu_work SET lease_until=now()-interval '1 second' WHERE id=$1`, holder.ID); err != nil {
		t.Fatal(err)
	}
	if got := acquire(training, "trainer"); got.Granted || got.WaitCode != "executor_unconfirmed" {
		t.Fatalf("expired lease allowed concurrent dispatch: %+v", got)
	}
	if renewed, err := repository.HeartbeatGPUWork(ctx, holder.ID, holder.LeaseToken, time.Minute); err != nil || renewed {
		t.Fatalf("old heartbeat revived uncertain lease: %v %v", renewed, err)
	}
	// A restarted Gateway must see the uncertain holder, even with an idle-looking observation.
	repository = store.New(db)
	observe("idle")
	if got := acquire(caption, "captioner"); got.Granted || got.WaitCode != "executor_unconfirmed" {
		t.Fatalf("restart discarded uncertain owner: %+v", got)
	}
	for _, code := range []string{"timeout", "request_cancelled", "not_dispatched", "request_completed"} {
		if _, err = repository.ReleaseGPUWork(ctx, holder.ID, holder.LeaseToken, domain.GPUReleaseEvidence{Code: code, ResourceRevision: resource().Revision}); !errors.Is(err, store.ErrGPUWorkInput) {
			t.Fatalf("weak release evidence %s accepted: %v", code, err)
		}
	}
	staleProof := resource().Revision
	observe("unknown")
	if _, err = repository.ReleaseGPUWork(ctx, holder.ID, holder.LeaseToken, domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: staleProof}); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("stale terminal proof accepted: %v", err)
	}
	released, err := repository.ReleaseGPUWork(ctx, holder.ID, holder.LeaseToken, domain.GPUReleaseEvidence{Code: "executor_terminal", Detail: "fixture prompt-1 cancellation confirmed", ResourceRevision: resource().Revision})
	if err != nil || released.State != domain.GPUWorkCancelled {
		t.Fatalf("confirmed release: %+v %v", released, err)
	}
	if got := acquire(training, "trainer"); got.Granted || got.WaitCode != "executor_unavailable" {
		t.Fatalf("next work reused old idle observation: %+v", got)
	}
	observe("idle")
	if got := acquire(training, "trainer"); !got.Granted {
		t.Fatalf("next priority work did not start: %+v", got)
	}
	if _, err = repository.ReleaseGPUWork(ctx, holder.ID, holder.LeaseToken, domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: resource().Revision}); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("old release changed a newer owner: %v", err)
	}
	if _, err = db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, priority); err != nil {
		t.Fatal(err)
	}
	if got := acquire(caption, "captioner"); got.Granted {
		t.Fatal("owner deletion silently released active trainer")
	}
	active, err := repository.ActiveGPUWork(ctx, domain.GPUPrimaryResource)
	if err != nil || active.ID != training.ID || active.UserID != nil {
		t.Fatalf("deleted owner's work lost: %+v %v", active, err)
	}
	if _, err = repository.ReleaseGPUWork(ctx, training.ID, "trainer", domain.GPUReleaseEvidence{Code: "not_dispatched", ResourceRevision: resource().Revision}); err != nil {
		t.Fatal(err)
	}
	observe("idle")
	if got := acquire(caption, "captioner"); !got.Granted {
		t.Fatalf("deleted waiting owner blocked next work: %+v", got)
	}
	if _, err = repository.SetGPUWorkPhase(ctx, caption.ID, "captioner", "dispatching", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ReleaseGPUWork(ctx, caption.ID, "captioner", domain.GPUReleaseEvidence{Code: "request_completed", Detail: "model response fully received", ResourceRevision: resource().Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err = repository.RetryGPUWork(ctx, caption.ID); err != nil {
		t.Fatal(err)
	}
	observe("idle")
	if got := acquire(caption, "captioner-2"); !got.Granted || got.Work.LeaseToken == "captioner" {
		t.Fatalf("retry did not get new fenced lease: %+v", got)
	}
	if _, err = repository.ReleaseGPUWork(ctx, caption.ID, "captioner", domain.GPUReleaseEvidence{Code: "request_completed", ResourceRevision: resource().Revision}); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("prior attempt completed new attempt: %v", err)
	}
	if _, err = repository.ReleaseGPUWork(ctx, caption.ID, "captioner-2", domain.GPUReleaseEvidence{Code: "request_completed", ResourceRevision: resource().Revision}); !errors.Is(err, store.ErrGPUWorkInput) {
		t.Fatalf("undispatched caption claimed a completed request: %v", err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE gpu_work SET lease_until=now()-interval '1 second' WHERE id=$1`, caption.ID); err != nil {
		t.Fatal(err)
	}
	// Expiration must be enforced even before another worker marks the lease uncertain.
	if _, err = repository.ReleaseGPUWork(ctx, caption.ID, "captioner-2", domain.GPUReleaseEvidence{Code: "not_dispatched", ResourceRevision: resource().Revision}); !errors.Is(err, store.ErrGPUWorkInput) {
		t.Fatalf("expired owner released without executor evidence: %v", err)
	}
	if _, err = repository.ReleaseGPUWork(ctx, caption.ID, "captioner-2", domain.GPUReleaseEvidence{Code: "executor_terminal", ResourceRevision: resource().Revision}); err != nil {
		t.Fatal(err)
	}
	transient := enqueue("expired-assistant", domain.GPUWorkAssistant, false)
	if _, err = db.ExecContext(ctx, `UPDATE gpu_work SET ready_until=now()-interval '1 second' WHERE id=$1`, transient.ID); err != nil {
		t.Fatal(err)
	}
	observe("idle")
	if got := acquire(transient, "ephemeral"); got.Granted || got.WaitCode != "intent_expired" {
		t.Fatalf("dead request acquired a GPU: %+v", got)
	}
	transient = enqueue(transient.ID, transient.Kind, false)
	if got := acquire(transient, "ephemeral"); !got.Granted {
		t.Fatalf("durable worker could not refresh its intent: %+v", got)
	}
	if _, err = repository.ReleaseGPUWork(ctx, transient.ID, "ephemeral", domain.GPUReleaseEvidence{Code: "not_dispatched", ResourceRevision: resource().Revision}); err != nil {
		t.Fatal(err)
	}
	cancelled := enqueue("cancel-before-start", domain.GPUWorkAssistant, false)
	if row, err := repository.RequestGPUWorkCancellation(ctx, cancelled.ID); err != nil || row.State != domain.GPUWorkCancelled {
		t.Fatalf("waiting cancellation: %+v %v", row, err)
	}
	var events, expired, terminal int
	if err = db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE code='lease_expired'),count(*) FILTER(WHERE code='executor_terminal') FROM gpu_work_events WHERE work_id=$1`, holder.ID).Scan(&events, &expired, &terminal); err != nil || events < 6 || expired != 1 || terminal != 1 {
		t.Fatalf("durable execution evidence: events=%d expired=%d terminal=%d err=%v", events, expired, terminal, err)
	}
}
