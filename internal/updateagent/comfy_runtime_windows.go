//go:build windows

package updateagent

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

type nativeComfyRuntimeBackend struct{}

const runtimeNoWindow = 0x08000000

func runtimeProcessHandle(p comfyProcess, access uint32) (windows.Handle, error) {
	h, err := windows.OpenProcess(access|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(p.PID))
	if err != nil {
		return 0, err
	}
	var created, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil || uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) != p.Created {
		windows.CloseHandle(h)
		return 0, errors.New("ComfyUI process identity changed")
	}
	return h, nil
}
func (nativeComfyRuntimeBackend) Inspect(parent context.Context, t ComfyTarget) ([]comfyProcess, error) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	parameters, _ := json.Marshal(map[string]string{"python": t.PythonExecutable, "main": filepath.Join(t.WorkingDirectory, "main.py")})
	// Only server configuration enters this fixed script. No shell text is accepted from HTTP.
	script := `$ErrorActionPreference='Stop'; $p=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('` + base64.StdEncoding.EncodeToString(parameters) + `'))|ConvertFrom-Json; $pattern='(?i)(?:^|\s)"?'+[regex]::Escape($p.main)+'"?(?=\s|$)'; $items=@(Get-CimInstance Win32_Process -Filter "Name='python.exe' OR Name='pythonw.exe'"|Where-Object { $_.ExecutablePath -and $_.CommandLine -and [IO.Path]::GetFullPath($_.ExecutablePath).Equals([IO.Path]::GetFullPath($p.python),[StringComparison]::OrdinalIgnoreCase) -and $_.CommandLine -match $pattern }|ForEach-Object { @{pid=[int]$_.ProcessId;created=$_.CreationDate.ToFileTimeUtc().ToString()} }); ConvertTo-Json -InputObject $items -Compress`
	units := utf16.Encode([]rune(script))
	encoded := make([]byte, 2*len(units))
	for i, v := range units {
		binary.LittleEndian.PutUint16(encoded[i*2:], v)
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: runtimeNoWindow, HideWindow: true}
	data, err := cmd.Output()
	if err != nil || len(data) > 16<<10 {
		return nil, errors.New("process inspection unavailable")
	}
	var values []struct {
		PID     int    `json:"pid"`
		Created string `json:"created"`
	}
	if json.Unmarshal(data, &values) != nil {
		return nil, errors.New("invalid process snapshot")
	}
	result := make([]comfyProcess, 0, len(values))
	for _, value := range values {
		created, err := strconv.ParseUint(value.Created, 10, 64)
		if err != nil || value.PID <= 0 || created == 0 {
			return nil, errors.New("invalid process identity")
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(value.PID))
		if err != nil {
			return nil, errors.New("process identity unavailable")
		}
		var native, exited, kernel, user windows.Filetime
		err = windows.GetProcessTimes(h, &native, &exited, &kernel, &user)
		windows.CloseHandle(h)
		ticks := uint64(native.HighDateTime)<<32 | uint64(native.LowDateTime)
		// CIM timestamps have microsecond precision; retain the exact native
		// timestamp after checking that this is still the inspected process.
		if err != nil || ticks < created || ticks-created >= 10 {
			return nil, errors.New("process identity changed during inspection")
		}
		result = append(result, comfyProcess{value.PID, ticks})
	}
	return result, nil
}
func (nativeComfyRuntimeBackend) Stop(ctx context.Context, _ ComfyTarget, p comfyProcess) error {
	h, err := runtimeProcessHandle(p, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	// Keep the verified process handle open so its PID cannot be reused while
	// taskkill stops this exact process tree (including ETUR child workers).
	cmd := exec.CommandContext(ctx, "taskkill.exe", "/PID", strconv.Itoa(p.PID), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: runtimeNoWindow, HideWindow: true}
	if err := cmd.Run(); err != nil {
		return errors.New("ComfyUI process tree stop failed")
	}
	state, err := windows.WaitForSingleObject(h, 3000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		return errors.New("ComfyUI termination unconfirmed")
	}
	return nil
}
func (nativeComfyRuntimeBackend) Watch(p comfyProcess, callback func(uint32)) error {
	h, err := runtimeProcessHandle(p, 0)
	if err != nil {
		return err
	}
	go func() {
		defer windows.CloseHandle(h)
		state, err := windows.WaitForSingleObject(h, windows.INFINITE)
		if err == nil && state == windows.WAIT_OBJECT_0 {
			var code uint32
			if windows.GetExitCodeProcess(h, &code) == nil {
				callback(code)
			}
		}
	}()
	return nil
}
func (nativeComfyRuntimeBackend) Start(t ComfyTarget, dir string, exited func(comfyProcess, uint32)) error {
	if len(t.LaunchArguments) == 0 || !strings.EqualFold(filepath.Clean(t.LaunchArguments[0]), filepath.Join(t.WorkingDirectory, "main.py")) {
		return errors.New("invalid ComfyUI entrypoint")
	}
	logPath := filepath.Join(dir, "console.log")
	if info, err := os.Stat(logPath); err == nil && info.Size() > 10<<20 {
		_ = os.Remove(logPath + ".previous")
		if err := os.Rename(logPath, logPath+".previous"); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cmd := exec.Command(t.PythonExecutable, t.LaunchArguments...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = t.WorkingDirectory, file, file
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1", "PYTHONFAULTHANDLER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: runtimeNoWindow | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB}
	if err := cmd.Start(); err != nil {
		file.Close()
		return fmt.Errorf("independent ComfyUI launch failed: %w", err)
	}
	p := comfyProcess{PID: cmd.Process.Pid}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.PID))
	if err == nil {
		var created, end, kernel, user windows.Filetime
		if windows.GetProcessTimes(h, &created, &end, &kernel, &user) == nil {
			p.Created = uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
		}
		windows.CloseHandle(h)
	}
	go func() {
		_ = cmd.Wait()
		file.Close()
		if p.Created != 0 {
			exited(p, uint32(cmd.ProcessState.ExitCode()))
		}
	}()
	return nil
}
