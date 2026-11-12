package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/buildinfo"
)

type readiness struct {
	Service              string `json:"service"`
	Protocol             int    `json:"protocol"`
	Ready                bool   `json:"ready"`
	Owned                bool   `json:"owned"`
	OllamaAdmission      bool   `json:"ollama_admission"`
	AssistantModelAccess int    `json:"assistant_model_access"`
}

// Readiness confirms the worker loop and storage, never completion of model work.
func (w *Worker) ReadinessHandler(storageProbe func(context.Context) error) http.Handler {
	type probeResult struct {
		done chan struct{}
		err  error
	}
	var mu sync.Mutex
	var flight *probeResult
	probe := func(ctx context.Context) bool {
		mu.Lock()
		result := flight
		if result == nil {
			result = &probeResult{done: make(chan struct{})}
			flight = result
			// Some drivers block inside cancellation. Share one in-flight probe,
			// but let each HTTP request finish within its own deadline.
			go func() {
				result.err = storageProbe(ctx)
				if ctx.Err() != nil {
					result.err = ctx.Err()
				}
				mu.Lock()
				flight = nil
				close(result.done)
				mu.Unlock()
			}()
		}
		mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-result.done:
			return result.err == nil && ctx.Err() == nil
		}
	}
	build := buildinfo.Handler(buildinfo.InferenceWorker)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/build" {
			build.ServeHTTP(response, request)
			return
		}
		if request.URL.Path != "/healthz" {
			http.NotFound(response, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", "GET, HEAD")
			response.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		ready := w.journal != nil && w.running.Load() && !w.journalBlocked.Load() && w.ownedReady() && storageProbe != nil && probe(ctx) && w.running.Load() && !w.journalBlocked.Load() && w.ownedReady()
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Type", "application/json")
		if !ready {
			response.WriteHeader(http.StatusServiceUnavailable)
		}
		if request.Method != http.MethodHead {
			_ = json.NewEncoder(response).Encode(readiness{Service: "inference-worker", Protocol: 2, Ready: ready, Owned: w.owned != nil, OllamaAdmission: w.ollamaAdmission.Load(), AssistantModelAccess: 1})
		}
	})
}

// ProbeReadiness rejects generic HTTP 200 pages and incompatible executors.
func ProbeReadiness(parent context.Context, base *url.URL) error {
	return probeReadiness(parent, base, false, false)
}

func ProbeOwnedOllamaReadiness(parent context.Context, base *url.URL) error {
	return probeReadiness(parent, base, true, false)
}

func ProbeAssistantModelReadiness(parent context.Context, base *url.URL) error {
	return probeReadiness(parent, base, false, true)
}

func probeReadiness(parent context.Context, base *url.URL, requireOllama, requireModelAccess bool) error {
	if base == nil || base.Host == "" || base.User != nil || (base.Scheme != "http" && base.Scheme != "https") {
		return errors.New("executor address is not configured")
	}
	target := *base
	target.Path = strings.TrimRight(target.Path, "/") + "/healthz"
	target.RawQuery, target.Fragment = "", ""
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("executor did not respond")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("executor is not ready")
	}
	var state readiness
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return errors.New("executor readiness response is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&state) != nil || state.Service != "inference-worker" || state.Protocol != 2 || !state.Ready {
		return errors.New("executor readiness response is invalid")
	}
	if requireOllama && (!state.Owned || !state.OllamaAdmission) {
		return errors.New("executor does not enforce owned Ollama admission")
	}
	if requireModelAccess && state.AssistantModelAccess != 1 {
		return errors.New("executor does not enforce assistant model access")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("executor readiness response is invalid")
	}
	return nil
}
