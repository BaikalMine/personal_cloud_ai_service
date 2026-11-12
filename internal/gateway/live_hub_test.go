package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/store"
)

func liveReceive(t *testing.T, sub *liveSubscription) store.LiveRevisionSnapshot {
	t.Helper()
	select {
	case state, ok := <-sub.states:
		if !ok {
			t.Fatal("subscription closed unexpectedly")
		}
		return state
	case <-time.After(3 * time.Second):
		t.Fatal("subscription timed out")
	}
	return store.LiveRevisionSnapshot{}
}

func TestLiveHubDeduplicatesBatchesAndCoalesces(t *testing.T) {
	var revision atomic.Int64
	revision.Store(1)
	var calls atomic.Int64
	h := newLiveHub(func(ctx context.Context, hashes []string) (map[string]store.LiveRevisionSnapshot, error) {
		calls.Add(1)
		if len(hashes) > store.LiveRevisionBatchLimit {
			t.Error("unbounded batch")
		}
		result := make(map[string]store.LiveRevisionSnapshot)
		for _, hash := range hashes {
			result[hash] = store.LiveRevisionSnapshot{Tasks: revision.Load()}
		}
		return result, nil
	}, 10*time.Millisecond)
	defer h.close()
	var subs []*liveSubscription
	for i := 0; i < 600; i++ {
		sub, err := h.subscribe(fmt.Sprint(i % 300))
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, sub)
	}
	for _, sub := range subs {
		if liveReceive(t, sub).Tasks != 1 {
			t.Fatal("initial revision lost")
		}
	}
	if calls.Load() > 4 {
		t.Fatalf("poll scaled by subscribers: %d", calls.Load())
	}
	revision.Store(99)
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.mu.Lock()
		ready := len(subs[0].states) == 1
		h.mu.Unlock()
		if ready && liveReceive(t, subs[0]).Tasks == 99 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("latest state never arrived")
		}
		time.Sleep(time.Millisecond)
	}
	// Do not read the other 599 subscribers while multiple polls run.
	time.Sleep(50 * time.Millisecond)
	for _, sub := range subs[1:] {
		// Observe between replacements, not between the hub's drain and send.
		h.mu.Lock()
		pending := len(sub.states)
		var state store.LiveRevisionSnapshot
		if pending > 0 {
			state = <-sub.states
		}
		h.mu.Unlock()
		if pending != 1 || state.Tasks != 99 {
			t.Fatal("slow consumer did not coalesce to latest state")
		}
	}
}

func TestLiveHubRevocationErrorBackoffAndRestart(t *testing.T) {
	var mode atomic.Int64
	var calls atomic.Int64
	h := newLiveHub(func(context.Context, []string) (map[string]store.LiveRevisionSnapshot, error) {
		calls.Add(1)
		if mode.Load() == 2 {
			return nil, errors.New("unavailable")
		}
		if mode.Load() == 1 {
			return map[string]store.LiveRevisionSnapshot{}, nil
		}
		return map[string]store.LiveRevisionSnapshot{"hash": {Tasks: 7}}, nil
	}, 5*time.Millisecond)
	defer h.close()
	closed := func(sub *liveSubscription) {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case _, ok := <-sub.states:
				if !ok {
					return
				}
			case <-timer.C:
				t.Fatal("revoked stream remained open")
			}
		}
	}
	sub, _ := h.subscribe("hash")
	liveReceive(t, sub)
	mode.Store(1)
	closed(sub)
	mode.Store(0)
	for i := 0; i < 20; i++ {
		sub, err := h.subscribe("hash")
		if err != nil {
			t.Fatal(err)
		}
		liveReceive(t, sub)
		h.unsubscribe(sub)
		time.Sleep(6 * time.Millisecond)
	}
	sub, _ = h.subscribe("hash")
	liveReceive(t, sub)
	mode.Store(2)
	closed(sub)
	before := calls.Load()
	for i := 0; i < 50; i++ {
		if _, err := h.subscribe("hash"); err == nil {
			t.Fatal("database failure did not back off registration")
		}
	}
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != before {
		t.Fatal("reconnect burst polled failed database")
	}
	h.close()
	if _, err := h.subscribe("hash"); err == nil {
		t.Fatal("closed hub restarted")
	}
}

func TestLiveHubConcurrentRegistrationAndShutdown(t *testing.T) {
	entered := make(chan struct{})
	h := newLiveHub(func(ctx context.Context, hashes []string) (map[string]store.LiveRevisionSnapshot, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}, time.Millisecond)
	sub, _ := h.subscribe("hash")
	<-entered
	var workers sync.WaitGroup
	for i := 0; i < 100; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s, err := h.subscribe("hash")
			if err == nil {
				h.unsubscribe(s)
			}
		}()
	}
	h.close()
	workers.Wait()
	if _, ok := <-sub.states; ok {
		t.Fatal("shutdown retained pending state")
	}
	deadline := time.Now().Add(time.Second)
	for {
		h.mu.Lock()
		running := h.running
		count := len(h.subs)
		h.mu.Unlock()
		if !running && count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hub did not stop")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLiveHubLimit(t *testing.T) {
	h := newLiveHub(nil, time.Hour)
	defer h.close()
	for i := 0; i < liveSubscriberLimit; i++ {
		if _, err := h.subscribe("hash"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.subscribe("hash"); err == nil {
		t.Fatal("unbounded subscription count")
	}
}
