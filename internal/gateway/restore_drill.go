package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	contentcrypto "ai-access-gateway/internal/content"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func (a *App) startBackgroundWork(ctx context.Context) {
	if a.cfg.RestoreDrillMode || a.cfg.ReleaseValidation != nil {
		close(a.maintenanceDone)
		return
	}
	go func() {
		defer close(a.maintenanceDone)
		a.runMaintenance(ctx)
	}()
	go func() {
		classificationCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		a.classifyPendingSensitiveContent(classificationCtx)
	}()
	a.queueSensitiveMediaClassification()
}

type TrainingDatasetRestoreReport struct {
	Datasets  int   `json:"datasets"`
	Snapshots int   `json:"snapshots"`
	Assets    int   `json:"assets"`
	Bytes     int64 `json:"bytes"`
}

// VerifyTrainingDatasetRestore reads an isolated, migrated database with workers
// stopped. It uses the same decryption and chunk integrity path as dataset downloads.
// No training, inference, retention or schema migration is started by this function.
func VerifyTrainingDatasetRestore(ctx context.Context, db *sql.DB, secret string) (report TrainingDatasetRestoreReport, err error) {
	cipher, err := contentcrypto.NewCipher(secret)
	if err != nil {
		return report, err
	}
	spool, err := os.MkdirTemp("", "gateway-restore-verify-")
	if err != nil {
		return report, err
	}
	defer os.Remove(spool) // materialized assets remove their own files on Close
	app := &App{cfg: Config{MediaSpoolDir: spool, MediaInFlightLimitBytes: 64 << 20}, store: store.New(db), contentCipher: cipher}
	rows, err := db.QueryContext(ctx, `
		SELECT 'dataset',id,user_id,manifest_cipher,'' FROM lora_datasets
		UNION ALL SELECT 'snapshot',id,user_id,manifest_cipher,manifest_hash FROM lora_dataset_snapshots`)
	if err != nil {
		return report, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id, expectedHash string
		var owner int64
		var encrypted []byte
		if err := rows.Scan(&kind, &id, &owner, &encrypted, &expectedHash); err != nil {
			return report, err
		}
		plain, err := cipher.DecryptBytes(encrypted)
		if err != nil {
			return report, fmt.Errorf("cannot decrypt %s %s: check SESSION_SECRET and backup integrity", kind, id)
		}
		hash := sha256.Sum256(plain)
		var manifest domain.LoraDatasetManifest
		err = json.Unmarshal(plain, &manifest)
		clear(plain)
		if err != nil || validateLoraDatasetManifest(manifest) != nil {
			return report, fmt.Errorf("invalid restored %s manifest: %s", kind, id)
		}
		if kind == "snapshot" && expectedHash != hex.EncodeToString(hash[:]) {
			return report, fmt.Errorf("restored snapshot hash mismatch: %s", id)
		}
		for _, image := range manifest.Images {
			if _, err := app.store.LoraDatasetAsset(ctx, owner, image.AssetID); err != nil {
				return report, fmt.Errorf("restored %s %s has missing/foreign asset %s", kind, id, image.AssetID)
			}
		}
		if kind == "dataset" {
			report.Datasets++
		} else {
			report.Snapshots++
		}
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	rows.Close()
	assets, err := db.QueryContext(ctx, `SELECT id,user_id FROM lora_dataset_assets ORDER BY id`)
	if err != nil {
		return report, err
	}
	defer assets.Close()
	for assets.Next() {
		var id string
		var owner int64
		if err := assets.Scan(&id, &owner); err != nil {
			return report, err
		}
		asset, err := app.store.LoraDatasetAsset(ctx, owner, id)
		if err != nil {
			return report, err
		}
		media, err := app.materializeLoraDatasetAsset(ctx, asset)
		if err != nil {
			return report, fmt.Errorf("restored dataset asset %s failed decryption/hash validation: %w", id, err)
		}
		media.Close()
		report.Assets++
		report.Bytes += asset.SizeBytes
	}
	return report, assets.Err()
}
