package updateagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ai-access-gateway/internal/updates"
)

const (
	commandTimeout = 10 * time.Minute
	healthTimeout  = 2 * time.Minute
)

type commandRunner interface {
	Run(context.Context, string, string, ...string) (string, error)
	Start(string, string, ...string) error
}

type execRunner struct{}

func (execRunner) Run(parent context.Context, directory, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = directory
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	text := strings.TrimSpace(output.String())
	if ctx.Err() != nil {
		return text, fmt.Errorf("%s timed out: %w", name, ctx.Err())
	}
	if err != nil {
		return text, fmt.Errorf("%s failed: %w: %s", name, err, shorten(text))
	}
	return text, nil
}

func (execRunner) Start(directory, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = directory
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	return nil
}

type ControllerImpl struct {
	config Config
	runner commandRunner
	client *http.Client
	mu     sync.Mutex
}

func NewController(config Config) (*ControllerImpl, error) {
	if err := config.Normalize(); err != nil {
		return nil, err
	}
	return &ControllerImpl{
		config: config,
		runner: execRunner{},
		client: &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (c *ControllerImpl) Status(ctx context.Context) (updates.Status, error) {
	return c.inspect(ctx, nil)
}

func (c *ControllerImpl) Check(ctx context.Context, request updates.Request) (updates.Status, error) {
	return c.inspect(ctx, selected(request))
}

func (c *ControllerImpl) Install(_ context.Context, request updates.Request) (updates.Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	components := selected(request)
	if components[updates.ComponentGateway] {
		if err := c.installGateway(ctx); err != nil {
			status, _ := c.inspect(context.Background(), components)
			return status, fmt.Errorf("gateway update: %w", err)
		}
	}
	if components[updates.ComponentComfyUI] {
		if err := c.installComfyUI(ctx); err != nil {
			status, _ := c.inspect(context.Background(), components)
			return status, fmt.Errorf("ComfyUI update: %w", err)
		}
	}
	if components[updates.ComponentOpenWebUI] {
		if err := c.installOpenWebUI(ctx); err != nil {
			status, _ := c.inspect(context.Background(), components)
			return status, fmt.Errorf("Open WebUI update: %w", err)
		}
	}
	return c.inspect(context.Background(), components)
}

func (c *ControllerImpl) inspect(ctx context.Context, only map[string]bool) (updates.Status, error) {
	statuses := make([]updates.ComponentStatus, 0, 3)
	var problems []string
	if only == nil || only[updates.ComponentGateway] {
		item := c.inspectGit(ctx, updates.ComponentGateway, "AI Access Gateway", c.config.Gateway)
		statuses = append(statuses, item)
		if item.Message != "" {
			problems = append(problems, item.Message)
		}
	}
	if only == nil || only[updates.ComponentComfyUI] {
		item := c.inspectComfyUI(ctx)
		statuses = append(statuses, item)
		if item.Message != "" {
			problems = append(problems, item.Message)
		}
	}
	if only == nil || only[updates.ComponentOpenWebUI] {
		item := c.inspectOpenWebUI(ctx)
		statuses = append(statuses, item)
		if item.Message != "" {
			problems = append(problems, item.Message)
		}
	}
	return updates.Status{Components: statuses, Message: strings.Join(problems, "; ")}, nil
}

func (c *ControllerImpl) inspectGit(ctx context.Context, name, label string, target GitTarget) updates.ComponentStatus {
	item := updates.ComponentStatus{Name: name, DisplayName: label, Configured: true, CheckedAt: time.Now().UTC()}
	current, err := c.git(ctx, target.WorkingDirectory, "rev-parse", "HEAD")
	if err != nil {
		item.Message = fmt.Sprintf("%s: %v", label, err)
		return item
	}
	latest, err := c.git(ctx, target.WorkingDirectory, "ls-remote", target.RemoteURL, "refs/heads/"+target.Branch)
	if err != nil {
		item.Message = fmt.Sprintf("%s: %v", label, err)
		return item
	}
	fields := strings.Fields(latest)
	if len(fields) == 0 {
		item.Message = fmt.Sprintf("%s: GitHub не вернул ревизию ветки.", label)
		return item
	}
	latest = fields[0]
	item.CurrentVersion = shortSHA(current)
	item.LatestVersion = shortSHA(latest)
	item.UpdateAvailable = !sameSHA(current, latest)
	if dirty, err := c.gitDirty(ctx, target.WorkingDirectory); err != nil {
		item.Message = fmt.Sprintf("%s: %v", label, err)
	} else if dirty {
		item.Message = "Есть локальные изменения: автоматическое обновление заблокировано."
	} else {
		item.CanInstall = item.UpdateAvailable
	}
	return item
}

func (c *ControllerImpl) inspectComfyUI(ctx context.Context) updates.ComponentStatus {
	target := c.config.ComfyUI
	item := updates.ComponentStatus{Name: updates.ComponentComfyUI, DisplayName: "ComfyUI", Configured: true, CheckedAt: time.Now().UTC()}
	current, err := c.git(ctx, target.WorkingDirectory, "rev-parse", "HEAD")
	if err != nil {
		item.Message = fmt.Sprintf("ComfyUI: %v", err)
		return item
	}
	latest, err := c.git(ctx, target.WorkingDirectory, "ls-remote", target.RemoteURL, "refs/heads/"+target.Branch)
	if err != nil {
		item.Message = fmt.Sprintf("ComfyUI: %v", err)
		return item
	}
	fields := strings.Fields(latest)
	if len(fields) == 0 {
		item.Message = "ComfyUI: GitHub не вернул ревизию ветки."
		return item
	}
	item.CurrentVersion = shortSHA(current)
	item.LatestVersion = shortSHA(fields[0])
	item.UpdateAvailable = !sameSHA(current, fields[0])
	if dirty, err := c.gitDirty(ctx, target.WorkingDirectory); err != nil {
		item.Message = fmt.Sprintf("ComfyUI: %v", err)
	} else if dirty {
		item.Message = "Есть локальные изменения ComfyUI: автоматическое обновление заблокировано."
	} else if active, err := c.comfyBusy(ctx); err != nil {
		item.Message = err.Error()
	} else if active {
		item.Message = "В ComfyUI есть активная очередь: обновление отложено."
	} else {
		item.CanInstall = item.UpdateAvailable
	}
	return item
}

func (c *ControllerImpl) inspectOpenWebUI(ctx context.Context) updates.ComponentStatus {
	target := c.config.OpenWebUI
	item := updates.ComponentStatus{Name: updates.ComponentOpenWebUI, DisplayName: "Open WebUI", Configured: true, CheckedAt: time.Now().UTC()}
	current, err := c.runner.Run(ctx, target.ComposeDirectory, "docker", "inspect", "--format", "{{.Config.Image}}", target.ContainerName)
	if err != nil {
		item.Message = fmt.Sprintf("Open WebUI: %v", err)
		return item
	}
	latest, err := c.latestRelease(ctx, target.ReleaseAPI)
	if err != nil {
		item.Message = fmt.Sprintf("Open WebUI: %v", err)
		return item
	}
	item.CurrentVersion = imageVersion(current)
	item.LatestVersion = latest
	item.UpdateAvailable = item.CurrentVersion != latest
	if _, err := os.Stat(target.ComposeFile); err != nil {
		item.Message = "Файл Docker Compose Open WebUI недоступен."
		return item
	}
	item.CanInstall = item.UpdateAvailable
	return item
}

func (c *ControllerImpl) installGateway(ctx context.Context) error {
	target := c.config.Gateway
	return c.installGitService(ctx, target, "AI Access Gateway")
}

func (c *ControllerImpl) installGitService(ctx context.Context, target GitTarget, label string) error {
	if dirty, err := c.gitDirty(ctx, target.WorkingDirectory); err != nil {
		return err
	} else if dirty {
		return errors.New("локальные изменения не позволяют безопасно обновить код")
	}
	previous, err := c.git(ctx, target.WorkingDirectory, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if _, err := c.git(ctx, target.WorkingDirectory, "fetch", "--prune", target.RemoteURL, target.Branch); err != nil {
		return err
	}
	if _, err := c.git(ctx, target.WorkingDirectory, "merge", "--ff-only", "FETCH_HEAD"); err != nil {
		return err
	}
	if _, err := c.compose(ctx, filepath.Dir(target.ComposeFile), target.ComposeFile, "up", "-d", "--build", target.ComposeService); err != nil {
		return c.rollbackGitService(ctx, target, previous, fmt.Errorf("пересборка %s: %w", label, err))
	}
	if err := c.waitHealth(ctx, target.HealthURL); err != nil {
		return c.rollbackGitService(ctx, target, previous, fmt.Errorf("проверка %s: %w", label, err))
	}
	return nil
}

func (c *ControllerImpl) rollbackGitService(ctx context.Context, target GitTarget, previous string, cause error) error {
	_, rollbackErr := c.git(ctx, target.WorkingDirectory, "reset", "--hard", previous)
	if rollbackErr == nil {
		_, rollbackErr = c.compose(ctx, filepath.Dir(target.ComposeFile), target.ComposeFile, "up", "-d", "--build", target.ComposeService)
	}
	if rollbackErr != nil {
		return fmt.Errorf("%w; откат не удался: %v", cause, rollbackErr)
	}
	return cause
}

func (c *ControllerImpl) installComfyUI(ctx context.Context) error {
	target := c.config.ComfyUI
	if dirty, err := c.gitDirty(ctx, target.WorkingDirectory); err != nil {
		return err
	} else if dirty {
		return errors.New("локальные изменения ComfyUI не позволяют безопасно обновить код")
	}
	if busy, err := c.comfyBusy(ctx); err != nil {
		return err
	} else if busy {
		return errors.New("очередь ComfyUI не пуста")
	}
	previous, err := c.git(ctx, target.WorkingDirectory, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if _, err := c.git(ctx, target.WorkingDirectory, "fetch", "--prune", target.RemoteURL, target.Branch); err != nil {
		return err
	}
	if _, err := c.git(ctx, target.WorkingDirectory, "merge", "--ff-only", "FETCH_HEAD"); err != nil {
		return err
	}
	if _, err := c.runner.Run(ctx, target.WorkingDirectory, target.PythonExecutable, "-m", "pip", "install", "-r", "requirements.txt"); err != nil {
		return c.rollbackComfyUI(ctx, previous, err)
	}
	if err := c.stopAndStartComfyUI(ctx); err != nil {
		return c.rollbackComfyUI(ctx, previous, err)
	}
	if err := c.waitHealth(ctx, target.HealthURL); err != nil {
		return c.rollbackComfyUI(ctx, previous, err)
	}
	return nil
}

func (c *ControllerImpl) rollbackComfyUI(ctx context.Context, previous string, cause error) error {
	target := c.config.ComfyUI
	if _, err := c.git(ctx, target.WorkingDirectory, "reset", "--hard", previous); err != nil {
		return fmt.Errorf("%w; откат ComfyUI не удался: %v", cause, err)
	}
	if err := c.stopAndStartComfyUI(ctx); err != nil {
		return fmt.Errorf("%w; ComfyUI откатился, но не перезапустился: %v", cause, err)
	}
	return cause
}

func (c *ControllerImpl) stopAndStartComfyUI(ctx context.Context) error {
	target := c.config.ComfyUI
	if len(target.StopCommand) == 0 {
		return errors.New("команда остановки ComfyUI не задана")
	}
	if _, err := c.runner.Run(ctx, target.WorkingDirectory, target.StopCommand[0], target.StopCommand[1:]...); err != nil {
		return fmt.Errorf("остановка ComfyUI: %w", err)
	}
	return c.runner.Start(target.WorkingDirectory, target.LaunchCommand[0], target.LaunchCommand[1:]...)
}

func (c *ControllerImpl) installOpenWebUI(ctx context.Context) error {
	target := c.config.OpenWebUI
	latest, err := c.latestRelease(ctx, target.ReleaseAPI)
	if err != nil {
		return err
	}
	image := target.ImageRepository + ":" + latest
	if _, err := c.runner.Run(ctx, target.ComposeDirectory, "docker", "pull", image); err != nil {
		return err
	}
	digest, err := c.runner.Run(ctx, target.ComposeDirectory, "docker", "image", "inspect", "--format", "{{index .RepoDigests 0}}", image)
	if err != nil {
		return err
	}
	previous, err := readEnvValue(target.EnvFile, target.ImageVariable)
	if err != nil {
		return err
	}
	if err := writeEnvValue(target.EnvFile, target.ImageVariable, digest); err != nil {
		return err
	}
	if _, err := c.compose(ctx, target.ComposeDirectory, target.ComposeFile, "up", "-d", "--force-recreate", target.ComposeService); err != nil {
		return c.rollbackOpenWebUI(ctx, previous, err)
	}
	if err := c.waitHealth(ctx, target.HealthURL); err != nil {
		return c.rollbackOpenWebUI(ctx, previous, err)
	}
	return nil
}

func (c *ControllerImpl) rollbackOpenWebUI(ctx context.Context, previous string, cause error) error {
	target := c.config.OpenWebUI
	if err := writeEnvValue(target.EnvFile, target.ImageVariable, previous); err != nil {
		return fmt.Errorf("%w; не удалось восстановить версию Open WebUI: %v", cause, err)
	}
	if _, err := c.compose(ctx, target.ComposeDirectory, target.ComposeFile, "up", "-d", "--force-recreate", target.ComposeService); err != nil {
		return fmt.Errorf("%w; откат Open WebUI не удался: %v", cause, err)
	}
	return cause
}

func (c *ControllerImpl) git(ctx context.Context, directory string, args ...string) (string, error) {
	return c.runner.Run(ctx, directory, "git", args...)
}

func (c *ControllerImpl) gitDirty(ctx context.Context, directory string) (bool, error) {
	output, err := c.git(ctx, directory, "status", "--porcelain", "--untracked-files=no")
	return strings.TrimSpace(output) != "", err
}

func (c *ControllerImpl) compose(ctx context.Context, directory, file string, args ...string) (string, error) {
	base := []string{"compose", "-f", file}
	return c.runner.Run(ctx, directory, "docker", append(base, args...)...)
}

func (c *ControllerImpl) latestRelease(ctx context.Context, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ai-access-gateway-update-agent")
	response, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub ответил HTTP %d", response.StatusCode)
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", err
	}
	value := strings.TrimSpace(payload.TagName)
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", errors.New("GitHub вернул некорректный тег релиза")
	}
	return value, nil
}

func (c *ControllerImpl) comfyBusy(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.ComfyUI.HealthURL+"queue", nil)
	if err != nil {
		return false, err
	}
	response, err := c.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("ComfyUI недоступен для проверки очереди: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("ComfyUI вернул HTTP %d для очереди", response.StatusCode)
	}
	var queue [2][]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&queue); err != nil {
		return false, fmt.Errorf("очередь ComfyUI: %w", err)
	}
	return len(queue[0]) > 0 || len(queue[1]) > 0, nil
}

