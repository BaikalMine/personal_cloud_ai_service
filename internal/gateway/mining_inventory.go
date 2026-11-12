package gateway

import (
	"ai-access-gateway/internal/mining"
	"strings"
)

func applyMiningInventory(overview *MiningOverview, inventory mining.Inventory) {
	overview.InventoryKnown = true
	overview.Catalog = inventory.Catalog
	overview.Running = false
	overview.External = nil
	for i := range overview.Miners {
		overview.Miners[i].State = mining.State{Available: true, CollectedAt: inventory.CollectedAt, ProcessName: overview.Miners[i].ProcessName}
	}
	runningGroups := 0
	for _, state := range inventory.States {
		if !state.Running {
			continue
		}
		overview.Running = true
		runningGroups++
		matched := false
		for i := range overview.Miners {
			miner := &overview.Miners[i]
			if strings.EqualFold(miner.ProcessName, state.ProcessName) && sameMiningScript(miner.ScriptPath, state.ScriptPath) {
				miner.State = state
				matched = true
			}
		}
		if !matched {
			overview.External = append(overview.External, state)
		}
	}
	normalizeMiningProfiles(overview)
	if runningGroups != 1 || len(overview.External) > 0 {
		overview.Active = nil
	}
}

func miningUncontrolled(overview MiningOverview) bool {
	return overview.Running && overview.Active == nil
}

func (overview MiningOverview) Uncontrolled() bool { return miningUncontrolled(overview) }
