package trainingagent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/loratraining"
)

func storageControllerFixture(t *testing.T) *Controller {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "training")
	lora := filepath.Join(base, "lora")
	for _, directory := range []string{filepath.Join(root, "models"), filepath.Join(root, "jobs", "job-one", "cache", "nested"), filepath.Join(lora, "Trained", "alice", "krea2")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	profile := ProfileConfig{ID: "test-profile", DiT: filepath.Join(base, "original.safetensors"), StripPrefix: "model."}
	artifact := filepath.Join(lora, "Trained", "alice", "krea2", "done.safetensors")
	for name, content := range map[string]string{
		profile.DiT: "original model bytes", artifact: "trained adapter bytes",
		filepath.Join(root, "models", "test-profile-format-v2.safetensors"):     "normalized model bytes",
		filepath.Join(root, "jobs", "job-one", "cache", "nested", "latent.bin"): "cached latent bytes",
		filepath.Join(root, "jobs", "job-one", "cache", "text.bin"):             "cached text bytes",
		filepath.Join(root, "jobs", "job-one", "dataset.zip"):                   "working archive bytes",
	} {
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	record := &jobRecord{Spec: loratraining.JobSpec{GatewayJobID: "gateway-one", ProfileID: profile.ID}, Status: loratraining.JobStatus{ID: "job-one", GatewayJobID: "gateway-one", State: "completed", ArtifactName: "done.safetensors", FinishedAt: time.Now()}, ArtifactPath: artifact, ProfileSnapshot: &profile, DatasetPath: filepath.Join(root, "jobs", "job-one", "dataset.zip")}
	return &Controller{config: Config{RootDir: root, ComfyLoraDir: lora, Profiles: []ProfileConfig{profile}}, ctx: ctx, cancel: cancel, jobs: map[string]*jobRecord{"job-one": record}, byGateway: map[string]string{"gateway-one": "job-one"}}
}

func storageItemForTest(t *testing.T, controller *Controller, kind string) loratraining.StorageItem {
	t.Helper()
	inventory, err := controller.StorageInventory(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := loratraining.ValidateStorageInventory(inventory); err != nil {
		t.Fatal(inventory, err)
	}
	for _, item := range inventory.Items {
		if item.Kind == kind && item.State == "present" {
			return item
		}
	}
	t.Fatalf("missing %s in %+v", kind, inventory)
	return loratraining.StorageItem{}
}

func cleanupRequestForTest(id string, items ...loratraining.StorageItem) loratraining.StorageCleanupRequest {
	request := loratraining.StorageCleanupRequest{ID: strings.Repeat(id, 32)}
	for _, item := range items {
		request.Items = append(request.Items, loratraining.StorageSelection{ID: item.ID, Version: item.Version})
	}
	return request
}

func TestTrainingStorageInventoryAndPreviewAreReadOnly(t *testing.T) {
	controller := storageControllerFixture(t)
	before := storageFileTreeForTest(t, filepath.Dir(controller.config.RootDir))
	var items []loratraining.StorageItem
	cursor := ""
	for {
		page, err := controller.StorageInventory(context.Background(), cursor, 2)
		if err != nil || !page.Complete || len(page.Items) > 2 {
			t.Fatal(page, err)
		}
		if err := loratraining.ValidateStorageInventory(page); err != nil {
			t.Fatal(page, err)
		}
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			if len(items) != page.TotalItems {
				t.Fatal(len(items), page.TotalItems)
			}
			break
		}
		if page.NextCursor <= cursor {
			t.Fatal("non-progressing cursor")
		}
		cursor = page.NextCursor
	}
	kinds := map[string]bool{}
	for _, item := range items {
		kinds[item.Kind] = true
		if (item.Kind == "original_model" || item.Kind == "trained_artifact") && (item.CanCleanup || item.ProtectedReason == "") {
			t.Fatal(item)
		}
		preview, err := controller.PreviewStorageCleanup(context.Background(), []string{item.ID})
		if err != nil || len(preview.Items) != 1 || preview.Items[0] != item {
			t.Fatal(preview, item, err)
		}
	}
	if len(kinds) != 7 {
		t.Fatal(kinds)
	}
	if after := storageFileTreeForTest(t, filepath.Dir(controller.config.RootDir)); !reflect.DeepEqual(before, after) {
		t.Fatal("preview changed files")
	}
	if _, err := os.Lstat(filepath.Join(controller.config.RootDir, storageReceiptDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview created journal", err)
	}
}

func TestTrainingStoragePreviewFingerprintStableAcrossReads(t *testing.T) {
	controller := storageControllerFixture(t)
	targets, originals, err := controller.storageTargets()
	if err != nil {
		t.Fatal(err)
	}
	var target storageTarget
	for _, item := range targets {
		if item.item.Kind == "training_cache" {
			target = item
		}
	}
	var firstVersion string
	var firstEntries []string
	for range 30 {
		snapshot, err := inspectStorageTarget(context.Background(), target, originals)
		snapshot.close()
		if err != nil {
			t.Fatal(err)
		}
		var entries []string
		for _, entry := range snapshot.entries {
			entries = append(entries, fmt.Sprintf("%s %s %d %d", entry.name, entry.info.Mode(), entry.info.Size(), entry.info.ModTime().UnixNano()))
		}
		if firstVersion == "" {
			firstVersion = snapshot.item.Version
			firstEntries = entries
		} else if snapshot.item.Version != firstVersion {
			t.Fatalf("read changed preview fingerprint: before=%v after=%v", firstEntries, entries)
		}
	}
}

func TestTrainingStorageCleanupReceiptsAndReplay(t *testing.T) {
	controller := storageControllerFixture(t)
	cache := storageItemForTest(t, controller, "training_cache")
	model := storageItemForTest(t, controller, "normalized_model")
	request := cleanupRequestForTest("a", cache, model)
	result, err := controller.CleanupStorage(context.Background(), request)
	if err != nil || result.State != "completed" {
		t.Fatal(result, err)
	}
	var bytes, files int64
	for _, item := range result.Items {
		if item.Outcome != "deleted" {
			t.Fatal(item)
		}
		bytes += item.FreedBytes
		files += item.DeletedFiles
	}
	if bytes != cache.SizeBytes+model.SizeBytes || files != 3 {
		t.Fatal(bytes, files, cache, model)
	}
	if controller.Activity().State != "idle" {
		t.Fatal(controller.Activity())
	}
	if _, err := os.Stat(filepath.Join(controller.config.RootDir, "jobs", "job-one", "cache")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	modelPath := filepath.Join(controller.config.RootDir, "models", "test-profile-format-v2.safetensors")
	if _, err := os.Stat(modelPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("newly rebuilt model"), 0o600); err != nil {
		t.Fatal(err)
	}
	controller.jobs["job-one"].Status.State = "running"
	replay, err := controller.CleanupStorage(context.Background(), request)
	if err != nil || !reflect.DeepEqual(result, replay) {
		t.Fatal(replay, result, err)
	}
	if content, err := os.ReadFile(modelPath); err != nil || string(content) != "newly rebuilt model" {
		t.Fatal("replay deleted rebuilt file", err)
	}
	changed := request
	changed.Items = append([]loratraining.StorageSelection(nil), request.Items...)
	changed.Items[0].Version = strings.Repeat("f", 64)
	if _, err := controller.CleanupStorage(context.Background(), changed); !errors.Is(err, ErrStorageOperationConflict) {
		t.Fatal("operation identity reused", err)
	}
	for _, filename := range []string{controller.config.Profiles[0].DiT, controller.jobs["job-one"].ArtifactPath, controller.jobs["job-one"].DatasetPath} {
		if _, err := os.Stat(filename); err != nil {
			t.Fatal("persistent source removed", filename, err)
		}
	}
}

func TestTrainingStorageCleanupRevalidatesWholeSelection(t *testing.T) {
	controller := storageControllerFixture(t)
	cache := storageItemForTest(t, controller, "training_cache")
	model := storageItemForTest(t, controller, "normalized_model")
	changed := filepath.Join(controller.config.RootDir, "jobs", "job-one", "cache", "new.bin")
	if err := os.WriteFile(changed, []byte("created after preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("b", cache, model))
	if err != nil || result.State != "rejected" {
		t.Fatal(result, err)
	}
	for _, item := range result.Items {
		if item.FreedBytes != 0 || item.DeletedFiles != 0 {
			t.Fatal(item)
		}
	}
	if _, err := os.Stat(filepath.Join(controller.config.RootDir, "models", "test-profile-format-v2.safetensors")); err != nil {
		t.Fatal("valid part of rejected plan was deleted", err)
	}
	if _, err := os.Stat(changed); err != nil {
		t.Fatal("changed file deleted", err)
	}
	cache = storageItemForTest(t, controller, "training_cache")
	if result, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("c", cache)); err != nil || result.State != "completed" {
		t.Fatal(result, err)
	}
}

func TestTrainingStorageCleanupProtectsModelsArtifactsAndActiveJobs(t *testing.T) {
	for _, kind := range []string{"original_model", "trained_artifact"} {
		t.Run(kind, func(t *testing.T) {
			controller := storageControllerFixture(t)
			item := storageItemForTest(t, controller, kind)
			result, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("d", item))
			if err != nil || result.State != "rejected" || result.Items[0].Outcome != "protected" {
				t.Fatal(result, err)
			}
		})
	}
	for _, state := range []string{"queued", "preparing", "caching", "running", "installing", "unconfirmed"} {
		t.Run(state, func(t *testing.T) {
			controller := storageControllerFixture(t)
			cache := storageItemForTest(t, controller, "training_cache")
			model := storageItemForTest(t, controller, "normalized_model")
			if state == "unconfirmed" {
				controller.jobs["job-one"].Status.ExecutionUnconfirmed = true
			} else {
				controller.jobs["job-one"].Status.State = state
			}
			result, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("e", cache, model))
			if err != nil || result.State != "rejected" {
				t.Fatal(result, err)
			}
			for _, item := range result.Items {
				if item.Outcome != "protected" || item.FreedBytes != 0 {
					t.Fatal(item)
				}
			}
		})
	}
}

func TestTrainingStorageCleanupHonorsControllerExclusion(t *testing.T) {
	controller := storageControllerFixture(t)
	cache := storageItemForTest(t, controller, "training_cache")
	controller.storageMu.RLock()
	_, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("f", cache))
	controller.storageMu.RUnlock()
	if !errors.Is(err, ErrStorageBusy) {
		t.Fatal(err)
	}
	controller.storageMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = controller.Submit(ctx, loratraining.JobSpec{}, strings.NewReader("unused"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("submission crossed cleanup gate", err)
	}
	controller.jobs["job-one"].Status.State = "queued"
	done := make(chan struct{})
	go func() { controller.execute("job-one"); close(done) }()
	controller.cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("executor did not cancel at cleanup gate")
	}
	controller.storageMu.Unlock()
	if controller.jobs["job-one"].Status.State != "queued" || controller.pendingSubmissions != 0 {
		t.Fatal("gated work changed state")
	}
}

func TestTrainingStorageIncompleteOperationNeverExecutesAgain(t *testing.T) {
	controller := storageControllerFixture(t)
	cache := storageItemForTest(t, controller, "training_cache")
	request, hash, err := loratraining.CanonicalStorageCleanupRequest(cleanupRequestForTest("1", cache))
	if err != nil {
		t.Fatal(err)
	}
	intent := loratraining.StorageCleanupResult{ProtocolVersion: 1, ID: request.ID, RequestHash: hash, State: "running", StartedAt: time.Now().UTC(), Items: []loratraining.StorageCleanupOutcome{{ID: cache.ID, Kind: cache.Kind, Label: cache.Label, Version: cache.Version, Outcome: "not_run"}}}
	if err := controller.writeStorageCleanupResult(intent); err != nil {
		t.Fatal(err)
	}
	before := storageFileTreeForTest(t, controller.config.RootDir)
	result, err := controller.CleanupStorage(context.Background(), request)
	if err != nil || result.State != "unconfirmed" || result.Items[0].Outcome != "unconfirmed" || result.Items[0].FreedBytes != 0 {
		t.Fatal(result, err)
	}
	if after := storageFileTreeForTest(t, controller.config.RootDir); !reflect.DeepEqual(before, after) {
		t.Fatal("incomplete replay changed files")
	}
}

func TestTrainingStorageMissingAndReceiptFailure(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		controller := storageControllerFixture(t)
		model := storageItemForTest(t, controller, "normalized_model")
		if err := os.Remove(filepath.Join(controller.config.RootDir, "models", model.Label)); err != nil {
			t.Fatal(err)
		}
		result, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("2", model))
		if err != nil || result.State != "completed" || result.Items[0].Outcome != "missing" || result.Items[0].FreedBytes != 0 {
			t.Fatal(result, err)
		}
	})
	t.Run("receipt cannot be persisted", func(t *testing.T) {
		controller := storageControllerFixture(t)
		cache := storageItemForTest(t, controller, "training_cache")
		if err := os.WriteFile(filepath.Join(controller.config.RootDir, storageReceiptDirectory), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := storageFileTreeForTest(t, controller.config.RootDir)
		if _, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("3", cache)); err == nil {
			t.Fatal("ignored journal failure")
		}
		if after := storageFileTreeForTest(t, controller.config.RootDir); !reflect.DeepEqual(before, after) {
			t.Fatal("deleted without durable intent")
		}
	})
}

