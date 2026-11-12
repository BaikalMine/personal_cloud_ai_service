package inference

import (
	"context"
	"time"

	"ai-access-gateway/internal/domain"
)

const executionCancellationInterval = 200 * time.Millisecond

func (w *Worker) executeCancellable(ctx context.Context, job domain.GPUInference) (int, []byte, string) {
	// The direct HTTP adapter cannot prove that Ollama stopped after a closed
	// connection. Keep that legacy path until an owned runtime is configured.
	if w.owned == nil {
		return w.execute(ctx, job)
	}
	cancelled, err := w.store.GPUInferenceCancellation(ctx, job.ID, job.RunToken)
	if err != nil || cancelled {
		return 0, nil, "request_not_dispatched"
	}
	execution, cancelExecution := context.WithCancel(ctx)
	defer cancelExecution()
	watch, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(executionCancellationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-watch.Done():
				return
			case <-ticker.C:
				probe, cancel := context.WithTimeout(watch, time.Second)
				requested, err := w.store.GPUInferenceCancellation(probe, job.ID, job.RunToken)
				cancel()
				if err == nil && requested {
					cancelExecution()
					return
				}
			}
		}
	}()
	defer func() { stop(); <-done }()
	return w.execute(execution, job)
}
