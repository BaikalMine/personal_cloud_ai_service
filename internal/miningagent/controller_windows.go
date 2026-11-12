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
	rootDir       string
	outputLog     string
	stateFile     string
	archivePolicy archiveSourcePolicy

	mu                 sync.Mutex
	updateMu           sync.Mutex
	managedPID         int
	managedLauncherSHA string
	managedProcessName string
	managedPIDs        []int
	scriptPath         string
	startedAt          time.Time
	owned              *ownedProcessGroup
	ownedHidden        bool
	ownedRequestPath   string
}

func NewController(rootDir, outputLog string, archivePrefixes ...string) (Controller, error) {
	return newWindowsController(rootDir, outputLog, false, archivePrefixes...)
}

func NewOwnedController(rootDir, outputLog string, archivePrefixes ...string) (Controller, error) {
	return newWindowsController(rootDir, outputLog, true, archivePrefixes...)
}

func newWindowsController(rootDir, outputLog string, owned bool, archivePrefixes ...string) (*windowsController, error) {
	root, err := canonicalDirectory(rootDir)
	if err != nil {
		return nil, fmt.Errorf("mining root: %w", err)
	}
	policy, err := newArchiveSourcePolicy(archivePrefixes)
	if err != nil {
		return nil, fmt.Errorf("miner archive policy: %w", err)
	}
	controller := &windowsController{
		rootDir:       root,
		outputLog:     outputLog,
		stateFile:     filepath.Join(filepath.Dir(outputLog), "managed-miner.json"),
		archivePolicy: policy,
	}
	if owned {
		controller.owned, err = openOwnedProcessGroup(filepath.Join(filepath.Dir(controller.stateFile), "mining-process-owner.lock"))
		if err != nil {
			return nil, err
		}
	}
	controller.restoreManagedState()
	return controller, nil
}

func (c *windowsController) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owned != nil {
		return c.owned.Close()
	}
	return nil
}

