package mining

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

var ErrUnavailable = errors.New("mining agent is unavailable")

type State struct {
	Available   bool      `json:"available"`
	Running     bool      `json:"running"`
	PIDs        []int     `json:"pids,omitempty"`
	ScriptPath  string    `json:"script_path,omitempty"`
	ProcessName string    `json:"process_name,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	Message     string    `json:"message,omitempty"`
}

type Request struct {
	ScriptPath  string `json:"script_path"`
	ProcessName string `json:"process_name"`
}

type Script struct {
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Message string `json:"message,omitempty"`
}

type Client struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

func NewClient(baseURL *url.URL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		http: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          4,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   3 * time.Second,
				ResponseHeaderTimeout: 4 * time.Second,
			},
		},
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.baseURL != nil && c.token != ""
}

func (c *Client) State(ctx context.Context, processName string) (State, error) {
	if !c.Configured() {
		return State{Message: "Агент управления майнингом не настроен."}, ErrUnavailable
	}
	target := c.resolve("/v1/state")
	query := target.Query()
	query.Set("process_name", processName)
	target.RawQuery = query.Encode()
	return c.do(ctx, http.MethodGet, target, nil)
}

func (c *Client) Start(ctx context.Context, request Request) (State, error) {
	return c.command(ctx, "/v1/start", request)
}

func (c *Client) Stop(ctx context.Context, request Request) (State, error) {
	return c.command(ctx, "/v1/stop", request)
}

func (c *Client) Script(ctx context.Context, scriptPath string) (Script, error) {
	if !c.Configured() {
		return Script{Message: "Агент управления майнингом не настроен."}, ErrUnavailable
	}
	target := c.resolve("/v1/script")
	query := target.Query()
	query.Set("script_path", scriptPath)
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Script{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return Script{Message: "Windows-agent недоступен."}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	var script Script
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&script); err != nil {
		return Script{}, fmt.Errorf("decode mining script response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if script.Message == "" {
			script.Message = "Windows-agent не прочитал скрипт."
		}
		return script, fmt.Errorf("mining agent returned %s", response.Status)
	}
	return script, nil
}

func (c *Client) command(ctx context.Context, path string, command Request) (State, error) {
	if !c.Configured() {
		return State{Message: "Агент управления майнингом не настроен."}, ErrUnavailable
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return State{}, err
	}
	return c.do(ctx, http.MethodPost, c.resolve(path), bytes.NewReader(payload))
}

func (c *Client) do(ctx context.Context, method string, target *url.URL, body io.Reader) (State, error) {
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return State{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return State{Message: "Windows-agent недоступен."}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxResponseBytes)
	var state State
	if err := json.NewDecoder(limited).Decode(&state); err != nil {
		return State{}, fmt.Errorf("decode mining agent response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if state.Message == "" {
			state.Message = "Windows-agent отклонил команду."
		}
		return state, fmt.Errorf("mining agent returned %s", response.Status)
	}
	state.Available = true
	return state, nil
}

func (c *Client) resolve(path string) *url.URL {
	target := *c.baseURL
	target.Path = path
	target.RawQuery = ""
	target.Fragment = ""
	return &target
}
