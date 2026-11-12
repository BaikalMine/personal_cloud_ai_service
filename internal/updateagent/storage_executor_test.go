package updateagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/updates"
)

type storageGuardFixture struct {
	mu            sync.Mutex
	server        *httptest.Server
	journal       string
	states        map[string]comfyStorageWindow
	calls         []string
	fail          map[string]bool
	wrongIdentity bool
	onClose       func()
	onBackup      func()
	backups       int
}

func newStorageGuardFixture(t *testing.T) *storageGuardFixture {
	t.Helper()
	g := &storageGuardFixture{journal: strings.Repeat("c", 32), states: map[string]comfyStorageWindow{}, fail: map[string]bool{}}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-ai-gateway-gpu-key") != strings.Repeat("k", 64) {
			w.WriteHeader(404)
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.URL.Path == "/ai-gateway/gpu/status" {
			held := false
			for _, v := range g.states {
				held = held || v.State == "held"
			}
			_ = json.NewEncoder(w).Encode(comfyStorageGuardState{Protocol: 1, Enforced: true, Instance: strings.Repeat("d", 32), StorageProtocol: 1, JournalID: g.journal, Held: held})
			return
		}
		if r.URL.Path == "/ai-gateway/storage/backup" && r.Method == http.MethodGet {
			g.backups++
			if g.onBackup != nil {
				g.onBackup()
			}
			if g.fail["backup"] {
				w.WriteHeader(503)
				return
			}
			snapshot := comfyGuardBackup{Protocol: 1, JournalID: g.journal, Windows: []comfyGuardBackupWindow{}, Receipts: []comfyGuardBackupReceipt{}}
			for _, window := range g.states {
				if window.State != "unknown" {
					snapshot.Windows = append(snapshot.Windows, comfyGuardBackupWindow{ID: window.ID, RequestHash: window.RequestHash, ExecutorID: window.ExecutorID, State: window.State})
				}
			}
			sort.Slice(snapshot.Windows, func(i, j int) bool { return snapshot.Windows[i].ID < snapshot.Windows[j].ID })
			_ = json.NewEncoder(w).Encode(snapshot)
			return
		}
		var request struct {
			Protocol       int    `json:"protocol"`
			Action         string `json:"action"`
			ID             string `json:"id"`
			RequestHash    string `json:"request_hash"`
			ExecutorID     string `json:"executor_id"`
			JournalID      string `json:"journal_id"`
			ExecutorFenced bool   `json:"executor_fenced"`
		}
		if r.URL.Path != "/ai-gateway/storage/window" || r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil || request.Protocol != 1 || request.JournalID != g.journal {
			w.WriteHeader(409)
			return
		}
		g.calls = append(g.calls, request.Action+":"+request.ID)
		current, ok := g.states[request.ID]
		if ok && (current.RequestHash != request.RequestHash || current.ExecutorID != request.ExecutorID) {
			w.WriteHeader(409)
			return
		}
		if !ok {
			current = comfyStorageWindow{Protocol: 1, ID: request.ID, RequestHash: request.RequestHash, ExecutorID: request.ExecutorID, JournalID: g.journal, State: "unknown"}
		}
		switch request.Action {
		case "hold":
			if current.State == "unknown" {
				for _, v := range g.states {
					if v.State == "held" {
						w.WriteHeader(409)
						return
					}
				}
				current.State = "held"
			}
		case "close":
			if !request.ExecutorFenced {
				t.Error("guard closed without executor fencing")
				w.WriteHeader(400)
				return
			}
			if g.onClose != nil {
				g.onClose()
			}
			current.State = "closed"
		case "read":
		default:
			w.WriteHeader(400)
			return
		}
		g.states[request.ID] = current
		if g.fail[request.Action] {
			w.WriteHeader(503)
			return
		}
		if g.wrongIdentity {
			current.RequestHash = strings.Repeat("f", 64)
		}
		_ = json.NewEncoder(w).Encode(current)
	}))
	t.Cleanup(g.server.Close)
	return g
}

type comfyStorageFixture struct {
	executor *ComfyStorageExecutor
	target   ComfyTarget
	guard    *storageGuardFixture
	request  updates.ComfyStorageRequest
	paths    []string
}

