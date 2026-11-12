package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/modelruntime"
	"ai-access-gateway/internal/promptassistant"
)

const maxAssistantStreamBytes int64 = 16 << 20

func streamRequest(body []byte) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	payload["stream"] = json.RawMessage("true")
	return json.Marshal(payload)
}

// Decode only a top-level prompt string, never model thinking or raw JSON.
func partialPrompt(content string) string {
	content = strings.TrimSpace(content)
	if end := strings.LastIndex(content, "</think>"); end >= 0 {
		content = strings.TrimSpace(content[end+len("</think>"):])
	} else if strings.HasPrefix(content, "<think") || content != "" && strings.HasPrefix("<think>", content) {
		return ""
	}
	if strings.HasPrefix(content, "```") {
		newline := strings.IndexByte(content, '\n')
		if newline < 0 {
			return ""
		}
		content = strings.TrimSpace(strings.TrimSuffix(content[newline+1:], "```"))
	}
	if content == "" || strings.HasPrefix(content, "[") {
		return ""
	}
	if !strings.HasPrefix(content, "{") {
		return strings.Trim(content, "\"` ")
	}
	decoder := json.NewDecoder(strings.NewReader(content))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return ""
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return ""
		}
		offset := decoder.InputOffset()
		if key == "prompt" {
			var value string
			if err = decoder.Decode(&value); err == nil {
				return value
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
				return ""
			}
			raw := strings.TrimSpace(content[offset:])
			raw = strings.TrimSpace(strings.TrimPrefix(raw, ":"))
			if !strings.HasPrefix(raw, `"`) {
				return ""
			}
			// A streamed JSON string may end inside an escape sequence.
			for trim := 0; trim < 12 && trim < len(raw)-1; trim++ {
				if json.Unmarshal([]byte(raw[:len(raw)-trim]+`"`), &value) == nil {
					return value
				}
			}
			return ""
		}
		var skip json.RawMessage
		if decoder.Decode(&skip) != nil {
			return ""
		}
	}
	return ""
}

func consumeChatStream(reader io.Reader, publish func(AssistantProgress)) ([]byte, error) {
	limited := &io.LimitedReader{R: reader, N: maxAssistantStreamBytes + 1}
	decoder := json.NewDecoder(limited)
	var content strings.Builder
	var final map[string]json.RawMessage
	for {
		var chunk struct {
			Message struct {
				Content  string `json:"content"`
				Thinking string `json:"thinking"`
			} `json:"message"`
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if errors.Is(err, io.EOF) && final != nil && limited.N > 0 {
			break
		}
		if err != nil || limited.N <= 0 || final != nil || json.Unmarshal(raw, &chunk) != nil || chunk.Error != "" {
			return nil, errors.New("incomplete or invalid model stream")
		}
		if content.Len()+len(chunk.Message.Content) > domain.MaxGPUInferenceResponseBytes {
			return nil, errors.New("model stream content exceeds limit")
		}
		content.WriteString(chunk.Message.Content)
		phase := "writing"
		if content.Len() == 0 && chunk.Message.Thinking != "" {
			phase = "thinking"
		}
		progress := AssistantProgress{Phase: phase, Prompt: partialPrompt(content.String())}
		if chunk.Done {
			if json.Unmarshal(raw, &final) != nil {
				return nil, errors.New("invalid model completion")
			}
			message, err := json.Marshal(map[string]string{"role": "assistant", "content": content.String()})
			if err != nil {
				return nil, err
			}
			final["message"] = message
			var metrics struct {
				Prompt         int   `json:"prompt_eval_count"`
				Completion     int   `json:"eval_count"`
				Total          int64 `json:"total_duration"`
				Load           int64 `json:"load_duration"`
				PromptTime     int64 `json:"prompt_eval_duration"`
				CompletionTime int64 `json:"eval_duration"`
			}
			if json.Unmarshal(raw, &metrics) != nil {
				return nil, errors.New("invalid model metrics")
			}
			progress.Phase = "finishing"
			progress.Usage = promptassistant.Usage{PromptTokens: metrics.Prompt, CompletionTokens: metrics.Completion,
				TotalDurationMS: metrics.Total / int64(1000000), LoadDurationMS: metrics.Load / int64(1000000),
				PromptDurationMS: metrics.PromptTime / int64(1000000), CompletionTimeMS: metrics.CompletionTime / int64(1000000)}
		}
		publish(progress)
	}
	result, err := json.Marshal(final)
	if err != nil || len(result) > domain.MaxGPUInferenceResponseBytes {
		return nil, errors.New("model response exceeds limit")
	}
	return result, nil
}

func (w *Worker) executeStreaming(ctx context.Context, job domain.GPUInference, body []byte) (int, []byte, string) {
	requestBody, err := streamRequest(body)
	if err != nil {
		return 0, nil, "request_not_dispatched"
	}
	defer clear(requestBody)
	publish := w.progressWriter(ctx, job)
	var assembled []byte
	consume := func(status int, _ http.Header, reader io.Reader) error {
		if status < 200 || status >= 300 {
			return errors.New("model HTTP failure")
		}
		var err error
		assembled, err = consumeChatStream(reader, publish)
		return err
	}
	if w.owned != nil {
		runtime, ok := w.owned.runtime.(ownedHTTPRuntime)
		if !ok {
			return w.executeOwned(ctx, body)
		}
		result, err := runtime.ExecuteHTTP(ctx, modelruntime.HTTPRequest{Method: http.MethodPost, Path: "/api/chat", Body: requestBody, ResponseLimit: maxAssistantStreamBytes}, consume)
		if !result.Stopped {
			w.invalidateOwned()
			clear(assembled)
			return result.Status, nil, ""
		}
		if !result.Dispatched {
			clear(assembled)
			return 0, nil, "request_not_dispatched"
		}
		if err != nil {
			clear(assembled)
			return 0, nil, "executor_stopped"
		}
		return result.Status, assembled, "executor_terminal"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.target, bytes.NewReader(requestBody))
	if err != nil {
		return 0, nil, "request_not_dispatched"
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/x-ndjson")
	response, err := w.http.Do(request)
	if err != nil {
		return 0, nil, ""
	}
	defer response.Body.Close()
	if err = consume(response.StatusCode, response.Header, response.Body); err != nil {
		clear(assembled)
		return response.StatusCode, nil, ""
	}
	return response.StatusCode, assembled, "executor_terminal"
}
