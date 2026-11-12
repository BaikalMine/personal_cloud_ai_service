package gateway

import (
	"ai-access-gateway/internal/promptassistant"
	"time"
)

// Retain the accepted text/model snapshot, but repair the old default vision
// deadline before first dispatch. Named models keep their explicit admin limit.
func (a *App) assistantDispatchTimeout(snapshot promptAssistantJobSnapshot) time.Duration {
	timeout := time.Duration(snapshot.Request.Policy.TimeoutMS) * time.Millisecond
	if snapshot.Think && snapshot.ModelID == "" {
		limit := a.cfg.PromptAssistantReasoningTimeout
		if limit <= 0 {
			limit = promptassistant.DefaultModelPolicy().ReasoningTimeout
		}
		timeout = max(timeout, limit)
	}
	return timeout
}
