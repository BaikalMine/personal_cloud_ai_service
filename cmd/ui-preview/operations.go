package main

import (
	"strings"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/gateway"
)

func previewOperationsData(source map[string]any, scenario string) map[string]any {
	data := make(map[string]any, len(source))
	for k, v := range source {
		data[k] = v
	}
	ops := make(map[string]any)
	for k, v := range source["Operations"].(map[string]any) {
		ops[k] = v
	}
	data["Operations"] = ops
	system := source["System"].(gateway.SystemOverview)
	if scenario == "empty" {
		ops["Attention"] = []map[string]any{}
		ops["ActiveJobs"] = []map[string]any{}
		ops["Failures"] = []domain.GenerationFailureSummary{}
		ops["ProblemWorkers"] = []gateway.MaintenanceWorkerState{}
		ops["BackgroundWorkers"] = []gateway.MaintenanceWorkerState{}
		ops["Generation"] = domain.GenerationObservabilitySummary{}
		ops["Leases"] = []domain.QuickGenerationMiningLease{}
		ops["OverallState"], ops["OverallLabel"], ops["OverallDetail"] = "healthy", "Отклонений нет", ""
		ops["Queue"] = map[string]any{"Available": true, "State": "idle", "StateLabel": "Свободна", "Detail": "Очередь ComfyUI пуста", "Running": 0, "Pending": 0, "EstimatedWaitSeconds": int64(0)}
		groups := []map[string]any{}
		for _, g := range ops["GPU"].([]map[string]any) {
			groups = append(groups, map[string]any{"Kind": g["Kind"], "Title": g["Title"], "Running": 0, "Reserved": 0, "Waiting": 0, "Unconfirmed": 0, "Paused": 0})
		}
		ops["GPU"] = groups
		system.Host, system.History, system.GenerationMarkers, system.OnlineUsers, system.Workers = nil, nil, nil, nil, nil
	} else if scenario == "changed" {
		problem := append([]gateway.MaintenanceWorkerState{}, system.Workers...)
		problem[0].Status, problem[0].StatusLabel, problem[0].LastError = "retrying", "Повтор после ошибки", "Новая ошибка фонового процесса"
		ops["ProblemWorkers"] = []gateway.MaintenanceWorkerState{problem[0], problem[3]}
		ops["BackgroundWorkers"] = append(append([]gateway.MaintenanceWorkerState{}, problem[1:3]...), problem[4:]...)
		system.Workers = problem
		ops["ActiveJobs"] = ops["ActiveJobs"].([]map[string]any)[1:]
	} else if scenario == "long" {
		jobs := []map[string]any{}
		for _, job := range ops["ActiveJobs"].([]map[string]any) {
			copy := map[string]any{}
			for k, v := range job {
				copy[k] = v
			}
			copy["Workflow"] = "Workflow_" + strings.Repeat("длинноеимя", 15)
			copy["Username"] = strings.Repeat("пользователь", 12)
			copy["StatusMessage"] = strings.Repeat("Подтверждение завершения ещё не получено. ", 8)
			jobs = append(jobs, copy)
		}
		ops["ActiveJobs"] = jobs
	}
	data["System"] = system
	return data
}
