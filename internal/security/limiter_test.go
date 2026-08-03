package security

import (
	"testing"
	"time"
)

func TestLoginLimiter(t *testing.T) {
	limiter := NewLoginLimiter(time.Minute, 2)
	if !limiter.Allow("client") {
		t.Fatal("new client should be allowed")
	}
	limiter.RecordFailure("client")
	limiter.RecordFailure("client")
	if limiter.Allow("client") {
		t.Fatal("client should be rate limited")
	}
	limiter.Clear("client")
	if !limiter.Allow("client") {
		t.Fatal("cleared client should be allowed")
	}
}

func TestLoginLimiterBoundsTrackedAddresses(t *testing.T) {
	limiter := NewLoginLimiter(10*time.Minute, 3)
	limiter.maxKeys = 2
	limiter.RecordFailure("192.0.2.1")
	limiter.RecordFailure("192.0.2.2")
	limiter.RecordFailure("192.0.2.3")
	if got := len(limiter.failures); got != 2 {
		t.Fatalf("tracked addresses = %d, want 2", got)
	}
	if _, ok := limiter.failures["192.0.2.3"]; ok {
		t.Fatal("new address was tracked after the bound was reached")
	}
}

func TestLoginLimiterSweepsExpiredAddresses(t *testing.T) {
	limiter := NewLoginLimiter(time.Minute, 3)
	limiter.failures["expired"] = []time.Time{time.Now().Add(-2 * time.Minute)}
	limiter.lastSweep = time.Now().Add(-2 * time.Minute)
	limiter.RecordFailure("current")
	if _, ok := limiter.failures["expired"]; ok {
		t.Fatal("expired address was not removed during sweep")
	}
}