func newComfyStorageFixture(t *testing.T, count int) *comfyStorageFixture {
	t.Helper()
	g := newStorageGuardFixture(t)
	f := &comfyStorageFixture{guard: g, target: ComfyTarget{InputDirectory: t.TempDir(), OutputDirectory: t.TempDir(), Storage: &ComfyStorageConfig{JournalDirectory: t.TempDir(), GuardURL: g.server.URL + "/ai-gateway/storage/window", GuardToken: strings.Repeat("k", 64)}}}
	var err error
	f.executor, err = NewComfyStorageExecutor(f.target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.executor.guard.client.CloseIdleConnections)
	f.request = updates.ComfyStorageRequest{Protocol: 1, ID: "comfy-storage-" + strings.Repeat("a", 32), ExecutorID: f.executor.identity.ExecutorID, GuardJournalID: g.journal}
	for i := 0; i < count; i++ {
		data := []byte(fmt.Sprintf("tracked fixture file %d", i))
		digest := sha256.Sum256(data)
		kind, directory := "input", f.target.InputDirectory
		if i%2 == 1 {
			kind, directory = "output", f.target.OutputDirectory
		}
		name := fmt.Sprintf("file-%03d.png", i)
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		f.paths = append(f.paths, path)
		f.request.Items = append(f.request.Items, updates.ComfyStorageSelection{Key: fmt.Sprintf("%s:%d", kind, i+1), Version: strings.Repeat("e", 64), File: updates.ComfyAssetFile{Filename: name, StorageType: kind, SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}})
	}
	f.request, _, err = updates.CanonicalComfyStorageRequest(f.request)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func assertStorageFiles(t *testing.T, paths []string, present bool) {
	t.Helper()
	for _, path := range paths {
		_, err := os.Stat(path)
		if present && err != nil || !present && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("file presence %s: %v want present=%v", path, err, present)
		}
	}
}

