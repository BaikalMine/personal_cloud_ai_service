package store_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func assertLoraDatasetLifecycle(t *testing.T, ctx context.Context, db *sql.DB, repository *store.Store, userID int64) {
	t.Helper()
	var foreignID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('dataset-foreign','disabled','user') RETURNING id`).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}
	insertAsset := func(id, hash string, owner int64) domain.LoraDatasetAsset {
		t.Helper()
		asset := domain.LoraDatasetAsset{ID: id, UserID: owner, Name: "source.png", Hash: strings.Repeat(hash, 64), MIMEType: "image/png", SizeBytes: 8, Width: 512, Height: 512}
		chunks := 0
		result, err := repository.InsertLoraDatasetAsset(ctx, asset, func() ([]byte, int, error) {
			chunks++
			if chunks > 2 {
				return nil, 0, io.EOF
			}
			return []byte("cipher"), 4, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	asset := insertAsset("dataset-image-a", "a", userID)
	if _, err := db.ExecContext(ctx, `UPDATE lora_dataset_assets SET last_used_at=now()-interval '2 days' WHERE id=$1`, asset.ID); err != nil {
		t.Fatal(err)
	}
	foreign := insertAsset("dataset-image-foreign", "a", foreignID)
	if _, err := repository.LoraDatasetAsset(ctx, userID, foreign.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign asset: %v", err)
	}
	duplicate, err := repository.InsertLoraDatasetAsset(ctx, asset, func() ([]byte, int, error) { t.Fatal("duplicate content was copied"); return nil, 0, io.EOF })
	if err != nil || duplicate.ID != asset.ID {
		t.Fatalf("dedup: %+v %v", duplicate, err)
	}
	if count, err := repository.CleanupLoraDatasets(ctx); err != nil || count != 0 {
		t.Fatalf("re-uploaded orphan was collected: %d %v", count, err)
	}
	broken := asset
	broken.ID = "broken-asset"
	broken.Hash = strings.Repeat("c", 64)
	if _, err := repository.InsertLoraDatasetAsset(ctx, broken, func() ([]byte, int, error) { return nil, 0, io.EOF }); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short chunks: %v", err)
	}
	if _, err := repository.LoraDatasetAsset(ctx, userID, broken.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("partial insert visible: %v", err)
	}
	var payload []byte
	if err := repository.ForEachLoraDatasetAssetChunk(ctx, userID, asset.ID, func(index int, cipher []byte, size int) error {
		if size != 4 {
			t.Fatalf("chunk size: %d", size)
		}
		payload = append(payload, cipher...)
		return nil
	}); err != nil || string(payload) != "ciphercipher" {
		t.Fatalf("chunk read: %q %v", payload, err)
	}
	if err := repository.ForEachLoraDatasetAssetChunk(ctx, foreignID, asset.ID, func(int, []byte, int) error { t.Fatal("foreign chunk exposed"); return nil }); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign chunks: %v", err)
	}
	dataset, err := repository.CreateLoraDataset(ctx, userID, "dataset-working", "Portrait", []byte("cipher-initial"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.LoraDataset(ctx, foreignID, dataset.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign dataset: %v", err)
	}
	if _, err := repository.SaveLoraDataset(ctx, userID, dataset.ID, dataset.Revision, "Portrait", []byte("foreign"), []string{foreign.ID}); !errors.Is(err, store.ErrLoraDatasetAsset) {
		t.Fatalf("foreign reference: %v", err)
	}
	if _, err := repository.SaveLoraDataset(ctx, userID, dataset.ID, dataset.Revision, "Portrait", []byte("too-many"), make([]string, 101)); !errors.Is(err, store.ErrLoraDatasetQuota) {
		t.Fatalf("image limit: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repository.SaveLoraDataset(ctx, userID, dataset.ID, dataset.Revision, "Portrait", []byte("cipher-saved"), []string{asset.ID, asset.ID})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	saved, conflicts := 0, 0
	for err := range results {
		if err == nil {
			saved++
		} else if errors.Is(err, store.ErrLoraDatasetConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if saved != 1 || conflicts != 7 {
		t.Fatalf("CAS %d saved %d conflicts", saved, conflicts)
	}
	dataset, err = repository.LoraDataset(ctx, userID, dataset.ID)
	if err != nil || dataset.ImageCount != 2 || dataset.SizeBytes != 16 {
		t.Fatalf("saved metadata: %+v %v", dataset, err)
	}
	snapshot, err := repository.CreateLoraDatasetSnapshot(ctx, userID, dataset.ID, dataset.Revision, "dataset-version", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	reused, err := repository.CreateLoraDatasetSnapshot(ctx, userID, dataset.ID, dataset.Revision, "dataset-duplicate-version", strings.Repeat("d", 64))
	if err != nil || reused.ID != snapshot.ID {
		t.Fatalf("snapshot retry: %+v %v", reused, err)
	}
	dataset, err = repository.SaveLoraDataset(ctx, userID, dataset.ID, dataset.Revision, "Edited", []byte("cipher-edited"), nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.LoraDatasetSnapshot(ctx, userID, snapshot.ID)
	if err != nil || !bytes.Equal(stored.ManifestCipher, []byte("cipher-saved")) {
		t.Fatalf("snapshot changed: %+v %v", stored, err)
	}
	params := domain.CreateLoraTrainingJobParams{PublicID: "dataset-training-job", UserID: userID, UsernameSnapshot: "admin", RequestID: "dataset-training-request", ProfileID: "krea2-test", Family: "krea2", BaseModel: "raw.safetensors", Name: "Dataset training", OutputName: "dataset_test", TriggerWord: "subject", ConceptType: "character", Preset: "quick", Resolution: 512, MaxTrainSteps: 800, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.0001, Seed: 42, SampleCount: 5, DatasetBytes: 100, DatasetPath: "/spool/dataset-test.zip", DatasetSnapshotID: snapshot.ID, DatasetSnapshotHash: strings.Repeat("x", 64)}
	if _, err := repository.CreateLoraTrainingJob(ctx, params); !errors.Is(err, store.ErrLoraDatasetAsset) {
		t.Fatalf("wrong snapshot hash: %v", err)
	}
	params.DatasetSnapshotHash = snapshot.Hash
	params.UserID = foreignID
	if _, err := repository.CreateLoraTrainingJob(ctx, params); !errors.Is(err, store.ErrLoraDatasetAsset) {
		t.Fatalf("foreign training snapshot: %v", err)
	}
	params.UserID = userID
	job, err := repository.CreateLoraTrainingJob(ctx, params)
	if err != nil || job.DatasetSnapshotID != snapshot.ID || job.DatasetSnapshotHash != snapshot.Hash {
		t.Fatalf("training link: %+v %v", job, err)
	}
	if err := repository.DeleteLoraDatasetSnapshot(ctx, userID, snapshot.ID); !errors.Is(err, store.ErrLoraDatasetInUse) {
		t.Fatalf("delete active version: %v", err)
	}
	if err := repository.DeleteLoraDataset(ctx, userID, dataset.ID, dataset.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE lora_dataset_snapshots SET expires_at=now()-interval '1 day' WHERE id=$1`, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE lora_dataset_assets SET last_used_at=now()-interval '2 days' WHERE id=$1`, asset.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.CleanupLoraDatasets(ctx); err != nil || count != 0 {
		t.Fatalf("cleanup active snapshot: %d %v", count, err)
	}
	if _, err := repository.LoraDatasetAsset(ctx, userID, asset.ID); err != nil {
		t.Fatalf("snapshot image lost: %v", err)
	}
	if _, err := repository.RequestLoraTrainingCancellation(ctx, job.PublicID, userID, false); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.CleanupLoraDatasets(ctx); err != nil || count != 0 {
		t.Fatalf("cleanup training history snapshot: %d %v", count, err)
	}
	if err := repository.DeleteLoraDatasetSnapshot(ctx, userID, snapshot.ID); err != nil {
		t.Fatal(err)
	}
	job, err = repository.LoraTrainingJobByID(ctx, job.ID)
	if err != nil || job.DatasetSnapshotID != "" || job.DatasetSnapshotHash != snapshot.Hash {
		t.Fatalf("deleted snapshot lineage: %+v %v", job, err)
	}
	if count, err := repository.CleanupLoraDatasets(ctx); err != nil || count != 1 {
		t.Fatalf("orphan collection: %d %v", count, err)
	}
	if _, err := repository.LoraDatasetAsset(ctx, userID, asset.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("orphan still exists: %v", err)
	}
	// Named datasets and immutable versions have independent, serialized quotas.
	for i := 0; i < domain.LoraDatasetMaxCount; i++ {
		if _, err := repository.CreateLoraDataset(ctx, userID, fmt.Sprintf("quota-%d", i), "quota", []byte("cipher")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.CreateLoraDataset(ctx, userID, "quota-overflow", "quota", []byte("cipher")); !errors.Is(err, store.ErrLoraDatasetQuota) {
		t.Fatalf("dataset count quota: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE lora_datasets SET expires_at=now()-interval '1 day' WHERE user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if rows, err := repository.ListLoraDatasets(ctx, userID); err != nil || len(rows) != 0 {
		t.Fatalf("expired listing: %d %v", len(rows), err)
	}
	if count, err := repository.CleanupLoraDatasets(ctx); err != nil || count != 20 {
		t.Fatalf("dataset expiry: %d %v", count, err)
	}
	versions, err := repository.CreateLoraDataset(ctx, userID, "versions-quota", "versions", []byte("cipher"))
	if err != nil {
		t.Fatal(err)
	}
	var lastVersion domain.LoraDatasetSnapshot
	for i := 0; i < domain.LoraDatasetMaxSnapshots; i++ {
		versions, err = repository.SaveLoraDataset(ctx, userID, versions.ID, versions.Revision, "versions", []byte("cipher"), nil)
		if err != nil {
			t.Fatal(err)
		}
		lastVersion, err = repository.CreateLoraDatasetSnapshot(ctx, userID, versions.ID, versions.Revision, fmt.Sprintf("version-quota-%d", i), strings.Repeat("e", 64))
		if err != nil {
			t.Fatal(err)
		}
	}
	if reused, err := repository.CreateLoraDatasetSnapshot(ctx, userID, versions.ID, versions.Revision, "quota-retry", lastVersion.Hash); err != nil || reused.ID != lastVersion.ID {
		t.Fatalf("retry at version quota: %+v %v", reused, err)
	}
	if rows, err := repository.ListLoraDatasetSnapshots(ctx, userID, versions.ID); err != nil || len(rows) != 100 {
		t.Fatalf("version listing: %d %v", len(rows), err)
	}
	versions, err = repository.SaveLoraDataset(ctx, userID, versions.ID, versions.Revision, "versions", []byte("cipher-next"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateLoraDatasetSnapshot(ctx, userID, versions.ID, versions.Revision, "version-overflow", strings.Repeat("f", 64)); !errors.Is(err, store.ErrLoraDatasetQuota) {
		t.Fatalf("snapshot quota: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE lora_dataset_snapshots SET expires_at=now()-interval '1 day' WHERE user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.CleanupLoraDatasets(ctx); err != nil || count != 100 {
		t.Fatalf("unused version expiry: %d %v", count, err)
	}
	if err := repository.DeleteLoraDataset(ctx, userID, versions.ID, versions.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM lora_training_jobs WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
}
