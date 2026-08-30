package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) FindUserWithPassword(ctx context.Context, identity string) (domain.User, string, error) {
	var user domain.User
	var passwordHash string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username, email, role, disabled, can_use_comfyui, can_use_openwebui, can_use_quick_generation, can_generate_text_to_image, can_generate_image_to_image, can_generate_video, can_manage_mining, pause_mining_for_quick_generation,
		       generation_daily_limit, generation_total_limit, generation_total_used,
		       failed_login_count, locked_until, created_at, last_login_at, password_hash
		FROM users
		WHERE (username = $1 OR (email IS NOT NULL AND LOWER(email) = LOWER($1)))
		  AND (account_expires_at IS NULL OR account_expires_at > now())
		ORDER BY CASE WHEN username = $1 THEN 0 ELSE 1 END
		LIMIT 1
	`, identity).Scan(
		&user.ID, &user.Username, &user.Email, &user.Role, &user.Disabled,
		&user.CanUseComfyUI, &user.CanUseOpenWebUI, &user.CanUseQuickGeneration, &user.CanGenerateTextToImage, &user.CanGenerateImageToImage, &user.CanGenerateVideo, &user.CanManageMining, &user.PauseMiningForQuickGeneration,
		&user.GenerationDailyLimit, &user.GenerationTotalLimit, &user.GenerationTotalUsed,
		&user.FailedLoginCount, &user.LockedUntil,
		&user.CreatedAt, &user.LastLoginAt, &passwordHash,
	)
	return user, passwordHash, err
}

func (s *Store) UserBySessionHash(ctx context.Context, tokenHash string, idleTimeout time.Duration) (domain.User, error) {
	var user domain.User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.email, u.role, u.disabled, u.can_use_comfyui,
		       u.can_use_openwebui, u.can_use_quick_generation, u.can_generate_text_to_image, u.can_generate_image_to_image, u.can_generate_video, u.can_manage_mining, u.pause_mining_for_quick_generation, u.generation_daily_limit,
		       u.generation_total_limit, u.generation_total_used, u.failed_login_count, u.locked_until, u.created_at, u.last_login_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.expires_at > now()
		  AND s.last_seen_at > now() - ($2::bigint * interval '1 second')
		  AND u.disabled = false
		  AND (u.account_expires_at IS NULL OR u.account_expires_at > now())
	`, tokenHash, int64(idleTimeout.Seconds())).Scan(
		&user.ID, &user.Username, &user.Email, &user.Role, &user.Disabled,
		&user.CanUseComfyUI, &user.CanUseOpenWebUI, &user.CanUseQuickGeneration, &user.CanGenerateTextToImage, &user.CanGenerateImageToImage, &user.CanGenerateVideo, &user.CanManageMining, &user.PauseMiningForQuickGeneration,
		&user.GenerationDailyLimit, &user.GenerationTotalLimit, &user.GenerationTotalUsed,
		&user.FailedLoginCount, &user.LockedUntil,
		&user.CreatedAt, &user.LastLoginAt,
	)
	return user, err
}

