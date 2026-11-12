package database

import "testing"

func TestReleaseMigrationsAreTheCompleteImmutableCatalog(t *testing.T) {
	items := ReleaseMigrations()
	if len(items) != len(migrationCatalog) {
		t.Fatal("partial release catalog")
	}
	for i, item := range items {
		if item.Version != migrationCatalog[i].version || item.Name != migrationCatalog[i].name || item.Checksum != migrationChecksum(migrationCatalog[i]) {
			t.Fatal("release catalog drift", item)
		}
	}
	items[0].Checksum = "changed"
	if ReleaseMigrations()[0].Checksum == "changed" {
		t.Fatal("release consumer mutated catalog")
	}
}
