package store

import (
	"context"
	"database/sql"

	"ai-access-gateway/internal/domain"
)

func (s *Store) CreateQuickGenerationMiningLease(ctx context.Context, lease domain.QuickGenerationMiningLease) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO quick_generation_mining_leases
			(id, prompt_id, user_id, miner_id, script_path, process_name, resume_mining)
		VALUES ($1, NULLIF($2,''), $3, $4, $5, $6, $7)
	`, lease.ID, lease.PromptID, lease.UserID, lease.MinerID, lease.ScriptPath, lease.ProcessName, lease.ResumeMining)
	return err
}

func (s *Store) AttachQuickGenerationMiningLease(ctx context.Context, leaseID, promptID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE quick_generation_mining_leases SET prompt_id = $2
		WHERE id = $1 AND prompt_id IS NULL
	`, leaseID, promptID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (s *Store) ActiveQuickGenerationMiningLease(ctx context.Context) (domain.QuickGenerationMiningLease, error) {
	return scanQuickGenerationMiningLease(s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(prompt_id,''), user_id, miner_id, script_path, process_name, resume_mining, created_at
		FROM quick_generation_mining_leases
		ORDER BY created_at ASC LIMIT 1
	`))
}

func (s *Store) QuickGenerationMiningLeaseByPrompt(ctx context.Context, promptID string) (domain.QuickGenerationMiningLease, error) {
	return scanQuickGenerationMiningLease(s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(prompt_id,''), user_id, miner_id, script_path, process_name, resume_mining, created_at
		FROM quick_generation_mining_leases WHERE prompt_id = $1
	`, promptID))
}

func (s *Store) ListQuickGenerationMiningLeases(ctx context.Context) ([]domain.QuickGenerationMiningLease, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(prompt_id,''), user_id, miner_id, script_path, process_name, resume_mining, created_at
		FROM quick_generation_mining_leases ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	leases := []domain.QuickGenerationMiningLease{}
	for rows.Next() {
		lease, err := scanQuickGenerationMiningLease(rows)
		if err != nil {
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, rows.Err()
}

func (s *Store) DeleteQuickGenerationMiningLease(ctx context.Context, leaseID string) (domain.QuickGenerationMiningLease, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.QuickGenerationMiningLease{}, 0, err
	}
	defer tx.Rollback()
	lease, err := scanQuickGenerationMiningLease(tx.QueryRowContext(ctx, `
		DELETE FROM quick_generation_mining_leases WHERE id = $1
		RETURNING id, COALESCE(prompt_id,''), user_id, miner_id, script_path, process_name, resume_mining, created_at
	`, leaseID))
	if err != nil {
		return domain.QuickGenerationMiningLease{}, 0, err
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM quick_generation_mining_leases`).Scan(&remaining); err != nil {
		return domain.QuickGenerationMiningLease{}, 0, err
	}
	if err := tx.Commit(); err != nil {
		return domain.QuickGenerationMiningLease{}, 0, err
	}
	return lease, remaining, nil
}

type quickGenerationMiningLeaseScanner interface {
	Scan(...any) error
}

func scanQuickGenerationMiningLease(row quickGenerationMiningLeaseScanner) (domain.QuickGenerationMiningLease, error) {
	var lease domain.QuickGenerationMiningLease
	err := row.Scan(&lease.ID, &lease.PromptID, &lease.UserID, &lease.MinerID, &lease.ScriptPath, &lease.ProcessName, &lease.ResumeMining, &lease.CreatedAt)
	if err != nil && err != sql.ErrNoRows {
		return domain.QuickGenerationMiningLease{}, err
	}
	return lease, err
}
