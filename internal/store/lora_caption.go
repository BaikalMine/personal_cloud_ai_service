package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
)

var ErrLoraCaptionQuota = errors.New("caption job quota exceeded")

const loraCaptionWorkerLockID int64 = 732190355
const captionColumns = `id,user_id,COALESCE(dataset_id,''),image_id,asset_id,request_key,instruction_version,input_cipher,result_cipher,state,status,error,attempts,run_token,cancel_requested,created_at,updated_at,expires_at,available_at`

func scanCaption(scanner datasetScanner) (row domain.LoraCaptionJob, err error) {
	err = scanner.Scan(&row.ID, &row.UserID, &row.DatasetID, &row.ImageID, &row.AssetID, &row.RequestKey, &row.InstructionVersion, &row.InputCipher, &row.ResultCipher, &row.State, &row.Status, &row.Error, &row.Attempts, &row.RunToken, &row.CancelRequested, &row.CreatedAt, &row.UpdatedAt, &row.ExpiresAt, &row.AvailableAt)
	return
}

func (s *Store) EnqueueLoraCaptions(ctx context.Context, userID int64, datasetID string, revision int64, jobs []domain.LoraCaptionJob) ([]domain.LoraCaptionJob, error) {
	if len(jobs) > domain.LoraCaptionMaxPending {
		return nil, ErrLoraCaptionQuota
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&owner); err != nil {
		return nil, err
	}
	if datasetID != "" {
		var current int64
		if err = tx.QueryRowContext(ctx, `SELECT revision FROM lora_datasets WHERE id=$1 AND user_id=$2 AND expires_at>now() FOR SHARE`, datasetID, userID).Scan(&current); err != nil {
			return nil, err
		}
		if current != revision {
			return nil, ErrLoraDatasetConflict
		}
	}
	var pending, total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE state IN ('queued','running')),count(*) FROM lora_caption_jobs WHERE user_id=$1`, userID).Scan(&pending, &total); err != nil {
		return nil, err
	}
	result := []domain.LoraCaptionJob{}
	for _, job := range jobs {
		// Reuse completed identical requests and any still-active job for this item.
		existing, e := scanCaption(tx.QueryRowContext(ctx, `SELECT `+captionColumns+` FROM lora_caption_jobs WHERE user_id=$1 AND (request_key=$2 OR ($3<>'' AND dataset_id=$3 AND image_id=$4 AND state IN ('queued','running'))) ORDER BY (state IN ('queued','running')) DESC,created_at DESC LIMIT 1`, userID, job.RequestKey, datasetID, job.ImageID))
		if e == nil {
			result = append(result, existing)
			continue
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		limit := domain.LoraCaptionMaxPending
		if datasetID == "" {
			limit = 4
		}
		if pending >= limit || total >= domain.LoraCaptionMaxJobs {
			return nil, ErrLoraCaptionQuota
		}
		var asset string
		if err = tx.QueryRowContext(ctx, `SELECT id FROM lora_dataset_assets WHERE id=$1 AND user_id=$2 FOR SHARE`, job.AssetID, userID).Scan(&asset); err != nil {
			return nil, err
		}
		row, e := scanCaption(tx.QueryRowContext(ctx, `INSERT INTO lora_caption_jobs(id,user_id,dataset_id,image_id,asset_id,request_key,instruction_version,input_cipher) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8) RETURNING `+captionColumns, job.ID, userID, datasetID, job.ImageID, job.AssetID, job.RequestKey, job.InstructionVersion, job.InputCipher))
		if e != nil {
			return nil, e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO lora_dataset_asset_refs(asset_id,caption_job_id) VALUES($1,$2)`, job.AssetID, row.ID); err != nil {
			return nil, err
		}
		pending++
		total++
		result = append(result, row)
	}
	return result, tx.Commit()
}

func (s *Store) LoraCaptionJob(ctx context.Context, userID int64, id string) (domain.LoraCaptionJob, error) {
	return scanCaption(s.db.QueryRowContext(ctx, `SELECT `+captionColumns+` FROM lora_caption_jobs WHERE id=$1 AND user_id=$2`, id, userID))
}

