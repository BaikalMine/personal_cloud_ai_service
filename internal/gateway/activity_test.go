package gateway

import (
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestPrepareUserActivitiesHidesNoiseAndGroupsRepeatedActions(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	activities := prepareUserActivities([]domain.Activity{
		{Service: "comfyui", Method: "GET", Path: "/comfyui/assets/app.js", Status: 200, CreatedAt: now},
		{Service: "comfyui", Method: "POST", Path: "/comfyui/prompt", Status: 200, Duration: 120, Bytes: 100, CreatedAt: now.Add(-10 * time.Second)},
		{Service: "comfyui", Method: "POST", Path: "/comfyui/prompt", Status: 200, Duration: 80, Bytes: 200, CreatedAt: now.Add(-30 * time.Second)},
		{Service: "openwebui", Method: "POST", Path: "/openwebui/api/chat/completions", Status: 200, CreatedAt: now.Add(-5 * time.Minute)},
	}, 12)
	if len(activities) != 2 {
		t.Fatalf("activity count = %d, want 2", len(activities))
	}
	if activities[0].Summary != "Запуск генерации" || activities[0].Count != 2 || activities[0].Bytes != 300 {
		t.Fatalf("grouped activity = %+v", activities[0])
	}
	if activities[1].ServiceLabel != "OpenWebUI" || activities[1].Summary != "Сообщение нейросети" {
		t.Fatalf("chat activity = %+v", activities[1])
	}
}

func TestPrepareUserActivitiesKeepsSeparatedActions(t *testing.T) {
	now := time.Now()
	activities := prepareUserActivities([]domain.Activity{
		{Service: "comfyui", Method: "POST", Path: "/prompt", Status: 200, CreatedAt: now},
		{Service: "comfyui", Method: "POST", Path: "/interrupt", Status: 200, CreatedAt: now.Add(-10 * time.Second)},
		{Service: "comfyui", Method: "POST", Path: "/prompt", Status: 200, CreatedAt: now.Add(-4 * time.Minute)},
	}, 12)
	if len(activities) != 3 {
		t.Fatalf("activity count = %d, want 3", len(activities))
	}
}
