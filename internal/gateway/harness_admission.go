package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// Only the native worker's explicit pre-dispatch refusal is replayable. Neither
// transport failures nor a response from an executed model is safe to retry.
type harnessAdmissionTransport struct {
	base     http.RoundTripper
	wait     time.Duration
	interval time.Duration
}

func (t harnessAdmissionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	deadline := time.Now().Add(t.wait)
	for {
		response, err := t.base.RoundTrip(request)
		if err != nil || request.Method != http.MethodPost || request.GetBody == nil ||
			response.StatusCode != http.StatusConflict || !time.Now().Before(deadline) {
			return response, err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(body))
		var reply struct {
			Error struct {
				Code    string `json:"code"`
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if readErr != nil || len(body) > 4096 || json.Unmarshal(body, &reply) != nil ||
			reply.Error.Code != "gpu_busy" || reply.Error.Type != "gateway_error" || reply.Error.Message != "gpu_busy" {
			return response, readErr
		}
		pause := min(t.interval, time.Until(deadline))
		timer := time.NewTimer(pause)
		select {
		case <-request.Context().Done():
			timer.Stop()
			return nil, request.Context().Err()
		case <-timer.C:
		}
		if !time.Now().Before(deadline) {
			return response, nil
		}
		bodyReader, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		replay := request.Clone(request.Context())
		replay.Body = bodyReader
		request = replay
	}
}
