package store

import (
	"context"
	"database/sql"
	"errors"

	"ai-access-gateway/internal/domain"
)

var ErrDefaultMinerRequired = errors.New("at least one enabled default miner is required")

type CreateMinerParams struct {
	Name            string
	ScriptPath      string
	ProcessName     string
	IconMIME        string
	IconData        []byte
	Enabled         bool
	Default         bool
	CreatedByUserID int64
}

func (s *Store) ListMiners(ctx context.Context, includeDisabled bool) ([]domain.Miner, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, script_path, process_name, icon_mime, enabled, is_default,
		       created_by_user_id, created_at, updated_at
		FROM miners
		WHERE $1 OR enabled
		ORDER BY is_default DESC, enabled DESC, name, id
	`, includeDisabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var miners []domain.Miner
	for rows.Next() {
		var miner domain.Miner
		if err := rows.Scan(
			&miner.ID, &miner.Name, &miner.ScriptPath, &miner.ProcessName, &miner.IconMIME,
			&miner.Enabled, &miner.Default, &miner.CreatedByUserID, &miner.CreatedAt, &miner.UpdatedAt,
		); err != nil {
			return nil, err
		}
		miners = append(miners, miner)
	}
	return miners, rows.Err()
}

func (s *Store) DefaultMiner(ctx context.Context) (domain.Miner, error) {
	return s.scanMiner(s.db.QueryRowContext(ctx, `
		SELECT id, name, script_path, process_name, icon_mime, icon_data, enabled, is_default,
		       created_by_user_id, created_at, updated_at
		FROM miners WHERE is_default AND enabled LIMIT 1
	`))
}

func (s *Store) MinerByID(ctx context.Context, id int64) (domain.Miner, error) {
	return s.scanMiner(s.db.QueryRowContext(ctx, `
		SELECT id, name, script_path, process_name, icon_mime, icon_data, enabled, is_default,
		       created_by_user_id, created_at, updated_at
		FROM miners WHERE id = $1
	`, id))
}

func (s *Store) MinerIcon(ctx context.Context, id int64) (domain.MinerIcon, error) {
	var icon domain.MinerIcon
	err := s.db.QueryRowContext(ctx, `SELECT icon_mime, icon_data FROM miners WHERE id = $1`, id).Scan(&icon.MIME, &icon.Data)
	return icon, err
}

func (s *Store) CreateMiner(ctx context.Context, params CreateMinerParams) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if params.Default {
		if _, err := tx.ExecContext(ctx, `UPDATE miners SET is_default = false, updated_at = now() WHERE is_default`); err != nil {
			return 0, err
		}
	}
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO miners (name, script_path, process_name, icon_mime, icon_data, enabled, is_default, created_by_user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id
	`, params.Name, params.ScriptPath, params.ProcessName, params.IconMIME, params.IconData,
		params.Enabled, params.Default, params.CreatedByUserID).Scan(&id)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) SetDefaultMiner(ctx context.Context, id int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM miners WHERE id=$1 AND enabled)`, id).Scan(&exists); err != nil || !exists {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE miners SET is_default=false, updated_at=now() WHERE is_default AND id<>$1`, id); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE miners SET is_default=true, updated_at=now() WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *Store) SetMinerEnabled(ctx context.Context, id int64, enabled bool) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var isDefault bool
	if err := tx.QueryRowContext(ctx, `SELECT is_default FROM miners WHERE id=$1 FOR UPDATE`, id).Scan(&isDefault); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if !enabled && isDefault {
		return false, ErrDefaultMinerRequired
	}
	result, err := tx.ExecContext(ctx, `UPDATE miners SET enabled=$2, updated_at=now() WHERE id=$1`, id, enabled)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *Store) DeleteMiner(ctx context.Context, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM miners WHERE id=$1 AND is_default=false`, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

type rowScanner interface {
	Scan(...any) error
}

func (s *Store) scanMiner(row rowScanner) (domain.Miner, error) {
	var miner domain.Miner
	err := row.Scan(
		&miner.ID, &miner.Name, &miner.ScriptPath, &miner.ProcessName, &miner.IconMIME, &miner.IconData,
		&miner.Enabled, &miner.Default, &miner.CreatedByUserID, &miner.CreatedAt, &miner.UpdatedAt,
	)
	return miner, err
}
