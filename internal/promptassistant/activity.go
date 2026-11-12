package promptassistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LoadedModels is a conservative admission probe. A loaded model may be idle,
// but it is not safe to infer idle GPU from that. This probe must never settle
// an unconfirmed request or release its resource lease.
func (c *Client) LoadedModels(ctx context.Context) (int, error) {
	if c == nil || c.baseURL == nil {
		return 0, errors.New("Ollama is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	endpoint := c.baseURL.JoinPath("api/ps")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Ollama activity HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return 0, errors.New("invalid Ollama activity response")
	}
	var result struct {
		Models *[]json.RawMessage `json:"models"`
	}
	if json.Unmarshal(body, &result) != nil || result.Models == nil {
		return 0, errors.New("Ollama activity omitted its model inventory")
	}
	return len(*result.Models), nil
}
