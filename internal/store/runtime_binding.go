package store

import (
	"context"

	"ai-access-gateway/internal/database"
)

func (s *Store) ObserveRuntimeBinding(ctx context.Context, request database.RuntimeBindingRequest) (string, error) {
	if s == nil {
		return "", database.ErrRuntimeBindingUnavailable
	}
	return database.ObserveRuntimeBinding(ctx, s.db, request)
}
