package trainingagent

import "ai-access-gateway/internal/loratraining"

func (controller *Controller) Activity() loratraining.Activity {
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	result := loratraining.Activity{ProtocolVersion: 1, State: "idle", PendingSubmissions: controller.pendingSubmissions, StorageCleanupActive: controller.activeStorageOperation != ""}
	for _, record := range controller.jobs {
		if record.Status.ExecutionUnconfirmed {
			result.UnconfirmedJobs++
		}
		if !record.Status.Terminal() {
			result.ActiveJobs++
		}
	}
	if result.UnconfirmedJobs > 0 {
		result.State = "uncertain"
	} else if result.ActiveJobs > 0 || result.PendingSubmissions > 0 || result.StorageCleanupActive {
		result.State = "busy"
	}
	return result
}
