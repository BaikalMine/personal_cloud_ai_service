package miningagent

import (
	"testing"
	"time"
)

func TestEncodedPowerShellCommandDigest(t *testing.T) {
	want := encodedPowerShellPayloadDigest("VABlAHMAdAA=")
	for _, commandLine := range []string{
		`powershell.exe -NoProfile -EncodedCommand VABlAHMAdAA=`,
		`"C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe" -enc "VABlAHMAdAA="`,
	} {
		if got := encodedPowerShellCommandDigest(commandLine); got != want {
			t.Fatalf("digest = %q, want %q", got, want)
		}
	}
	if got := encodedPowerShellCommandDigest(`powershell.exe -File start.ps1`); got != "" {
		t.Fatalf("unexpected digest %q", got)
	}
}

func TestManagedProcessStateMatchesDurableIdentity(t *testing.T) {
	startedAt := time.Date(2026, time.August, 30, 7, 38, 4, 0, time.FixedZone("MSK", 3*60*60))
	state := managedMinerState{
		ProcessName: "SRBMiner-MULTI.exe",
		PIDs:        []int{24848},
		ScriptPath:  `C:\Mining\SRBMiner\start.bat`,
		StartedAt:   startedAt,
	}
	created := startedAt.Add(-time.Second).Unix()
	if !managedProcessStateMatches(24848, "srbminer-multi.EXE", created, "SRBMiner-MULTI.exe", `c:/mining/srbminer/start.bat`, state) {
		t.Fatal("matching durable miner identity was rejected")
	}
	for name, mutate := range map[string]func(*managedMinerState) (int, string, int64, string){
		"untracked PID": func(state *managedMinerState) (int, string, int64, string) {
			return 999, "SRBMiner-MULTI.exe", created, state.ScriptPath
		},
		"wrong process": func(state *managedMinerState) (int, string, int64, string) {
			return 24848, "other.exe", created, state.ScriptPath
		},
		"wrong script": func(state *managedMinerState) (int, string, int64, string) {
			return 24848, state.ProcessName, created, `C:\Mining\Other\start.bat`
		},
		"reused PID": func(state *managedMinerState) (int, string, int64, string) {
			return 24848, state.ProcessName, startedAt.Add(30 * time.Second).Unix(), state.ScriptPath
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyState := state
			pid, processName, creationUnix, scriptPath := mutate(&copyState)
			if managedProcessStateMatches(pid, processName, creationUnix, state.ProcessName, scriptPath, copyState) {
				t.Fatal("mismatched durable miner identity was accepted")
			}
		})
	}
}
