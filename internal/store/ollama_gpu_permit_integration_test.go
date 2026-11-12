package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestOwnedOllamaGPUExecutorPermits(t *testing.T) {
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
	if _, err = db.ExecContext(ctx, `TRUNCATE gpu_resources,gpu_inference_executors CASCADE`); err != nil {
		t.Fatal(err)
	}
	s := store.New(db)
	if err = s.EnsurePrimaryGPUResource(ctx); err != nil {
		t.Fatal(err)
	}
	observe := func() {
		t.Helper()
		r, err := s.GPUResource(ctx, domain.GPUPrimaryResource)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ObserveGPUResource(ctx, r.ID, r.Revision, "idle", "owned Ollama fixture", 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	owner := domain.GPUInferenceExecutor{ID: strings.Repeat("a", 64), Generation: 1, Token: strings.Repeat("b", 64)}
	input := domain.GPUExecutorPermitRequest{Executor: domain.GPUExecutorOllama, ID: strings.Repeat("c", 32), InstanceID: strings.Repeat("d", 32), Token: strings.Repeat("e", 64), OwnerID: owner.ID, OwnerGeneration: owner.Generation, OwnerToken: owner.Token}
	miningLease := domain.QuickGenerationMiningLease{ExecutorPermitID: input.ID, ExecutorOwnerID: owner.ID, ExecutorGeneration: owner.Generation}
	assertMiningSettled := func(want bool) {
		t.Helper()
		if settled, err := s.HarnessMiningLeaseSettled(ctx, miningLease); err != nil || settled != want {
			t.Fatalf("Harness mining completion: settled=%v want=%v err=%v", settled, want, err)
		}
	}
	assertMiningSettled(false)
	observe()
	if _, err = s.ReserveGPUExecutorPermit(ctx, input); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("unregistered native owner: %v", err)
	}
	if err = s.RegisterOwnedInferenceExecutor(ctx, owner); err != nil {
		t.Fatal(err)
	}
	assertMiningSettled(false)
	for _, bad := range []domain.GPUExecutorPermitRequest{
		{Executor: domain.GPUExecutorOllama, ID: input.ID, InstanceID: input.InstanceID, Token: input.Token},
		{Executor: domain.GPUExecutorComfyUI, ID: input.ID, InstanceID: input.InstanceID, Token: input.Token, OwnerID: owner.ID, OwnerGeneration: 1, OwnerToken: owner.Token},
	} {
		if _, err = s.ReserveGPUExecutorPermit(ctx, bad); !errors.Is(err, store.ErrGPUWorkInput) {
			t.Fatalf("invalid owner scope accepted: %v", err)
		}
	}
	bad := input
	bad.WorkID = "managed-generation"
	bad.WorkLease = "lease"
	if _, err = s.ReserveGPUExecutorPermit(ctx, bad); !errors.Is(err, store.ErrGPUWorkInput) {
		t.Fatalf("Ollama adopted managed Comfy work: %v", err)
	}
	permit, err := s.ReserveGPUExecutorPermit(ctx, input)
	if err != nil || permit.Executor != domain.GPUExecutorOllama || permit.OwnerID != owner.ID || permit.OwnerGeneration != 1 {
		t.Fatalf("owner binding: %+v %v", permit, err)
	}
	bad = input
	bad.OwnerToken = strings.Repeat("f", 64)
	if _, err = s.GPUExecutorPermit(ctx, bad); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("wrong owner token: %v", err)
	}
	if n, err := s.RecoverOwnedGPUExecutorPermits(ctx, owner); err != nil || n != 0 {
		t.Fatalf("current generation recovered: %d %v", n, err)
	}
	foreign := domain.GPUInferenceExecutor{ID: strings.Repeat("f", 64), Generation: 2, Token: strings.Repeat("a", 64)}
	if err = s.RegisterOwnedInferenceExecutor(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RecoverOwnedGPUExecutorPermits(ctx, foreign); err != nil || n != 0 {
		t.Fatalf("foreign owner recovered work: %d %v", n, err)
	}
	assertMiningSettled(false)
	next := owner
	next.Generation = 2
	next.Token = strings.Repeat("1", 64)
	if err = s.RegisterOwnedInferenceExecutor(ctx, next); err != nil {
		t.Fatal(err)
	}
	assertMiningSettled(true)
	if started, err := s.StartGPUExecutorPermit(ctx, input); started || !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("old generation started after ownership advance: %v %v", started, err)
	}
	if n, err := s.RecoverOwnedGPUExecutorPermits(ctx, owner); !errors.Is(err, store.ErrGPUWorkConflict) || n != 0 {
		t.Fatalf("stale owner recovery: %d %v", n, err)
	}
	before, err := s.GPUResource(ctx, domain.GPUPrimaryResource)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.RecoverOwnedGPUExecutorPermits(ctx, next); err != nil || n != 1 {
		t.Fatalf("owned recovery: %d %v", n, err)
	}
	permit, err = s.GPUExecutorPermit(ctx, input)
	if err != nil || permit.Active() || permit.CompletionCode != "owner_stopped" {
		t.Fatalf("recovery result: %+v %v", permit, err)
	}
	if _, err = s.ObserveGPUResource(ctx, before.ID, before.Revision, "idle", "late old observation", time.Second); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("recovery did not invalidate observation: %v", err)
	}
	if n, err := s.RecoverOwnedGPUExecutorPermits(ctx, next); err != nil || n != 0 {
		t.Fatalf("repeat recovery: %d %v", n, err)
	}
	if permit, err = s.ReserveGPUExecutorPermit(ctx, input); err != nil || permit.Active() {
		t.Fatalf("old identity reopened: %+v %v", permit, err)
	}
	if started, err := s.StartGPUExecutorPermit(ctx, input); err != nil || started {
		t.Fatalf("closed identity restarted: %v %v", started, err)
	}
	observe()
	late := input
	late.ID = strings.Repeat("2", 32)
	if _, err = s.ReserveGPUExecutorPermit(ctx, late); !errors.Is(err, store.ErrGPUWorkConflict) {
		t.Fatalf("late old reservation accepted: %v", err)
	}
	current := late
	current.OwnerGeneration = next.Generation
	current.OwnerToken = next.Token
	if _, err = s.ReserveGPUExecutorPermit(ctx, current); err != nil {
		t.Fatal(err)
	}
	if started, err := s.StartGPUExecutorPermit(ctx, current); err != nil || !started {
		t.Fatalf("current start: %v %v", started, err)
	}
	if n, err := s.RecoverOwnedGPUExecutorPermits(ctx, next); err != nil || n != 0 {
		t.Fatalf("live current generation recovered: %d %v", n, err)
	}
	if err = s.FinishGPUExecutorPermit(ctx, current, "quiesced"); err != nil {
		t.Fatal(err)
	}
	miningLease.ExecutorPermitID = current.ID
	miningLease.ExecutorGeneration = current.OwnerGeneration
	assertMiningSettled(true)
	observe()
	comfy := domain.GPUExecutorPermitRequest{ID: strings.Repeat("3", 32), InstanceID: strings.Repeat("4", 32), Token: strings.Repeat("5", 64)}
	if _, err = s.ReserveGPUExecutorPermit(ctx, comfy); err != nil {
		t.Fatal(err)
	}
	next.Generation++
	if err = s.RegisterOwnedInferenceExecutor(ctx, next); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RecoverOwnedGPUExecutorPermits(ctx, next); err != nil || n != 0 {
		t.Fatalf("ComfyUI recovered by Ollama owner: %d %v", n, err)
	}
	permit, err = s.GPUExecutorPermit(ctx, comfy)
	if err != nil || !permit.Active() || permit.OwnerID != "" {
		t.Fatalf("unowned ComfyUI changed: %+v %v", permit, err)
	}
}
