//go:build windows

package miningagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
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
	createNewConsole      = 0x00000010
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
	maxScriptBytes        = 64 << 10
)

type windowsController struct {
	rootDir   string
	outputLog string
	stateFile string

	mu                 sync.Mutex
	managedPID         int
	managedProcessName string
	managedPIDs        []int
	scriptPath         string
	startedAt          time.Time
}

type managedMinerState struct {
	ProcessName string    `json:"process_name"`
	PIDs        []int     `json:"pids"`
	LauncherPID int       `json:"launcher_pid,omitempty"`
	ScriptPath  string    `json:"script_path"`
	StartedAt   time.Time `json:"started_at"`
}

func NewController(rootDir, outputLog string) (Controller, error) {
	root, err := canonicalDirectory(rootDir)
	if err != nil {
		return nil, fmt.Errorf("mining root: %w", err)
	}
	controller := &windowsController{
		rootDir:   root,
		outputLog: outputLog,
		stateFile: filepath.Join(filepath.Dir(outputLog), "managed-miner.json"),
	}
	controller.restoreManagedState()
	return controller, nil
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
	return c.startLocked(ctx, request)
}

func (c *windowsController) startLocked(ctx context.Context, request mining.Request) (mining.State, error) {
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
	// The visible console is the tracked process itself. This makes a stop
	// command close the exact console window instead of leaving wrappers behind.
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole | createNewProcessGroup, HideWindow: false}
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
			c.setManagedLocked(request.ProcessName, state.PIDs, scriptPath, time.Now())
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
	return c.stopLocked(ctx, request)
}

