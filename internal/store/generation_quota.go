package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
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
