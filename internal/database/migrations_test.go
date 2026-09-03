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

func TestChunkedContentMediaMigration(t *testing.T) {
	var chunkMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 43 {
			chunkMigration = &migrationCatalog[index]
			break
		}
	}
	if chunkMigration == nil || chunkMigration.name != "chunked_content_media" {
		t.Fatal("chunked content media migration is missing")
	}
	sql := strings.Join(chunkMigration.statements, "\n")
	for _, expected := range []string{"storage_format", "inline_v1", "chunked_v1", "content_media_chunks", "ON DELETE CASCADE", "plain_size"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("chunked media migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestGenerationMediaLibraryMigration(t *testing.T) {
	var libraryMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 44 {
			libraryMigration = &migrationCatalog[index]
			break
		}
	}
	if libraryMigration == nil || libraryMigration.name != "generation_media_library" {
		t.Fatal("generation media library migration is missing")
	}
	sql := strings.Join(libraryMigration.statements, "\n")
	for _, expected := range []string{
		"favorite_at", "pinned_at", "generation_media_collections", "generation_media_collection_items",
		"generation_media_tags", "generation_media_references", "source_media_id", "target_job_id", "bump_content_change_revision",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("generation media library migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestUnifiedContentTasksMigration(t *testing.T) {
	var taskMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 45 {
			taskMigration = &migrationCatalog[index]
			break
		}
	}
	if taskMigration == nil || taskMigration.name != "unified_content_tasks" {
		t.Fatal("unified content tasks migration is missing")
	}
	sql := strings.Join(taskMigration.statements, "\n")
	for _, expected := range []string{
		"username_snapshot", "updated_at", "touch_content_event_updated_at",
		"generation_jobs_content_revision", "generation_job_transitions_content_revision",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("unified content tasks migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestGenerationNotificationsMigration(t *testing.T) {
	var notificationMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 46 {
			notificationMigration = &migrationCatalog[index]
			break
		}
	}
	if notificationMigration == nil || notificationMigration.name != "generation_notifications" {
		t.Fatal("generation notifications migration is missing")
	}
	sql := strings.Join(notificationMigration.statements, "\n")
	for _, expected := range []string{
		"user_notification_preferences", "user_notifications", "user_notification_revision",
		"generation_completed", "generation_failed", "UNIQUE(generation_job_id)", "ON DELETE CASCADE",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("generation notifications migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestPromptAssistantQualityMigration(t *testing.T) {
	var qualityMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 47 {
			qualityMigration = &migrationCatalog[index]
			break
		}
	}
	if qualityMigration == nil || qualityMigration.name != "prompt_assistant_quality" {
		t.Fatal("prompt assistant quality migration is missing")
	}
	sql := strings.Join(qualityMigration.statements, "\n")
	for _, expected := range []string{
		"prompt_assistant_runs", "edited_after_apply", "prompt_tokens", "completion_tokens",
		"latency_ms", "num_predict", "timeout_ms", "keep_alive", "ON DELETE CASCADE",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("prompt assistant quality migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestControlledGenerationBatchesMigration(t *testing.T) {
	var batchMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 48 {
			batchMigration = &migrationCatalog[index]
			break
		}
	}
	if batchMigration == nil || batchMigration.name != "controlled_generation_batches" {
		t.Fatal("controlled generation batches migration is missing")
	}
	sql := strings.Join(batchMigration.statements, "\n")
	for _, expected := range []string{
		"generation_batches", "experiment_mode", "parameter_values", "winner_job_id",
		"batch_id", "batch_position", "experiment_value", "total_count BETWEEN 2 AND 20",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("controlled generation batches migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestFeatureSuggestionReviewWorkflowMigration(t *testing.T) {
	var suggestionMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 49 {
			suggestionMigration = &migrationCatalog[index]
			break
		}
	}
	if suggestionMigration == nil || suggestionMigration.name != "feature_suggestion_review_workflow" {
		t.Fatal("feature suggestion review workflow migration is missing")
	}
	sql := strings.Join(suggestionMigration.statements, "\n")
	for _, expected := range []string{
		"json_size_bytes", "scan_status", "review_comment_cipher", "reviewed_by", "submitted_at", "reviewed_at",
		"'draft','submitted','scanning','review','accepted','rejected'", "source_index", "lease_expires_at",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("feature suggestion review migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestSeparateGenerationQuotasAndInviteControlsMigration(t *testing.T) {
	var quotaMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 50 {
			quotaMigration = &migrationCatalog[index]
			break
		}
	}
	if quotaMigration == nil || quotaMigration.name != "separate_generation_quotas_and_invite_controls" {
		t.Fatal("separate generation quota migration is missing")
	}
	sql := strings.Join(quotaMigration.statements, "\n")
	for _, expected := range []string{
		"video_generation_daily_limit", "video_generation_total_used", "video_used_count",
		"can_use_advanced_generation_settings", "pause_mining_for_quick_generation",
		"max_video_generation_quality", "template_id='minimax-h3-video'",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("separate generation quota migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestImageLoraTrainingMigration(t *testing.T) {
	var trainingMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 51 {
			trainingMigration = &migrationCatalog[index]
			break
		}
	}
	if trainingMigration == nil || trainingMigration.name != "image_lora_training" {
		t.Fatal("image LoRA training migration is missing")
	}
	sql := strings.Join(trainingMigration.statements, "\n")
	for _, expected := range []string{
		"can_train_image_lora", "grant_train_image_lora", "lora_training_jobs",
		"lora_training_jobs_user_active_idx", "lora_training_job_id", "flux2-klein",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("image LoRA training migration does not contain %q: %s", expected, sql)
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

func TestBoundedDatabaseRetentionMigration(t *testing.T) {
	var retentionMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 39 {
			retentionMigration = &migrationCatalog[index]
			break
		}
	}
	if retentionMigration == nil || retentionMigration.name != "bounded_database_retention" {
		t.Fatal("bounded database retention migration is missing")
	}
	sql := strings.Join(retentionMigration.statements, "\n")
	for _, expected := range []string{
		"database_cleanup_state", "deleted_rows", "errors JSONB", "proxy_requests_retention_idx",
		"websocket_sessions_retention_idx", "generation_requests_retention_idx",
		"quick_generation_daily_usage_retention_idx", "invites_retention_idx",
		"audit_log_retention_idx", "host_metrics_retention_idx",
		"quick_generation_variants_retention_idx", "comfy_output_ownership_retention_idx",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("bounded database retention migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestDurableGenerationJobsMigration(t *testing.T) {
	var jobsMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 40 {
			jobsMigration = &migrationCatalog[index]
			break
		}
	}
	if jobsMigration == nil || jobsMigration.name != "durable_generation_jobs" {
		t.Fatal("durable generation jobs migration is missing")
	}
	sql := strings.Join(jobsMigration.statements, "\n")
	for _, expected := range []string{
		"generation_jobs", "generation_job_transitions", "generation_job_revision",
		"waiting_for_resources", "postprocessing", "archiving", "parent_job_id",
		"generation_requests_job_idx", "quick_generation_variants_job_idx",
		"content_events_generation_job_idx", "quick_generation_mining_leases_job_idx",
		"ON DELETE SET NULL", "legacy_generation_error",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("durable generation jobs migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestGenerationJobExecutionResourcesMigration(t *testing.T) {
	var resourceMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 41 {
			resourceMigration = &migrationCatalog[index]
			break
		}
	}
	if resourceMigration == nil || resourceMigration.name != "generation_job_execution_resources" {
		t.Fatal("generation job execution resources migration is missing")
	}
	sql := strings.Join(resourceMigration.statements, "\n")
	for _, expected := range []string{"quota_reserved_on", "quota_committed_at", "cancellation_requested_at", "cancellation_confirmed_at", "content_events_generation_job_prompt_idx", "generation_jobs_cancellation_idx"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("generation job execution migration does not contain %q: %s", expected, sql)
		}
	}
}

func TestEndToEndObservabilityMigration(t *testing.T) {
	var observabilityMigration *migration
	for index := range migrationCatalog {
		if migrationCatalog[index].version == 42 {
			observabilityMigration = &migrationCatalog[index]
			break
		}
	}
	if observabilityMigration == nil || observabilityMigration.name != "end_to_end_observability" {
		t.Fatal("end-to-end observability migration is missing")
	}
	sql := strings.Join(observabilityMigration.statements, "\n")
	for _, expected := range []string{
		"correlation_id", "duration_ms", "generation_job_id", "service_observations",
		"gateway_observations", "content_moderation_backlog", "cleanup_age_seconds",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("observability migration does not contain %q: %s", expected, sql)
		}
	}
}
