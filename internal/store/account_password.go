package store

import "context"

// The password and revocation of other stored sessions commit together.
// Matching the verified hash prevents a concurrent password change being overwritten.
func (s *Store) ChangeOwnPassword(ctx context.Context, userID int64, verifiedHash, newHash, currentSessionHash string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=$3,failed_login_count=0,locked_until=NULL WHERE id=$1 AND password_hash=$2`, userID, verifiedHash, newHash)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2`, userID, currentSessionHash); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
