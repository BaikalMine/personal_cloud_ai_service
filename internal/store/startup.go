package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrInvalidBootstrapAdmin = errors.New("invalid bootstrap administrator")

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) EnsureBootstrapAdmin(ctx context.Context, username, initialPasswordHash string) error {
	if strings.TrimSpace(username) == "" || initialPasswordHash == "" {
		return ErrInvalidBootstrapAdmin
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, disabled, can_use_comfyui, can_use_openwebui)
		VALUES ($1, $2, 'admin', false, true, true)
		ON CONFLICT (username) DO UPDATE SET
			role = 'admin',
			disabled = false,
			can_use_comfyui = true,
			can_use_openwebui = true,
			failed_login_count = 0,
			locked_until = NULL
	`, username, initialPasswordHash)
	return err
}

func (s *Store) CloseStaleWebSockets(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE websocket_sessions
		SET closed_at = now(),
		    duration_ms = GREATEST(0, (EXTRACT(EPOCH FROM (now() - opened_at)) * 1000)::bigint)
		WHERE closed_at IS NULL
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, idleTimeout time.Duration) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM sessions
		WHERE expires_at <= now()
		   OR last_seen_at <= now() - ($1::bigint * interval '1 second')
	`, int64(idleTimeout.Seconds()))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
