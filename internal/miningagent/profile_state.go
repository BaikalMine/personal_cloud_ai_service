package miningagent

import (
	"strings"
	"time"

	"ai-access-gateway/internal/mining"
)

func observedProfileState(processName, managedName, script string, pids, managedPIDs []int, startedAt time.Time) mining.State {
	state := mining.State{Running: len(pids) > 0, PIDs: pids, ProcessName: processName}
	if !strings.EqualFold(processName, managedName) {
		return state
	}
	for _, pid := range pids {
		for _, managedPID := range managedPIDs {
			if pid == managedPID {
				state.ScriptPath, state.StartedAt = script, startedAt
				return state
			}
		}
	}
	return state
}
