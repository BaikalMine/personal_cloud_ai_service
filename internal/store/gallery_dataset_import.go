package store

import (
	"context"
	"database/sql"
	"errors"

	"ai-access-gateway/internal/domain"
)

var ErrGalleryImportConflict = errors.New("gallery import request identity changed")

const galleryImportColumns = `user_id,request_id,request_sha256,media_id,dataset_id,image_id,revision,already_present,cancelled,created_at`

func scanGalleryImport(scanner datasetScanner) (row domain.GalleryDatasetImportReceipt, err error) {
	err = scanner.Scan(&row.UserID, &row.RequestID, &row.RequestSHA256, &row.MediaID, &row.DatasetID, &row.ImageID, &row.Revision, &row.AlreadyPresent, &row.Cancelled, &row.CreatedAt)
	return
}

func (s *Store) GalleryDatasetImport(ctx context.Context, userID int64, requestID string) (domain.GalleryDatasetImportReceipt, error) {
	return scanGalleryImport(s.db.QueryRowContext(ctx, `SELECT `+galleryImportColumns+` FROM gallery_dataset_imports WHERE user_id=$1 AND request_id=$2`, userID, requestID))
}

// The callback only transforms the locked encrypted manifest. It must not call
// the store or external services while this transaction holds owner/row locks.
func (s *Store) ImportGalleryDataset(ctx context.Context, p domain.GalleryDatasetImportParams,
	prepare func(domain.LoraDatasetRow) (name string, cipher []byte, assets []string, imageID string, already bool, err error),
) (domain.GalleryDatasetImportReceipt, error) {
	var empty domain.GalleryDatasetImportReceipt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var disabled, expired, allowed bool
	if err = tx.QueryRowContext(ctx, `SELECT disabled,COALESCE(account_expires_at<=now(),false),(role='admin' OR can_train_image_lora) FROM users WHERE id=$1 FOR UPDATE`, p.UserID).Scan(&disabled, &expired, &allowed); err != nil {
		return empty, err
	}
	if disabled || expired || !allowed {
		return empty, ErrQuickGenerationForbidden
	}
	existing, err := scanGalleryImport(tx.QueryRowContext(ctx, `SELECT `+galleryImportColumns+` FROM gallery_dataset_imports WHERE user_id=$1 AND request_id=$2`, p.UserID, p.RequestID))
	if err == nil {
		if existing.Cancelled {
			return existing, tx.Commit()
		}
		if existing.RequestSHA256 != p.RequestSHA256 || existing.MediaID != p.MediaID {
			return empty, ErrGalleryImportConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	var sourceID int64
	if err = tx.QueryRowContext(ctx, `SELECT m.id FROM content_media m JOIN content_events e ON e.id=m.event_id
		WHERE m.id=$1 AND e.user_id=$2 AND e.service='comfyui' AND e.kind='comfyui_prompt'
		AND m.media_type='image' AND m.size_bytes=$3 AND m.expires_at>now() AND e.expires_at>now()
		AND m.profile_hidden_at IS NULL FOR SHARE OF m,e`, p.MediaID, p.UserID, p.MediaSize).Scan(&sourceID); err != nil {
		return empty, err
	}
	var assetID string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM lora_dataset_assets WHERE id=$1 AND user_id=$2 FOR SHARE`, p.AssetID, p.UserID).Scan(&assetID); err != nil {
		return empty, err
	}
	row := domain.LoraDatasetRow{ID: p.DatasetID, UserID: p.UserID}
	if p.Create {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM lora_datasets WHERE user_id=$1 AND expires_at>now()`, p.UserID).Scan(&count); err != nil {
			return empty, err
		}
		if count >= domain.LoraDatasetMaxCount {
			return empty, ErrLoraDatasetQuota
		}
	} else {
		row, err = scanDataset(tx.QueryRowContext(ctx, `SELECT `+datasetColumns+` FROM lora_datasets WHERE id=$1 AND user_id=$2 AND expires_at>now() FOR UPDATE`, p.DatasetID, p.UserID))
		if err != nil {
			return empty, err
		}
		if row.Revision != p.ExpectedRevision {
			return empty, ErrLoraDatasetConflict
		}
	}
	name, cipher, assets, imageID, already, err := prepare(row)
	if err != nil {
		return empty, err
	}
	if p.Create {
		row, err = scanDataset(tx.QueryRowContext(ctx, `INSERT INTO lora_datasets(id,user_id,name,manifest_cipher) VALUES($1,$2,$3,$4) RETURNING `+datasetColumns, p.DatasetID, p.UserID, name, cipher))
		if err != nil {
			return empty, err
		}
	}
	if !already {
		row, err = saveLoraDatasetTx(ctx, tx, row, name, cipher, assets)
		if err != nil {
			return empty, err
		}
	}
	receipt, err := scanGalleryImport(tx.QueryRowContext(ctx, `INSERT INTO gallery_dataset_imports(user_id,request_id,request_sha256,media_id,dataset_id,image_id,revision,already_present)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+galleryImportColumns, p.UserID, p.RequestID, p.RequestSHA256, p.MediaID, row.ID, imageID, row.Revision, already))
	if err != nil {
		return empty, err
	}
	return receipt, tx.Commit()
}

func (s *Store) DiscardGalleryDatasetImport(ctx context.Context, userID int64, requestID string, mediaID int64) (domain.GalleryDatasetImportReceipt, error) {
	var empty domain.GalleryDatasetImportReceipt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&owner); err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO gallery_dataset_imports(user_id,request_id,request_sha256,media_id,dataset_id,image_id,revision,cancelled)
		VALUES($1,$2,'',$3,'','',0,true) ON CONFLICT(user_id,request_id) DO NOTHING`, userID, requestID, mediaID); err != nil {
		return empty, err
	}
	row, err := scanGalleryImport(tx.QueryRowContext(ctx, `SELECT `+galleryImportColumns+` FROM gallery_dataset_imports WHERE user_id=$1 AND request_id=$2`, userID, requestID))
	if err != nil {
		return empty, err
	}
	return row, tx.Commit()
}
