package store

import (
	"context"
	"database/sql"
	"time"
)

// A separate session lock bounds hashing across all Gateway processes. It does
// not hold the download worker or a GPU permit while reading a large file.
func (s *Store) ClaimModelVerification(ctx context.Context) (ModelInstall, func(), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return ModelInstall{}, nil, err
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, `SELECT pg_advisory_unlock(746218498)`)
		conn.Close()
	}
	var locked bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(746218498)`).Scan(&locked); err != nil || !locked {
		release()
		if err == nil {
			err = sql.ErrNoRows
		}
		return ModelInstall{}, nil, err
	}
	j, err := scanModelInstall(conn.QueryRowContext(ctx, `UPDATE model_install_jobs SET verification_after=now()+interval '1 minute' WHERE id=(SELECT id FROM model_install_jobs WHERE state='reconciling' AND verification_after<=now() ORDER BY verification_after,created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING `+modelInstallColumns))
	if err != nil {
		release()
		return j, nil, err
	}
	return j, release, nil
}
