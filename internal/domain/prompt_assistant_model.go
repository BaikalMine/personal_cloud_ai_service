package domain

type PromptAssistantModel struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Model          string `json:"model"`
	VisionModel    string `json:"vision_model,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// The empty choice uses the configured default; named alternatives need a grant.
func ValidPromptAssistantModelID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
