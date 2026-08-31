package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"ai-access-gateway/internal/domain"
)

var (
	ErrQuickGenerationForbidden  = errors.New("quick generation is not allowed")
	ErrQuickGenerationDailyLimit = errors.New("daily quick generation limit reached")
	ErrQuickGenerationTotalLimit = errors.New("total quick generation limit reached")
)

type QuickGenerationReservation struct {
	UserID    int64
	UsageDate time.Time
}

// ReserveQuickGenerationForJob couples quota accounting to the durable job.
// Repeated calls return the original reservation without charging twice.
func (s *Store) ReserveQuickGenerationForJob(ctx context.Context, jobID, userID int64) (QuickGenerationReservation, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return QuickGenerationReservation{}, false, err
	}
	defer tx.Rollback()
	var state string
	var reservedOn sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT state,quota_reserved_on FROM generation_jobs
		WHERE id=$1 AND user_id=$2 FOR UPDATE`, jobID, userID).Scan(&state, &reservedOn); err != nil {
		return QuickGenerationReservation{}, false, err
	}
	if reservedOn.Valid {
		if err := tx.Commit(); err != nil {
			return QuickGenerationReservation{}, false, err
		}
		return QuickGenerationReservation{UserID: userID, UsageDate: reservedOn.Time}, false, nil
	}
	if domain.GenerationJobState(state) != domain.GenerationJobWaitingForResources {
		return QuickGenerationReservation{}, false, fmt.Errorf("%w: reserve quota in state %s", ErrGenerationJobStateConflict, state)
	}
	var allowed bool
	var dailyLimit int
	var totalLimit, totalUsed int64
	if err := tx.QueryRowContext(ctx, `SELECT can_use_quick_generation,generation_daily_limit,generation_total_limit,generation_total_used
		FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&allowed, &dailyLimit, &totalLimit, &totalUsed); err != nil {
		return QuickGenerationReservation{}, false, err
	}
	if !allowed {
		return QuickGenerationReservation{}, false, ErrQuickGenerationForbidden
	}
	if totalLimit > 0 && totalUsed >= totalLimit {
		return QuickGenerationReservation{}, false, ErrQuickGenerationTotalLimit
	}
	var usageDate time.Time
	if err := tx.QueryRowContext(ctx, `SELECT timezone('Europe/Moscow',now())::date`).Scan(&usageDate); err != nil {
		return QuickGenerationReservation{}, false, err
	}
	var usedToday int
	err = tx.QueryRowContext(ctx, `SELECT used_count FROM quick_generation_daily_usage
		WHERE user_id=$1 AND usage_date=$2`, userID, usageDate).Scan(&usedToday)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return QuickGenerationReservation{}, false, err
	}
	if dailyLimit > 0 && usedToday >= dailyLimit {
		return QuickGenerationReservation{}, false, ErrQuickGenerationDailyLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quick_generation_daily_usage(user_id,usage_date,used_count)
		VALUES($1,$2,1) ON CONFLICT(user_id,usage_date) DO UPDATE
		SET used_count=quick_generation_daily_usage.used_count+1`, userID, usageDate); err != nil {
		return QuickGenerationReservation{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET generation_total_used=generation_total_used+1 WHERE id=$1`, userID); err != nil {
		return QuickGenerationReservation{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_jobs
		SET quota_reserved_on=$2,updated_at=now() WHERE id=$1`, jobID, usageDate); err != nil {
		return QuickGenerationReservation{}, false, err
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return QuickGenerationReservation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return QuickGenerationReservation{}, false, err
	}
	return QuickGenerationReservation{UserID: userID, UsageDate: usageDate}, true, nil
}

func (s *Store) CommitQuickGenerationForJob(ctx context.Context, jobID int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var reservedOn, committedAt sql.NullTime
	var promptID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT quota_reserved_on,quota_committed_at,prompt_id
		FROM generation_jobs WHERE id=$1 FOR UPDATE`, jobID).Scan(&reservedOn, &committedAt, &promptID); err != nil {
		return false, err
	}
	if committedAt.Valid {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if !reservedOn.Valid || !promptID.Valid || promptID.String == "" {
		return false, fmt.Errorf("%w: cannot commit unbound quota reservation", ErrGenerationJobStateConflict)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_jobs
		SET quota_committed_at=now(),updated_at=now() WHERE id=$1`, jobID); err != nil {
		return false, err
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ReleaseQuickGenerationForJob(ctx context.Context, jobID int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var userID sql.NullInt64
	var reservedOn, committedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT user_id,quota_reserved_on,quota_committed_at
		FROM generation_jobs WHERE id=$1 FOR UPDATE`, jobID).Scan(&userID, &reservedOn, &committedAt); err != nil {
		return false, err
	}
	if !reservedOn.Valid || committedAt.Valid {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if userID.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE quick_generation_daily_usage
			SET used_count=GREATEST(used_count-1,0) WHERE user_id=$1 AND usage_date=$2`, userID.Int64, reservedOn.Time); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET generation_total_used=GREATEST(generation_total_used-1,0)
			WHERE id=$1`, userID.Int64); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_jobs
		SET quota_reserved_on=NULL,updated_at=now() WHERE id=$1`, jobID); err != nil {
		return false, err
	}
	if err := incrementGenerationJobRevision(ctx, tx); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// QuickGenerationQuota describes the current usage of the limits applied to a
// user. Daily usage is measured in the same Moscow calendar day as reservation.
type QuickGenerationQuota struct {
	DailyLimit int
	DailyUsed  int
	TotalLimit int64
	TotalUsed  int64
}

func (s *Store) QuickGenerationQuota(ctx context.Context, userID int64) (QuickGenerationQuota, error) {
	var quota QuickGenerationQuota
	err := s.db.QueryRowContext(ctx, `
		SELECT u.generation_daily_limit, COALESCE(d.used_count, 0),
		       u.generation_total_limit, u.generation_total_used
		FROM users u
		LEFT JOIN quick_generation_daily_usage d
		  ON d.user_id = u.id
		 AND d.usage_date = timezone('Europe/Moscow', now())::date
		WHERE u.id = $1
	`, userID).Scan(&quota.DailyLimit, &quota.DailyUsed, &quota.TotalLimit, &quota.TotalUsed)
	return quota, err
}

// ReserveQuickGeneration atomically reserves one accepted quick-generation run.
// A failed upstream submission must call ReleaseQuickGeneration.
func (s *Store) ReserveQuickGeneration(ctx context.Context, userID int64) (QuickGenerationReservation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return QuickGenerationReservation{}, err
	}
	defer tx.Rollback()

	var allowed bool
	var dailyLimit int
	var totalLimit, totalUsed int64
	err = tx.QueryRowContext(ctx, `
		SELECT can_use_quick_generation, generation_daily_limit, generation_total_limit, generation_total_used
		FROM users WHERE id = $1 FOR UPDATE
	`, userID).Scan(&allowed, &dailyLimit, &totalLimit, &totalUsed)
	if err != nil {
		return QuickGenerationReservation{}, err
	}
	if !allowed {
		return QuickGenerationReservation{}, ErrQuickGenerationForbidden
	}
	if totalLimit > 0 && totalUsed >= totalLimit {
		return QuickGenerationReservation{}, ErrQuickGenerationTotalLimit
	}

	var usageDate time.Time
	if err := tx.QueryRowContext(ctx, `SELECT timezone('Europe/Moscow', now())::date`).Scan(&usageDate); err != nil {
		return QuickGenerationReservation{}, err
	}
	var usedToday int
	err = tx.QueryRowContext(ctx, `
		SELECT used_count FROM quick_generation_daily_usage
		WHERE user_id = $1 AND usage_date = $2
	`, userID, usageDate).Scan(&usedToday)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return QuickGenerationReservation{}, err
	}
	if dailyLimit > 0 && usedToday >= dailyLimit {
		return QuickGenerationReservation{}, ErrQuickGenerationDailyLimit
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO quick_generation_daily_usage (user_id, usage_date, used_count)
		VALUES ($1,$2,1)
		ON CONFLICT (user_id, usage_date) DO UPDATE SET used_count = quick_generation_daily_usage.used_count + 1
	`, userID, usageDate); err != nil {
		return QuickGenerationReservation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET generation_total_used = generation_total_used + 1 WHERE id = $1
	`, userID); err != nil {
		return QuickGenerationReservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return QuickGenerationReservation{}, err
	}
	return QuickGenerationReservation{UserID: userID, UsageDate: usageDate}, nil
}

func (s *Store) ReleaseQuickGeneration(ctx context.Context, reservation QuickGenerationReservation) error {
	if reservation.UserID == 0 || reservation.UsageDate.IsZero() {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE quick_generation_daily_usage
		SET used_count = GREATEST(used_count - 1, 0)
		WHERE user_id = $1 AND usage_date = $2
	`, reservation.UserID, reservation.UsageDate); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE users SET generation_total_used = GREATEST(generation_total_used - 1, 0) WHERE id = $1
	`, reservation.UserID); err != nil {
		return err
	}
	return tx.Commit()
}
