package store

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"ai-access-gateway/internal/domain"
	"github.com/lib/pq"
)

var (
	ErrLoraDatasetConflict = errors.New("dataset revision conflict")
	ErrLoraDatasetQuota    = errors.New("dataset storage quota exceeded")
	ErrLoraDatasetAsset    = errors.New("dataset asset unavailable")
	ErrLoraDatasetInUse    = errors.New("dataset version used by active training")
)

const datasetColumns = `id,user_id,name,revision,manifest_cipher,image_count,size_bytes,created_at,updated_at,expires_at`
const datasetAssetColumns = `id,user_id,name,content_hash,mime_type,size_bytes,width,height,created_at`
const datasetSnapshotColumns = `id,COALESCE(dataset_id,''),user_id,name,revision,manifest_cipher,manifest_hash,image_count,size_bytes,created_at,expires_at`

type datasetScanner interface{ Scan(...any) error }

func scanDataset(scanner datasetScanner) (row domain.LoraDatasetRow, err error) {
	err = scanner.Scan(&row.ID, &row.UserID, &row.Name, &row.Revision, &row.ManifestCipher, &row.ImageCount, &row.SizeBytes, &row.CreatedAt, &row.UpdatedAt, &row.ExpiresAt)
	return
}
func scanDatasetAsset(scanner datasetScanner) (row domain.LoraDatasetAsset, err error) {
	err = scanner.Scan(&row.ID, &row.UserID, &row.Name, &row.Hash, &row.MIMEType, &row.SizeBytes, &row.Width, &row.Height, &row.CreatedAt)
	return
}
func scanDatasetSnapshot(scanner datasetScanner) (row domain.LoraDatasetSnapshot, err error) {
	err = scanner.Scan(&row.ID, &row.DatasetID, &row.UserID, &row.Name, &row.Revision, &row.ManifestCipher, &row.Hash, &row.ImageCount, &row.SizeBytes, &row.CreatedAt, &row.ExpiresAt)
	return
}

