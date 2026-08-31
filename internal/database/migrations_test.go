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

func TestQuickGenerationMiningPoolMigration(t *testing.T) {
	var poolMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 16 {
			poolMigration = &migrationCatalog[index]
			break
		}
	}
	if poolMigration == nil {
		t.Fatal("quick generation mining pool migration is missing")
	}
	if poolMigration.name != "quick_generation_mining_pool" {
		t.Fatalf("migration name = %q", poolMigration.name)
	}
	sql := strings.Join(poolMigration.statements, "\n")
	for _, expected := range []string{
		"pause_mining_for_quick_generation",
		"quick_generation_mining_leases",
		"prompt_id TEXT NULL UNIQUE",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("pool migration does not contain %q", expected)
		}
	}
}

func TestPromptAssistantContentAuditMigration(t *testing.T) {
	var auditMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 17 {
			auditMigration = &migrationCatalog[index]
			break
		}
	}
	if auditMigration == nil || auditMigration.name != "prompt_assistant_content_audit" {
		t.Fatal("prompt assistant audit migration is missing")
	}
	if sql := strings.Join(auditMigration.statements, "\n"); !strings.Contains(sql, "prompt_assistant") {
		t.Fatalf("prompt assistant audit migration does not allow the event kind: %s", sql)
	}
}

func TestPromptAssistantContentServiceMigration(t *testing.T) {
	var serviceMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 33 {
			serviceMigration = &migrationCatalog[index]
			break
		}
	}
	if serviceMigration == nil || serviceMigration.name != "prompt_assistant_content_service" {
		t.Fatal("prompt assistant content service migration is missing")
	}
	if sql := strings.Join(serviceMigration.statements, "\n"); !strings.Contains(sql, "'ollama'") {
		t.Fatalf("prompt assistant service migration does not allow ollama: %s", sql)
	}
}

func TestContentLiveRevisionMigration(t *testing.T) {
	var liveMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 36 {
			liveMigration = &migrationCatalog[index]
			break
		}
	}
	if liveMigration == nil || liveMigration.name != "content_live_revision" {
		t.Fatal("content live revision migration is missing")
	}
	sql := strings.Join(liveMigration.statements, "\n")
	for _, expected := range []string{"content_change_revision", "content_events_change_revision", "content_media_change_revision"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("content live revision migration does not contain %q", expected)
		}
	}
}

func TestUnifiedContentRetentionDefaultsMigration(t *testing.T) {
	var retentionMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 37 {
			retentionMigration = &migrationCatalog[index]
			break
		}
	}
	if retentionMigration == nil || retentionMigration.name != "unified_content_retention_defaults" {
		t.Fatal("unified content retention migration is missing")
	}
	sql := strings.Join(retentionMigration.statements, "\n")
	for _, expected := range []string{"content_events", "interval '7 days'", "content_media", "comfy_output_ownership", "interval '24 hours'", "LEAST", "media_expires_at"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("retention migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestQuickGenerationRequestTelemetryMigration(t *testing.T) {
	var telemetryMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 38 {
			telemetryMigration = &migrationCatalog[index]
			break
		}
	}
	if telemetryMigration == nil || telemetryMigration.name != "quick_generation_request_telemetry" {
		t.Fatal("quick generation request telemetry migration is missing")
	}
	sql := strings.Join(telemetryMigration.statements, "\n")
	for _, expected := range []string{"generation_requests", "prompt_id IS NOT NULL", "proxy_requests", "'/generate/run/'", "status_code", "202", "CREATE UNIQUE INDEX"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("quick generation telemetry migration does not contain %q: %s", expected, sql)
		}
	}
}