func TestComfyStorageExecutorDeletesOnceAndClosesGuardAfterJournal(t *testing.T) {
	f := newComfyStorageFixture(t, 2)
	f.guard.onClose = func() {
		root, err := os.OpenRoot(f.target.Storage.JournalDirectory)
		if err != nil {
			t.Error(err)
			return
		}
		defer root.Close()
		pending, err := readComfyStorageRecord(root, comfyStoragePendingFile, f.request.ID)
		if err != nil || !pending.ExecutorFenced || pending.State != "completed" || pending.GuardClosed {
			t.Error("close lacked durable local closure", pending, err)
		}
	}
	result, err := f.executor.Execute(context.Background(), f.request)
	if err != nil || result.State != "completed" || !result.ExecutorFenced || !result.GuardClosed || updates.MatchComfyStorageResult(result, f.request) != nil {
		t.Fatal(result, err)
	}
	assertStorageFiles(t, f.paths, false)
	for _, item := range result.Items {
		if item.Outcome != "deleted" || item.FreedBytes != item.Selection.File.SizeBytes {
			t.Fatal(item)
		}
	}
	for _, path := range f.paths {
		if err := os.WriteFile(path, []byte("rebuilt data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	again, err := f.executor.Execute(context.Background(), f.request)
	if err != nil || again.RequestHash != result.RequestHash || !again.FinishedAt.Equal(*result.FinishedAt) {
		t.Fatal(again, err)
	}
	assertStorageFiles(t, f.paths, true)
	restarted, err := NewComfyStorageExecutor(f.target)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.guard.client.CloseIdleConnections()
	if _, err := restarted.Execute(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
	assertStorageFiles(t, f.paths, true)
	if len(f.guard.calls) != 2 {
		t.Fatal("replay contacted guard", f.guard.calls)
	}
	data, err := os.ReadFile(filepath.Join(f.target.Storage.JournalDirectory, "identity.json"))
	if err != nil || strings.Contains(string(data), f.target.Storage.GuardToken) {
		t.Fatal("guard secret persisted", err)
	}
}

func TestComfyStorageExecutorRejectsWholeChangedSelection(t *testing.T) {
	f := newComfyStorageFixture(t, 2)
	if err := os.WriteFile(f.paths[1], []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := f.executor.Execute(context.Background(), f.request)
	if err != nil || result.State != "rejected" || result.MutationStarted || !result.GuardClosed {
		t.Fatal(result, err)
	}
	assertStorageFiles(t, f.paths, true)
	for _, item := range result.Items {
		if item.FreedBytes != 0 || item.Outcome == "deleted" {
			t.Fatal(item)
		}
	}
}

func TestComfyStorageExecutorMissingFilesNeverCreditBytes(t *testing.T) {
	f := newComfyStorageFixture(t, 2)
	if err := os.Remove(f.paths[0]); err != nil {
		t.Fatal(err)
	}
	result, err := f.executor.Execute(context.Background(), f.request)
	if err != nil || result.State != "completed" || result.Items[0].Outcome != "missing" || result.Items[0].FreedBytes != 0 {
		t.Fatal(result, err)
	}
	assertStorageFiles(t, f.paths, false)
}

func TestComfyStorageExecutorFenceBeforeExecute(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	result, err := f.executor.Fence(context.Background(), f.request)
	if err != nil || result.State != "rejected" || !result.ExecutorFenced || !result.GuardClosed || result.Items[0].Outcome != "not_run" {
		t.Fatal(result, err)
	}
	if _, err := f.executor.Execute(context.Background(), f.request); err != nil {
		t.Fatal(err)
	}
	assertStorageFiles(t, f.paths, true)
	if len(f.guard.calls) != 1 || !strings.HasPrefix(f.guard.calls[0], "close:") {
		t.Fatal(f.guard.calls)
	}
}

func TestComfyStorageExecutorJournalFailuresNeverReplayDeletion(t *testing.T) {
	for _, failure := range []int{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			f := newComfyStorageFixture(t, 1)
			writes := 0
			original := f.executor.persist
			f.executor.persist = func(root *os.Root, name string, result updates.ComfyStorageResult) error {
				writes++
				if writes == failure {
					return errors.New("synthetic journal failure")
				}
				return original(root, name, result)
			}
			if _, err := f.executor.Execute(context.Background(), f.request); err == nil {
				t.Fatal("write failure hidden")
			}
			f.executor.persist = original
			if failure <= 2 {
				assertStorageFiles(t, f.paths, true)
			} else {
				assertStorageFiles(t, f.paths, false)
			}
			if err := os.WriteFile(f.paths[0], []byte("rebuilt after interrupted operation"), 0o600); err != nil {
				t.Fatal(err)
			}
			if failure > 1 {
				observed, err := f.executor.Execute(context.Background(), f.request)
				if err != nil || observed.GuardClosed {
					t.Fatal(observed, err)
				}
				assertStorageFiles(t, f.paths, true)
				if failure == 3 && (observed.State != "unconfirmed" || observed.Items[0].Outcome != "unconfirmed" || observed.Items[0].FreedBytes != 0) {
					t.Fatal("unpersisted delete was credited", observed)
				}
			}
			fenced, err := f.executor.Fence(context.Background(), f.request)
			if err != nil || !fenced.ExecutorFenced || !fenced.GuardClosed {
				t.Fatal(fenced, err)
			}
			if failure == 3 && fenced.State != "closed_unconfirmed" {
				t.Fatal("lost deletion guessed", fenced)
			}
			if failure >= 4 && (fenced.State != "completed" || fenced.Items[0].Outcome != "deleted") {
				t.Fatal("durable evidence lost", fenced)
			}
			assertStorageFiles(t, f.paths, true)
		})
	}
}

func TestComfyStorageExecutorLostGuardResponses(t *testing.T) {
	for _, action := range []string{"hold", "close"} {
		t.Run(action, func(t *testing.T) {
			f := newComfyStorageFixture(t, 1)
			f.guard.fail[action] = true
			result, err := f.executor.Execute(context.Background(), f.request)
			if action == "hold" {
				if err != nil || result.State != "rejected" || !result.GuardClosed {
					t.Fatal(result, err)
				}
				assertStorageFiles(t, f.paths, true)
			} else {
				if !errors.Is(err, ErrComfyStorageGuard) || !result.ExecutorFenced || result.GuardClosed {
					t.Fatal(result, err)
				}
				assertStorageFiles(t, f.paths, false)
				f.guard.mu.Lock()
				f.guard.fail[action] = false
				f.guard.mu.Unlock()
				if err := os.WriteFile(f.paths[0], []byte("rebuilt"), 0o600); err != nil {
					t.Fatal(err)
				}
				result, err = f.executor.Fence(context.Background(), f.request)
				if err != nil || !result.GuardClosed || result.State != "completed" {
					t.Fatal(result, err)
				}
				assertStorageFiles(t, f.paths, true)
			}
		})
	}
}

func TestComfyStorageExecutorConflictsAndOtherPendingOperation(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	f.guard.fail["close"] = true
	if _, err := f.executor.Execute(context.Background(), f.request); !errors.Is(err, ErrComfyStorageGuard) {
		t.Fatal(err)
	}
	other := f.request
	other.ID = strings.Repeat("b", 32)
	if _, err := f.executor.Execute(context.Background(), other); !errors.Is(err, ErrComfyStorageBusy) {
		t.Fatal(err)
	}
	if _, err := f.executor.Fence(context.Background(), other); !errors.Is(err, ErrComfyStorageBusy) {
		t.Fatal(err)
	}
	changed := f.request
	changed.Items = append([]updates.ComfyStorageSelection(nil), changed.Items...)
	changed.Items[0].Version = strings.Repeat("f", 64)
	if _, err := f.executor.Execute(context.Background(), changed); !errors.Is(err, ErrComfyStorageConflict) {
		t.Fatal(err)
	}
	if _, err := f.executor.Fence(context.Background(), changed); !errors.Is(err, ErrComfyStorageConflict) {
		t.Fatal(err)
	}
}

func TestComfyStorageExecutorIndependentInstancesShareOSLock(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	other, err := NewComfyStorageExecutor(f.target)
	if err != nil {
		t.Fatal(err)
	}
	defer other.guard.client.CloseIdleConnections()
	entered, release := make(chan struct{}), make(chan struct{})
	original := f.executor.remove
	f.executor.remove = func(ctx context.Context, dir string, file updates.ComfyAssetFile, remove bool) (string, error) {
		if remove {
			close(entered)
			<-release
		}
		return original(ctx, dir, file, remove)
	}
	done := make(chan error, 1)
	go func() { _, err := f.executor.Execute(context.Background(), f.request); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not enter deletion")
	}
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if _, err := other.Execute(context.Background(), f.request); !errors.Is(err, ErrComfyStorageBusy) {
		t.Fatal(err)
	}
	if _, err := other.Fence(context.Background(), f.request); !errors.Is(err, ErrComfyStorageBusy) {
		t.Fatal(err)
	}
	if _, err := other.Result(context.Background(), f.request.ID); !errors.Is(err, ErrComfyStorageBusy) {
		t.Fatal(err)
	}
	if err := other.Prune(context.Background(), time.Now()); !errors.Is(err, ErrComfyStorageBusy) {
		t.Fatal(err)
	}
}

func TestComfyStorageExecutorCancellationPreservesKnownEvidence(t *testing.T) {
	f := newComfyStorageFixture(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := f.executor.remove
	f.executor.remove = func(ctx context.Context, dir string, file updates.ComfyAssetFile, remove bool) (string, error) {
		outcome, err := original(ctx, dir, file, remove)
		if remove {
			cancel()
		}
		return outcome, err
	}
	result, err := f.executor.Execute(ctx, f.request)
	if err != nil || result.State != "partial" || !result.GuardClosed || result.Items[0].Outcome != "deleted" || result.Items[1].Outcome != "not_run" {
		t.Fatal(result, err)
	}
	assertStorageFiles(t, f.paths[:1], false)
	assertStorageFiles(t, f.paths[1:], true)
}

func TestComfyStorageExecutorChangedAfterPreflightIsNotDeleted(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	original := f.executor.remove
	f.executor.remove = func(ctx context.Context, dir string, file updates.ComfyAssetFile, remove bool) (string, error) {
		if remove {
			if err := os.WriteFile(f.paths[0], []byte("changed after preview"), 0o600); err != nil {
				return "", err
			}
		}
		return original(ctx, dir, file, remove)
	}
	result, err := f.executor.Execute(context.Background(), f.request)
	if err != nil || result.State != "partial" || result.Items[0].Outcome != "changed" || result.Items[0].FreedBytes != 0 {
		t.Fatal(result, err)
	}
	assertStorageFiles(t, f.paths, true)
}

func TestComfyStorageExecutorRootAndConfigurationContinuity(t *testing.T) {
	for _, scenario := range []string{"input", "output", "receipts", "token", "journal"} {
		t.Run(scenario, func(t *testing.T) {
			f := newComfyStorageFixture(t, 1)
			target := f.target
			config := *target.Storage
			target.Storage = &config
			switch scenario {
			case "input":
				target.InputDirectory = t.TempDir()
			case "output":
				target.OutputDirectory = t.TempDir()
			case "receipts":
				old := filepath.Join(config.JournalDirectory, "receipts")
				if err := os.Rename(old, old+"-previous"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(old, 0o700); err != nil {
					t.Fatal(err)
				}
			case "token":
				config.GuardToken = strings.Repeat("z", 64)
			case "journal":
				config.JournalDirectory = t.TempDir()
				fresh, err := NewComfyStorageExecutor(target)
				if err != nil {
					t.Fatal(err)
				}
				defer fresh.guard.client.CloseIdleConnections()
				if _, err := fresh.Execute(context.Background(), f.request); !errors.Is(err, ErrComfyStorageIdentity) {
					t.Fatal("old request accepted by fresh journal", err)
				}
				return
			}
			if _, err := NewComfyStorageExecutor(target); !errors.Is(err, ErrComfyStorageIdentity) {
				t.Fatal("changed identity accepted", err)
			}
			assertStorageFiles(t, f.paths, true)
		})
	}
}

func TestComfyStorageExecutorPruningKeepsClosedIdentity(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	result, err := f.executor.Execute(context.Background(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.executor.Prune(context.Background(), result.FinishedAt.Add(91*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.paths[0], []byte("rebuilt cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	replay, err := f.executor.Execute(context.Background(), f.request)
	if err != nil || replay.State != "expired" || len(replay.Items) != 0 || !replay.GuardClosed || replay.RequestHash != result.RequestHash {
		t.Fatal(replay, err)
	}
	assertStorageFiles(t, f.paths, true)
}

func TestComfyStorageExecutorCorruptJournalNeverBecomesUnknown(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	if err := os.WriteFile(filepath.Join(f.target.Storage.JournalDirectory, comfyStoragePendingFile), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executor.Execute(context.Background(), f.request); !errors.Is(err, ErrComfyStorageIdentity) {
		t.Fatal(err)
	}
	if _, err := f.executor.Fence(context.Background(), f.request); !errors.Is(err, ErrComfyStorageIdentity) {
		t.Fatal(err)
	}
	if len(f.guard.calls) != 0 {
		t.Fatal("corrupt journal contacted guard", f.guard.calls)
	}
	assertStorageFiles(t, f.paths, true)
}

func TestComfyStorageExecutorRejectsDirectoryLinks(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	outside := t.TempDir()
	data := []byte("tracked fixture file 0")
	path := filepath.Join(outside, "file-000.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.target.InputDirectory, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f.request.Items[0].File.Subfolder = "escape"
	result, err := f.executor.Execute(context.Background(), f.request)
	if err != nil || result.State != "rejected" || result.Items[0].FreedBytes != 0 {
		t.Fatal(result, err)
	}
	assertStorageFiles(t, []string{path}, true)
}

func TestComfyStorageProcessLock(t *testing.T) {
	if raw := os.Getenv("GATEWAY_TEST_STORAGE_LOCK"); raw != "" {
		var target ComfyTarget
		if err := json.Unmarshal([]byte(raw), &target); err != nil {
			t.Fatal(err)
		}
		if _, err := NewComfyStorageExecutor(target); !errors.Is(err, ErrComfyStorageBusy) {
			t.Fatal("second process did not honor the journal lock", err)
		}
		return
	}
	f := newComfyStorageFixture(t, 1)
	root, lock, err := f.executor.openJournal()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestComfyStorageProcessLock$")
	payload, _ := json.Marshal(f.target)
	command.Env = append(os.Environ(), "GATEWAY_TEST_STORAGE_LOCK="+string(payload))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("lock helper: %v: %s", err, output)
	}
}

func TestComfyStoragePublishedReceiptUnblocksStalePendingPointer(t *testing.T) {
	f := newComfyStorageFixture(t, 1)
	result, err := f.executor.Execute(context.Background(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(f.target.Storage.JournalDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	result.GuardClosed = false
	if err := writeComfyStorageRecord(root, comfyStoragePendingFile, result); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.paths[0], []byte("tracked fixture file 0"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.request.ID = strings.Repeat("b", 32)
	result, err = f.executor.Execute(context.Background(), f.request)
	if err != nil || result.State != "completed" {
		t.Fatal(result, err)
	}
	assertStorageFiles(t, f.paths, false)
}
