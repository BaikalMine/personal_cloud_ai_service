package gateway

import (
	"time"

	"ai-access-gateway/internal/domain"
)

// The list suggests a next step, not a mutation grant. A click reloads the
// exact source detail and the source handler rechecks state and access again.
func taskNextAction(task domain.Task, now time.Time) string {
	if task.CancellationPending {
		return "inspect"
	}
	switch task.Kind {
	case "generation":
		state := domain.GenerationJobState(task.State)
		if state.Cancellable() {
			return "cancel"
		}
		if state.Terminal() && task.SourceAvailable {
			return "edit_generation"
		}
	case "lora_training":
		state := domain.LoraTrainingState(task.State)
		if state.Cancellable() {
			return "cancel"
		}
		if state == domain.LoraTrainingCompleted && task.ArtifactAvailable {
			return "download_training"
		}
		if state.Terminal() && task.SourceAvailable {
			return "open_training"
		}
	case "lora_caption":
		if task.ExpiresAt == nil || !task.ExpiresAt.After(now) {
			return "inspect"
		}
		if task.State == "queued" || task.State == "running" {
			return "cancel"
		}
		if task.SourceAvailable {
			return "open_caption"
		}
	}
	return "inspect"
}
