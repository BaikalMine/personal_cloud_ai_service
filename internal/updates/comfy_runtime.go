package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

type ComfyRuntimeRequest struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	Version   string    `json:"version"`
	Force     bool      `json:"force"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (r ComfyRuntimeRequest) Valid() bool {
	return ComfyStorageHex(r.ID, 16) && (r.Action == "start" || r.Action == "stop" || r.Action == "restart") && (r.Version == "stopped" || ComfyStorageHex(r.Version, 32))
}

type ComfyRuntimeReceipt struct {
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	State      string    `json:"state"`
	Code       string    `json:"code"`
	FinishedAt time.Time `json:"finished_at"`
}

type ComfyRuntimeExit struct {
	At        time.Time `json:"at"`
	Code      uint32    `json:"code"`
	Requested bool      `json:"requested"`
}

type ComfyRuntimeStatus struct {
	CommandUntil time.Time            `json:"command_until"`
	Error        string               `json:"error,omitempty"`
	Available    bool                 `json:"available"`
	Running      bool                 `json:"running"`
	Ready        bool                 `json:"ready"`
	PID          int                  `json:"pid"`
	Version      string               `json:"version"`
	State        string               `json:"state"`
	LastExit     *ComfyRuntimeExit    `json:"last_exit,omitempty"`
	Receipt      *ComfyRuntimeReceipt `json:"receipt,omitempty"`
}

var ErrComfyRuntimeNotFound = errors.New("runtime receipt not found")

func (c *Client) ComfyRuntime(ctx context.Context, command *ComfyRuntimeRequest, id string) (ComfyRuntimeStatus, error) {
	var result ComfyRuntimeStatus
	if !c.Configured() {
		return result, ErrUnavailable
	}
	target := *c.baseURL
	target.Path, target.RawQuery, target.Fragment = "/v1/comfy-runtime", "", ""
	if id != "" {
		if !ComfyStorageHex(id, 16) {
			return result, ErrUnavailable
		}
		target.RawQuery = "id=" + id
	}
	var body io.Reader
	method := http.MethodGet
	if command != nil {
		payload, err := json.Marshal(command)
		if err != nil {
			return result, err
		}
		method, body = http.MethodPost, bytes.NewReader(payload)
	}
	r, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return result, err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(r)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<10))
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return result, ErrUnavailable
	}
	if response.StatusCode == http.StatusNotFound {
		return result, ErrComfyRuntimeNotFound
	}
	if response.StatusCode != http.StatusOK {
		return result, errors.New("ComfyUI runtime command was not confirmed")
	}
	return result, nil
}
