package store

import (
	"context"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
)

func (s *Store) ListAdminSessions(ctx context.Context, limit int, idleTimeout time.Duration) ([]domain.SessionRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, u.username, s.created_at, s.expires_at, s.last_seen_at, s.ip, s.user_agent
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.expires_at > now()
		  AND s.last_seen_at > now() - ($2::bigint * interval '1 second')
		ORDER BY s.last_seen_at DESC LIMIT $1
	`, limit, int64(idleTimeout.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []domain.SessionRow
	for rows.Next() {
		var session domain.SessionRow
		if err := rows.Scan(&session.ID, &session.Username, &session.CreatedAt, &session.ExpiresAt, &session.LastSeenAt, &session.IP, &session.UserAgent); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) RevokeSession(ctx context.Context, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]domain.AuditRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 250
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, COALESCE(u.username,''), a.action, a.target_type, a.target_id,
		       a.ip, a.user_agent, a.created_at, a.metadata::text
		FROM audit_log a LEFT JOIN users u ON u.id = a.actor_user_id
		ORDER BY a.created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []domain.AuditRow
	for rows.Next() {
		var event domain.AuditRow
		if err := rows.Scan(
			&event.ID, &event.Actor, &event.Action, &event.TargetType, &event.TargetID,
			&event.IP, &event.UserAgent, &event.CreatedAt, &event.Metadata,
		); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) VisitAuditBefore(ctx context.Context, before time.Time, visit func(domain.AuditRow) error) error {
	if visit == nil {
		return errors.New("audit visitor is required")
	}
	if before.IsZero() {
		before = time.Now().UTC()
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id,COALESCE(u.username,''),a.action,a.target_type,a.target_id,
		       a.ip,a.user_agent,a.created_at,a.metadata::text
		FROM audit_log a LEFT JOIN users u ON u.id=a.actor_user_id
		WHERE a.created_at < $1
		ORDER BY a.created_at DESC,a.id DESC
	`, before.UTC())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var event domain.AuditRow
		if err := rows.Scan(
			&event.ID, &event.Actor, &event.Action, &event.TargetType, &event.TargetID,
			&event.IP, &event.UserAgent, &event.CreatedAt, &event.Metadata,
		); err != nil {
			return err
		}
		if err := visit(event); err != nil {
			return err
		}
	}
	return rows.Err()
}
