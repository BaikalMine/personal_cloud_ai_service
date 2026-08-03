//go:build windows

package miningagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"ai-access-gateway/internal/mining"
)

const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
	maxScriptBytes        = 64 << 10
)

type windowsController struct {
	rootDir   string
	outputLog string

	mu         sync.Mutex
	managedPID int
	scriptPath string
	startedAt  time.Time
}

func NewController(rootDir, outputLog string) (Controller, error) {
	root, err := canonicalDirectory(rootDir)
	if err != nil {
		return nil, fmt.Errorf("mining root: %w", err)
	}
	return &windowsController{rootDir: root, outputLog: outputLog}, nil
}

func (c *windowsController) State(ctx context.Context, processName string) (mining.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateLocked(ctx, processName)
}

func (c *windowsController) Script(ctx context.Context, rawPath string) (mining.Script, error) {
	select {
	case <-ctx.Done():
		return mining.Script{}, ctx.Err()
	default:
	}
	scriptPath, err := c.allowedScript(rawPath)
	if err != nil {
		return mining.Script{}, err
	}
	extension := strings.ToLower(filepath.Ext(scriptPath))
	if extension != ".bat" && extension != ".cmd" && extension != ".ps1" {
		return mining.Script{Path: scriptPath}, errors.New("содержимое доступно только для .bat, .cmd и .ps1")
	}
	info, err := os.Stat(scriptPath)
	if err != nil {
		return mining.Script{Path: scriptPath}, err
	}
	if info.Size() > maxScriptBytes {
		return mining.Script{Path: scriptPath}, errors.New("скрипт больше 64 КБ")
	}
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		return mining.Script{Path: scriptPath}, err
	}
	sum := sha256.Sum256(data)
	return mining.Script{
		Path:    scriptPath,
		Content: decodeScriptContent(data),
		SHA256:  fmt.Sprintf("%x", sum),
	}, nil
}

