package gateway

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestGenerationSubmissionFailure(t *testing.T) {
	cause := errors.New("stop rejected: HTTP 409")
	preparation := fmt.Errorf("%w: %w", errGenerationMiningPause, cause)
	for _, failure := range []error{preparation, fmt.Errorf("dispatch: %w", preparation)} {
		code, message := generationSubmissionFailure(failure)
		if code != "mining_pause_failed" || strings.Contains(message, "ComfyUI") || !strings.Contains(message, "Генерация не запускалась") {
			t.Fatalf("incorrect preparation failure: %s, %s", code, message)
		}
		if !errors.Is(failure, cause) {
			t.Fatal("technical cause lost")
		}
	}
	code, message := generationSubmissionFailure(errors.New("ComfyUI connection lost"))
	if code != "comfy_submission_failed" || message != "ComfyUI не подтвердил запуск" {
		t.Fatal("ordinary submission failure changed")
	}
}
