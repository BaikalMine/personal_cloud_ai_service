package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

const harnessAgentResponseLimit = 16 << 20

type harnessAgentFault struct {
	Status int
	Code   string
}

func (e *harnessAgentFault) Error() string { return e.Code }

type harnessAgentClient struct {
	app    *App
	cookie *http.Cookie
}

func (a *App) newHarnessAgentClient(ctx context.Context) (*harnessAgentClient, error) {
	if a.cfg.PublicURL == nil || a.cfg.Harness.BootstrapURL == nil {
		return nil, &harnessAgentFault{503, "harness_unavailable"}
	}
	cookie, err := a.harnessSession(ctx, a.cfg.PublicURL.Host)
	if err != nil {
		return nil, &harnessAgentFault{503, "harness_unavailable"}
	}
	return &harnessAgentClient{app: a, cookie: cookie}, nil
}

func (c *harnessAgentClient) request(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Request, error) {
	u := *c.app.cfg.Harness.Upstream
	u.Path, u.RawPath, u.RawQuery = path, "", query.Encode()
	r, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Host = c.app.cfg.PublicURL.Host
	r.Header.Set("Origin", c.app.cfg.PublicURL.Scheme+"://"+c.app.cfg.PublicURL.Host)
	r.Header.Set("Accept-Encoding", "identity")
	r.AddCookie(&http.Cookie{Name: c.cookie.Name, Value: c.cookie.Value})
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r, nil
}

func (c *harnessAgentClient) json(ctx context.Context, path string, body []byte, target any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := c.request(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return err
	}
	client := &http.Client{CheckRedirect: rejectUpstreamRedirect}
	response, err := client.Do(r)
	if err != nil {
		return &harnessAgentFault{502, "harness_outcome_unknown"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &harnessAgentFault{502, "harness_outcome_unknown"}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, harnessAgentResponseLimit+1))
	if err != nil || len(data) > harnessAgentResponseLimit {
		return &harnessAgentFault{502, "harness_response_unavailable"}
	}
	if json.Unmarshal(data, target) != nil {
		return &harnessAgentFault{502, "harness_response_unavailable"}
	}
	return nil
}

func (c *harnessAgentClient) rpc(ctx context.Context, method string, args any, target any) error {
	body, err := json.Marshal(map[string]any{"type": "client-request", "rpcId": newRequestID(), "method": method, "payload": map[string]any{"args": args}})
	if err != nil {
		return err
	}
	var response struct {
		Result struct {
			OK    bool            `json:"ok"`
			Value json.RawMessage `json:"value"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"result"`
	}
	if err := c.json(ctx, "/api/"+method, body, &response); err != nil {
		return err
	}
	if !response.Result.OK {
		fault := &harnessAgentFault{502, "harness_request_failed"}
		switch response.Result.Error.Code {
		case "session/not-found":
			fault.Status, fault.Code = 404, "task_not_found"
		case "gateway/bad-request", "session/attachment-invalid":
			fault.Status, fault.Code = 400, "invalid_harness_request"
		case "session/model-unavailable", "session/provider-models-unavailable":
			fault.Status, fault.Code = 409, "model_unavailable"
		case "session/agent-busy", "session/writer-held", "session/conflict":
			fault.Status, fault.Code = 409, "task_busy"
		}
		return fault
	}
	if target != nil && json.Unmarshal(response.Result.Value, target) != nil {
		return &harnessAgentFault{502, "harness_response_unavailable"}
	}
	return nil
}

func harnessAgentWriteError(w http.ResponseWriter, err error, id string) {
	fault := &harnessAgentFault{502, "harness_unavailable"}
	var typed *harnessAgentFault
	if errors.As(err, &typed) {
		fault = typed
	}
	writeJSON(w, fault.Status, map[string]any{"error": map[string]string{"code": fault.Code}, "task_id": id})
}

func harnessAgentArgs(request any) map[string]any { return map[string]any{"request": request} }
