package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/domain"
)

func TestTaskNextAction(t *testing.T) {
	now := time.Now()
	future, past := now.Add(time.Hour), now.Add(-time.Second)
	for _, test := range []struct {
		name string
		task domain.Task
		want string
	}{
		{"queued generation", domain.Task{Kind: "generation", State: "queued"}, "cancel"},
		{"running generation", domain.Task{Kind: "generation", State: "running"}, "cancel"},
		{"postprocessing cannot cancel", domain.Task{Kind: "generation", State: "postprocessing"}, "inspect"},
		{"archiving cannot cancel", domain.Task{Kind: "generation", State: "archiving"}, "inspect"},
		{"pending cancellation", domain.Task{Kind: "generation", State: "running", CancellationPending: true}, "inspect"},
		{"saved failure", domain.Task{Kind: "generation", State: "failed", SourceAvailable: true}, "edit_generation"},
		{"saved completed", domain.Task{Kind: "generation", State: "completed", SourceAvailable: true}, "edit_generation"},
		{"expired output saved input", domain.Task{Kind: "generation", State: "expired", SourceAvailable: true}, "edit_generation"},
		{"lost input", domain.Task{Kind: "generation", State: "failed"}, "inspect"},
		{"training active", domain.Task{Kind: "lora_training", State: "installing"}, "cancel"},
		{"training download", domain.Task{Kind: "lora_training", State: "completed", ArtifactAvailable: true, SourceAvailable: true}, "download_training"},
		{"failed training keeps version", domain.Task{Kind: "lora_training", State: "failed", SourceAvailable: true}, "open_training"},
		{"failed training artifact not ready", domain.Task{Kind: "lora_training", State: "failed", ArtifactAvailable: true}, "inspect"},
		{"training source gone", domain.Task{Kind: "lora_training", State: "completed"}, "inspect"},
		{"caption active", domain.Task{Kind: "lora_caption", State: "running", ExpiresAt: &future}, "cancel"},
		{"caption completed", domain.Task{Kind: "lora_caption", State: "completed", SourceAvailable: true, ExpiresAt: &future}, "open_caption"},
		{"caption failed edits source", domain.Task{Kind: "lora_caption", State: "failed", SourceAvailable: true, ExpiresAt: &future}, "open_caption"},
		{"caption no set", domain.Task{Kind: "lora_caption", State: "failed", ExpiresAt: &future}, "inspect"},
		{"caption expired", domain.Task{Kind: "lora_caption", State: "failed", SourceAvailable: true, ExpiresAt: &past}, "inspect"},
		{"caption no expiry", domain.Task{Kind: "lora_caption", State: "running"}, "inspect"},
		{"unknown state", domain.Task{Kind: "generation", State: "mystery", SourceAvailable: true}, "inspect"},
		{"unknown kind", domain.Task{Kind: "mystery", State: "queued"}, "inspect"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := taskNextAction(test.task, now); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestTaskNextActionDoesNotExposeSourceFlags(t *testing.T) {
	view := makeTaskView(domain.Task{Kind: "generation", State: "failed", SourceAvailable: true, ArtifactAvailable: true})
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"next_action":"edit_generation"`) {
		t.Fatalf("missing action: %s", data)
	}
	for _, private := range []string{"SourceAvailable", "ArtifactAvailable", "source_available", "artifact_available"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("source flag exposed: %s", data)
		}
	}
}
