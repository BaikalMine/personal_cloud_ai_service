package database

// MigrationRecord is the immutable migration identity, without SQL or user data.
// A release uses the full ordered catalog, not only its highest version number.
type MigrationRecord struct {
	Version  int64  `json:"version"`
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
}

func ReleaseMigrations() []MigrationRecord {
	result := make([]MigrationRecord, len(migrationCatalog))
	for i, item := range migrationCatalog {
		result[i] = MigrationRecord{Version: item.version, Name: item.name, Checksum: migrationChecksum(item)}
	}
	return result
}
