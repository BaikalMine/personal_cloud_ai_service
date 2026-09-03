package gateway

import (
	"strings"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/loratraining"
)

func TestTruncateLoraTextPreservesUTF8(t *testing.T) {
	t.Parallel()
	result := truncateLoraText("  обучение готово  ", 7)
	if result != "обучени" || strings.ToValidUTF8(result, "") != result {
		t.Fatalf("truncateLoraText returned %q", result)
	}
}

func TestLoraTrainingJSONUsesLiveAgentState(t *testing.T) {
	t.Parallel()
	job := domain.LoraTrainingJob{PublicID: "lora-job-0123456789", State: domain.LoraTrainingRunning, Progress: 45}
	status := &loratraining.JobStatus{State: "installing", Stage: "Установка", Progress: 96, Message: "Копируем файл"}
	result := loraTrainingJSON(job, status, false)
	if result.State != string(domain.LoraTrainingInstalling) || result.StateLabel != "Установка" || result.Progress != 96 || !result.CanCancel {
		t.Fatalf("live LoRA status was not projected: %+v", result)
	}
}
