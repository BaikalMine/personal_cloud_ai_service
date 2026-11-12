package inference

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
	"ai-access-gateway/internal/store"
)

// Advance must issue a durable generation only after exclusively owning and
// draining this runtime's previous process tree. Health alone cannot implement it.
type OwnedModelRuntime interface {
	Advance(context.Context) (domain.GPUInferenceExecutor, error)
	Execute(context.Context, []byte) (modelruntime.Result, error)
	Close() error
}

type ownedExecution struct {
	runtime  OwnedModelRuntime
	executor domain.GPUInferenceExecutor
	ready    atomic.Bool
}

func NewOwnedWorker(repository *store.Store, cipher *contentcrypto.Cipher, runtime OwnedModelRuntime, directory string) (*Worker, error) {
	if repository == nil || cipher == nil || runtime == nil {
		return nil, errors.New("owned inference worker requires storage, cipher and native runtime")
	}
	journal, err := openOutcomeJournal(directory, cipher)
	if err != nil {
		return nil, err
	}
	w := &Worker{store: repository, cipher: cipher, journal: journal, owned: &ownedExecution{runtime: runtime}}
	w.journalBlocked.Store(true)
	return w, nil
}

func (w *Worker) prepareOwned(ctx context.Context) error {
	o := w.owned
	if o.ready.Load() {
		return nil
	}
	if o.executor.ID == "" {
		executor, err := o.runtime.Advance(ctx)
		if err != nil {
			return err
		}
		o.executor = executor
	}
	if err := w.store.RegisterOwnedInferenceExecutor(ctx, o.executor); err != nil {
		return err
	}
	for {
		recovered, err := w.store.RecoverOwnedGPUInference(ctx, o.executor)
		if err != nil {
			return err
		}
		if recovered == 0 {
			break
		}
	}
	if _, err := w.store.RecoverOwnedGPUExecutorPermits(ctx, o.executor); err != nil {
		return err
	}
	o.ready.Store(true)
	return nil
}

func (w *Worker) executeOwned(ctx context.Context, body []byte) (int, []byte, string) {
	result, err := w.owned.runtime.Execute(ctx, body)
	if !result.Stopped {
		w.invalidateOwned()
		clear(result.Body)
		return result.Status, nil, ""
	}
	if !result.Dispatched {
		clear(result.Body)
		return 0, nil, "request_not_dispatched"
	}
	var envelope struct {
		Done bool `json:"done"`
	}
	if err == nil && result.Status >= 200 && result.Status < 300 && len(result.Body) <= domain.MaxGPUInferenceResponseBytes && json.Unmarshal(result.Body, &envelope) == nil && envelope.Done {
		return result.Status, result.Body, "executor_terminal"
	}
	clear(result.Body)
	return 0, nil, "executor_stopped"
}

// Caller holds runMu. Advancing before the next dispatch fences late admission
// messages and recovers permits only after physical process-tree stop evidence.
func (w *Worker) invalidateOwned() {
	w.owned.ready.Store(false)
	w.owned.executor = domain.GPUInferenceExecutor{}
}

func (w *Worker) ownedReady() bool { return w.owned == nil || w.owned.ready.Load() }
