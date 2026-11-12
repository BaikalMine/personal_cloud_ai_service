package store

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) ModelInstallReceipt(ctx context.Context, id string) (ModelInstall, error) {
	j, err := scanModelInstall(s.db.QueryRowContext(ctx, `SELECT `+modelInstallColumns+` FROM model_install_jobs WHERE id=$1`, id))
	if !errors.Is(err, sql.ErrNoRows) {
		return j, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT id,coalesce(owner_id,0),created_at FROM model_install_requests WHERE id=$1 AND closed`, id).Scan(&j.ID, &j.OwnerID, &j.CreatedAt)
	j.State = "cancelled"
	return j, err
}

// Closing an unaccepted request leaves a durable fence against a delayed POST.
func (s *Store) CloseModelInstallRequest(ctx context.Context, id string, actorID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actor int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 AND role='admin' AND NOT disabled AND (account_expires_at IS NULL OR account_expires_at>now()) FOR SHARE`, actorID).Scan(&actor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO model_install_requests(id,owner_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, actorID); err != nil {
		return err
	}
	var owner int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(owner_id,0) FROM model_install_requests WHERE id=$1 FOR UPDATE`, id).Scan(&owner); err != nil {
		return err
	}
	if owner != actorID {
		return ErrModelInstallConflict
	}
	var state string
	err = tx.QueryRowContext(ctx, `SELECT state FROM model_install_jobs WHERE id=$1 FOR UPDATE`, id).Scan(&state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && state != "queued" && state != "cancelled" {
		return ErrModelInstallConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE model_install_requests SET closed=true WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE model_install_jobs SET state='cancelled',updated_at=now() WHERE id=$1 AND state='queued'`, id); err != nil {
		return err
	}
	return tx.Commit()
}