func (s *Store) UserByID(ctx context.Context, id int64) (domain.User, error) {
	var user domain.User
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username, email, role, disabled, can_use_comfyui, can_use_openwebui, can_use_quick_generation, can_generate_text_to_image, can_generate_image_to_image, can_generate_video, can_manage_mining, pause_mining_for_quick_generation,
		       generation_daily_limit, generation_total_limit, generation_total_used,
		       failed_login_count, locked_until, created_at, last_login_at
		FROM users WHERE id = $1
	`, id).Scan(
		&user.ID, &user.Username, &user.Email, &user.Role, &user.Disabled,
		&user.CanUseComfyUI, &user.CanUseOpenWebUI, &user.CanUseQuickGeneration, &user.CanGenerateTextToImage, &user.CanGenerateImageToImage, &user.CanGenerateVideo, &user.CanManageMining, &user.PauseMiningForQuickGeneration,
		&user.GenerationDailyLimit, &user.GenerationTotalLimit, &user.GenerationTotalUsed,
		&user.FailedLoginCount, &user.LockedUntil,
		&user.CreatedAt, &user.LastLoginAt,
	)
	return user, err
}

func (s *Store) ListUsers(ctx context.Context, search string) ([]domain.UserRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id, u.username, COALESCE(u.email,''), u.role, u.disabled,
		       u.can_use_comfyui, u.can_use_openwebui, u.can_use_quick_generation, u.can_generate_text_to_image, u.can_generate_image_to_image, u.can_generate_video, u.can_manage_mining, u.pause_mining_for_quick_generation,
		       u.generation_daily_limit, u.generation_total_limit, u.generation_total_used,
		       u.failed_login_count, u.locked_until,
		       COALESCE(u.locked_until > now(), false), u.created_at, u.last_login_at,
		       COUNT(pr.id) AS requests
		FROM users u
		LEFT JOIN proxy_requests pr ON pr.user_id = u.id
		WHERE ($1 = '' OR u.username ILIKE '%' || $1 || '%' OR COALESCE(u.email,'') ILIKE '%' || $1 || '%')
		GROUP BY u.id
		ORDER BY u.created_at DESC
	`, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []domain.UserRow
	for rows.Next() {
		var user domain.UserRow
		if err := rows.Scan(
			&user.ID, &user.Username, &user.Email, &user.Role, &user.Disabled,
			&user.CanUseComfyUI, &user.CanUseOpenWebUI, &user.CanUseQuickGeneration, &user.CanGenerateTextToImage, &user.CanGenerateImageToImage, &user.CanGenerateVideo, &user.CanManageMining, &user.PauseMiningForQuickGeneration,
			&user.GenerationDailyLimit, &user.GenerationTotalLimit, &user.GenerationTotalUsed,
			&user.FailedLoginCount, &user.LockedUntil,
			&user.Locked, &user.CreatedAt, &user.LastLoginAt, &user.Requests,
		); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time, userAgent, ip string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (user_id, token_hash, expires_at, user_agent, ip)
		VALUES ($1,$2,$3,$4,$5)
	`, userID, tokenHash, expiresAt, userAgent, ip)
	return err
}

func (s *Store) TouchSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET last_seen_at = now()
		WHERE token_hash = $1 AND last_seen_at < now() - interval '1 minute'
	`, tokenHash)
	return err
}

func (s *Store) DeleteSessionByHash(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *Store) PasswordHash(ctx context.Context, userID int64) (string, error) {
	var passwordHash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = $1`, userID).Scan(&passwordHash)
	return passwordHash, err
}

func (s *Store) UpdatePassword(ctx context.Context, userID int64, passwordHash string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE users
		SET password_hash = $1, failed_login_count = 0, locked_until = NULL
		WHERE id = $2
	`, passwordHash, userID)
	return err
}

// UpdateOwnProfile updates only the account identified by the authenticated session.
// A regular user keeps their username; only an administrator may change their own one.
func (s *Store) UpdateOwnProfile(ctx context.Context, userID int64, username, email string, allowUsernameChange bool) error {
	var emailValue any
	if email != "" {
		emailValue = email
	}

	var err error
	if allowUsernameChange {
		_, err = s.db.ExecContext(ctx, `
			UPDATE users
			SET username = $2, email = $3
			WHERE id = $1 AND role = 'admin'
		`, userID, username, emailValue)
	} else {
		_, err = s.db.ExecContext(ctx, `
			UPDATE users
			SET email = $2
			WHERE id = $1 AND role <> 'admin'
		`, userID, emailValue)
	}
	if err == nil {
		return nil
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" {
		if pqErr.Constraint == "users_email_lower_unique_idx" {
			return ErrEmailExists
		}
		return ErrUsernameExists
	}
	return err
}

func (s *Store) SetDisabled(ctx context.Context, userID int64, disabled bool) (bool, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET disabled = $2 WHERE id = $1 AND role <> 'admin'`, userID, disabled)
	if err != nil {
		return false, 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, 0, err
	}
	var revoked int64
	if disabled && affected > 0 {
		result, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
		if err != nil {
			return false, 0, err
		}
		revoked, err = result.RowsAffected()
		if err != nil {
			return false, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	return affected > 0, revoked, nil
}

// DeleteUser permanently removes a regular account. Sessions and account-scoped
// records are cascaded; retained AI content is anonymized by its foreign key.
func (s *Store) DeleteUser(ctx context.Context, userID int64, username string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM users
		WHERE id = $1 AND username = $2 AND role <> 'admin'
	`, userID, username)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// DeleteExpiredTemporaryUsers permanently removes accounts created from temporary
