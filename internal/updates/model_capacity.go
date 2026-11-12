package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

type ModelCapacity struct {
	Protocol  int    `json:"protocol"`
	FreeBytes uint64 `json:"free_bytes"`
}

func (c *Client) ModelCapacity(ctx context.Context, root string) (ModelCapacity, error) {
	var out ModelCapacity
	if !c.Configured() {
		return out, ErrUnavailable
	}
	raw, _ := json.Marshal(map[string]string{"root": root})
	target := *c.baseURL
	target.Path = "/v1/model-capacity"
	target.RawQuery = ""
	target.Fragment = ""
	r, err := http.NewRequestWithContext(ctx, "POST", target.String(), bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return out, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&out) != nil || out.Protocol != 1 {
		return out, ErrUnavailable
	}
	return out, nil
}