func (c *windowsController) stopLocked(ctx context.Context, request mining.Request) (mining.State, error) {
	state, err := c.stateLocked(ctx, request.ProcessName)
	if err != nil {
		return state, err
	}
	scriptPath, err := c.allowedScript(request.ScriptPath)
	if err != nil {
		return state, err
	}
	managedPIDs := c.managedPIDsForLocked(request.ProcessName, state.PIDs)
	if len(managedPIDs) == 0 && state.Running {
		// Stop is an explicit operator action for a stored Gateway profile. Capture
		// only the matching current PIDs, never all processes with the same image name.
		c.setManagedLocked(request.ProcessName, state.PIDs, scriptPath, time.Now())
		managedPIDs = append([]int(nil), state.PIDs...)
	}
	if len(managedPIDs) == 0 {
		state.Message = "Майнинг уже остановлен."
		return state, nil
	}
	launcherPID := c.managedPID
	stoppedTree := false
	if launcherPID > 0 {
		if err := runTaskkill(ctx, "/PID", strconv.Itoa(launcherPID)); err == nil {
			stoppedTree = true
		}
	}
	if !stoppedTree {
		for _, pid := range c.minerConsolePIDs(ctx, managedPIDs, scriptPath) {
			if err := runTaskkill(ctx, "/PID", strconv.Itoa(pid)); err == nil {
				stoppedTree = true
			}
		}
	}
	if !stoppedTree {
		for _, pid := range managedPIDs {
			if err := runTaskkill(ctx, "/PID", strconv.Itoa(pid)); err != nil {
				return state, fmt.Errorf("stop managed miner: %w", err)
			}
		}
	}
	c.resetManagedLocked()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, err = c.stateLocked(ctx, request.ProcessName)
		if err != nil {
			return state, err
		}
		if !state.Running {
			state.Message = "Майнинг остановлен."
			return state, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return state, errors.New("майнер не завершился после команды остановки")
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
		return exec.Command(
			"powershell.exe", "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-EncodedCommand", innerEncoded,
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

type windowsProcess struct {
	PID         int    `json:"ProcessId"`
	ParentPID   int    `json:"ParentProcessId"`
	Name        string `json:"Name"`
	CommandLine string `json:"CommandLine"`
}

func windowsProcesses(ctx context.Context) ([]windowsProcess, error) {
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", `@(Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,CommandLine) | ConvertTo-Json -Compress`)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list Windows processes: %w", err)
	}
	var processes []windowsProcess
	if err := json.Unmarshal(output, &processes); err != nil {
		return nil, fmt.Errorf("decode Windows processes: %w", err)
	}
	return processes, nil
}

// minerConsolePIDs finds only a shell that explicitly references the approved
// mining script. It is used for a miner that was started manually before the
// agent began tracking its own launcher PID.
func (c *windowsController) minerConsolePIDs(ctx context.Context, minerPIDs []int, scriptPath string) []int {
	processes, err := windowsProcesses(ctx)
	if err != nil {
		return nil
	}
	byPID := make(map[int]windowsProcess, len(processes))
	for _, process := range processes {
		byPID[process.PID] = process
	}
	scriptPath = strings.ToLower(scriptPath)
	scriptName := strings.ToLower(filepath.Base(scriptPath))
	rootDir := strings.ToLower(c.rootDir)
	candidates := make(map[int]struct{})
	for _, minerPID := range minerPIDs {
		current, exists := byPID[minerPID]
		for hops := 0; exists && hops < 8 && current.ParentPID > 0; hops++ {
			parent, found := byPID[current.ParentPID]
			if !found {
				break
			}
			name := strings.ToLower(parent.Name)
			commandLine := strings.ToLower(parent.CommandLine)
			isShell := name == "cmd.exe" || name == "powershell.exe" || name == "pwsh.exe"
			referencesScript := strings.Contains(commandLine, scriptPath) || (strings.Contains(commandLine, rootDir) && strings.Contains(commandLine, scriptName))
			if isShell && referencesScript {
				candidates[parent.PID] = struct{}{}
				break
			}
			current, exists = parent, true
		}
	}
	result := make([]int, 0, len(candidates))
	for pid := range candidates {
		result = append(result, pid)
	}
	return result
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
	c.managedProcessName = ""
	c.managedPIDs = nil
	c.scriptPath = ""
	c.startedAt = time.Time{}
	_ = os.Remove(c.stateFile)
}

func (c *windowsController) setManagedLocked(processName string, pids []int, scriptPath string, startedAt time.Time) {
	unique := make([]int, 0, len(pids))
	seen := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if _, exists := seen[pid]; exists {
			continue
		}
		seen[pid] = struct{}{}
		unique = append(unique, pid)
	}
	if len(unique) == 0 {
		return
	}
	c.managedProcessName = processName
	c.managedPIDs = unique
	c.scriptPath = scriptPath
	c.startedAt = startedAt
	payload, err := json.Marshal(managedMinerState{
		ProcessName: processName,
		PIDs:        unique,
		LauncherPID: c.managedPID,
		ScriptPath:  scriptPath,
		StartedAt:   startedAt,
	})
	if err != nil {
		return
	}
	temporary := c.stateFile + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o600); err == nil {
		_ = os.Rename(temporary, c.stateFile)
	}
}

func (c *windowsController) restoreManagedState() {
	payload, err := os.ReadFile(c.stateFile)
	if err != nil {
		return
	}
	var state managedMinerState
	if json.Unmarshal(payload, &state) != nil || !validProcessName(state.ProcessName) || len(state.PIDs) == 0 {
		_ = os.Remove(c.stateFile)
		return
	}
	if _, err := c.allowedScript(state.ScriptPath); err != nil {
		_ = os.Remove(c.stateFile)
		return
	}
	c.managedPID = state.LauncherPID
	c.setManagedLocked(state.ProcessName, state.PIDs, state.ScriptPath, state.StartedAt)
}

func (c *windowsController) managedPIDsForLocked(processName string, runningPIDs []int) []int {
	if !strings.EqualFold(c.managedProcessName, processName) || len(c.managedPIDs) == 0 {
		return nil
	}
	running := make(map[int]struct{}, len(runningPIDs))
	for _, pid := range runningPIDs {
		running[pid] = struct{}{}
	}
	matched := make([]int, 0, len(c.managedPIDs))
	for _, pid := range c.managedPIDs {
		if _, ok := running[pid]; ok {
			matched = append(matched, pid)
		}
	}
	if len(matched) == 0 {
		c.resetManagedLocked()
	}
	return matched
}
