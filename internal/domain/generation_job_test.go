package domain

import "testing"

func TestGenerationJobStateMachine(t *testing.T) {
	valid := [][2]GenerationJobState{
		{GenerationJobDraft, GenerationJobPreparing},
		{GenerationJobPreparing, GenerationJobUploading},
		{GenerationJobPreparing, GenerationJobWaitingForResources},
		{GenerationJobUploading, GenerationJobWaitingForResources},
		{GenerationJobWaitingForResources, GenerationJobQueued},
		{GenerationJobQueued, GenerationJobRunning},
		{GenerationJobQueued, GenerationJobPostprocessing},
		{GenerationJobRunning, GenerationJobPostprocessing},
		{GenerationJobPostprocessing, GenerationJobArchiving},
		{GenerationJobArchiving, GenerationJobCompleted},
		{GenerationJobRunning, GenerationJobFailed},
		{GenerationJobQueued, GenerationJobCancelled},
	}
	for _, transition := range valid {
		if !CanTransitionGenerationJob(transition[0], transition[1]) {
			t.Errorf("expected transition %s -> %s", transition[0], transition[1])
		}
	}
	invalid := [][2]GenerationJobState{
		{GenerationJobDraft, GenerationJobCompleted},
		{GenerationJobRunning, GenerationJobQueued},
		{GenerationJobPostprocessing, GenerationJobCancelled},
		{GenerationJobCompleted, GenerationJobRunning},
		{GenerationJobFailed, GenerationJobPreparing},
	}
	for _, transition := range invalid {
		if CanTransitionGenerationJob(transition[0], transition[1]) {
			t.Errorf("unexpected transition %s -> %s", transition[0], transition[1])
		}
	}
}

func TestGenerationJobTerminalAndCancellableStates(t *testing.T) {
	for _, state := range []GenerationJobState{GenerationJobCompleted, GenerationJobFailed, GenerationJobCancelled, GenerationJobExpired} {
		if !state.Terminal() || state.Cancellable() {
			t.Errorf("terminal state %s has inconsistent capabilities", state)
		}
	}
	for _, state := range []GenerationJobState{GenerationJobDraft, GenerationJobPreparing, GenerationJobUploading, GenerationJobWaitingForResources, GenerationJobQueued, GenerationJobRunning} {
		if state.Terminal() || !state.Cancellable() {
			t.Errorf("active state %s has inconsistent capabilities", state)
		}
	}
	for _, state := range []GenerationJobState{GenerationJobPostprocessing, GenerationJobArchiving} {
		if state.Terminal() || state.Cancellable() {
			t.Errorf("finishing state %s has inconsistent capabilities", state)
		}
	}
}
