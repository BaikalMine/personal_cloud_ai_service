package security

import (
	"sync"
	"time"
)

type LoginLimiter struct {
	mu        sync.Mutex
	failures  map[string][]time.Time
	window    time.Duration
	limit     int
	maxKeys   int
	lastSweep time.Time
}

const defaultLoginLimiterMaxKeys = 10000

func NewLoginLimiter(window time.Duration, limit int) *LoginLimiter {
	if window <= 0 {
		window = 10 * time.Minute
	}
	if limit <= 0 {
		limit = 10
	}
	return &LoginLimiter{
		failures:  make(map[string][]time.Time),
		window:    window,
		limit:     limit,
		maxKeys:   defaultLoginLimiterMaxKeys,
		lastSweep: time.Now(),
	}
}

func (l *LoginLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := time.Now().Add(-l.window)
	recent := l.failures[key][:0]
	for _, failure := range l.failures[key] {
		if failure.After(cutoff) {
			recent = append(recent, failure)
		}
	}
	if len(recent) == 0 {
		delete(l.failures, key)
	} else {
		l.failures[key] = recent
	}
	return len(recent) < l.limit
}

func (l *LoginLimiter) RecordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.sweepLocked(now)
	if _, tracked := l.failures[key]; !tracked && len(l.failures) >= l.maxKeys {
		return
	}
	l.failures[key] = append(l.failures[key], now)
}

func (l *LoginLimiter) Clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

func (l *LoginLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	cutoff := now.Add(-l.window)
	for key, failures := range l.failures {
		recent := failures[:0]
		for _, failure := range failures {
			if failure.After(cutoff) {
				recent = append(recent, failure)
			}
		}
		if len(recent) == 0 {
			delete(l.failures, key)
		} else {
			l.failures[key] = recent
		}
	}
	l.lastSweep = now
}
