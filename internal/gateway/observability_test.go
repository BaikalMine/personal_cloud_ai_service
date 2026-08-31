package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServiceLatencyRegistryBuildsCumulativeBuckets(t *testing.T) {
	registry := newServiceLatencyRegistry()
	registry.observe("ollama", "enhance_video", 5*time.Millisecond)
	registry.observe("ollama", "enhance_video", 26*time.Millisecond)
	registry.observe("ollama", "enhance_video", 2*time.Second)

	snapshots := registry.snapshot()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	snapshot := snapshots[0]
	if snapshot.Count != 3 || snapshot.SumMS != 2031 {
		t.Fatalf("latency totals = %+v", snapshot)
	}
	if snapshot.Buckets[0] != 1 || snapshot.Buckets[1] != 1 || snapshot.Buckets[2] != 2 || snapshot.Buckets[7] != 3 {
		t.Fatalf("cumulative buckets = %v", snapshot.Buckets)
	}
}

func TestServiceObservationOutcome(t *testing.T) {
	timeout := &net.DNSError{IsTimeout: true}
	for name, test := range map[string]struct {
		err           error
		misconfigured bool
		want          string
	}{
		"ok":              {want: "ok"},
		"error":           {err: errors.New("failed"), want: "error"},
		"deadline":        {err: context.DeadlineExceeded, want: "timeout"},
		"network timeout": {err: timeout, want: "timeout"},
		"misconfigured":   {err: errors.New("unauthorized"), misconfigured: true, want: "misconfigured"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := serviceObservationOutcome(test.err, test.misconfigured); got != test.want {
				t.Fatalf("outcome = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFormatDurationUsesReadableUnits(t *testing.T) {
	for input, want := range map[int64]string{
		430:     "430 мс",
		12800:   "12.80 с",
		74000:   "1 мин. 14 сек.",
		1274000: "21 мин. 14 сек.",
	} {
		if got := formatDuration(input); got != want {
			t.Fatalf("formatDuration(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestReadinessRequiresOnlyDatabase(t *testing.T) {
	dependencies := []DependencyStatus{{Key: dependencyComfyUI, State: DependencyOffline}}
	if status, degraded := readinessStatus(nil, dependencies); status != http.StatusOK || !degraded {
		t.Fatalf("optional dependency status = %d degraded=%v", status, degraded)
	}
	if status, degraded := readinessStatus(errors.New("database unavailable"), nil); status != http.StatusServiceUnavailable || degraded {
		t.Fatalf("database failure status = %d degraded=%v", status, degraded)
	}
}
