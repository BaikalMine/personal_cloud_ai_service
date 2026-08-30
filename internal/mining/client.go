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

type UpdateRequest struct {
	ScriptPath    string `json:"script_path"`
	ProcessName   string `json:"process_name"`
	MinerName     string `json:"miner_name,omitempty"`
	ArchiveURL    string `json:"archive_url"`
	ArchiveSHA256 string `json:"archive_sha256"`
}

type UpdateResult struct {
	Success          bool   `json:"success"`
	ArchiveName      string `json:"archive_name,omitempty"`
	InstalledPath    string `json:"installed_path,omitempty"`
	BackupPath       string `json:"backup_path,omitempty"`
	PreservedScripts int    `json:"preserved_scripts,omitempty"`
	Restarted        bool   `json:"restarted,omitempty"`
	Message          string `json:"message,omitempty"`
}

type Script struct {
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Message string `json:"message,omitempty"`
}

// SystemMetrics is a read-only snapshot of the Windows host. It deliberately
// contains only aggregate utilization data: no processes, paths, or commands.
type SystemMetrics struct {
	CollectedAt         time.Time `json:"collected_at"`
	CPUPercent          float64   `json:"cpu_percent"`
	MemoryUsedBytes     int64     `json:"memory_used_bytes"`
	MemoryTotalBytes    int64     `json:"memory_total_bytes"`
	GPUAvailable        bool      `json:"gpu_available"`
	GPUName             string    `json:"gpu_name,omitempty"`
	GPUPercent          float64   `json:"gpu_percent"`
	GPUMemoryUsedBytes  int64     `json:"gpu_memory_used_bytes"`
	GPUMemoryTotalBytes int64     `json:"gpu_memory_total_bytes"`
	Message             string    `json:"message,omitempty"`
}

// ComfyMemoryTrim reports a best-effort reduction of ComfyUI's idle Windows
// working set. The monitor owns process discovery; callers cannot supply a PID.
type ComfyMemoryTrim struct {
	Trimmed int    `json:"trimmed"`
	Message string `json:"message,omitempty"`
}

type Client struct {
	baseURL    *url.URL
	token      string
	http       *http.Client
	updateHTTP *http.Client
}

func NewClient(baseURL *url.URL, token string) *Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
	}
	return &Client{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		http: &http.Client{
			Timeout:   5 * time.Second,
			Transport: transport,
		},
		updateHTTP: &http.Client{Timeout: 35 * time.Minute, Transport: transport},
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

func (c *Client) System(ctx context.Context) (SystemMetrics, error) {
	if !c.Configured() {
		return SystemMetrics{Message: "Windows-агент недоступен."}, ErrUnavailable
	}
	target := c.resolve("/v1/system")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return SystemMetrics{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return SystemMetrics{Message: "Windows-агент недоступен."}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	var metrics SystemMetrics
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&metrics); err != nil {
		return SystemMetrics{}, fmt.Errorf("decode mining system response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if metrics.Message == "" {
			metrics.Message = "Windows-агент не отдал метрики."
		}
		return metrics, fmt.Errorf("mining agent returned %s", response.Status)
	}
	return metrics, nil
}

func (c *Client) TrimComfyMemory(ctx context.Context) (ComfyMemoryTrim, error) {
	if !c.Configured() {
		return ComfyMemoryTrim{Message: "Windows-агент недоступен."}, ErrUnavailable
	}
	target := c.resolve("/v1/comfyui/trim")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), nil)
	if err != nil {
		return ComfyMemoryTrim{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return ComfyMemoryTrim{Message: "Windows-агент недоступен."}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return ComfyMemoryTrim{}, fmt.Errorf("read ComfyUI memory trim response: %w", err)
	}
	var result ComfyMemoryTrim
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_ = json.Unmarshal(body, &result)
		if result.Message == "" {
			result.Message = "Windows-агент пока не поддерживает очистку памяти ComfyUI."
		}
		return result, fmt.Errorf("system monitor returned %s", response.Status)
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return ComfyMemoryTrim{}, fmt.Errorf("decode ComfyUI memory trim response: %w", err)
	}
	return result, nil
}

func (c *Client) Start(ctx context.Context, request Request) (State, error) {
	return c.command(ctx, "/v1/start", request)
}

func (c *Client) Stop(ctx context.Context, request Request) (State, error) {
	return c.command(ctx, "/v1/stop", request)
}

func (c *Client) Update(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	if !c.Configured() {
		return UpdateResult{Message: "Агент управления майнингом не настроен."}, ErrUnavailable
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return UpdateResult{}, err
	}
	target := c.resolve("/v1/update")
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return UpdateResult{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.token)
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := c.updateHTTP.Do(httpRequest)
	if err != nil {
		return UpdateResult{Message: "Windows-agent недоступен."}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	var result UpdateResult
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&result); err != nil {
		return UpdateResult{}, fmt.Errorf("decode mining update response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if result.Message == "" {
			result.Message = "Windows-agent отклонил обновление майнера."
		}
		return result, fmt.Errorf("mining agent returned %s", response.Status)
	}
	return result, nil
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
