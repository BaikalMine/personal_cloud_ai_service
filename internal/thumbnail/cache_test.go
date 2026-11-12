package thumbnail

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheBoundsExpiryAndCopies(t *testing.T) {
	c := NewCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	expires := now.Add(time.Hour)
	var calls int
	produce := func(context.Context) ([]byte, error) { calls++; return []byte{1, 2, 3}, nil }
	first, err := c.Get(context.Background(), "owner:ref", expires, produce)
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 99
	second, err := c.Get(context.Background(), "owner:ref", expires, produce)
	if err != nil || second[0] != 1 || calls != 1 {
		t.Fatalf("mutable hit: %v %v %d", second, err, calls)
	}
	if _, err := c.Get(context.Background(), "another-owner:ref", expires, produce); err != nil || calls != 2 {
		t.Fatal("owner not separated")
	}
	now = now.Add(cacheTTL + time.Second)
	if _, err := c.Get(context.Background(), "owner:ref", expires, produce); err != nil || calls != 3 {
		t.Fatal("TTL not enforced")
	}
	for i := 0; i < 400; i++ {
		_, err := c.Get(context.Background(), fmt.Sprint(i), expires, produce)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(c.entries) != cacheEntries {
		t.Fatalf("entry bound: %d", len(c.entries))
	}
	large := func(context.Context) ([]byte, error) { return make([]byte, MaxOutputBytes), nil }
	for i := 0; i < 100; i++ {
		if _, err := c.Get(context.Background(), fmt.Sprint("large", i), expires, large); err != nil {
			t.Fatal(err)
		}
	}
	if c.bytes > cacheBytes || len(c.entries) > cacheEntries {
		t.Fatal("cache not bounded")
	}
	now = expires
	if _, err := c.Get(context.Background(), "expired", expires, produce); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired: %v", err)
	}
}

func TestCacheCoalescesAndSerializes(t *testing.T) {
	c := NewCache()
	var active, maxActive, calls atomic.Int32
	produce := func(context.Context) ([]byte, error) {
		n := active.Add(1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		return []byte{1}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := c.Get(context.Background(), fmt.Sprint(i%4), time.Now().Add(time.Hour), produce)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if calls.Load() != 4 || maxActive.Load() != 1 {
		t.Fatalf("calls=%d active=%d", calls.Load(), maxActive.Load())
	}
	if len(c.flights) != 0 || len(c.slot) != 0 || len(c.waiters) != 0 {
		t.Fatal("resources leaked")
	}
}

func TestCacheCancellationFailureRetryAndQueueLimit(t *testing.T) {
	c := NewCache()
	expires := time.Now().Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := c.Get(ctx, "key", expires, func(ctx context.Context) ([]byte, error) { close(started); <-ctx.Done(); return nil, ctx.Err() })
		done <- err
	}()
	<-started
	waitCtx, waitCancel := context.WithCancel(context.Background())
	waitCancel()
	if _, err := c.Get(waitCtx, "key", expires, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter cancel: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(c.entries) != 0 || len(c.flights) != 0 || len(c.slot) != 0 {
		t.Fatal("failed work retained")
	}
	_, err := c.Get(context.Background(), "key", expires, func(context.Context) ([]byte, error) { return []byte{1}, nil })
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	for i := 0; i < maxWaiters; i++ {
		c.waiters <- struct{}{}
	}
	if _, err := c.Get(context.Background(), "key", expires, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("queue bound: %v", err)
	}
	for i := 0; i < maxWaiters; i++ {
		<-c.waiters
	}
	_, err = c.Get(context.Background(), "too-large", expires, func(context.Context) ([]byte, error) { return make([]byte, MaxOutputBytes+1), nil })
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("output bound: %v", err)
	}
}

func TestCacheExpiryDuringProductionAndLRU(t *testing.T) {
	c := NewCache()
	now := time.Now()
	c.now = func() time.Time { return now }
	expires := now.Add(time.Second)
	_, err := c.Get(context.Background(), "expires", expires, func(context.Context) ([]byte, error) { now = expires; return []byte{1}, nil })
	if !errors.Is(err, ErrInvalid) || len(c.entries) != 0 {
		t.Fatal("expired production cached")
	}
	expires = now.Add(time.Hour)
	produce := func(context.Context) ([]byte, error) { return []byte{1}, nil }
	for i := 0; i < cacheEntries; i++ {
		if _, err := c.Get(context.Background(), fmt.Sprint(i), expires, produce); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = c.Get(context.Background(), "0", expires, produce)
	_, _ = c.Get(context.Background(), "new", expires, produce)
	if _, ok := c.entries["0"]; !ok {
		t.Fatal("recent entry evicted")
	}
	if _, ok := c.entries["1"]; ok {
		t.Fatal("old entry not evicted")
	}
}
