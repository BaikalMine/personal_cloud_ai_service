package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestStructuredLogIncludesEndToEndTrace(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(newStructuredLogger(&output))
	defer slog.SetDefault(previous)

	ctx := context.WithValue(context.Background(), requestIDKey, "request-1234567890")
	ctx = traceContext(ctx, "correlation-1234567890", 42, "prompt-1234567890")
	logGateway(ctx, slog.LevelInfo, "generation_test", "Generation trace", "state", "queued")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode structured log: %v: %s", err, output.String())
	}
	for key, expected := range map[string]any{
		"event": "generation_test", "request_id": "request-1234567890",
		"correlation_id": "correlation-1234567890", "generation_job_id": float64(42),
		"comfy_prompt_id": "prompt-1234567890", "state": "queued",
	} {
		if record[key] != expected {
			t.Fatalf("%s = %#v, want %#v; record=%v", key, record[key], expected, record)
		}
	}
}

func TestValidCorrelationID(t *testing.T) {
	for _, value := range []string{"0123456789abcdef", "assistant_trace-1234567890"} {
		if !validCorrelationID(value) {
			t.Fatalf("valid correlation ID rejected: %q", value)
		}
	}
	for _, value := range []string{"short", "contains spaces 123456", "../../invalid-correlation"} {
		if validCorrelationID(value) {
			t.Fatalf("invalid correlation ID accepted: %q", value)
		}
	}
}
