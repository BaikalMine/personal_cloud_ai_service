package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

type ModelVerifyRequest struct {
	Root         string `json:"root"`
	RelativePath string `json:"relative_path"`
	SHA256       string `json:"sha256"`
}

type ModelVerification struct {
	Protocol int    `json:"protocol"`
	State    string `json:"state"`
	Size     int64  `json:"size_bytes"`
}

func (c *Client) VerifyModel(ctx context.Context, in ModelVerifyRequest) (ModelVerification, error) {
	var out ModelVerification
	if !c.Configured() {
		return out, ErrUnavailable
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	target := *c.baseURL
	target.Path, target.RawQuery, target.Fragment = "/v1/model-verify", "", ""
	r, err := http.NewRequestWithContext(ctx, "POST", target.String(), bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Content-Type", "application/json")
	client := *c.install
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(r)
	if err != nil {
		return out, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&out) != nil || out.Protocol != 1 {
		return out, ErrUnavailable
	}
	switch out.State {
	case "verified", "missing", "incomplete", "mismatch", "changed":
		return out, nil
	default:
		return ModelVerification{}, ErrUnavailable
	}
}
