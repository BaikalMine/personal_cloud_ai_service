package store

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) RecordLoginFailure(ctx context.Context, username string, threshold int, lockDuration time.Duration) (sql.NullTime, error) {
	var lockedUntil sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		WITH next_failure AS (
			SELECT id,
			       CASE WHEN locked_until IS NOT NULL AND locked_until <= now()
			            THEN 1 ELSE failed_login_count + 1 END AS failure_count
			FROM users
			WHERE username = $1 AND disabled = false
			FOR UPDATE
		)
		UPDATE users u
		SET failed_login_count = next_failure.failure_count,
		    locked_until = CASE
		      WHEN next_failure.failure_count >= $2
		      THEN now() + ($3::bigint * interval '1 second')
		      ELSE NULL
		    END
		FROM next_failure
		WHERE u.id = next_failure.id
		RETURNING u.locked_until
	`, username, threshold, int64(lockDuration.Seconds())).Scan(&lockedUntil)
	return lockedUntil, err
}

func (s *Store) RecordLoginSuccess(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE users
		SET last_login_at = now(), failed_login_count = 0, locked_until = NULL
		WHERE id = $1
	`, userID)
	return err
}

func (s *Store) UnlockUser(ctx context.Context, userID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE users
		SET failed_login_count = 0, locked_until = NULL
		WHERE id = $1 AND (failed_login_count <> 0 OR locked_until IS NOT NULL)
	`, userID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}
