package trainingagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"ai-access-gateway/internal/loratraining"
)

// Fence before checking absence: a delayed upload may otherwise arrive after
// the Gateway has already released the GPU. Fences outlive deleted job records.
func (controller *Controller) FenceGatewaySubmission(id string) (loratraining.SubmissionFenceResult, error) {
	result := loratraining.SubmissionFenceResult{GatewayJobID: id}
	if strings.TrimSpace(id) == "" || len(id) > 96 {
		return result, errors.New("invalid Gateway job ID")
	}
	controller.mu.Lock()
	if controller.submissionFences == nil {
		controller.submissionFences = make(map[string]bool)
	}
	if !controller.submissionFences[id] {
		if err := controller.persistSubmissionFence(id); err != nil {
			controller.mu.Unlock()
			return result, err
		}
		controller.submissionFences[id] = true
	}
	result.Fenced = true
	agentID := controller.byGateway[id]
	controller.mu.Unlock()
	if agentID == "" {
		result.Settled = true
		return result, nil
	}
	status, err := controller.Cancel(agentID)
	if errors.Is(err, os.ErrNotExist) {
		// Deletion is permitted only for a confirmed terminal record.
		result.Settled = true
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Job = &status
	result.Settled = status.Terminal() && !status.ExecutionUnconfirmed
	return result, nil
}

func submissionFenceName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]) + ".json"
}

func (controller *Controller) persistSubmissionFence(id string) error {
	directory := filepath.Join(controller.config.RootDir, "submission-fences")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	payload, err := json.Marshal(id)
	if err != nil {
		return err
	}
	target := filepath.Join(directory, submissionFenceName(id))
	if existing, err := os.ReadFile(target); err == nil {
		var saved string
		if json.Unmarshal(existing, &saved) != nil || saved != id {
			return errors.New("invalid persisted submission fence")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(directory, "fence-*.partial")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	_, writeErr := temporary.Write(payload)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), target)
}

func (controller *Controller) loadSubmissionFences() error {
	controller.submissionFences = make(map[string]bool)
	files, err := filepath.Glob(filepath.Join(controller.config.RootDir, "submission-fences", "*.json"))
	if err != nil {
		return err
	}
	for _, filename := range files {
		payload, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		var id string
		if json.Unmarshal(payload, &id) != nil || strings.TrimSpace(id) == "" || len(id) > 96 || submissionFenceName(id) != filepath.Base(filename) {
			return errors.New("invalid persisted submission fence")
		}
		controller.submissionFences[id] = true
	}
	return nil
}
