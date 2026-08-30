//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"ai-access-gateway/internal/mining"
)

const (
	processQueryInformation = 0x0400
	processSetQuota         = 0x0100
	createNoWindow          = 0x08000000
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	psapi           = syscall.NewLazyDLL("psapi.dll")
	openProcess     = kernel32.NewProc("OpenProcess")
	closeHandle     = kernel32.NewProc("CloseHandle")
	emptyWorkingSet = psapi.NewProc("EmptyWorkingSet")
)

type windowsProcess struct {
	PID         int    `json:"ProcessId"`
	Name        string `json:"Name"`
	CommandLine string `json:"CommandLine"`
}

func trimComfyWorkingSet(ctx context.Context, signature string) (mining.ComfyMemoryTrim, error) {
	processes, err := windowsProcesses(ctx)
	if err != nil {
		return mining.ComfyMemoryTrim{}, err
	}
	signature = strings.ToLower(strings.TrimSpace(signature))
	result := mining.ComfyMemoryTrim{}
	for _, process := range processes {
		isPython := strings.EqualFold(process.Name, "python.exe") || strings.EqualFold(process.Name, "pythonw.exe")
		if process.PID <= 0 || !isPython || !strings.Contains(strings.ToLower(process.CommandLine), signature) {
			continue
		}
		if err := emptyProcessWorkingSet(uint32(process.PID)); err != nil {
			return result, err
		}
		result.Trimmed++
	}
	if result.Trimmed == 0 {
		result.Message = "Процесс ComfyUI не найден."
	}
	return result, nil
}

func windowsProcesses(ctx context.Context) ([]windowsProcess, error) {
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "@(Get-CimInstance Win32_Process | Select-Object ProcessId,Name,CommandLine) | ConvertTo-Json -Compress")
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

func emptyProcessWorkingSet(pid uint32) error {
	handle, _, err := openProcess.Call(processQueryInformation|processSetQuota, 0, uintptr(pid))
	if handle == 0 {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer closeHandle.Call(handle)
	if ok, _, err := emptyWorkingSet.Call(handle); ok == 0 {
		return fmt.Errorf("trim process %d working set: %w", pid, err)
	}
	return nil
}
