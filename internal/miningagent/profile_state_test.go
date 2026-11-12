package miningagent

import (
	"testing"
	"time"
)

func TestObservedMiningProfileRequiresProcessAndPIDMatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		process string
		pids    []int
		matched bool
	}{
		{name: "managed", process: "miner.exe", pids: []int{17}, matched: true},
		{name: "different executable", process: "other.exe", pids: []int{17}},
		{name: "external instance", process: "miner.exe", pids: []int{18}},
		{name: "stopped", process: "miner.exe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := observedProfileState(tc.process, "miner.exe", `C:\Mining\managed.bat`, tc.pids, []int{17}, time.Now())
			if (state.ScriptPath != "") != tc.matched || state.Running != (len(tc.pids) > 0) {
				t.Fatalf("state=%+v", state)
			}
		})
	}
}
