package gateway

import (
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"ai-access-gateway/internal/domain"
)

const correlationHeader = "X-Correlation-ID"

func configureGatewayLogging() *slog.Logger {
	logger := newStructuredLogger(os.Stdout)
	slog.SetDefault(logger)
	log.SetFlags(0)
	log.SetOutput(legacySlogWriter{logger: logger})
	return logger
}

func newStructuredLogger(writer io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

type legacySlogWriter struct {
	logger *slog.Logger
}

func (writer legacySlogWriter) Write(body []byte) (int, error) {
	message := strings.TrimSpace(string(body))
	if message != "" && writer.logger != nil {
		writer.logger.Info("legacy gateway log", "event", "legacy_log", "detail", message)
	}
	return len(body), nil
}

func traceContext(ctx context.Context, correlationID string, generationJobID int64, promptID string) context.Context {
	if validCorrelationID(correlationID) {
		ctx = context.WithValue(ctx, correlationIDKey, correlationID)
	}
	if generationJobID > 0 {
		ctx = context.WithValue(ctx, generationJobKey, generationJobID)
	}
	if strings.TrimSpace(promptID) != "" {
		ctx = context.WithValue(ctx, comfyPromptIDKey, strings.TrimSpace(promptID))
	}
	return ctx
}

func generationJobTraceContext(ctx context.Context, job domain.GenerationJob) context.Context {
	return traceContext(ctx, job.CorrelationID, job.ID, job.PromptID)
}

func correlationIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(correlationIDKey).(string)
	return value
}

func correlationID(r *http.Request) string {
	if r == nil {
		return ""
	}
	return correlationIDFromContext(r.Context())
}

func generationJobIDFromContext(ctx context.Context) *int64 {
	if ctx == nil {
		return nil
	}
	value, ok := ctx.Value(generationJobKey).(int64)
	if !ok || value <= 0 {
		return nil
	}
	return &value
}

func promptIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(comfyPromptIDKey).(string)
	return value
}

func validCorrelationID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 16 || len(value) > 96 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func logGateway(ctx context.Context, level slog.Level, event, message string, attrs ...any) {
	base := []any{"event", event}
	if requestID := requestIDFromContext(ctx); requestID != "" {
		base = append(base, "request_id", requestID)
	}
	if correlation := correlationIDFromContext(ctx); correlation != "" {
		base = append(base, "correlation_id", correlation)
	}
	if jobID := generationJobIDFromContext(ctx); jobID != nil {
		base = append(base, "generation_job_id", *jobID)
	}
	if promptID := promptIDFromContext(ctx); promptID != "" {
		base = append(base, "comfy_prompt_id", promptID)
	}
	base = append(base, attrs...)
	slog.Default().Log(ctx, level, message, base...)
}

func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func requestLogLevel(path string, status int) slog.Level {
	if status >= http.StatusInternalServerError {
		return slog.LevelError
	}
	if status >= http.StatusBadRequest {
		return slog.LevelWarn
	}
	if strings.HasPrefix(path, "/static/") || path == "/healthz" || path == "/readyz" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func logHTTPRequest(ctx context.Context, r *http.Request, status int, bytesOut int64, started time.Time) {
	logGateway(ctx, requestLogLevel(r.URL.Path, status), "http_request", "HTTP request completed",
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"duration_ms", time.Since(started).Milliseconds(),
		"bytes_out", bytesOut,
	)
}
