package gateway

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWeightedByteLimiterAccountsAndRejects(t *testing.T) {
	limiter := newWeightedByteLimiter(100)
	releaseFirst, ok := limiter.tryAcquire(70)
	if !ok {
		t.Fatal("first allocation was rejected")
	}
	if _, ok := limiter.tryAcquire(31); ok {
		t.Fatal("allocation beyond the shared byte budget was accepted")
	}
	releaseFirst()
	releaseFirst()
	releaseLarge, ok := limiter.tryAcquire(1000)
	if !ok {
		t.Fatal("one oversized file must be allowed exclusively")
	}
	snapshot := limiter.snapshot()
	if snapshot.InUse != 100 || snapshot.HighWater != 100 || snapshot.Rejections != 1 {
		t.Fatalf("unexpected limiter snapshot: %+v", snapshot)
	}
	releaseLarge()
	if limiter.snapshot().InUse != 0 {
		t.Fatal("byte budget was not released")
	}
}

func TestMaterializedByteReadReservesTheFullPlaintextSize(t *testing.T) {
	const mediaSize = int64(64 << 20)
	chunkReservation := mediaMemoryReservation("chunked_v1", mediaSize)
	if got, want := chunkReservation, chunkedMediaMemoryReservation; got != want {
		t.Fatalf("chunk materialization reservation=%d want=%d", got, want)
	}
	if got, want := chunkReservation+(mediaSize-chunkReservation), mediaSize; got != want {
		t.Fatalf("materialize plus byte-read reservation=%d want=%d", got, want)
	}
	if got := mediaMemoryReservation("inline_v1", mediaSize); got != mediaSize {
		t.Fatalf("legacy inline reservation=%d want=%d", got, mediaSize)
	}
}

func TestMediaOperationRegistrySeparatesOutcome(t *testing.T) {
	registry := newMediaOperationRegistry()
	registry.observe("upload", 2<<20, 25*time.Millisecond, nil)
	registry.observe("upload", 3<<20, 40*time.Millisecond, errors.New("failed"))
	snapshot := registry.snapshot()
	if len(snapshot) != 2 || snapshot[0].Count != 1 || snapshot[1].Count != 1 {
		t.Fatalf("unexpected media operation snapshot: %+v", snapshot)
	}
}

func TestPrepareMediaSpoolRemovesOnlyOwnedFiles(t *testing.T) {
	directory := t.TempDir()
	stale := filepath.Join(directory, "gateway-media-stale")
	unrelated := filepath.Join(directory, "keep.txt")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareMediaSpool(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned stale spool file still exists: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated spool file was removed: %v", err)
	}
}
