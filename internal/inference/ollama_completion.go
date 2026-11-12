package inference

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const maxOllamaCompletionTail = 1 << 20

// A tool adapter can close its HTTP response as soon as finish_reason arrives.
// Keep that event and the following usage/DONE events until physical quiescence.
func copyOllamaCompletion(writer io.Writer, reader io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxOllamaCompletionTail)
	scanner.Split(splitOllamaSSE)
	var tail []byte
	terminal := false
	for scanner.Scan() {
		frame := scanner.Bytes()
		terminal = terminal || ollamaSSETerminal(frame)
		if terminal {
			if len(tail)+len(frame) > maxOllamaCompletionTail {
				clear(tail)
				return nil, errors.New("model completion tail exceeds limit")
			}
			tail = append(tail, frame...)
		} else if _, err := writer.Write(frame); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		clear(tail)
		return nil, err
	}
	if !terminal {
		return nil, io.ErrUnexpectedEOF
	}
	return tail, nil
}

func splitOllamaSSE(data []byte, atEOF bool) (int, []byte, error) {
	for i, c := range data {
		if c != '\n' {
			continue
		}
		if i > 0 && data[i-1] == '\n' || i > 2 && bytes.Equal(data[i-3:i+1], []byte("\r\n\r\n")) {
			return i + 1, data[:i+1], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func ollamaSSETerminal(frame []byte) bool {
	var payload []byte
	for _, line := range bytes.Split(frame, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		if len(payload) > 0 {
			payload = append(payload, '\n')
		}
		payload = append(payload, bytes.TrimPrefix(line[5:], []byte(" "))...)
	}
	defer clear(payload)
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return true
	}
	var event struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return false
	}
	for _, choice := range event.Choices {
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			return true
		}
	}
	return false
}
