package gateway

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"time"
)

// Only transport timing and byte counts are retained; response content is never stored.
type harnessStreamSummary struct {
	Bytes      int64
	Reads      int64
	Duration   time.Duration
	Idle       time.Duration
	Completion string
}

type harnessStreamObserver struct {
	io.ReadCloser
	ctx     context.Context
	started time.Time
	mu      sync.Mutex
	last    time.Time
	bytes   int64
	reads   int64
	readErr error
	once    sync.Once
	report  func(harnessStreamSummary)
}

func observeHarnessStream(ctx context.Context, body io.ReadCloser, report func(harnessStreamSummary)) io.ReadCloser {
	now := time.Now()
	return &harnessStreamObserver{ReadCloser: body, ctx: ctx, started: now, last: now, report: report}
}

func (s *harnessStreamObserver) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > 0 {
		s.bytes += int64(n)
		s.reads++
		s.last = time.Now()
	}
	if err != nil {
		s.readErr = err
	}
	return n, err
}

func (s *harnessStreamObserver) Close() error {
	err := s.ReadCloser.Close()
	s.once.Do(func() {
		now := time.Now()
		s.mu.Lock()
		completion := "closed"
		switch {
		case errors.Is(s.readErr, io.EOF):
			completion = "eof"
		case s.ctx.Err() != nil:
			completion = "cancelled"
		case s.readErr != nil:
			completion = "read_error"
		}
		summary := harnessStreamSummary{Bytes: s.bytes, Reads: s.reads, Duration: now.Sub(s.started), Idle: now.Sub(s.last), Completion: completion}
		s.mu.Unlock()
		s.report(summary)
	})
	return err
}

func logHarnessStream(summary harnessStreamSummary) {
	log.Printf("Harness model stream: bytes=%d reads=%d duration_ms=%d idle_ms=%d completion=%s",
		summary.Bytes, summary.Reads, summary.Duration.Milliseconds(), summary.Idle.Milliseconds(), summary.Completion)
}