func TestTrainingStorageProtectsOriginalModelAliases(t *testing.T) {
	controller := storageControllerFixture(t)
	controller.config.Profiles[0].VAE = filepath.Join(controller.config.RootDir, "jobs", "job-one", "cache", "text.bin")
	cache := storageItemForTest(t, controller, "training_cache")
	if cache.CanCleanup || cache.ProtectedReason != "original_model" {
		t.Fatal(cache)
	}
	controller.config.Profiles[0].DiT = filepath.Join(controller.config.RootDir, "models", "test-profile-format-v2.safetensors")
	model := storageItemForTest(t, controller, "normalized_model")
	if model.CanCleanup || model.ProtectedReason != "original_model" {
		t.Fatal(model)
	}
}

func TestTrainingStorageOriginalIdentityFailureBlocksCleanup(t *testing.T) {
	controller := storageControllerFixture(t)
	cache := storageItemForTest(t, controller, "training_cache")
	// Force an inspection error on both platforms without relying on permission
	// denial (tests may run as an administrator).
	controller.config.Profiles[0].VAE = controller.config.Profiles[0].DiT + "\x00"
	before := storageFileTreeForTest(t, controller.config.RootDir)
	if _, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("7", cache)); err == nil {
		t.Fatal("cleanup ignored an uninspectable original model")
	}
	if after := storageFileTreeForTest(t, controller.config.RootDir); !reflect.DeepEqual(before, after) {
		t.Fatal("failed original identity check changed storage")
	}
}

