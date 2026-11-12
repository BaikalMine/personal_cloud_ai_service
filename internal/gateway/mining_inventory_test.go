package gateway

import (
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/mining"
	"testing"
	"time"
)

func TestMiningInventoryExactProfileAndExternal(t *testing.T) {
	now := time.Now().UTC()
	overview := MiningOverview{Available: true, Miners: []MinerView{
		{Miner: Miner{ID: 1, Default: true, ProcessName: "miner.exe", ScriptPath: `C:\Mining\a.bat`}},
		{Miner: Miner{ID: 2, ProcessName: "miner.exe", ScriptPath: `C:\Mining\b.bat`}},
	}}
	snapshot := mining.Inventory{CollectedAt: now, States: []mining.State{{Available: true, Running: true, CollectedAt: now, ProcessName: "miner.exe", ScriptPath: `C:\Mining\b.bat`, PIDs: []int{12}}}}
	applyMiningInventory(&overview, snapshot)
	if overview.Active == nil || overview.Active.ID != 2 || overview.Miners[0].State.Running || !overview.Running {
		t.Fatalf("wrong profile: %+v", overview)
	}
	snapshot.States[0].ProcessName = "other.exe"
	applyMiningInventory(&overview, snapshot)
	if !miningUncontrolled(overview) || len(overview.External) != 1 || overview.Miners[1].State.Running {
		t.Fatal(overview)
	}
	view := newAdminResourceView(now, "mining", DependencyStatus{}, nil, domain.GPUResource{}, nil, &overview, false)
	if view.Profiles[1].CanStart || view.Profiles[1].CanUpdate {
		t.Fatal("external miner did not block commands")
	}
	snapshot.States = nil
	applyMiningInventory(&overview, snapshot)
	if overview.Running || overview.Active != nil || len(overview.External) != 0 {
		t.Fatal("stale running state", overview)
	}
}
