package loratraining

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"ai-access-gateway/internal/componentinspection"
)

// InspectGenerationComponents schedules only bounded filesystem reads. Hashing
// runs on the agent, not inside the Gateway dispatch request deadline.
func (client *Client) InspectGenerationComponents(ctx context.Context, request componentinspection.Request) (componentinspection.Response, error) {
	var response componentinspection.Response
	if !client.Configured() {
		return response, ErrUnavailable
	}
	if !request.Valid() {
		return response, errors.New("invalid generation component inspection")
	}
	data, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	httpClient := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint("/v1/generation/components/inspect"), bytes.NewReader(data))
	if err != nil {
		return response, err
	}
	client.authorize(httpRequest)
	httpRequest.Header.Set("Content-Type", "application/json")
	reply, err := httpClient.Do(httpRequest)
	if err != nil {
		return response, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer reply.Body.Close()
	if reply.StatusCode != http.StatusOK {
		return response, decodeHTTPError(reply)
	}
	const limit = 128 << 10
	payload, err := io.ReadAll(io.LimitReader(reply.Body, limit+1))
	if err != nil || len(payload) > limit {
		return response, errors.New("invalid component inspection response size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var trailing any
	if decoder.Decode(&response) != nil || decoder.Decode(&trailing) != io.EOF {
		return componentinspection.Response{}, errors.New("invalid component inspection JSON")
	}
	if !response.ValidFor(request) {
		return componentinspection.Response{}, errors.New("mismatched generation component inspection")
	}
	now := time.Now()
	for _, item := range response.Items {
		if now.Sub(item.CheckedAt) > time.Minute || item.State == "verified" && item.CheckedAt.Sub(item.HashedAt) > 11*time.Minute {
			return componentinspection.Response{}, errors.New("stale component inspection")
		}
	}
	return response, nil
}