func (s *Store) CreateLoraDataset(ctx context.Context, userID int64, id, name string, cipher []byte) (domain.LoraDatasetRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.LoraDatasetRow{}, err
	}
	defer tx.Rollback()
	var owner, count int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&owner); err != nil {
		return domain.LoraDatasetRow{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM lora_datasets WHERE user_id=$1 AND expires_at>now()`, userID).Scan(&count); err != nil {
		return domain.LoraDatasetRow{}, err
	}
	if count >= domain.LoraDatasetMaxCount {
		return domain.LoraDatasetRow{}, ErrLoraDatasetQuota
	}
	row, err := scanDataset(tx.QueryRowContext(ctx, `INSERT INTO lora_datasets(id,user_id,name,manifest_cipher) VALUES($1,$2,$3,$4) RETURNING `+datasetColumns, id, userID, name, cipher))
	if err != nil {
		return row, err
	}
	return row, tx.Commit()
}

func (s *Store) LoraDataset(ctx context.Context, userID int64, id string) (domain.LoraDatasetRow, error) {
	return scanDataset(s.db.QueryRowContext(ctx, `SELECT `+datasetColumns+` FROM lora_datasets WHERE id=$1 AND user_id=$2 AND expires_at>now()`, id, userID))
}

func (s *Store) ListLoraDatasets(ctx context.Context, userID int64) ([]domain.LoraDatasetRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+datasetColumns+` FROM lora_datasets WHERE user_id=$1 AND expires_at>now() ORDER BY updated_at DESC,id LIMIT 20`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.LoraDatasetRow{}
	for rows.Next() {
		row, err := scanDataset(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) SaveLoraDataset(ctx context.Context, userID int64, id string, expected int64, name string, cipher []byte, assetIDs []string) (domain.LoraDatasetRow, error) {
	if len(assetIDs) > domain.LoraDatasetMaxImages {
		return domain.LoraDatasetRow{}, ErrLoraDatasetQuota
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.LoraDatasetRow{}, err
	}
	defer tx.Rollback()
	row, err := scanDataset(tx.QueryRowContext(ctx, `SELECT `+datasetColumns+` FROM lora_datasets WHERE id=$1 AND user_id=$2 AND expires_at>now() FOR UPDATE`, id, userID))
	if err != nil {
		return row, err
	}
	if row.Revision != expected {
		return row, ErrLoraDatasetConflict
	}
	// Share locks protect referenced assets from orphan collection until the
	// new manifest and its reference set are committed together.
	assets, err := tx.QueryContext(ctx, `SELECT id,size_bytes FROM lora_dataset_assets WHERE user_id=$1 AND id=ANY($2) FOR SHARE`, userID, pq.Array(assetIDs))
	if err != nil {
		return row, err
	}
	sizes := map[string]int64{}
	for assets.Next() {
		var id string
		var size int64
		if err := assets.Scan(&id, &size); err != nil {
			assets.Close()
			return row, err
		}
		sizes[id] = size
	}
	err = assets.Err()
	assets.Close()
	if err != nil {
		return row, err
	}
	var total int64
	for _, assetID := range assetIDs {
		size, ok := sizes[assetID]
		if !ok {
			return row, ErrLoraDatasetAsset
		}
		total += size
	}
	if total > domain.LoraDatasetMaxBytes {
		return row, ErrLoraDatasetQuota
	}
	row, err = scanDataset(tx.QueryRowContext(ctx, `UPDATE lora_datasets SET name=$3,manifest_cipher=$4,image_count=$5,size_bytes=$6,
		revision=nextval('lora_dataset_revision_seq'),updated_at=now(),expires_at=now()+interval '30 days'
		WHERE id=$1 AND user_id=$2 RETURNING `+datasetColumns, id, userID, name, cipher, len(assetIDs), total))
	if err != nil {
		return row, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM lora_dataset_asset_refs WHERE dataset_id=$1`, id); err != nil {
		return row, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO lora_dataset_asset_refs(asset_id,dataset_id) SELECT DISTINCT unnest($1::text[]),$2`, pq.Array(assetIDs), id); err != nil {
		return row, err
	}
	return row, tx.Commit()
}

func (s *Store) DeleteLoraDataset(ctx context.Context, userID int64, id string, revision int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM lora_datasets WHERE id=$1 AND user_id=$2 AND revision=$3`, id, userID, revision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrLoraDatasetConflict
	}
	return err
}

func (s *Store) LoraDatasetAsset(ctx context.Context, userID int64, id string) (domain.LoraDatasetAsset, error) {
	return scanDatasetAsset(s.db.QueryRowContext(ctx, `SELECT `+datasetAssetColumns+` FROM lora_dataset_assets WHERE id=$1 AND user_id=$2`, id, userID))
}

func (s *Store) LoraDatasetStorageBytes(ctx context.Context, userID int64) (int64, error) {
	var size int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(sum(size_bytes),0) FROM lora_dataset_assets WHERE user_id=$1`, userID).Scan(&size)
	return size, err
}

// next returns already encrypted chunks. The metadata and all chunks become
// visible atomically; a retry with the same hash reuses the owner's asset.
func (s *Store) InsertLoraDatasetAsset(ctx context.Context, asset domain.LoraDatasetAsset, next func() ([]byte, int, error)) (domain.LoraDatasetAsset, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return asset, err
	}
	defer tx.Rollback()
	var owner, used int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, asset.UserID).Scan(&owner); err != nil {
		return asset, err
	}
	existing, err := scanDatasetAsset(tx.QueryRowContext(ctx, `UPDATE lora_dataset_assets SET last_used_at=now() WHERE user_id=$1 AND content_hash=$2 RETURNING `+datasetAssetColumns, asset.UserID, asset.Hash))
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return asset, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(size_bytes),0) FROM lora_dataset_assets WHERE user_id=$1`, asset.UserID).Scan(&used); err != nil {
		return asset, err
	}
	if used+asset.SizeBytes > domain.LoraDatasetUserMaxBytes {
		return asset, ErrLoraDatasetQuota
	}
	asset, err = scanDatasetAsset(tx.QueryRowContext(ctx, `INSERT INTO lora_dataset_assets(id,user_id,name,content_hash,mime_type,size_bytes,width,height)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+datasetAssetColumns, asset.ID, asset.UserID, asset.Name, asset.Hash, asset.MIMEType, asset.SizeBytes, asset.Width, asset.Height))
	if err != nil {
		return asset, err
	}
	var total int64
	for index := 0; ; index++ {
		cipher, size, readErr := next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return asset, readErr
		}
		if size <= 0 || size > 1<<20 || total+int64(size) > asset.SizeBytes {
			return asset, io.ErrUnexpectedEOF
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO lora_dataset_asset_chunks(asset_id,chunk_index,plain_size,payload_cipher) VALUES($1,$2,$3,$4)`, asset.ID, index, size, cipher); err != nil {
			return asset, err
		}
		total += int64(size)
	}
	if total != asset.SizeBytes {
		return asset, io.ErrUnexpectedEOF
	}
	return asset, tx.Commit()
}