func (c *windowsController) State(ctx context.Context, processName string) (mining.State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.stateLocked(ctx, processName)
	if err == nil && c.owned != nil {
		active, ownedErr := c.owned.Active()
		if ownedErr != nil {
			return state, ownedErr
		}
		if active > 0 && !state.Running && (c.managedProcessName == "" || strings.EqualFold(c.managedProcessName, processName)) {
			return state, errors.New("дерево процессов майнера ещё запускается или завершается")
		}
	}
	return state, err
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
	scriptPath, pathErr := c.allowedScript(request.ScriptPath)
	if pathErr != nil {
		return mining.State{}, fmt.Errorf("%w: %s", errCommandRejected, pathErr)
	}
	request.ScriptPath = scriptPath
	state, err := c.stateLocked(ctx, request.ProcessName)
	if err != nil {
		return state, err
	}
	if c.owned != nil {
		active, ownedErr := c.owned.Active()
		if ownedErr != nil {
			return state, ownedErr
		}
		if active > 0 && (!state.Running || !strings.EqualFold(c.managedProcessName, request.ProcessName) || !c.ownedScriptMatches(request.ScriptPath)) {
			return state, fmt.Errorf("%w: предыдущее дерево процессов майнера ещё не завершено", errCommandRejected)
		}
	}
	inventory, inventoryErr := c.inventoryLocked(ctx)
	if inventoryErr != nil {
		return state, inventoryErr
	}
	if err := inventoryAllowsStart(inventory, request); err != nil {
		return state, fmt.Errorf("%w: %s", errCommandRejected, err)
	}
	if state.Running {
		for _, observed := range inventory.States {
			if strings.EqualFold(observed.ProcessName, request.ProcessName) && sameWindowsPath(observed.ScriptPath, scriptPath) {
				state = observed
				break
			}
		}
		state.Message = "Майнинг уже запущен."
		return state, nil
	}
	if c.managedPID > 0 {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if matches, _ := c.managedLauncherMatches(cleanupCtx, c.managedPID, c.managedLauncherSHA); matches {
			_ = runTaskkill(cleanupCtx, "/PID", strconv.Itoa(c.managedPID))
		}
		cancel()
		c.resetManagedLocked()
	}
	if err := c.prepareOutputLog(); err != nil {
		return state, err
	}
	command, launcherSHA, err := commandForScript(scriptPath, c.outputLog)
	if err != nil {
		return state, err
	}
	command.Dir = filepath.Dir(scriptPath)
	// The visible console is the tracked process itself. This makes a stop
	// command close the exact console window instead of leaving wrappers behind.
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole | createNewProcessGroup, HideWindow: false}
	var managedPID int
	var wait func() error
	if c.owned != nil {
		process, err := c.owned.Start(ctx, command, !c.ownedHidden)
		if err != nil {
			return state, err
		}
		managedPID, wait = process.PID, process.Wait
		c.managedProcessName = request.ProcessName
		c.ownedRequestPath, _ = filepath.Abs(request.ScriptPath)
	} else {
		if err := command.Start(); err != nil {
			return state, fmt.Errorf("start mining script: %w", err)
		}
		managedPID, wait = command.Process.Pid, command.Wait
	}
	c.managedPID = managedPID
	c.managedLauncherSHA = launcherSHA
	c.scriptPath = scriptPath
	c.startedAt = time.Now()
	go func() {
		_ = wait()
		c.mu.Lock()
		if c.managedPID == managedPID {
			if c.owned == nil {
				c.resetManagedLocked()
			} else if active, err := c.owned.Active(); err == nil && active == 0 {
				c.resetManagedLocked()
			}
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
			state.ScriptPath, state.StartedAt = scriptPath, c.startedAt
			state.Message = "Майнинг запущен."
			return state, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if c.owned != nil {
		cleanupErr := c.owned.Stop(cleanupCtx)
		cancel()
		if cleanupErr != nil {
			return state, fmt.Errorf("завершение неудачного запуска майнера не подтверждено: %w", cleanupErr)
		}
		c.resetManagedLocked()
		return state, errors.New("майнер не появился после запуска; управляемое дерево завершено")
	}
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
	if c.owned != nil {
		active, ownedErr := c.owned.Active()
		if ownedErr != nil {
			return state, ownedErr
		}
		if active > 0 {
			if !c.ownedScriptMatches(request.ScriptPath) || !strings.EqualFold(c.managedProcessName, request.ProcessName) {
				return state, fmt.Errorf("%w: активное дерево принадлежит другому профилю майнинга", errCommandRejected)
			}
			if err := c.owned.Stop(ctx); err != nil {
				return state, err
			}
			c.resetManagedLocked()
			state.Running = false
			state.PIDs = nil
			state.Message = "Майнинг остановлен, дерево процессов завершено."
			return state, nil
		}
	}
	scriptPath, err := c.allowedScript(request.ScriptPath)
	if err != nil {
		return state, fmt.Errorf("%w: %s", errCommandRejected, err)
	}
	candidates := state.PIDs
	if strings.EqualFold(c.managedProcessName, request.ProcessName) && len(c.managedPIDs) > 0 {
		candidates = intersectProcessIDs(c.managedPIDs, state.PIDs)
	}
	managedPIDs, consolePIDs, err := c.verifiedMinerProcessTree(ctx, candidates, request.ProcessName, scriptPath)
	if err != nil {
		return state, fmt.Errorf("проверка процесса майнера: %w", err)
	}
	if len(managedPIDs) == 0 {
		if state.Running {
			return state, errors.New("найден процесс с таким именем, но агент не смог подтвердить, что он запущен выбранным профилем")
		}
		state.Message = "Майнинг уже остановлен."
		return state, nil
	}
	c.setManagedLocked(request.ProcessName, managedPIDs, scriptPath, c.startedAt)
	launcherPID := c.managedPID
	launcherSHA := c.managedLauncherSHA
	if launcherPID > 0 {
		if matches, _ := c.managedLauncherMatches(ctx, launcherPID, launcherSHA); matches {
			_ = runTaskkill(ctx, "/PID", strconv.Itoa(launcherPID))
		}
	}
	for _, pid := range consolePIDs {
		if pid != launcherPID {
			_ = runTaskkill(ctx, "/PID", strconv.Itoa(pid))
		}
	}
	for _, pid := range managedPIDs {
		_ = runTaskkill(ctx, "/PID", strconv.Itoa(pid))
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runningPIDs, listErr := processIDs(ctx, request.ProcessName)
		if listErr != nil {
			return state, listErr
		}
		remaining, _, verifyErr := c.verifiedMinerProcessTree(ctx, runningPIDs, request.ProcessName, scriptPath)
		if verifyErr != nil {
			return state, verifyErr
		}
		if len(remaining) == 0 {
			c.resetManagedLocked()
			state.Running = false
			state.PIDs = nil
			state.Message = "Майнинг остановлен."
			return state, nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return state, errors.New("майнер не завершился после команды остановки")
}

func (c *windowsController) stateLocked(ctx context.Context, processName string) (mining.State, error) {
	pids, err := processIDs(ctx, processName)
	state := observedProfileState(processName, c.managedProcessName, c.scriptPath, pids, c.managedPIDs, c.startedAt)
	return state, err
}

func (c *windowsController) ownedScriptMatches(raw string) bool {
	path, err := filepath.Abs(raw)
	if err != nil {
		return false
	}
	// Keep the submitted alias usable even if the launcher file was removed.
	if strings.EqualFold(path, c.ownedRequestPath) || strings.EqualFold(path, c.scriptPath) {
		return true
	}
	resolved, err := c.allowedScript(path)
	return err == nil && strings.EqualFold(resolved, c.scriptPath)
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

func commandForScript(path, outputLog string) (*exec.Cmd, string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".bat", ".cmd", ".ps1", ".exe":
		innerScript := buildMinerLauncherScript(path, outputLog)
		innerEncoded := encodePowerShell(innerScript)
		return exec.Command(
			"powershell.exe", "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-EncodedCommand", innerEncoded,
		), encodedPowerShellPayloadDigest(innerEncoded), nil
	default:
		return nil, "", errors.New("неподдерживаемый тип скрипта")
	}
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

func windowsProcesses(ctx context.Context) ([]windowsProcess, error) {
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", `[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); @(Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,CommandLine,ExecutablePath,@{Name='CreationUnix';Expression={if ($_.CreationDate) { ([DateTimeOffset]$_.CreationDate).ToUnixTimeSeconds() } else { 0 }}}) | ConvertTo-Json -Compress`)
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
func (c *windowsController) verifiedMinerProcessTree(ctx context.Context, minerPIDs []int, processName, scriptPath string) ([]int, []int, error) {
	processes, err := windowsProcesses(ctx)
	if err != nil {
		return nil, nil, err
	}
	byPID := make(map[int]windowsProcess, len(processes))
	for _, process := range processes {
		byPID[process.PID] = process
	}
	scriptPath = strings.ToLower(scriptPath)
	scriptDir := filepath.Dir(scriptPath)
	catalog, err := launchFiles(ctx, c.rootDir)
	if err != nil {
		return nil, nil, err
	}
	var scripts []string
	for _, file := range catalog.Files {
		if file.Kind == "script" {
			scripts = append(scripts, file.Path)
		}
	}
	managedState := managedMinerState{
		ProcessName: c.managedProcessName,
		PIDs:        c.managedPIDs,
		ScriptPath:  c.scriptPath,
		StartedAt:   c.startedAt,
	}
	verified := make(map[int]struct{})
	consoles := make(map[int]struct{})
	for _, minerPID := range minerPIDs {
		current, exists := byPID[minerPID]
		if !exists || !strings.EqualFold(current.Name, processName) {
			continue
		}
		if current.ExecutablePath != "" && !pathWithinDirectoryFold(c.rootDir, current.ExecutablePath) {
			continue
		}
		durableMatch := managedProcessStateMatches(current.PID, current.Name, current.CreationUnix, processName, scriptPath, managedState)
		if durableMatch && current.ExecutablePath != "" && pathWithinDirectoryFold(scriptDir, current.ExecutablePath) {
			verified[minerPID] = struct{}{}
		}
		seen := map[int]bool{current.PID: true}
		for hops := 0; exists && hops < 16 && current.ParentPID > 0; hops++ {
			parent, found := byPID[current.ParentPID]
			if !found || seen[parent.PID] || (current.CreationUnix > 0 && parent.CreationUnix > current.CreationUnix) {
				break
			}
			seen[parent.PID] = true
			name := strings.ToLower(parent.Name)
			commandLine := strings.ToLower(parent.CommandLine)
			isShell := name == "cmd.exe" || name == "powershell.exe" || name == "pwsh.exe"
			referencesScript := sameWindowsPath(commandScriptPath(commandLine, scripts), scriptPath)
			if isShell && referencesScript {
				verified[minerPID] = struct{}{}
				consoles[parent.PID] = struct{}{}
				break
			}
			if parent.PID == c.managedPID && strings.EqualFold(encodedPowerShellCommandDigest(parent.CommandLine), c.managedLauncherSHA) {
				verified[minerPID] = struct{}{}
				consoles[parent.PID] = struct{}{}
				break
			}
			current, exists = parent, true
		}
	}
	verifiedPIDs := make([]int, 0, len(verified))
	for pid := range verified {
		verifiedPIDs = append(verifiedPIDs, pid)
	}
	consolePIDs := make([]int, 0, len(consoles))
	for pid := range consoles {
		consolePIDs = append(consolePIDs, pid)
	}
	return verifiedPIDs, consolePIDs, nil
}

func (c *windowsController) managedLauncherMatches(ctx context.Context, pid int, digest string) (bool, error) {
	if pid <= 0 || digest == "" {
		return false, nil
	}
	processes, err := windowsProcesses(ctx)
	if err != nil {
		return false, err
	}
	for _, process := range processes {
		if process.PID != pid {
			continue
		}
		name := strings.ToLower(process.Name)
		return (name == "powershell.exe" || name == "pwsh.exe") && strings.EqualFold(encodedPowerShellCommandDigest(process.CommandLine), digest), nil
	}
	return false, nil
}

func pathWithinDirectoryFold(root, target string) bool {
	relative, err := filepath.Rel(strings.ToLower(filepath.Clean(root)), strings.ToLower(filepath.Clean(target)))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func intersectProcessIDs(left, right []int) []int {
	running := make(map[int]struct{}, len(right))
	for _, pid := range right {
		running[pid] = struct{}{}
	}
	result := make([]int, 0, len(left))
	for _, pid := range left {
		if _, ok := running[pid]; ok {
			result = append(result, pid)
		}
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
	c.managedLauncherSHA = ""
	c.managedProcessName = ""
	c.managedPIDs = nil
	c.scriptPath = ""
	c.ownedRequestPath = ""
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
		LauncherSHA: c.managedLauncherSHA,
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
	if len(state.LauncherSHA) == sha256.Size*2 {
		probeCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		if matches, _ := c.managedLauncherMatches(probeCtx, state.LauncherPID, state.LauncherSHA); matches {
			c.managedPID = state.LauncherPID
			c.managedLauncherSHA = state.LauncherSHA
		}
		cancel()
	}
	c.setManagedLocked(state.ProcessName, state.PIDs, state.ScriptPath, state.StartedAt)
}
