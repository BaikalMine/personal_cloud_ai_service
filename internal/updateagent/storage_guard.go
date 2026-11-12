package updateagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"ai-access-gateway/internal/updates"
)

var ErrComfyStorageBusy = updates.ErrComfyStorageBusy
var ErrComfyStorageIdentity = errors.New("ComfyUI storage identity changed")
var ErrComfyStorageGuard = errors.New("ComfyUI storage window is unconfirmed")
var ErrComfyStorageConflict = updates.ErrComfyStorageConflict

type comfyStorageGuard struct {
	endpoint *url.URL
	token    string
	client   *http.Client
}

type comfyStorageGuardState struct {
	Protocol        int    `json:"protocol"`
	Enforced        bool   `json:"enforced"`
	Instance        string `json:"instance_id"`
	StorageProtocol int    `json:"storage_protocol"`
	JournalID       string `json:"storage_journal_id"`
	Held            bool   `json:"storage_held"`
}

type comfyStorageWindow struct {
	Protocol    int    `json:"protocol"`
	ID          string `json:"id"`
	RequestHash string `json:"request_hash"`
	ExecutorID  string `json:"executor_id"`
	JournalID   string `json:"journal_id"`
	State       string `json:"state"`
}

func newComfyStorageGuard(config ComfyStorageConfig) *comfyStorageGuard {
	endpoint, _ := url.Parse(config.GuardURL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &comfyStorageGuard{endpoint: endpoint, token: config.GuardToken,
		client: &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (g *comfyStorageGuard) status(ctx context.Context) (comfyStorageGuardState, error) {
	endpoint := *g.endpoint
	endpoint.Path = "/ai-gateway/gpu/status"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return comfyStorageGuardState{}, ErrComfyStorageGuard
	}
	var state comfyStorageGuardState
	if err := g.do(request, &state); err != nil {
		return state, err
	}
	if state.Protocol != 1 || !state.Enforced || state.StorageProtocol != 1 || !updates.ComfyStorageHex(state.Instance, 16) || !updates.ComfyStorageHex(state.JournalID, 16) {
		return comfyStorageGuardState{}, ErrComfyStorageGuard
	}
	return state, nil
}

func (g *comfyStorageGuard) window(ctx context.Context, action string, result updates.ComfyStorageResult) (string, error) {
	payload := struct {
		Protocol       int    `json:"protocol"`
		Action         string `json:"action"`
		ID             string `json:"id"`
		RequestHash    string `json:"request_hash"`
		ExecutorID     string `json:"executor_id"`
		JournalID      string `json:"journal_id"`
		ExecutorFenced bool   `json:"executor_fenced,omitempty"`
	}{Protocol: 1, Action: action, ID: result.ID, RequestHash: result.RequestHash, ExecutorID: result.ExecutorID, JournalID: result.GuardJournalID}
	if action == "close" {
		if !result.ExecutorFenced {
			return "", ErrComfyStorageGuard
		}
		payload.ExecutorFenced = true
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", ErrComfyStorageGuard
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return "", ErrComfyStorageGuard
	}
	request.Header.Set("Content-Type", "application/json")
	var window comfyStorageWindow
	if err := g.do(request, &window); err != nil {
		return "", err
	}
	if window.Protocol != 1 || window.ID != result.ID || window.RequestHash != result.RequestHash || window.ExecutorID != result.ExecutorID || window.JournalID != result.GuardJournalID {
		return "", ErrComfyStorageGuard
	}
	switch window.State {
	case "held", "closed", "unknown":
		return window.State, nil
	default:
		return "", ErrComfyStorageGuard
	}
}

func (g *comfyStorageGuard) do(request *http.Request, target any) error {
	request.Header.Set("x-ai-gateway-gpu-key", g.token)
	request.Header.Set("Accept", "application/json")
	response, err := g.client.Do(request)
	if err != nil {
		return ErrComfyStorageGuard
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 || response.StatusCode != http.StatusOK {
		// A rejected or ambiguous hold is still closed by ID before admission can
		// be released; do not treat an HTTP error as proof it was never received.
		return ErrComfyStorageGuard
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrComfyStorageGuard
	}
	return nil
}
