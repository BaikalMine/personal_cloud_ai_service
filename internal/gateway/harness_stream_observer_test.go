package gateway

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestHarnessStreamObserverPreservesDataAndReportsEOF(t *testing.T) {
	var reports []harnessStreamSummary
	stream := observeHarnessStream(context.Background(), io.NopCloser(strings.NewReader("private stream data")), func(s harnessStreamSummary) { reports = append(reports, s) })
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "private stream data" {
		t.Fatalf("stream changed: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Bytes != int64(len(data)) || reports[0].Reads == 0 || reports[0].Completion != "eof" {
		t.Fatalf("unexpected summary: %+v", reports)
	}
}

func TestHarnessStreamObserverReportsCancellationOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var reports []harnessStreamSummary
	stream := observeHarnessStream(ctx, io.NopCloser(strings.NewReader("")), func(s harnessStreamSummary) { reports = append(reports, s) })
	cancel()
	_ = stream.Close()
	_ = stream.Close()
	if len(reports) != 1 || reports[0].Completion != "cancelled" || reports[0].Bytes != 0 {
		t.Fatalf("unexpected summary: %+v", reports)
	}
}
