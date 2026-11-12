package database

import (
	"context"
	"database/sql"
	"errors"
)

var ErrRuntimeSchemaMismatch = errors.New("database migration catalog does not match the validation binary")

// RequireCurrentSchema is read-only. Validation startup cannot create migration
// bookkeeping, apply DDL or repair checksums as a side effect of being probed.
func RequireCurrentSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrRuntimeSchemaMismatch
	}
	rows, err := db.QueryContext(ctx, `SELECT version,left(name,101),left(checksum,65) FROM public.schema_migrations ORDER BY version LIMIT 10001`)
	if err != nil {
		return ErrRuntimeSchemaMismatch
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		var actual MigrationRecord
		if err := rows.Scan(&actual.Version, &actual.Name, &actual.Checksum); err != nil || index >= len(migrationCatalog) {
			return ErrRuntimeSchemaMismatch
		}
		expected := migrationCatalog[index]
		if actual.Version != expected.version || actual.Name != expected.name || actual.Checksum != migrationChecksum(expected) {
			return ErrRuntimeSchemaMismatch
		}
		index++
	}
	if rows.Err() != nil || index != len(migrationCatalog) {
		return ErrRuntimeSchemaMismatch
	}
	return nil
}
