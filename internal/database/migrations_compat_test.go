package database

import "testing"

func TestOnlyMiningProfilesMigrationCanReplaceRedactedChecksum(t *testing.T) {
	for _, item := range []migration{
		{version: 9, name: "mining_profiles"},
		{version: 8, name: "mining_profiles"},
		{version: 9, name: "another_migration"},
	} {
		allowed := isRedactedMigration(item)
		want := item.version == 9 && item.name == "mining_profiles"
		if allowed != want {
			t.Fatalf("compatibility for %#v = %v, want %v", item, allowed, want)
		}
	}
}
