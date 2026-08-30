package miningagent

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

const managedProcessStartTolerance = 5 * time.Second

type managedMinerState struct {
	ProcessName string    `json:"process_name"`
	PIDs        []int     `json:"pids"`
	LauncherPID int       `json:"launcher_pid,omitempty"`
	LauncherSHA string    `json:"launcher_sha256,omitempty"`
	ScriptPath  string    `json:"script_path"`
	StartedAt   time.Time `json:"started_at"`
}

func encodedPowerShellPayloadDigest(payload string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(payload)))
	return fmt.Sprintf("%x", digest)
}

func encodedPowerShellCommandDigest(commandLine string) string {
	fields := strings.Fields(commandLine)
	for index := 0; index+1 < len(fields); index++ {
		flag := strings.ToLower(strings.Trim(fields[index], `"'`))
		if flag != "-encodedcommand" && flag != "-enc" {
			continue
		}
		payload := strings.Trim(fields[index+1], `"'`)
		if payload == "" {
			return ""
		}
		return encodedPowerShellPayloadDigest(payload)
	}
	return ""
}

func managedProcessStateMatches(pid int, name string, creationUnix int64, requestedProcessName, requestedScriptPath string, state managedMinerState) bool {
	if pid <= 0 || creationUnix <= 0 || state.StartedAt.IsZero() ||
		!strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(requestedProcessName)) ||
		!strings.EqualFold(strings.TrimSpace(state.ProcessName), strings.TrimSpace(requestedProcessName)) ||
		!sameWindowsPath(state.ScriptPath, requestedScriptPath) {
		return false
	}
	tracked := false
	for _, trackedPID := range state.PIDs {
		if trackedPID == pid {
			tracked = true
			break
		}
	}
	if !tracked {
		return false
	}
	createdAt := time.Unix(creationUnix, 0)
	delta := createdAt.Sub(state.StartedAt)
	return delta >= -managedProcessStartTolerance && delta <= managedProcessStartTolerance
}

func sameWindowsPath(left, right string) bool {
	normalize := func(value string) string {
		value = strings.TrimSpace(strings.ReplaceAll(value, "/", `\`))
		return strings.TrimRight(value, `\`)
	}
	return strings.EqualFold(normalize(left), normalize(right))
}
