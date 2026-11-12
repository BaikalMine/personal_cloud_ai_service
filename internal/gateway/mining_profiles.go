package gateway

import (
	"strings"
	"time"
)

func sameMiningScript(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(strings.ReplaceAll(a, "/", "\\"), strings.ReplaceAll(b, "/", "\\"))
}

func (miner MinerView) CanEditScript() bool {
	path := strings.ToLower(miner.ScriptPath)
	return strings.HasSuffix(path, ".bat") || strings.HasSuffix(path, ".cmd")
}

func normalizeMiningProfiles(overview *MiningOverview) {
	overview.Active, overview.Default = nil, nil
	for i := range overview.Miners {
		miner := &overview.Miners[i]
		if miner.State.Running && miner.State.ScriptPath != "" && !sameMiningScript(miner.ScriptPath, miner.State.ScriptPath) {
			miner.State.Running = false
			miner.State.PIDs = nil
			miner.State.StartedAt = time.Time{}
		}
		if miner.Default {
			overview.Default = miner
		}
		if miner.State.Running && (overview.Active == nil || sameMiningScript(miner.ScriptPath, miner.State.ScriptPath)) {
			overview.Active = miner
		}
	}
}