// invitations. Sessions and account-scoped records are cascaded, while retained
// AI-content records stay available with a null user reference. Administrators
// are explicitly excluded.
func (s *Store) DeleteExpiredTemporaryUsers(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM users
		WHERE role = 'user' AND account_expires_at IS NOT NULL AND account_expires_at <= now()
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) SetServiceAccess(ctx context.Context, userID int64, comfyUI, openWebUI, quickGeneration, textToImage, imageToImage, video, manageMining, pauseMiningForQuickGeneration bool, dailyLimit int, totalLimit int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE users
		SET can_use_comfyui = $2, can_use_openwebui = $3, can_use_quick_generation = $4,
		    can_generate_text_to_image = $5, can_generate_image_to_image = $6, can_generate_video = $7,
		    can_manage_mining = $8, pause_mining_for_quick_generation = $9, generation_daily_limit = $10, generation_total_limit = $11
		WHERE id = $1 AND role <> 'admin'
	`, userID, comfyUI, openWebUI, quickGeneration, textToImage, imageToImage, video, manageMining, pauseMiningForQuickGeneration, dailyLimit, totalLimit)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

// SetAdminQuickGenerationMiningPriority changes only the current administrator's
// own priority-pool setting. It intentionally cannot modify regular accounts.
func (s *Store) SetAdminQuickGenerationMiningPriority(ctx context.Context, userID int64, enabled bool) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE users
		SET pause_mining_for_quick_generation = $2
		WHERE id = $1 AND role = 'admin'
	`, userID, enabled)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (s *Store) RevokeSessions(ctx context.Context, userID int64) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) RevokeOtherSessions(ctx context.Context, userID int64, currentHash string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, userID, currentHash)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) RevokeOwnedSession(ctx context.Context, sessionID, userID int64, currentHash string) (bool, error) {
	_, revoked, err := s.RevokeOwnedSessionWithHash(ctx, sessionID, userID, currentHash)
	return revoked, err
}

func (s *Store) RevokeOwnedSessionWithHash(ctx context.Context, sessionID, userID int64, currentHash string) (string, bool, error) {
	var tokenHash string
	err := s.db.QueryRowContext(ctx, `
		DELETE FROM sessions WHERE id = $1 AND user_id = $2 AND token_hash <> $3
		RETURNING token_hash
	`, sessionID, userID, currentHash).Scan(&tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return tokenHash, err == nil, err
}

func (s *Store) ActiveSessionHashes(ctx context.Context, tokenHashes []string, idleTimeout time.Duration) (map[string]struct{}, error) {
	active := make(map[string]struct{}, len(tokenHashes))
	if len(tokenHashes) == 0 {
		return active, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.token_hash
		FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash = ANY($1)
		  AND s.expires_at > now()
		  AND s.last_seen_at > now() - ($2::bigint * interval '1 second')
		  AND u.disabled=false
		  AND (u.account_expires_at IS NULL OR u.account_expires_at > now())
	`, pq.Array(tokenHashes), int64(idleTimeout.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		active[hash] = struct{}{}
	}
	return active, rows.Err()
}

func (s *Store) ListAccountSessions(ctx context.Context, userID int64, currentHash string, idleTimeout time.Duration) ([]domain.AccountSession, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, created_at, expires_at, last_seen_at, ip, user_agent, token_hash = $2
		FROM sessions
		WHERE user_id = $1
		  AND expires_at > now()
		  AND last_seen_at > now() - ($3::bigint * interval '1 second')
		ORDER BY last_seen_at DESC
	`, userID, currentHash, int64(idleTimeout.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []domain.AccountSession
	for rows.Next() {
		var session domain.AccountSession
		if err := rows.Scan(&session.ID, &session.CreatedAt, &session.ExpiresAt, &session.LastSeenAt, &session.IP, &session.UserAgent, &session.Current); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}