func (c *windowsController) Start(ctx context.Context, request mining.Request) (mining.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.stateLocked(ctx, request.ProcessName)
	if err != nil {
		return state, err
	}
	if state.Running {
		state.Message = "Майнинг уже запущен."
		return state, nil
	}
	if c.managedPID > 0 {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = runTaskkill(cleanupCtx, "/PID", strconv.Itoa(c.managedPID))
		cancel()
		c.resetManagedLocked()
	}
	scriptPath, err := c.allowedScript(request.ScriptPath)
	if err != nil {
		return state, err
	}
	if err := c.prepareOutputLog(); err != nil {
		return state, err
	}
	command, err := commandForScript(scriptPath, c.outputLog)
	if err != nil {
		return state, err
	}
	command.Dir = filepath.Dir(scriptPath)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow, HideWindow: true}
	if err := command.Start(); err != nil {
		return state, fmt.Errorf("start mining script: %w", err)
	}
	managedPID := command.Process.Pid
	c.managedPID = managedPID
	c.scriptPath = scriptPath
	c.startedAt = time.Now()
	go func() {
		_ = command.Wait()
		c.mu.Lock()
		if c.managedPID == managedPID {
			c.resetManagedLocked()
		}
		c.mu.Unlock()
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		state, probeErr := c.stateLocked(probeCtx, request.ProcessName)
		cancel()
		if probeErr == nil && state.Running {
			state.Message = "Майнинг запущен."
			return state, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = runTaskkill(cleanupCtx, "/PID", strconv.Itoa(c.managedPID))
	cancel()
	c.resetManagedLocked()
	return mining.State{ProcessName: request.ProcessName, ScriptPath: scriptPath}, errors.New("майнер не появился в списке процессов после запуска скрипта")
}

func (c *windowsController) Stop(ctx context.Context, request mining.Request) (mining.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.stateLocked(ctx, request.ProcessName)
	if err != nil {
		return state, err
	}
	if !state.Running {
		if c.managedPID > 0 {
			_ = runTaskkill(ctx, "/PID", strconv.Itoa(c.managedPID))
			c.resetManagedLocked()
		}
		state.Message = "Майнинг уже остановлен."
		return state, nil
	}
	if c.managedPID > 0 {
		_ = runTaskkill(ctx, "/PID", strconv.Itoa(c.managedPID))
	}
	if err := runTaskkill(ctx, "/IM", request.ProcessName); err != nil {
		probe, probeErr := c.stateLocked(ctx, request.ProcessName)
		if probeErr != nil || probe.Running {
			return probe, fmt.Errorf("stop miner: %w", err)
		}
	}
	c.resetManagedLocked()
	state, err = c.stateLocked(ctx, request.ProcessName)
	if err != nil {
		return state, err
	}
	state.Message = "Майнинг остановлен."
	return state, nil
}

func (c *windowsController) stateLocked(ctx context.Context, processName string) (mining.State, error) {
	pids, err := processIDs(ctx, processName)
	state := mining.State{
		Running:     len(pids) > 0,
		PIDs:        pids,
		ScriptPath:  c.scriptPath,
		ProcessName: processName,
		StartedAt:   c.startedAt,
	}
	return state, err
}

func (c *windowsController) allowedScript(rawPath string) (string, error) {
	path, err := filepath.Abs(filepath.Clean(rawPath))
	if err != nil {
		return "", errors.New("некорректный путь к скрипту")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("script path: %w", err)
	}
	relative, err := filepath.Rel(c.rootDir, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, `..\`) || filepath.IsAbs(relative) {
		return "", errors.New("скрипт находится вне разрешённой папки майнинга")
	}
	extension := strings.ToLower(filepath.Ext(resolved))
	if extension != ".bat" && extension != ".cmd" && extension != ".ps1" && extension != ".exe" {
		return "", errors.New("поддерживаются только .bat, .cmd, .ps1 и .exe")
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return "", errors.New("скрипт запуска не найден")
	}
	return resolved, nil
}

func commandForScript(path, outputLog string) (*exec.Cmd, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".bat", ".cmd", ".ps1", ".exe":
		invocation := fmt.Sprintf(`& '%s'`, powerShellQuote(path))
		innerScript := fmt.Sprintf(
			`$Host.UI.RawUI.WindowTitle = '%s'; $ErrorActionPreference = 'Continue'; try { $banner = [Environment]::NewLine + "=== Запуск майнера $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') ==="; $banner | Tee-Object -FilePath '%s' -Append; %s 2>&1 | Tee-Object -FilePath '%s' -Append } finally { }`,
			powerShellQuote("Майнинг - "+filepath.Base(path)),
			powerShellQuote(outputLog),
			invocation,
			powerShellQuote(outputLog),
		)
		innerEncoded := encodePowerShell(innerScript)
		launcherScript := fmt.Sprintf(
			`$ErrorActionPreference = 'Stop'; try { $arguments = @('-NoLogo','-NoProfile','-ExecutionPolicy','Bypass','-EncodedCommand','%s'); $child = Start-Process -FilePath 'powershell.exe' -ArgumentList $arguments -WorkingDirectory '%s' -WindowStyle Normal -PassThru; $child.WaitForExit(); exit $child.ExitCode } catch { $_ | Out-File -FilePath '%s' -Append; exit 1 }`,
			innerEncoded,
			powerShellQuote(filepath.Dir(path)),
			powerShellQuote(outputLog),
		)
		return exec.Command(
			"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encodePowerShell(launcherScript),
		), nil
	default:
		return nil, errors.New("неподдерживаемый тип скрипта")
	}
}

func powerShellQuote(value string) string {
	return strings.ReplaceAll(value, `'`, `''`)
}

func encodePowerShell(script string) string {
	encoded := utf16.Encode([]rune(script))
	data := make([]byte, len(encoded)*2)
	for index, value := range encoded {
		data[index*2] = byte(value)
		data[index*2+1] = byte(value >> 8)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func decodeScriptContent(data []byte) string {
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		data = data[3:]
	}
	if len(data) >= 2 && ((data[0] == 0xff && data[1] == 0xfe) || (data[0] == 0xfe && data[1] == 0xff)) {
		littleEndian := data[0] == 0xff
		data = data[2:]
		units := make([]uint16, 0, len(data)/2)
		for index := 0; index+1 < len(data); index += 2 {
			if littleEndian {
				units = append(units, uint16(data[index])|uint16(data[index+1])<<8)
			} else {
				units = append(units, uint16(data[index])<<8|uint16(data[index+1]))
			}
		}
		return string(utf16.Decode(units))
	}
	if !utf8.Valid(data) {
		return strings.ToValidUTF8(string(data), "�")
	}
	return string(data)
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("directory does not exist")
	}
	return resolved, nil
}

func processIDs(ctx context.Context, processName string) ([]int, error) {
	command := exec.CommandContext(ctx, "tasklist.exe", "/FI", "IMAGENAME eq "+processName, "/FO", "CSV", "/NH")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	reader := csv.NewReader(strings.NewReader(string(output)))
	var pids []int
	for {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || len(record) < 2 || !strings.EqualFold(record[0], processName) {
			continue
		}
		pid, parseErr := strconv.Atoi(record[1])
		if parseErr == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func runTaskkill(ctx context.Context, selector, value string) error {
	command := exec.CommandContext(ctx, "taskkill.exe", selector, value, "/T", "/F")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (c *windowsController) prepareOutputLog() error {
	if err := os.MkdirAll(filepath.Dir(c.outputLog), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(c.outputLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return statErr
	}
	if closeErr != nil {
		return closeErr
	}
	if info.Size() == 0 {
		return os.WriteFile(c.outputLog, []byte{0xff, 0xfe}, 0o600)
	}
	return nil
}

func (c *windowsController) resetManagedLocked() {
	c.managedPID = 0
	c.scriptPath = ""
	c.startedAt = time.Time{}
}
