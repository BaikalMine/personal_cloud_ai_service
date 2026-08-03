package database

import (
	"strings"
	"testing"
)

func TestMigrationCatalogIsValid(t *testing.T) {
	if err := validateMigrations(migrationCatalog); err != nil {
		t.Fatalf("validateMigrations() error = %v", err)
	}

	checksums := make(map[string]int64, len(migrationCatalog))
	for _, item := range migrationCatalog {
		checksum := migrationChecksum(item)
		if len(checksum) != 64 {
			t.Fatalf("migration %d checksum length = %d, want 64", item.version, len(checksum))
		}
		if previousVersion, exists := checksums[checksum]; exists {
			t.Fatalf("migrations %d and %d have identical checksums", previousVersion, item.version)
		}
		checksums[checksum] = item.version
	}
}

func TestValidateMigrationsRejectsInvalidCatalogs(t *testing.T) {
	tests := []struct {
		name       string
		migrations []migration
	}{
		{name: "empty"},
		{name: "zero version", migrations: []migration{{version: 0, name: "zero", statements: []string{"SELECT 1"}}}},
		{name: "empty name", migrations: []migration{{version: 1, statements: []string{"SELECT 1"}}}},
		{name: "empty statements", migrations: []migration{{version: 1, name: "empty"}}},
		{name: "gap", migrations: []migration{
			{version: 1, name: "one", statements: []string{"SELECT 1"}},
			{version: 3, name: "three", statements: []string{"SELECT 3"}},
		}},
		{name: "out of order", migrations: []migration{
			{version: 2, name: "two", statements: []string{"SELECT 2"}},
			{version: 1, name: "one", statements: []string{"SELECT 1"}},
		}},
		{name: "duplicate name", migrations: []migration{
			{version: 1, name: "same", statements: []string{"SELECT 1"}},
			{version: 2, name: "same", statements: []string{"SELECT 2"}},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateMigrations(test.migrations); err == nil {
				t.Fatal("validateMigrations() error = nil, want error")
			}
		})
	}
}

func TestMigrationChecksumIncludesDefinition(t *testing.T) {
	base := migration{version: 1, name: "example", statements: []string{"SELECT 1"}}
	withWhitespace := migration{version: 1, name: "example", statements: []string{"  SELECT 1\n"}}
	changed := migration{version: 1, name: "example", statements: []string{"SELECT 2"}}

	if migrationChecksum(base) != migrationChecksum(withWhitespace) {
		t.Fatal("outer statement whitespace should not change migration checksum")
	}
	if migrationChecksum(base) == migrationChecksum(changed) {
		t.Fatal("changed migration statement must change checksum")
	}
	if strings.Contains(migrationChecksum(base), "SELECT") {
		t.Fatal("checksum must not expose migration SQL")
	}
}
