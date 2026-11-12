//go:build windows

package modelruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/processowner"
)

var ErrBusy = errors.New("owned model runtime is busy")

type Runtime struct {
	mu           sync.Mutex
	group        *processowner.Group
	config       Config
	command      func() *exec.Cmd
	lifetime     context.Context
	cancel       context.CancelFunc
	identity     string
	retainedDone chan error
}

func New(config Config) (*Runtime, error) {
	if _, err := config.OllamaEnvironment(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(config.Executable) || !strings.EqualFold(filepath.Ext(config.Executable), ".exe") ||
		!filepath.IsAbs(config.ModelsDirectory) || !filepath.IsAbs(config.StateDirectory) {
		return nil, errors.New("model runtime requires absolute executable, models and state paths")
	}
	info, err := os.Stat(config.Executable)
	if err != nil || info.IsDir() {
		return nil, errors.New("model executable is unavailable")
	}
	info, err = os.Stat(config.ModelsDirectory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("model directory is unavailable")
	}
	host, port, err := net.SplitHostPort(config.Address)
	number, portErr := strconv.Atoi(port)
	if err != nil || host != "127.0.0.1" || portErr != nil || number < 1024 || number > 65535 {
		return nil, errors.New("model runtime requires a dedicated IPv4 loopback port")
	}
	if config.StartupTimeout == 0 {
		config.StartupTimeout = 30 * time.Second
	}
	if config.StopTimeout == 0 {
		config.StopTimeout = 10 * time.Second
	}
	if config.StartupTimeout < time.Millisecond || config.StartupTimeout > time.Minute || config.StopTimeout < time.Millisecond || config.StopTimeout > time.Minute {
		return nil, errors.New("invalid model runtime timeouts")
	}
	group, err := processowner.Open(filepath.Join(config.StateDirectory, "model-process-owner.lock"), "Model")
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	r := &Runtime{config: config, group: group, lifetime: lifetime, cancel: cancel}
	r.identity, err = runtimeIdentity(config.StateDirectory)
	if err != nil {
		r.Close()
		return nil, err
	}
	r.command = func() *exec.Cmd {
		command := exec.Command(config.Executable, "serve")
		command.Dir = filepath.Dir(config.Executable)
		command.Env = append(os.Environ(),
			"OLLAMA_HOST="+config.Address, "OLLAMA_MODELS="+config.ModelsDirectory,
			"OLLAMA_KEEP_ALIVE=0", "OLLAMA_NUM_PARALLEL=1", "OLLAMA_MAX_LOADED_MODELS=1", "OLLAMA_NO_CLOUD=1")
		return command
	}
	return r, nil
}

func (r *Runtime) Close() error {
	r.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.group.Close()
}

// The caller must hold the global GPU permit for the entire call. The result's
// Stopped flag is physical process-tree evidence, not a model-response status.
func (r *Runtime) Execute(parent context.Context, body []byte) (result Result, err error) {
	var responseBody []byte
	result, err = r.executeHTTP(parent, HTTPRequest{Method: http.MethodPost, Path: "/api/chat", Body: body, ResponseLimit: domain.MaxGPUInferenceResponseBytes}, func(_ int, _ http.Header, reader io.Reader) error {
		var err error
		responseBody, err = io.ReadAll(reader)
		return err
	}, true)
	if result.Stopped && !errors.Is(err, ErrHTTPResponseLimit) {
		result.Body = responseBody
	} else {
		clear(responseBody)
	}
	return
}

// ExecuteHTTP preserves the native model protocol, including NDJSON/SSE frames,
// while retaining the same process ownership and cancellation proof as Execute.
func (r *Runtime) ExecuteHTTP(parent context.Context, request HTTPRequest, consume HTTPConsumer) (Result, error) {
	return r.executeHTTP(parent, request, consume, false)
}

func (r *Runtime) executeHTTP(parent context.Context, input HTTPRequest, consume HTTPConsumer, nonStreamingChat bool) (result Result, err error) {
	if !r.mu.TryLock() {
		return result, ErrBusy
	}
	defer r.mu.Unlock()
	if err = r.lifetime.Err(); err != nil {
		return result, err
	}
	if err = validateHTTPRequest(input, consume); err != nil {
		return result, err
	}
	if nonStreamingChat {
		var payload struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if json.Unmarshal(input.Body, &payload) != nil || strings.TrimSpace(payload.Model) == "" || payload.Stream {
			return result, errors.New("invalid non-streaming local model request")
		}
	}
	ctx, cancel := context.WithCancel(parent)
	stopLifetime := context.AfterFunc(r.lifetime, cancel)
	defer func() { stopLifetime(); cancel() }()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if r.retainedDone != nil && !input.Retain {
		return result, ErrBusy
	}
	processDone := r.retainedDone
	if processDone == nil {
		// Reject a foreign listener before starting, never contact its owner.
		reservation, reserveErr := net.Listen("tcp4", r.config.Address)
		if reserveErr != nil {
			return result, fmt.Errorf("model port is unavailable: %w", reserveErr)
		}
		reservation.Close()
	}
	completed := false
	defer func() {
		if completed && err == nil && ctx.Err() == nil && input.Retain && result.Status >= 200 && result.Status < 300 {
			r.retainedDone = processDone
			result.Retained = true
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), r.config.StopTimeout)
		stopped, stopErr := r.stopHTTPLocked(stopCtx, processDone)
		result.Stopped = stopped
		stopCancel()
		if !result.Stopped {
			clear(result.Body)
			result.Body = nil
		}
		err = errors.Join(err, stopErr)
	}()
	modelEnvironment, settingsErr := r.config.OllamaEnvironmentFor(input.Path)
	if settingsErr != nil {
		return result, settingsErr
	}
	if processDone == nil {
		command := r.command()
		command.Env = append(command.Env, modelEnvironment...)
		processContext := ctx
		if input.Retain {
			command.Env = append(command.Env, "OLLAMA_KEEP_ALIVE=-1")
			processContext = r.lifetime
		}
		process, startErr := r.group.Start(processContext, command, false)
		if startErr != nil {
			return result, startErr
		}
		processDone = make(chan error, 1)
		go func() { processDone <- process.Wait() }()
	}
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != r.config.Address {
			return nil, processowner.ErrUnownedPeer
		}
		connection, err := dialer.DialContext(ctx, "tcp4", address)
		if err != nil {
			return nil, err
		}
		if err = r.group.ConfirmPeer(ctx, connection); err != nil {
			connection.Close()
			return nil, err
		}
		return connection, nil
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	startup, startupCancel := context.WithTimeout(ctx, r.config.StartupTimeout)
	err = r.waitReady(startup, client)
	startupCancel()
	if err != nil {
		return result, err
	}
	request, err := http.NewRequestWithContext(ctx, input.Method, "http://"+r.config.Address+input.Path, bytes.NewReader(input.Body))
	if err != nil {
		return result, err
	}
	request.Header.Set("Content-Type", "application/json")
	result.Dispatched = true
	response, err := client.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	err = consumeHTTPResponse(response, input.ResponseLimit, consume)
	completed = true
	return result, err
}

func (r *Runtime) stopHTTPLocked(ctx context.Context, done chan error) (bool, error) {
	err := r.group.Stop(ctx)
	if err != nil {
		return false, err
	}
	var waitErr error
	if done != nil {
		select {
		case waitErr = <-done:
		case <-ctx.Done():
			return false, errors.New("model launcher handle did not signal")
		}
	}
	r.retainedDone = nil
	return true, waitErr
}

// The task owner cancels in-flight HTTP before asking for this physical proof.
func (r *Runtime) StopRetained(ctx context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopHTTPLocked(ctx, r.retainedDone)
}

func (r *Runtime) waitReady(ctx context.Context, client *http.Client) error {
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+r.config.Address+"/api/version", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 8193))
			response.Body.Close()
			var version struct {
				Version string `json:"version"`
			}
			if readErr == nil && response.StatusCode == 200 && len(body) <= 8192 && json.Unmarshal(body, &version) == nil && version.Version != "" {
				return nil
			}
		}
		if errors.Is(err, processowner.ErrUnownedPeer) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
