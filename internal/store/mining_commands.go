package store

import "context"

// Revisions survive Gateway restarts and are shared by all Gateway instances.
// Restoring an older DB requires a coordinated agent session restart as well.
func (s *Store) NextMiningCommandRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT nextval('mining_command_revision_seq')`).Scan(&revision)
	return revision, err
}