func (s *Store) ForEachLoraDatasetAssetChunk(ctx context.Context, userID int64, id string, consume func(int, []byte, int) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT c.chunk_index,c.payload_cipher,c.plain_size FROM lora_dataset_asset_chunks c
		JOIN lora_dataset_assets a ON a.id=c.asset_id WHERE a.id=$1 AND a.user_id=$2 ORDER BY c.chunk_index`, id, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var actual, size int
		var cipher []byte
		if err := rows.Scan(&actual, &cipher, &size); err != nil {
			return err
		}
		if actual != index {
			return io.ErrUnexpectedEOF
		}
		if err := consume(index, cipher, size); err != nil {
			return err
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if index == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateLoraDatasetSnapshot(ctx context.Context, userID int64, id string, revision int64, snapshotID, hash string) (domain.LoraDatasetSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.LoraDatasetSnapshot{}, err
	}
	defer tx.Rollback()
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&owner); err != nil {
		return domain.LoraDatasetSnapshot{}, err
	}
	dataset, err := scanDataset(tx.QueryRowContext(ctx, `SELECT `+datasetColumns+` FROM lora_datasets WHERE id=$1 AND user_id=$2 AND expires_at>now() FOR UPDATE`, id, userID))
	if err != nil {
		return domain.LoraDatasetSnapshot{}, err
	}
	if dataset.Revision != revision {
		return domain.LoraDatasetSnapshot{}, ErrLoraDatasetConflict
	}
	var count int
	var existing bool
	if err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(bool_or(dataset_id=$2 AND revision=$3),false) FROM lora_dataset_snapshots WHERE user_id=$1`, userID, id, revision).Scan(&count, &existing); err != nil {
		return domain.LoraDatasetSnapshot{}, err
	}
	if !existing && count >= domain.LoraDatasetMaxSnapshots {
		return domain.LoraDatasetSnapshot{}, ErrLoraDatasetQuota
	}
	snapshot, err := scanDatasetSnapshot(tx.QueryRowContext(ctx, `INSERT INTO lora_dataset_snapshots(id,dataset_id,user_id,name,revision,manifest_cipher,manifest_hash,image_count,size_bytes)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(dataset_id,revision) DO UPDATE SET expires_at=now()+interval '30 days' RETURNING `+datasetSnapshotColumns,
		snapshotID, id, userID, dataset.Name, revision, dataset.ManifestCipher, hash, dataset.ImageCount, dataset.SizeBytes))
	if err != nil {
		return snapshot, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO lora_dataset_asset_refs(asset_id,snapshot_id) SELECT asset_id,$2 FROM lora_dataset_asset_refs WHERE dataset_id=$1 ON CONFLICT DO NOTHING`, id, snapshot.ID); err != nil {
		return snapshot, err
	}
	return snapshot, tx.Commit()
}

func (s *Store) LoraDatasetSnapshot(ctx context.Context, userID int64, id string) (domain.LoraDatasetSnapshot, error) {
	return scanDatasetSnapshot(s.db.QueryRowContext(ctx, `SELECT `+datasetSnapshotColumns+` FROM lora_dataset_snapshots WHERE id=$1 AND user_id=$2`, id, userID))
}

func (s *Store) ListLoraDatasetSnapshots(ctx context.Context, userID int64, datasetID string) ([]domain.LoraDatasetSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+datasetSnapshotColumns+` FROM lora_dataset_snapshots WHERE user_id=$1 AND ($2='' OR dataset_id=$2) ORDER BY created_at DESC LIMIT 100`, userID, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.LoraDatasetSnapshot{}
	for rows.Next() {
		row, err := scanDatasetSnapshot(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) DeleteLoraDatasetSnapshot(ctx context.Context, userID int64, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM lora_dataset_snapshots WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, userID).Scan(&found); err != nil {
		return err
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lora_training_jobs WHERE dataset_snapshot_id=$1 AND state NOT IN ('completed','failed','cancelled'))`, id).Scan(&active); err != nil {
		return err
	}
	if active {
		return ErrLoraDatasetInUse
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM lora_dataset_snapshots WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CleanupLoraDatasets(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var total int64
	// Lock first, then check job references in a new statement snapshot. A
	// concurrent training insert holds a key-share lock on its dataset version.
	snapshots, err := tx.QueryContext(ctx, `SELECT s.id FROM lora_dataset_snapshots s WHERE s.expires_at<=now() AND NOT EXISTS(SELECT 1 FROM lora_training_jobs j WHERE j.dataset_snapshot_id=s.id) LIMIT 100 FOR UPDATE OF s SKIP LOCKED`)
	if err != nil {
		return total, err
	}
	snapshotIDs := []string{}
	for snapshots.Next() {
		var id string
		if err := snapshots.Scan(&id); err != nil {
			snapshots.Close()
			return total, err
		}
		snapshotIDs = append(snapshotIDs, id)
	}
	err = snapshots.Err()
	snapshots.Close()
	if err != nil {
		return total, err
	}
	deleted, err := tx.ExecContext(ctx, `DELETE FROM lora_dataset_snapshots s WHERE s.id=ANY($1) AND s.expires_at<=now() AND NOT EXISTS(SELECT 1 FROM lora_training_jobs j WHERE j.dataset_snapshot_id=s.id)`, pq.Array(snapshotIDs))
	if err != nil {
		return total, err
	}
	total, err = deleted.RowsAffected()
	if err != nil {
		return total, err
	}
	deleted, err = tx.ExecContext(ctx, `DELETE FROM lora_datasets WHERE id IN (SELECT id FROM lora_datasets WHERE expires_at<=now() LIMIT 100 FOR UPDATE SKIP LOCKED) AND expires_at<=now()`)
	if err != nil {
		return total, err
	}
	datasetCount, err := deleted.RowsAffected()
	if err != nil {
		return total, err
	}
	total += datasetCount
	rows, err := tx.QueryContext(ctx, `SELECT a.id FROM lora_dataset_assets a WHERE a.last_used_at<now()-interval '24 hours' AND NOT EXISTS(SELECT 1 FROM lora_dataset_asset_refs r WHERE r.asset_id=a.id) LIMIT 100 FOR UPDATE OF a SKIP LOCKED`)
	if err != nil {
		return total, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return total, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return total, err
	}
	// A second statement rechecks references after row locks have been acquired.
	result, err := tx.ExecContext(ctx, `DELETE FROM lora_dataset_assets a WHERE id=ANY($1) AND a.last_used_at<now()-interval '24 hours' AND NOT EXISTS(SELECT 1 FROM lora_dataset_asset_refs r WHERE r.asset_id=a.id)`, pq.Array(ids))
	if err != nil {
		return total, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return total, err
	}
	total += count
	return total, tx.Commit()
}
