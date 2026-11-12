package main

import (
	"ai-access-gateway/internal/buildinfo"
	"ai-access-gateway/internal/gateway"
	"strings"
	"time"
)

func previewReleaseSnapshot(now time.Time) gateway.ReleaseSnapshot {
	report := func(component string) buildinfo.Report {
		return buildinfo.Report{SchemaVersion: 1, Component: component, Contracts: buildinfo.Contracts(component), Build: buildinfo.Info{Version: "v2026.09.10", Revision: strings.Repeat("abcdef01", 5), BuiltAt: now.Format(time.RFC3339), Modified: "false", GoVersion: "go1.25.0", Platform: "windows/amd64"}}
	}
	update := report(buildinfo.UpdateAgent)
	training := report(buildinfo.TrainingAgent)
	training.Contracts[0].Version++
	monitor := report(buildinfo.SystemMonitor)
	monitor.Build.Version = "dev"
	monitor.Build.Modified = "true"
	monitor.Build.BuiltAt = ""
	return gateway.ReleaseSnapshot{SchemaVersion: 1, CheckedAt: now, Gateway: report(buildinfo.Gateway), Peers: []gateway.ReleasePeer{
		{Name: "Агент обновлений", Component: buildinfo.UpdateAgent, Check: buildinfo.Compare(buildinfo.UpdateAgent, update)},
		{Name: "Агент обучения", Component: buildinfo.TrainingAgent, Check: buildinfo.Compare(buildinfo.TrainingAgent, training)},
		{Name: "Агент майнинга", Component: buildinfo.MiningAgent, Check: buildinfo.Check{Status: "unsupported", Checked: now}},
		{Name: "Монитор системы", Component: buildinfo.SystemMonitor, Check: buildinfo.Compare(buildinfo.SystemMonitor, monitor)},
		{Name: "Обработчик моделей", Component: buildinfo.InferenceWorker, Check: buildinfo.Check{Status: "unavailable", Checked: now}},
	}}
}
