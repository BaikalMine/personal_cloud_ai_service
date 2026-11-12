package thumbnail

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	cacheEntries = 256
	cacheBytes   = 8 << 20
	cacheTTL     = time.Minute
	maxWaiters   = 64
)

var ErrBusy = errors.New("thumbnail: busy")

type entry struct {
	data    []byte
	expires time.Time
	used    uint64
}

type flight struct {
	done chan struct{}
	data []byte
	err  error
}

// Cache is process-local, bounded, and deliberately has no disk persistence.
// Keys must include the owner and immutable source reference. Authorization is
// the caller's responsibility before Get and again before sending its result.
type Cache struct {
	mu      sync.Mutex
	entries map[string]entry
	flights map[string]*flight
	bytes   int
	clock   uint64
	slot    chan struct{}
	waiters chan struct{}
	now     func() time.Time
}

func NewCache() *Cache {
	return &Cache{entries: make(map[string]entry), flights: make(map[string]*flight),
		slot: make(chan struct{}, 1), waiters: make(chan struct{}, maxWaiters), now: time.Now}
}

// Get coalesces same-key requests and serializes production, including source
// materialization/cleanup inside produce. Cancelled leaders do not poison retry.
func (c *Cache) Get(ctx context.Context, key string, availableUntil time.Time, produce func(context.Context) ([]byte, error)) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if key == "" || !availableUntil.After(c.now()) {
		return nil, ErrInvalid
	}
	select {
	case c.waiters <- struct{}{}:
		defer func() { <-c.waiters }()
	default:
		return nil, ErrBusy
	}
	c.mu.Lock()
	now := c.now()
	for k, e := range c.entries {
		if !e.expires.After(now) {
			c.bytes -= len(e.data)
			delete(c.entries, k)
		}
	}
	c.clock++
	if e, ok := c.entries[key]; ok {
		e.used = c.clock
		c.entries[key] = e
		data := append([]byte(nil), e.data...)
		c.mu.Unlock()
		return data, nil
	}
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.done:
			return append([]byte(nil), f.data...), f.err
		}
	}
	f := &flight{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()

	var data []byte
	var err error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case c.slot <- struct{}{}:
		if err = ctx.Err(); err == nil {
			if !availableUntil.After(c.now()) {
				err = ErrInvalid
			} else {
				data, err = produce(ctx)
			}
		}
		<-c.slot
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (len(data) == 0 || len(data) > MaxOutputBytes) {
		err = ErrLimit
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && !availableUntil.After(c.now()) {
		err = ErrInvalid
	}
	if err == nil {
		data = append([]byte(nil), data...)
		for len(c.entries) >= cacheEntries || c.bytes+len(data) > cacheBytes {
			oldKey, oldest := "", ^uint64(0)
			for k, e := range c.entries {
				if e.used < oldest {
					oldKey, oldest = k, e.used
				}
			}
			c.bytes -= len(c.entries[oldKey].data)
			delete(c.entries, oldKey)
		}
		expires := c.now().Add(cacheTTL)
		if availableUntil.Before(expires) {
			expires = availableUntil
		}
		c.clock++
		c.entries[key] = entry{data: data, expires: expires, used: c.clock}
		c.bytes += len(data)
		f.data = data
	}
	f.err = err
	delete(c.flights, key)
	close(f.done)
	return append([]byte(nil), f.data...), err
}
