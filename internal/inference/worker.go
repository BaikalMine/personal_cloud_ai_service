package inference

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

type Worker struct {
	store           *store.Store
	cipher          *contentcrypto.Cipher
	target          string
	http            *http.Client
	running         atomic.Bool
	journal         *outcomeJournal
	journalBlocked  atomic.Bool
	runMu           sync.Mutex
	owned           *ownedExecution
	ollamaAdmission atomic.Bool
}

func NewWorker(repository *store.Store, cipher *contentcrypto.Cipher, upstream *url.URL) (*Worker, error) {
	if repository == nil || cipher == nil || upstream == nil || upstream.Host == "" || (upstream.Scheme != "http" && upstream.Scheme != "https") || upstream.User != nil {
		return nil, errors.New("inference worker requires a store, cipher and HTTP model endpoint")
	}
	target := *upstream
	target.Path = strings.TrimRight(target.Path, "/") + "/api/chat"
	target.RawQuery = ""
	target.Fragment = ""
	return &Worker{store: repository, cipher: cipher, target: target.String(), http: &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second},
		// A redirect may replay a POST to a second executor without a durable ID.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// NewDurableWorker retains observed completions across executor process restarts.
// The directory must be persistent and dedicated to this database/secret pair.
func NewDurableWorker(repository *store.Store, cipher *contentcrypto.Cipher, upstream *url.URL, directory string) (*Worker, error) {
	w, err := NewWorker(repository, cipher, upstream)
	if err != nil {
		return nil, err
	}
	w.journal, err = openOutcomeJournal(directory, cipher)
	if err != nil {
		w.Close()
		return nil, err
	}
	w.journalBlocked.Store(true)
	return w, nil
}

func (w *Worker) Close() {
	if w.owned != nil {
		w.owned.runtime.Close()
	}
	if w.http != nil {
		w.http.CloseIdleConnections()
	}
	if w.journal != nil {
		w.journal.root.Close()
	}
}

func (w *Worker) Run(ctx context.Context) error {
	defer w.running.Store(false)
	lastCleanup := time.Time{}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(lastCleanup) >= time.Minute {
			if _, err := w.store.RedactExpiredGPUInference(ctx); err != nil {
				return err
			}
			lastCleanup = time.Now()
		}
		w.running.Store(true)
		worked, err := w.RunOne(ctx)
		if err != nil {
			return err
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (w *Worker) RunOne(ctx context.Context) (bool, error) {
	if !w.runMu.TryLock() {
		return false, nil
	}
	defer w.runMu.Unlock()
	if err := w.prepareExecution(ctx); err != nil {
		return false, err
	}
	return w.runOnePrepared(ctx)
}

// Callers hold runMu through preparation, dispatch and persistence. The external
// Ollama API uses the same owner and cannot advance it while managed work runs.
func (w *Worker) prepareExecution(ctx context.Context) error {
	if w.journal != nil {
		if err := w.restoreOutcomes(ctx); err != nil {
			w.journalBlocked.Store(true)
			return err
		}
		if err := w.journal.checkWritable(); err != nil {
			w.journalBlocked.Store(true)
			return err
		}
		w.journalBlocked.Store(false)
	}
	if w.owned != nil {
		if err := w.prepareOwned(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) runOnePrepared(ctx context.Context) (bool, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return false, err
	}
	var job domain.GPUInference
	var err error
	if w.owned != nil {
		job, err = w.store.ClaimOwnedGPUInference(ctx, hex.EncodeToString(token[:]), w.owned.executor)
	} else {
		job, err = w.store.ClaimGPUInference(ctx, hex.EncodeToString(token[:]))
	}
	if err != nil || job.ID == "" {
		return false, err
	}
	if job.Settled() {
		return true, nil
	}
	defer clear(job.RequestCipher)
	status, body, code := w.executeCancellable(ctx, job)
	observedAt := time.Now().UTC()
	if job.StartedAt != nil && observedAt.Before(*job.StartedAt) {
		observedAt = *job.StartedAt
	}
	clear(job.RequestCipher)
	defer clear(body)
	var encrypted []byte
	if len(body) > 0 {
		encrypted, err = w.cipher.EncryptBytesWithAAD(body, responseAAD(job.ID))
		if err != nil {
			return true, err
		}
		defer clear(encrypted)
	}
	clear(body)
	outcome := observedOutcome{Version: 1, ID: job.ID, RunToken: job.RunToken, LeaseToken: job.LeaseToken,
		RequestHash: job.RequestHash, Code: code, Status: status, Response: encrypted, ObservedAt: observedAt}
	recorded := false
	// Never reissue the model request on persistence failure. Keep the observed
	// response and retry only its transaction while this executor is alive.
	for {
		var journalErr error
		if w.journal != nil && code != "" {
			if len(outcome.Response) > 0 && !outcome.ObservedAt.Add(24*time.Hour).After(time.Now()) {
				clear(outcome.Response)
				outcome.Response = nil
				recorded = false
			}
			if !recorded {
				journalErr = w.journal.save(outcome)
				recorded = journalErr == nil
			}
			w.journalBlocked.Store(journalErr != nil)
		}
		persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if w.journal != nil && code != "" {
			err = w.persistOutcome(persistCtx, outcome)
		} else {
			err = w.store.FinishGPUInference(persistCtx, job.ID, job.RunToken, code, status, encrypted)
		}
		cancel()
		if err == nil {
			if recorded {
				err = w.journal.remove(job.ID)
			}
			return true, err
		}
		if errors.Is(err, store.ErrGPUWorkConflict) || errors.Is(err, store.ErrGPUWorkInput) {
			return true, err
		}
		select {
		case <-ctx.Done():
			return true, errors.Join(err, journalErr, ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func (w *Worker) execute(parent context.Context, job domain.GPUInference) (int, []byte, string) {
	body, err := w.cipher.DecryptBytesWithAAD(job.RequestCipher, requestAAD(job.ID))
	if err != nil {
		return 0, nil, "request_not_dispatched"
	}
	defer clear(body)
	hash := sha256.Sum256(body)
	if hex.EncodeToString(hash[:]) != job.RequestHash {
		return 0, nil, "request_not_dispatched"
	}
	var payload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if len(body) > domain.MaxGPUInferenceRequestBytes || json.Unmarshal(body, &payload) != nil || strings.TrimSpace(payload.Model) == "" || payload.Stream {
		return 0, nil, "request_not_dispatched"
	}
	if parent.Err() != nil {
		return 0, nil, "request_not_dispatched"
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(job.TimeoutMS)*time.Millisecond)
	defer cancel()
	assistant, err := w.store.IsAssistantInference(ctx, job.ID)
	if err != nil {
		return 0, nil, "request_not_dispatched"
	}
	if assistant {
		return w.executeStreaming(ctx, job, body)
	}
	if w.owned != nil {
		return w.executeOwned(ctx, body)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, "request_not_dispatched"
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	// Do not advertise an idempotency header: net/http can use it to retry a
	// POST on a reused connection, but Ollama does not implement deduplication.
	response, err := w.http.Do(request)
	if err != nil {
		return 0, nil, ""
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, domain.MaxGPUInferenceResponseBytes+1))
	if err != nil || len(result) > domain.MaxGPUInferenceResponseBytes {
		clear(result)
		return response.StatusCode, nil, ""
	}
	var envelope struct {
		Done bool `json:"done"`
	}
	code := ""
	if response.StatusCode >= 200 && response.StatusCode < 300 && json.Unmarshal(result, &envelope) == nil && envelope.Done {
		code = "executor_terminal"
	}
	return response.StatusCode, result, code
}
