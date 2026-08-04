package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20

var ErrUnavailable = errors.New("update agent is unavailable")

type Client struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

func NewClient(baseURL *url.URL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: 8 * time.Second},
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.baseURL != nil && len(c.token) >= 32
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	return c.do(ctx, http.MethodGet, "/v1/status", nil)
}

func (c *Client) Check(ctx context.Context, request Request) (Status, error) {
	return c.command(ctx, "/v1/check", request)
}

func (c *Client) Install(ctx context.Context, request Request) (Status, error) {
	return c.command(ctx, "/v1/install", request)
}

func (c *Client) command(ctx context.Context, path string, command Request) (Status, error) {
	payload, err := json.Marshal(command)
	if err != nil {
		return Status{}, err
	}
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(payload))
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader) (Status, error) {
	if !c.Configured() {
		return Status{Message: "Агент обновлений не настроен."}, ErrUnavailable
	}
	target := *c.baseURL
	target.Path = path
	target.RawQuery = ""
	target.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return Status{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Status{Message: "Агент обновлений недоступен."}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	var status Status
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&status); err != nil {
		return Status{}, fmt.Errorf("decode update agent response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if status.Message == "" {
			status.Message = "Агент обновлений отклонил команду."
		}
		return status, fmt.Errorf("update agent returned %s", response.Status)
	}
	status.Available = true
	return status, nil
}