func (c *ControllerImpl) waitHealth(ctx context.Context, endpoint string) error {
	deadline := time.Now().Add(healthTimeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err == nil {
			response, requestErr := c.client.Do(req)
			if requestErr == nil {
				response.Body.Close()
				if response.StatusCode >= 200 && response.StatusCode < 400 {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New("сервис не прошёл проверку работоспособности за 2 минуты")
}

func selected(request updates.Request) map[string]bool {
	items := make(map[string]bool, len(request.Components))
	for _, item := range request.Components {
		items[item] = true
	}
	return items
}

func readEnvValue(path, key string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	prefix := key + "="
	for _, line := range strings.Split(string(content), "\n") {
		if value, found := strings.CutPrefix(strings.TrimSuffix(line, "\r"), prefix); found && strings.TrimSpace(value) != "" {
			return value, nil
		}
	}
	return "", fmt.Errorf("%s не найден в %s", key, path)
}

func writeEnvValue(path, key, value string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	newline := "\n"
	if strings.Contains(string(content), "\r\n") {
		newline = "\r\n"
	}
	prefix := key + "="
	found := false
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = prefix + value
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%s не найден в %s", key, path)
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, []byte(strings.Join(lines, newline)), 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func shortSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
func sameSHA(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}
func imageVersion(value string) string {
	value = strings.TrimSpace(value)
	if at := strings.Index(value, "@"); at >= 0 {
		value = value[:at]
	}
	if colon := strings.LastIndex(value, ":"); colon >= 0 {
		return value[colon+1:]
	}
	return value
}
func shorten(value string) string {
	if len(value) > 600 {
		return value[:600] + "..."
	}
	return value
}
