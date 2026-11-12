package gateway

import (
	"context"
	"testing"

	"ai-access-gateway/internal/domain"
)

func TestDisabledMiningPauseDoesNotAccessMiningServices(t *testing.T) {
	users := []struct {
		name string
		user *User
	}{
		{name: "no user"},
		{name: "normal queue", user: &User{PauseMiningForQuickGeneration: false, QueuePriority: false}},
		{name: "priority queue", user: &User{PauseMiningForQuickGeneration: false, QueuePriority: true}},
	}
	for _, tc := range users {
		t.Run(tc.name, func(t *testing.T) {
			// Missing dependencies ensure disabled policy cannot access the agent or store.
			app := &App{gpuCoordinator: &gpuCoordinator{}}
			lease, warning, err := app.pauseMiningForQuickGeneration(context.Background(), tc.user, 1)
			if lease != nil || warning != "" || err != nil {
				t.Fatalf("generation pause = %v, %q, %v", lease, warning, err)
			}
			lease, warning, err = app.pauseMiningForInference(context.Background(), tc.user, domain.GPUWork{})
			if lease != nil || warning != "" || err != nil {
				t.Fatalf("inference pause = %v, %q, %v", lease, warning, err)
			}
		})
	}
}
