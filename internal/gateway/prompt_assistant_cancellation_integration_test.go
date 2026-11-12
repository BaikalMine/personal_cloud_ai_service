package gateway

import (
	"context"
	"testing"
)

func TestDurableAssistantCancellationSignalsExecutorImmediately(t *testing.T) {
	f := newDurableAssistantFixture(t, nil)
	job := f.step(t)
	if job.InferenceWorkID == "" {
		t.Fatal("assistant did not dispatch inference")
	}
	ctx := context.Background()
	claimed, err := f.app.store.ClaimGPUInference(ctx, "cancel-running-assistant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.app.store.CancelPromptAssistantSubmission(ctx, f.user.ID+1, job.ID); err == nil {
		t.Fatal("foreign cancellation accepted")
	}
	if cancelled, err := f.app.store.GPUInferenceCancellation(ctx, claimed.ID, claimed.RunToken); err != nil || cancelled {
		t.Fatal("foreign cancellation reached executor", cancelled, err)
	}
	if _, err = f.app.store.CancelPromptAssistantSubmission(ctx, f.user.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	// No maintenance tick between the HTTP-side store call and executor read.
	if cancelled, err := f.app.store.GPUInferenceCancellation(ctx, claimed.ID, claimed.RunToken); err != nil || !cancelled {
		t.Fatal("executor was not signalled", cancelled, err)
	}
	work, err := f.app.store.GPUWork(ctx, job.InferenceWorkID)
	if err != nil || !work.CancellationRequested {
		t.Fatal("GPU cancellation not recorded", err)
	}
	if work.ReleasedAt != nil {
		t.Fatal("GPU released before physical stop")
	}
}
