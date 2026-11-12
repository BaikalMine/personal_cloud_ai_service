package gateway

import (
	"context"
	"errors"
	"sync"
	"time"

	"ai-access-gateway/internal/store"
)

const livePollInterval = 750 * time.Millisecond
const liveSubscriberLimit = 4096

type liveRevisionReader func(context.Context, []string) (map[string]store.LiveRevisionSnapshot, error)

type liveSubscription struct {
	hash   string
	states chan store.LiveRevisionSnapshot
}

// One loop owns polling for all four topics. It exists only while subscribed;
// registration waits for the next tick so a burst of tabs is a single batch.
type liveHub struct {
	mu               sync.Mutex
	read             liveRevisionReader
	interval         time.Duration
	subs             map[*liveSubscription]struct{}
	running, stopped bool
	cancel           context.CancelFunc
	nextAttempt      time.Time
	failures         int
}

func newLiveHub(read liveRevisionReader, interval time.Duration) *liveHub {
	return &liveHub{read: read, interval: interval, subs: make(map[*liveSubscription]struct{})}
}

func (a *App) liveUpdates() *liveHub {
	a.liveOnce.Do(func() {
		a.live = newLiveHub(func(ctx context.Context, hashes []string) (map[string]store.LiveRevisionSnapshot, error) {
			return a.store.LiveRevisions(ctx, hashes, a.cfg.SessionIdleTimeout)
		}, livePollInterval)
	})
	return a.live
}

func (h *liveHub) subscribe(hash string) (*liveSubscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if hash == "" || h.stopped || time.Now().Before(h.nextAttempt) || len(h.subs) >= liveSubscriberLimit {
		return nil, errors.New("live stream unavailable")
	}
	s := &liveSubscription{hash: hash, states: make(chan store.LiveRevisionSnapshot, 1)}
	h.subs[s] = struct{}{}
	if !h.running {
		h.running = true
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		go h.run(ctx)
	}
	return s, nil
}

func (h *liveHub) unsubscribe(s *liveSubscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.remove(s)
}

func (h *liveHub) remove(s *liveSubscription) {
	if _, exists := h.subs[s]; !exists {
		return
	}
	delete(h.subs, s)
	select {
	case <-s.states:
	default:
	}
	close(s.states)
}

func (h *liveHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = true
	if h.cancel != nil {
		h.cancel()
	}
	for s := range h.subs {
		h.remove(s)
	}
}

func (h *liveHub) run(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			h.running = false
			h.mu.Unlock()
			return
		case <-ticker.C:
			h.mu.Lock()
			if len(h.subs) == 0 || h.stopped {
				// Set under the same lock as subscribe, preventing a lost restart.
				h.running = false
				h.cancel()
				h.mu.Unlock()
				return
			}
			if time.Now().Before(h.nextAttempt) {
				h.mu.Unlock()
				continue
			}
			members := make([]*liveSubscription, 0, len(h.subs))
			unique := make(map[string]struct{})
			for s := range h.subs {
				members = append(members, s)
				unique[s.hash] = struct{}{}
			}
			h.mu.Unlock()
			hashes := make([]string, 0, len(unique))
			for hash := range unique {
				hashes = append(hashes, hash)
			}
			states := make(map[string]store.LiveRevisionSnapshot, len(unique))
			var err error
			queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			for start := 0; start < len(hashes); start += store.LiveRevisionBatchLimit {
				end := min(start+store.LiveRevisionBatchLimit, len(hashes))
				var batch map[string]store.LiveRevisionSnapshot
				batch, err = h.read(queryCtx, hashes[start:end])
				if err != nil {
					break
				}
				for hash, state := range batch {
					states[hash] = state
				}
			}
			cancel()
			h.mu.Lock()
			if err != nil {
				h.failures = min(h.failures+1, 5)
				h.nextAttempt = time.Now().Add(time.Second * time.Duration(1<<h.failures))
				for s := range h.subs {
					h.remove(s)
				}
			} else {
				h.failures = 0
				h.nextAttempt = time.Time{}
			}
			for _, s := range members {
				if _, exists := h.subs[s]; !exists {
					continue
				}
				state, valid := states[s.hash]
				if err != nil || !valid {
					h.remove(s)
					continue
				}
				select {
				case <-s.states:
				default:
				}
				s.states <- state
			}
			h.mu.Unlock()
		}
	}
}
