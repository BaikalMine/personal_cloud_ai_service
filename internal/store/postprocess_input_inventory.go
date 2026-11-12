package store

import (
	"context"
	"time"
)

// UnresolvedPostprocessInput is administrative evidence, not cleanup permission.
// Nullable identities retain their meaning after the owner or job is deleted.
type UnresolvedPostprocessInput struct {
	ID        string
	UserID    *int64
	JobID     *int64
	Filename  string
	Subfolder string
	SizeBytes int64
	SHA256    string
	State     string
	CreatedAt time.Time
}

// AdminUnresolvedPostprocessInputs requires authorization at the caller. It
// deliberately exposes no mutation and uses an exclusive opaque asset cursor.
func (s *Store) AdminUnresolvedPostprocessInputs(ctx context.Context, after string, limit int) ([]UnresolvedPostprocessInput, error) {
	if limit < 1 || limit > 100 || len(after) > 192 {
		return nil, ErrPostprocessInput
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.user_id,p.job_id,a.filename,a.subfolder,a.size_bytes,a.content_hash,a.state,p.created_at
 FROM generation_postprocess_inputs p JOIN comfy_input_assets a ON a.id=p.input_id
 WHERE p.cleanup_blocked AND a.id>$1 ORDER BY a.id LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UnresolvedPostprocessInput, 0, limit)
	for rows.Next() {
		var item UnresolvedPostprocessInput
		if err := rows.Scan(&item.ID, &item.UserID, &item.JobID, &item.Filename, &item.Subfolder, &item.SizeBytes, &item.SHA256, &item.State, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
