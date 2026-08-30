package security

import (
	"sync"
	"time"
)

type LoginLimiter struct {
	mu            sync.Mutex
	failures      map[string][]time.Time
	window        time.Duration
	limit         int
	maxKeys       int
	overflow      []time.Time
	overflowLimit int
	lastSweep     time.Time
}

const (
	defaultLoginLimiterMaxKeys       = 10000
	defaultLoginLimiterOverflowLimit = 1000
	loginLimiterOverflowWindow       = time.Minute
)

func NewLoginLimiter(window time.Duration, limit int) *LoginLimiter {
	if window <= 0 {
		window = 10 * time.Minute
	}
	if limit <= 0 {
		limit = 10
	}
	return &LoginLimiter{
		failures:      make(map[string][]time.Time),
		window:        window,
		limit:         limit,
		maxKeys:       defaultLoginLimiterMaxKeys,
		overflowLimit: defaultLoginLimiterOverflowLimit,
		lastSweep:     time.Now(),
	}
}

func (l *LoginLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.sweepLocked(now)
	if _, tracked := l.failures[key]; !tracked && len(l.failures) >= l.maxKeys {
		l.sweepOverflowLocked(now)
		return len(l.overflow) < l.overflowLimit
	}
	cutoff := now.Add(-l.window)
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
		l.sweepOverflowLocked(now)
		if len(l.overflow) < l.overflowLimit {
			l.overflow = append(l.overflow, now)
		}
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
	l.sweepOverflowLocked(now)
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

func (l *LoginLimiter) sweepOverflowLocked(now time.Time) {
	cutoff := now.Add(-loginLimiterOverflowWindow)
	recent := l.overflow[:0]
	for _, failure := range l.overflow {
		if failure.After(cutoff) {
			recent = append(recent, failure)
		}
	}
	l.overflow = recent
}
