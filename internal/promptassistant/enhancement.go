package promptassistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// The request body is persisted separately. Response metadata contains no image
// bytes and preserves the exact reference map even if workflow defaults change.
type PreparedEnhancement struct {
	LongParts  int                      `json:"long_parts,omitempty"`
	LongAudio  bool                     `json:"long_audio,omitempty"`
	LTXAudio   bool                     `json:"ltx_audio,omitempty"`
	Body       []byte                   `json:"-"`
	Model      string                   `json:"model"`
	Mode       Mode                     `json:"mode"`
	Policy     RequestPolicy            `json:"policy"`
	References []ReferenceUnderstanding `json:"references"`
}

func (prepared PreparedEnhancement) ResponseByteLimit() int {
	if prepared.Mode == ModeTextToVideo && ValidLongVideoParts(prepared.LongParts) {
		return 512 << 10
	}
	return maxResponseBytes
}

func ParseEnhancementResponse(prepared PreparedEnhancement, status int, responseBody []byte) (output Result, err error) {
	output = Result{Model: prepared.Model, Policy: prepared.Policy, LongParts: prepared.LongParts, Execution: ExecutionUnconfirmed}
	if len(responseBody) > prepared.ResponseByteLimit() {
		return output, errors.New("локальная модель вернула слишком большой ответ")
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return output, fmt.Errorf("локальная модель ответила HTTP %d", status)
	}
	var response chatResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return output, fmt.Errorf("некорректный ответ локальной модели: %w", err)
	}
	if response.Done {
		output.Execution = ExecutionCompleted
	}
	output.Usage = Usage{
		PromptTokens: response.PromptEvalCount, CompletionTokens: response.EvalCount,
		TotalDurationMS: durationMilliseconds(response.TotalDuration), LoadDurationMS: durationMilliseconds(response.LoadDuration),
		PromptDurationMS: durationMilliseconds(response.PromptEvalDuration), CompletionTimeMS: durationMilliseconds(response.EvalDuration),
		DoneReason: strings.TrimSpace(response.DoneReason),
	}
	if prepared.LongParts > 0 && output.Usage.DoneReason == "length" {
		return output, errors.New("план длинного видео не завершён: модель исчерпала лимит ответа; сократите запрос или повторите без рассуждений")
	}
	if prepared.LongParts > 0 && !response.Done {
		return output, errors.New("завершение плана длинного видео не подтверждено")
	}
	if prepared.LongAudio {
		response.Message.Content, err = assembleLongVideoContent(response.Message.Content)
		if err != nil {
			return output, err
		}
	}
	parsed, err := parseModelResultWithLimit(response.Message.Content, prepared.References, PromptCharacterLimitForVideo(prepared.Mode, prepared.LongParts))
	if err != nil {
		return output, err
	}
	if prepared.LongParts > 0 {
		if err := ValidateLongVideoTimeline(parsed.Prompt, prepared.LongParts); err != nil {
			return output, err
		}
	}
	output.Prompt, output.NegativePrompt, output.References = parsed.Prompt, parsed.NegativePrompt, parsed.References
	if prepared.LTXAudio {
		if parsed.AudioPrompt == "" {
			return output, ErrMissingAudioPrompt
		}
		output.AudioPrompt = parsed.AudioPrompt
	}
	return output, nil
}
