//go:build windows

package miningagent

import (
	"context"
	"encoding/csv"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ai-access-gateway/internal/mining"
)

// System only invokes fixed, local Windows/NVIDIA diagnostic commands. Values
// supplied by the web client are never used to build a command line.
func (c *windowsController) System(ctx context.Context) (mining.SystemMetrics, error) {
	metrics, err := ReadWindowsSystemMetrics(ctx)
	if err != nil {
		return metrics, err
	}
	return metrics, nil
}

func ReadWindowsSystemMetrics(ctx context.Context) (mining.SystemMetrics, error) {
	metrics := mining.SystemMetrics{CollectedAt: time.Now().UTC()}
	command := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average | Select-Object -ExpandProperty Average; $os=Get-CimInstance Win32_OperatingSystem; \"$($os.TotalVisibleMemorySize),$($os.FreePhysicalMemory)\"")
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	output, err := command.Output()
	if err != nil {
		return metrics, fmt.Errorf("read Windows CPU and memory: %w", err)
	}
	lines := strings.Fields(strings.TrimSpace(string(output)))
	if len(lines) < 2 {
		return metrics, fmt.Errorf("unexpected Windows metrics response")
	}
	metrics.CPUPercent, _ = strconv.ParseFloat(strings.ReplaceAll(lines[0], ",", "."), 64)
	memory := strings.Split(lines[1], ",")
	if len(memory) != 2 {
		return metrics, fmt.Errorf("unexpected Windows memory response")
	}
	totalKB, totalErr := strconv.ParseInt(memory[0], 10, 64)
	freeKB, freeErr := strconv.ParseInt(memory[1], 10, 64)
	if totalErr != nil || freeErr != nil || totalKB <= 0 || freeKB < 0 {
		return metrics, fmt.Errorf("invalid Windows memory values")
	}
	metrics.MemoryTotalBytes = totalKB * 1024
	metrics.MemoryUsedBytes = (totalKB - freeKB) * 1024

	gpuCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	gpu := exec.CommandContext(gpuCtx, "nvidia-smi.exe", "--query-gpu=name,utilization.gpu,memory.used,memory.total", "--format=csv,noheader,nounits")
	gpu.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
	gpuOutput, gpuErr := gpu.Output()
	if gpuErr != nil {
		metrics.Message = "GPU-метрики временно недоступны."
		return metrics, nil
	}
	rows, parseErr := csv.NewReader(strings.NewReader(strings.TrimSpace(string(gpuOutput)))).ReadAll()
	if parseErr != nil || len(rows) == 0 {
		metrics.Message = "GPU-метрики временно недоступны."
		return metrics, nil
	}
	var utilization, usedMB, totalMB float64
	for _, row := range rows {
		if len(row) != 4 {
			continue
		}
		value, utilErr := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		used, usedErr := strconv.ParseFloat(strings.TrimSpace(row[2]), 64)
		total, totalErr := strconv.ParseFloat(strings.TrimSpace(row[3]), 64)
		if utilErr != nil || usedErr != nil || totalErr != nil || total <= 0 {
			continue
		}
		if metrics.GPUName == "" {
			metrics.GPUName = strings.TrimSpace(row[0])
		}
		utilization += value
		usedMB += used
		totalMB += total
	}
	if totalMB > 0 {
		metrics.GPUAvailable = true
		metrics.GPUPercent = utilization / float64(len(rows))
		metrics.GPUMemoryUsedBytes = int64(usedMB * 1024 * 1024)
		metrics.GPUMemoryTotalBytes = int64(totalMB * 1024 * 1024)
	}
	return metrics, nil
}