func (s *Store) ListLoraCaptionJobs(ctx context.Context, userID int64, datasetID string) ([]domain.LoraCaptionJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+captionColumns+` FROM lora_caption_jobs WHERE user_id=$1 AND COALESCE(dataset_id,'')=$2 ORDER BY created_at DESC,id LIMIT 500`, userID, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.LoraCaptionJob{}
	for rows.Next() {
		row, err := scanCaption(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// The session lock stays held for the entire model call. A second Gateway
// cannot take an old-looking job while its owning worker is still connected.
func (s *Store) WithLoraCaptionWorker(ctx context.Context, work func(*sql.Conn) error) (bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	var acquired bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, loraCaptionWorkerLockID).Scan(&acquired); err != nil || !acquired {
		return false, err
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(release, `SELECT pg_advisory_unlock($1)`, loraCaptionWorkerLockID); err != nil {
			conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	return true, work(conn)
}

// Must be called only while holding WithLoraCaptionWorker's session lock.
func (s *Store) ClaimLoraCaptionJob(ctx context.Context, token string) (domain.LoraCaptionJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.LoraCaptionJob{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE lora_caption_jobs SET state=CASE WHEN cancel_requested THEN 'cancelled' WHEN attempts>=$1 THEN 'failed' ELSE 'queued' END,
		status='Восстановлено после остановки обработчика',error=CASE WHEN attempts>=$1 THEN 'Обработчик был прерван несколько раз; повторите задание' ELSE '' END,
		run_token='',available_at=now(),updated_at=now(),expires_at=CASE WHEN cancel_requested OR attempts>=$1 THEN now()+interval '24 hours' ELSE expires_at END WHERE state='running'`, domain.LoraCaptionMaxAttempts); err != nil {
		return domain.LoraCaptionJob{}, err
	}
	row, err := scanCaption(tx.QueryRowContext(ctx, `UPDATE lora_caption_jobs SET state='running',run_token=$1,attempts=attempts+1,status='Ожидаем vision-модель',error='',updated_at=now()
		WHERE id=(SELECT id FROM lora_caption_jobs WHERE state='queued' AND available_at<=now() AND NOT cancel_requested ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING `+captionColumns, token))
	if errors.Is(err, sql.ErrNoRows) {
		return row, tx.Commit()
	}
	if err != nil {
		return row, err
	}
	return row, tx.Commit()
}

func (s *Store) LoraCaptionRunActive(ctx context.Context, id, token string) (bool, error) {
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lora_caption_jobs j JOIN users u ON u.id=j.user_id WHERE j.id=$1 AND j.run_token=$2 AND j.state='running' AND NOT j.cancel_requested AND NOT u.disabled AND (u.role='admin' OR u.can_train_image_lora) AND (u.account_expires_at IS NULL OR u.account_expires_at>now()))`, id, token).Scan(&active)
	return active, err
}

func (s *Store) FinishLoraCaptionJob(ctx context.Context, id, token, state, message string, result []byte, retryAfter time.Duration) (bool, error) {
	if result == nil {
		result = []byte{}
	}
	res, err := s.db.ExecContext(ctx, `UPDATE lora_caption_jobs SET state=CASE WHEN cancel_requested THEN 'cancelled' ELSE $3 END,
		status=CASE WHEN cancel_requested THEN 'Описание отменено' ELSE $4 END,error=CASE WHEN $3='failed' AND NOT cancel_requested THEN $4 ELSE '' END,
		result_cipher=CASE WHEN cancel_requested THEN ''::bytea ELSE $5 END,run_token='',updated_at=now(),available_at=now()+($6::bigint*interval '1 second'),
		expires_at=now()+CASE WHEN cancel_requested OR $3 IN ('failed','cancelled') THEN interval '24 hours' ELSE interval '30 days' END
		WHERE id=$1 AND run_token=$2 AND state='running'`, id, token, state, message, result, int64(retryAfter.Seconds()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) CancelLoraCaptions(ctx context.Context, userID int64, datasetID, id string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE lora_caption_jobs SET cancel_requested=true,state=CASE WHEN state='queued' THEN 'cancelled' ELSE state END,
		status='Отмена описания',updated_at=now(),expires_at=now()+interval '24 hours' WHERE user_id=$1 AND ($2='' OR dataset_id=$2) AND ($3='' OR id=$3) AND state IN ('queued','running')`, userID, datasetID, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) RetryLoraCaption(ctx context.Context, userID int64, id string) (domain.LoraCaptionJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.LoraCaptionJob{}, err
	}
	defer tx.Rollback()
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&owner); err != nil {
		return domain.LoraCaptionJob{}, err
	}
	row, err := scanCaption(tx.QueryRowContext(ctx, `SELECT `+captionColumns+` FROM lora_caption_jobs WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, userID))
	if err != nil {
		return row, err
	}
	if row.State != "failed" && row.State != "cancelled" {
		return row, tx.Commit()
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM lora_caption_jobs WHERE user_id=$1 AND state IN ('queued','running')`, userID).Scan(&count); err != nil {
		return row, err
	}
	limit := domain.LoraCaptionMaxPending
	if row.DatasetID == "" {
		limit = 4
	}
	if count >= limit {
		return row, ErrLoraCaptionQuota
	}
	if row.DatasetID != "" {
		var active bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lora_caption_jobs WHERE dataset_id=$1 AND image_id=$2 AND state IN ('queued','running'))`, row.DatasetID, row.ImageID).Scan(&active); err != nil {
			return row, err
		}
		if active {
			return row, ErrLoraDatasetConflict
		}
	}
	row, err = scanCaption(tx.QueryRowContext(ctx, `UPDATE lora_caption_jobs SET state='queued',cancel_requested=false,attempts=0,status='Повтор описания поставлен в очередь',error='',result_cipher='',available_at=now(),updated_at=now(),expires_at=now()+interval '30 days' WHERE id=$1 RETURNING `+captionColumns, id))
	if err != nil {
		return row, err
	}
	return row, tx.Commit()
}

func (s *Store) CleanupLoraCaptionJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM lora_caption_jobs WHERE id IN (SELECT id FROM lora_caption_jobs WHERE expires_at<=now() AND state IN ('completed','failed','cancelled') LIMIT 100 FOR UPDATE SKIP LOCKED)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
