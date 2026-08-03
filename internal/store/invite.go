package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"

	"github.com/lib/pq"
)

var (
	ErrInviteUnavailable = errors.New("invite is unavailable")
	ErrUsernameExists    = errors.New("username already exists")
)

type CreateInviteParams struct {
	TokenHash       string
	CreatedByUserID int64
	MaxUses         int
	ExpiresAt       time.Time
	GrantComfyUI    bool
	GrantOpenWebUI  bool
}

type RegisterFromInviteParams struct {
	TokenHash    string
	Username     string
	Email        string
	PasswordHash string
	IP           string
}

func (s *Store) AvailableInvite(ctx context.Context, tokenHash string) (domain.InviteAccess, error) {
	var access domain.InviteAccess
	err := s.db.QueryRowContext(ctx, `
		SELECT id, grant_comfyui, grant_openwebui
		FROM invites
		WHERE token_hash = $1 AND revoked = false AND expires_at > now() AND used_count < max_uses
	`, tokenHash).Scan(&access.ID, &access.GrantComfyUI, &access.GrantOpenWebUI)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.InviteAccess{}, ErrInviteUnavailable
	}
	return access, err
}

func (s *Store) RegisterFromInvite(ctx context.Context, params RegisterFromInviteParams) (int64, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	var inviteID int64
	var grantComfyUI, grantOpenWebUI bool
	err = tx.QueryRowContext(ctx, `
		UPDATE invites
		SET used_count = used_count + 1
		WHERE token_hash = $1 AND revoked = false AND expires_at > now() AND used_count < max_uses
		RETURNING id, grant_comfyui, grant_openwebui
	`, params.TokenHash).Scan(&inviteID, &grantComfyUI, &grantOpenWebUI)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrInviteUnavailable
	}
	if err != nil {
		return 0, 0, err
	}

	var email any
	if params.Email != "" {
		email = params.Email
	}
	var userID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO users
			(username, email, password_hash, role, can_use_comfyui, can_use_openwebui)
		VALUES ($1,$2,$3,'user',$4,$5)
		RETURNING id
	`, params.Username, email, params.PasswordHash, grantComfyUI, grantOpenWebUI).Scan(&userID)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return 0, 0, ErrUsernameExists
		}
		return 0, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO invite_uses (invite_id, user_id, ip) VALUES ($1,$2,$3)`, inviteID, userID, params.IP); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return userID, inviteID, nil
}

func (s *Store) CreateInvite(ctx context.Context, params CreateInviteParams) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO invites
			(token_hash, created_by_user_id, max_uses, expires_at, grant_comfyui, grant_openwebui)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id
	`, params.TokenHash, params.CreatedByUserID, params.MaxUses, params.ExpiresAt, params.GrantComfyUI, params.GrantOpenWebUI).Scan(&id)
	return id, err
}

func (s *Store) SetInviteRevoked(ctx context.Context, id int64, revoked bool) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE invites SET revoked = $2 WHERE id = $1`, id, revoked)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (s *Store) DeleteInvite(ctx context.Context, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM invites WHERE id = $1`, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (s *Store) ListInvites(ctx context.Context, limit int) ([]domain.InviteRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, COALESCE(u.username,''), i.max_uses, i.used_count, i.expires_at,
		       i.revoked, i.grant_comfyui, i.grant_openwebui,
		       CASE
		         WHEN i.revoked THEN 'revoked'
		         WHEN i.expires_at <= now() THEN 'expired'
		         WHEN i.used_count >= i.max_uses THEN 'used'
		         ELSE 'active'
		       END,
		       i.created_at
		FROM invites i LEFT JOIN users u ON u.id = i.created_by_user_id
		ORDER BY i.created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []domain.InviteRow
	for rows.Next() {
		var invite domain.InviteRow
		if err := rows.Scan(
			&invite.ID, &invite.CreatedBy, &invite.MaxUses, &invite.UsedCount,
			&invite.ExpiresAt, &invite.Revoked, &invite.GrantComfyUI,
			&invite.GrantOpenWebUI, &invite.Status, &invite.CreatedAt,
		); err != nil {
			return nil, err
		}
		invites = append(invites, invite)
	}
	return invites, rows.Err()
}
