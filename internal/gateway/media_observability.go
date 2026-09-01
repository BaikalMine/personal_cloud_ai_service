package gateway

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultMediaInFlightBytes = int64(256 << 20)

var (
	mediaDurationBucketsMS = []int64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000}
	mediaSizeBucketsBytes  = []int64{64 << 10, 256 << 10, 1 << 20, 4 << 20, 16 << 20, 32 << 20, 64 << 20, 128 << 20, 256 << 20, 512 << 20}
)

type mediaOperationKey struct {
	Stage   string
	Outcome string
}

type mediaOperationValue struct {
	Count           int64
	BytesTotal      int64
	DurationSumMS   int64
	DurationBuckets []int64
	SizeBuckets     []int64
}

type mediaOperationSnapshot struct {
	Stage           string
	Outcome         string
	Count           int64
	BytesTotal      int64
	DurationSumMS   int64
	DurationBuckets []int64
	SizeBuckets     []int64
}

type mediaOperationRegistry struct {
	mu     sync.RWMutex
	values map[mediaOperationKey]*mediaOperationValue
}

func newMediaOperationRegistry() *mediaOperationRegistry {
	return &mediaOperationRegistry{values: make(map[mediaOperationKey]*mediaOperationValue)}
}

func (registry *mediaOperationRegistry) observe(stage string, sizeBytes int64, duration time.Duration, operationErr error) {
	if registry == nil {
		return
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return
	}
	outcome := "ok"
	if operationErr != nil {
		outcome = "error"
	}
	if sizeBytes < 0 {
		sizeBytes = 0
	}
	durationMS := max(int64(0), duration.Milliseconds())
	key := mediaOperationKey{Stage: stage, Outcome: outcome}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	value := registry.values[key]
	if value == nil {
		value = &mediaOperationValue{
			DurationBuckets: make([]int64, len(mediaDurationBucketsMS)),
			SizeBuckets:     make([]int64, len(mediaSizeBucketsBytes)),
		}
		registry.values[key] = value
	}
	value.Count++
	value.BytesTotal += sizeBytes
	value.DurationSumMS += durationMS
	for index, upperBound := range mediaDurationBucketsMS {
		if durationMS <= upperBound {
			value.DurationBuckets[index]++
		}
	}
	for index, upperBound := range mediaSizeBucketsBytes {
		if sizeBytes <= upperBound {
			value.SizeBuckets[index]++
		}
	}
}

func (registry *mediaOperationRegistry) snapshot() []mediaOperationSnapshot {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]mediaOperationSnapshot, 0, len(registry.values))
	for key, value := range registry.values {
		result = append(result, mediaOperationSnapshot{
			Stage: key.Stage, Outcome: key.Outcome, Count: value.Count,
			BytesTotal: value.BytesTotal, DurationSumMS: value.DurationSumMS,
			DurationBuckets: append([]int64(nil), value.DurationBuckets...),
			SizeBuckets:     append([]int64(nil), value.SizeBuckets...),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Stage == result[j].Stage {
			return result[i].Outcome < result[j].Outcome
		}
		return result[i].Stage < result[j].Stage
	})
	return result
}

func (a *App) mediaOperationRegistry() *mediaOperationRegistry {
	if a == nil {
		return nil
	}
	a.mediaObservabilityOnce.Do(func() {
		if a.mediaOperations == nil {
			a.mediaOperations = newMediaOperationRegistry()
		}
		if a.mediaBytes == nil {
			a.mediaBytes = newWeightedByteLimiter(a.cfg.MediaInFlightLimitBytes)
		}
	})
	return a.mediaOperations
}

func (a *App) observeMediaOperation(stage string, sizeBytes int64, started time.Time, operationErr error) {
	a.mediaOperationRegistry().observe(stage, sizeBytes, time.Since(started), operationErr)
}

type weightedByteLimiter struct {
	mu         sync.Mutex
	capacity   int64
	inUse      int64
	highWater  int64
	rejections int64
}

type weightedByteLimiterSnapshot struct {
	Capacity   int64
	InUse      int64
	HighWater  int64
	Rejections int64
}

func newWeightedByteLimiter(capacity int64) *weightedByteLimiter {
	if capacity <= 0 {
		capacity = defaultMediaInFlightBytes
	}
	return &weightedByteLimiter{capacity: capacity}
}

func (limiter *weightedByteLimiter) tryAcquire(sizeBytes int64) (func(), bool) {
	if limiter == nil {
		return func() {}, true
	}
	weight := sizeBytes
	if weight <= 0 {
		weight = 1
	}
	limiter.mu.Lock()
	if weight > limiter.capacity {
		weight = limiter.capacity
	}
	if limiter.inUse+weight > limiter.capacity {
		limiter.rejections++
		limiter.mu.Unlock()
		return nil, false
	}
	limiter.inUse += weight
	if limiter.inUse > limiter.highWater {
		limiter.highWater = limiter.inUse
	}
	limiter.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			limiter.mu.Lock()
			limiter.inUse -= weight
			if limiter.inUse < 0 {
				limiter.inUse = 0
			}
			limiter.mu.Unlock()
		})
	}, true
}

func (limiter *weightedByteLimiter) snapshot() weightedByteLimiterSnapshot {
	if limiter == nil {
		return weightedByteLimiterSnapshot{}
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return weightedByteLimiterSnapshot{
		Capacity: limiter.capacity, InUse: limiter.inUse,
		HighWater: limiter.highWater, Rejections: limiter.rejections,
	}
}

func (a *App) mediaByteLimiter() *weightedByteLimiter {
	_ = a.mediaOperationRegistry()
	return a.mediaBytes
}

func prepareMediaSpool(directory string) error {
	directory = filepath.Clean(strings.TrimSpace(directory))
	if directory == "" || !filepath.IsAbs(directory) {
		return errors.New("media spool directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create media spool: %w", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read media spool: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "gateway-media-") {
			continue
		}
		if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clean media spool file: %w", err)
		}
	}
	return nil
}

func (a *App) mediaSpoolDir() string {
	directory := strings.TrimSpace(a.cfg.MediaSpoolDir)
	if directory == "" {
		return os.TempDir()
	}
	return directory
}
