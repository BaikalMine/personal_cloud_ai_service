package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
)

var errComfyPromptRejected = errors.New("ComfyUI отклонил workflow до постановки в очередь")

// Only ComfyUI's pre-queue validation responses prove non-execution. A proxy
// error page, an unknown error or even a truncated 2xx receipt proves nothing.
func comfyPromptValidationRejected(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	var response struct {
		PromptID string `json:"prompt_id"`
		Error    struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil || response.PromptID != "" {
		return false
	}
	switch response.Error.Type {
	case "no_prompt", "invalid_prompt_id", "prompt_no_outputs", "prompt_outputs_failed_validation":
		return true
	default:
		return false
	}
}
