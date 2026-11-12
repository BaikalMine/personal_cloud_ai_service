package store

import (
	"context"
	"errors"
	"time"

	"ai-access-gateway/internal/domain"
	"github.com/lib/pq"
)

const LiveRevisionBatchLimit = 256

// LiveRevisionSnapshot contains no session token or user content. Missing hashes
// are invalid sessions; reads deliberately do not extend their idle lifetime.
type LiveRevisionSnapshot struct {
	User                                domain.User
	Tasks, Notifications, Jobs, Content int64
}

func (s *Store) LiveRevisions(ctx context.Context, hashes []string, idleTimeout time.Duration) (map[string]LiveRevisionSnapshot, error) {
	if len(hashes) > LiveRevisionBatchLimit {
		return nil, errors.New("live revision batch too large")
	}
	result := make(map[string]LiveRevisionSnapshot, len(hashes))
	if len(hashes) == 0 {
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.token_hash, u.id, u.role, u.can_use_quick_generation, u.can_train_image_lora,
		       COALESCE(t.revision,0), COALESCE(n.revision,0), c.revision
		FROM sessions s JOIN users u ON u.id=s.user_id
		LEFT JOIN user_task_revisions t ON t.user_id=u.id
		LEFT JOIN user_notification_revision n ON n.user_id=u.id
		CROSS JOIN content_change_revision c
		WHERE s.token_hash=ANY($1::text[]) AND c.id=1
		  AND s.expires_at>now()
		  AND s.last_seen_at>now()-($2::bigint * interval '1 second')
		  AND NOT u.disabled AND (u.account_expires_at IS NULL OR u.account_expires_at>now())
	`, pq.Array(hashes), int64(idleTimeout.Seconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var state LiveRevisionSnapshot
		if err := rows.Scan(&hash, &state.User.ID, &state.User.Role, &state.User.CanUseQuickGeneration,
			&state.User.CanTrainImageLora, &state.Tasks, &state.Notifications, &state.Content); err != nil {
			return nil, err
		}
		state.Jobs = state.Tasks
		result[hash] = state
	}
	return result, rows.Err()
}
