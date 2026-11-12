package store

import (
	"context"
	"database/sql/driver"
	"time"
)

// Shares the download lock so a file cannot be removed while Gateway installs models.
func (s *Store) WithModelRemovalGuard(ctx context.Context, operation func() error) error {
	return s.withModelFilesGuard(ctx, true, operation)
}

// Starting an absent ComfyUI must remain possible with waiting generation jobs.
// File installation and training still exclude lifecycle operations.
func (s *Store) WithComfyRuntimeGuard(ctx context.Context, stopping bool, operation func() error) error {
	return s.withModelFilesGuard(ctx, stopping, operation)
}

func (s *Store) withModelFilesGuard(ctx context.Context, requireIdle bool, operation func() error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked bool
	if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(746218497)").Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return ErrModelInstallConflict
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(cleanup, "SELECT pg_advisory_unlock(746218497)"); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	var busy bool
	err = conn.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM model_install_jobs WHERE state IN ('queued','running','reconciling'))
 OR ($1 AND EXISTS(SELECT 1 FROM generation_jobs WHERE state NOT IN ('completed','failed','cancelled','expired')))
 OR EXISTS(SELECT 1 FROM lora_training_jobs WHERE state NOT IN ('completed','failed','cancelled','expired'))
 OR ($1 AND EXISTS(SELECT 1 FROM gpu_work WHERE state IN ('held','uncertain')))`, requireIdle).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return ErrModelInstallConflict
	}
	return operation()
}