func TestTrainingStorageNormalizedModelRequiresRebuildSource(t *testing.T) {
	for _, mode := range []string{"missing model", "unknown profile"} {
		t.Run(mode, func(t *testing.T) {
			controller := storageControllerFixture(t)
			name := "test-profile-format-v2.safetensors"
			if mode == "missing model" {
				if err := os.Remove(controller.config.Profiles[0].DiT); err != nil {
					t.Fatal(err)
				}
			} else {
				name = "orphan-profile-format-v2.safetensors"
				if err := os.WriteFile(filepath.Join(controller.config.RootDir, "models", name), []byte("only remaining copy"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			inventory, err := controller.StorageInventory(context.Background(), "", 100)
			if err != nil || !inventory.Complete {
				t.Fatal(inventory, err)
			}
			var model loratraining.StorageItem
			for _, item := range inventory.Items {
				if item.Label == name {
					model = item
				}
			}
			if model.ID == "" || model.CanCleanup || model.ProtectedReason == "" {
				t.Fatal("copy without source was rebuildable", model)
			}
			result, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("8", model))
			if err != nil || result.State != "rejected" || result.Items[0].Outcome != "protected" {
				t.Fatal(result, err)
			}
			if _, err := os.Stat(filepath.Join(controller.config.RootDir, "models", name)); err != nil {
				t.Fatal("only copy removed", err)
			}
		})
	}
}

func TestTrainingStorageRejectsSymlinksAndDirectorySubstitution(t *testing.T) {
	for _, mode := range []string{"nested link", "root link", "normalized directory"} {
		t.Run(mode, func(t *testing.T) {
			controller := storageControllerFixture(t)
			cache := filepath.Join(controller.config.RootDir, "jobs", "job-one", "cache")
			external := t.TempDir()
			sentinel := filepath.Join(external, "sentinel")
			if err := os.WriteFile(sentinel, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			kind := "training_cache"
			switch mode {
			case "nested link":
				if err := storageDirectoryLinkForTest(filepath.Join(cache, "link"), external); err != nil {
					t.Skip(err)
				}
			case "root link":
				if err := os.Rename(cache, cache+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := storageDirectoryLinkForTest(cache, external); err != nil {
					t.Skip(err)
				}
			case "normalized directory":
				kind = "normalized_model"
				model := filepath.Join(controller.config.RootDir, "models", "test-profile-format-v2.safetensors")
				if err := os.Remove(model); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(model, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			inventory, err := controller.StorageInventory(context.Background(), "", 100)
			if err != nil || inventory.Complete {
				t.Fatal(inventory, err)
			}
			found := false
			for _, item := range inventory.Items {
				if item.Kind == kind && item.State == "unavailable" {
					found = true
					if item.CanCleanup {
						t.Fatal(item)
					}
				}
			}
			if !found {
				t.Fatal(inventory)
			}
			if content, err := os.ReadFile(sentinel); err != nil || string(content) != "untouched" {
				t.Fatal(err)
			}
		})
	}
}

func TestTrainingStoragePartialRemovalCountsOnlyConfirmedFiles(t *testing.T) {
	controller := storageControllerFixture(t)
	targets, originals, err := controller.storageTargets()
	if err != nil {
		t.Fatal(err)
	}
	var target storageTarget
	for _, candidate := range targets {
		if candidate.item.Kind == "training_cache" {
			target = candidate
		}
	}
	snapshot, err := inspectStorageTarget(context.Background(), target, originals)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.close()
	changed := filepath.Join(controller.config.RootDir, "jobs", "job-one", "cache", "nested", "latent.bin")
	if err := os.WriteFile(changed, []byte("changed after inspection"), 0o600); err != nil {
		t.Fatal(err)
	}
	freed, files, err := removeStorageSnapshot(context.Background(), &snapshot)
	if !errors.Is(err, errStorageChanged) || freed != int64(len("cached text bytes")) || files != 1 {
		t.Fatal(freed, files, err)
	}
	if content, err := os.ReadFile(changed); err != nil || string(content) != "changed after inspection" {
		t.Fatal(err)
	}
}

func TestTrainingStorageReceiptRetention(t *testing.T) {
	controller := storageControllerFixture(t)
	cache := storageItemForTest(t, controller, "training_cache")
	result, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("4", cache))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-91 * 24 * time.Hour)
	result.StartedAt = old
	result.FinishedAt = &old
	if err := controller.writeStorageCleanupResult(result); err != nil {
		t.Fatal(err)
	}
	if err := controller.pruneStorageCleanupReceipts(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StorageCleanupResult(context.Background(), result.ID); !errors.Is(err, ErrStorageResultExpired) {
		t.Fatal(err)
	}
	if _, err := controller.CleanupStorage(context.Background(), cleanupRequestForTest("4", cache)); !errors.Is(err, ErrStorageResultExpired) {
		t.Fatal("retention reopened operation", err)
	}
	fenced, err := controller.FenceStorageCleanup(context.Background(), cleanupRequestForTest("4", cache))
	if err != nil || !fenced.Settled || fenced.Result.State != "closed_unconfirmed" {
		t.Fatal(fenced, err)
	}
	request, hash, err := loratraining.CanonicalStorageCleanupRequest(cleanupRequestForTest("5", cache))
	if err != nil {
		t.Fatal(err)
	}
	result.ID, result.RequestHash = request.ID, hash
	result.State = "running"
	result.FinishedAt = nil
	result.Items[0].Outcome = "not_run"
	result.Items[0].FreedBytes = 0
	result.Items[0].DeletedFiles = 0
	if err := controller.writeStorageCleanupResult(result); err != nil {
		t.Fatal(err)
	}
	if err := controller.pruneStorageCleanupReceipts(time.Now()); err != nil {
		t.Fatal(err)
	}
	if retained, err := controller.StorageCleanupResult(context.Background(), result.ID); err != nil || retained.State != "unconfirmed" {
		t.Fatal(retained, err)
	}
}

func TestTrainingStorageFencePreventsLateCleanup(t *testing.T) {
	controller := storageControllerFixture(t)
	cache := storageItemForTest(t, controller, "training_cache")
	request := cleanupRequestForTest("6", cache)
	controller.storageMu.RLock()
	_, err := controller.FenceStorageCleanup(context.Background(), request)
	controller.storageMu.RUnlock()
	if !errors.Is(err, ErrStorageBusy) {
		t.Fatal("fence bypassed active controller", err)
	}
	fenced, err := controller.FenceStorageCleanup(context.Background(), request)
	if err != nil || !fenced.Settled || !fenced.Fenced || fenced.Result.State != "rejected" {
		t.Fatal(fenced, err)
	}
	before := storageFileTreeForTest(t, controller.config.RootDir)
	late, err := controller.CleanupStorage(context.Background(), request)
	if err != nil || late.State != "rejected" || late.Items[0].Reason != "request_fenced" {
		t.Fatal(late, err)
	}
	if after := storageFileTreeForTest(t, controller.config.RootDir); !reflect.DeepEqual(before, after) {
		t.Fatal("late cleanup changed files")
	}
}

func storageFileTreeForTest(t *testing.T, root string) map[string]string {
	t.Helper()
	items := map[string]string{}
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			items[relative] = "directory"
			return nil
		}
		content, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		items[relative] = string(content)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return items
}

func storageDirectoryLinkForTest(link, target string) error {
	err := os.Symlink(target, link)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	// Junctions exercise Windows directory reparse points without changing
	// Developer Mode or requiring the symbolic-link privilege on the host.
	output, junctionErr := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput()
	if junctionErr != nil {
		return fmt.Errorf("directory link: %v; junction: %w: %s", err, junctionErr, output)
	}
	return nil
}
