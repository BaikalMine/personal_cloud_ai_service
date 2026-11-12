package gateway

import (
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
	"testing"
)

func TestMiningProfilesDistinguishSameExecutable(t *testing.T) {
	overview := MiningOverview{Running: true, Miners: []MinerView{
		{Miner: domain.Miner{ID: 1, Default: true, ScriptPath: `C:\Mining\first.bat`, ProcessName: "miner.exe"}, State: mining.State{Running: true, ScriptPath: `C:\Mining\second.bat`}},
		{Miner: domain.Miner{ID: 2, ScriptPath: `C:\Mining\second.bat`, ProcessName: "miner.exe"}, State: mining.State{Running: true, ScriptPath: `C:\Mining\second.bat`}},
	}}
	normalizeMiningProfiles(&overview)
	if overview.Active == nil || overview.Active.ID != 2 || overview.Default.ID != 1 || overview.Miners[0].State.Running || !overview.Miners[1].State.Running {
		t.Fatalf("overview=%+v", overview)
	}
}
