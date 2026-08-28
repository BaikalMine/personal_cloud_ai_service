package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

const migrationLockID int64 = 746218390

type migration struct {
	version    int64
	name       string
	statements []string
}

var migrationCatalog = []migration{
	{
		version: 1,
		name:    "initial_schema",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS users (
				id BIGSERIAL PRIMARY KEY,
				username TEXT NOT NULL UNIQUE,
				email TEXT NULL,
				password_hash TEXT NOT NULL,
				role TEXT NOT NULL CHECK (role IN ('admin','user')),
				disabled BOOLEAN NOT NULL DEFAULT FALSE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				last_login_at TIMESTAMPTZ NULL
			)`,
			`CREATE TABLE IF NOT EXISTS sessions (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				token_hash TEXT NOT NULL UNIQUE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				expires_at TIMESTAMPTZ NOT NULL,
				last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				user_agent TEXT NOT NULL DEFAULT '',
				ip TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE TABLE IF NOT EXISTS invites (
				id BIGSERIAL PRIMARY KEY,
				token_hash TEXT NOT NULL UNIQUE,
				created_by_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
				max_uses INT NOT NULL DEFAULT 1,
				used_count INT NOT NULL DEFAULT 0,
				expires_at TIMESTAMPTZ NOT NULL,
				revoked BOOLEAN NOT NULL DEFAULT FALSE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE TABLE IF NOT EXISTS invite_uses (
				id BIGSERIAL PRIMARY KEY,
				invite_id BIGINT NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				ip TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE TABLE IF NOT EXISTS proxy_requests (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				service TEXT NOT NULL,
				method TEXT NOT NULL,
				path TEXT NOT NULL,
				status_code INT NOT NULL,
				duration_ms BIGINT NOT NULL,
				bytes_in BIGINT NOT NULL DEFAULT 0,
				bytes_out BIGINT NOT NULL DEFAULT 0,
				is_websocket BOOLEAN NOT NULL DEFAULT FALSE,
				client_ip TEXT NOT NULL DEFAULT '',
				user_agent TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE TABLE IF NOT EXISTS websocket_sessions (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				service TEXT NOT NULL,
				opened_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				closed_at TIMESTAMPTZ NULL,
				duration_ms BIGINT NULL,
				client_ip TEXT NOT NULL DEFAULT '',
				user_agent TEXT NOT NULL DEFAULT ''
			)`,
			`CREATE TABLE IF NOT EXISTS audit_log (
				id BIGSERIAL PRIMARY KEY,
				actor_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
				action TEXT NOT NULL,
				target_type TEXT NOT NULL,
				target_id BIGINT NULL,
				ip TEXT NOT NULL DEFAULT '',
				user_agent TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				metadata JSONB NOT NULL DEFAULT '{}'::jsonb
			)`,
		},
	},
	{
		version: 2,
		name:    "service_permissions",
		statements: []string{
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS can_use_comfyui BOOLEAN NOT NULL DEFAULT TRUE`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS can_use_openwebui BOOLEAN NOT NULL DEFAULT TRUE`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS grant_comfyui BOOLEAN NOT NULL DEFAULT TRUE`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS grant_openwebui BOOLEAN NOT NULL DEFAULT TRUE`,
		},
	},
	{
		version: 3,
		name:    "query_indexes",
		statements: []string{
			`CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions(user_id)`,
			`CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions(expires_at)`,
			`CREATE INDEX IF NOT EXISTS proxy_requests_user_time_idx ON proxy_requests(user_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS proxy_requests_service_time_idx ON proxy_requests(service, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS websocket_sessions_open_idx ON websocket_sessions(closed_at)`,
			`CREATE INDEX IF NOT EXISTS websocket_sessions_service_open_idx ON websocket_sessions(service) WHERE closed_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS audit_log_time_idx ON audit_log(created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS audit_log_actor_time_idx ON audit_log(actor_user_id, created_at DESC)`,
		},
	},
	{
		version: 4,
		name:    "data_integrity_constraints",
		statements: []string{
			`DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'proxy_requests_service_valid' AND conrelid = 'proxy_requests'::regclass) THEN
					ALTER TABLE proxy_requests ADD CONSTRAINT proxy_requests_service_valid CHECK (service IN ('comfyui','openwebui')) NOT VALID;
				END IF;
			END $$`,
			`DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'proxy_requests_values_valid' AND conrelid = 'proxy_requests'::regclass) THEN
					ALTER TABLE proxy_requests ADD CONSTRAINT proxy_requests_values_valid CHECK (status_code BETWEEN 100 AND 599 AND duration_ms >= 0 AND bytes_in >= 0 AND bytes_out >= 0) NOT VALID;
				END IF;
			END $$`,
			`DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'websocket_sessions_service_valid' AND conrelid = 'websocket_sessions'::regclass) THEN
					ALTER TABLE websocket_sessions ADD CONSTRAINT websocket_sessions_service_valid CHECK (service IN ('comfyui','openwebui')) NOT VALID;
				END IF;
			END $$`,
			`DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'invites_usage_valid' AND conrelid = 'invites'::regclass) THEN
					ALTER TABLE invites ADD CONSTRAINT invites_usage_valid CHECK (max_uses > 0 AND used_count >= 0 AND used_count <= max_uses) NOT VALID;
				END IF;
			END $$`,
			`ALTER TABLE proxy_requests VALIDATE CONSTRAINT proxy_requests_service_valid`,
			`ALTER TABLE proxy_requests VALIDATE CONSTRAINT proxy_requests_values_valid`,
			`ALTER TABLE websocket_sessions VALIDATE CONSTRAINT websocket_sessions_service_valid`,
			`ALTER TABLE invites VALIDATE CONSTRAINT invites_usage_valid`,
		},
	},
	{
		version: 5,
		name:    "account_lockout",
		statements: []string{
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS failed_login_count INT NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS locked_until TIMESTAMPTZ NULL`,
			`CREATE INDEX IF NOT EXISTS users_locked_until_idx ON users(locked_until) WHERE locked_until IS NOT NULL`,
			`DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_failed_login_count_valid' AND conrelid = 'users'::regclass) THEN
					ALTER TABLE users ADD CONSTRAINT users_failed_login_count_valid CHECK (failed_login_count >= 0) NOT VALID;
				END IF;
			END $$`,
			`ALTER TABLE users VALIDATE CONSTRAINT users_failed_login_count_valid`,
		},
	},
	{
		version: 6,
		name:    "encrypted_content_audit",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS content_events (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				service TEXT NOT NULL CHECK (service IN ('comfyui','openwebui')),
				kind TEXT NOT NULL CHECK (kind IN ('comfyui_prompt','openwebui_chat')),
				external_id TEXT NULL,
				model TEXT NOT NULL DEFAULT '',
				prompt_cipher BYTEA NOT NULL,
				response_cipher BYTEA NOT NULL,
				metadata_cipher BYTEA NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '7 days')
			)`,
			`CREATE INDEX IF NOT EXISTS content_events_created_at_idx ON content_events(created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS content_events_user_created_idx ON content_events(user_id, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS content_events_external_idx ON content_events(service, external_id) WHERE external_id IS NOT NULL`,
			`CREATE TABLE IF NOT EXISTS content_media (
				id BIGSERIAL PRIMARY KEY,
				event_id BIGINT NOT NULL REFERENCES content_events(id) ON DELETE CASCADE,
				media_type TEXT NOT NULL CHECK (media_type IN ('image','video')),
				mime_type TEXT NOT NULL,
				original_name TEXT NOT NULL,
				payload_cipher BYTEA NOT NULL,
				size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '3 days'),
				UNIQUE(event_id, original_name)
			)`,
			`CREATE INDEX IF NOT EXISTS content_media_expires_idx ON content_media(expires_at)`,
		},
	},
	{
		version: 7,
		name:    "comfy_output_ownership",
		statements: []string{
			`ALTER TABLE content_media ADD COLUMN IF NOT EXISTS subfolder TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE content_media ADD COLUMN IF NOT EXISTS storage_type TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE content_media DROP CONSTRAINT IF EXISTS content_media_event_id_original_name_key`,
			`DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'content_media_event_output_unique' AND conrelid = 'content_media'::regclass) THEN
					ALTER TABLE content_media ADD CONSTRAINT content_media_event_output_unique UNIQUE(event_id,original_name,subfolder,storage_type);
				END IF;
			END $$`,
			`CREATE TABLE IF NOT EXISTS comfy_output_ownership (
				id BIGSERIAL PRIMARY KEY,
				event_id BIGINT NOT NULL REFERENCES content_events(id) ON DELETE CASCADE,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				prompt_id TEXT NOT NULL,
				filename TEXT NOT NULL,
				subfolder TEXT NOT NULL DEFAULT '',
				storage_type TEXT NOT NULL DEFAULT '',
				media_type TEXT NOT NULL CHECK (media_type IN ('image','video')),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '7 days'),
				UNIQUE(event_id,filename,subfolder,storage_type)
			)`,
			`CREATE INDEX IF NOT EXISTS comfy_output_lookup_idx ON comfy_output_ownership(filename,subfolder,storage_type,created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS comfy_output_expires_idx ON comfy_output_ownership(expires_at)`,
		},
	},
	{
		version: 8,
		name:    "comfy_user_state",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS comfy_settings (
				user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
				settings JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(settings) = 'object'),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE TABLE IF NOT EXISTS comfy_userdata (
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				path TEXT NOT NULL,
				payload BYTEA NOT NULL,
				size_bytes BIGINT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				modified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				PRIMARY KEY (user_id, path),
				CHECK (char_length(path) BETWEEN 1 AND 1024),
				CHECK (size_bytes = octet_length(payload)),
				CHECK (size_bytes BETWEEN 0 AND 33554432)
			)`,
			`CREATE INDEX IF NOT EXISTS comfy_userdata_user_path_idx ON comfy_userdata(user_id, path text_pattern_ops)`,
		},
	},
	{
		version: 9,
		name:    "mining_profiles",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS miners (
				id BIGSERIAL PRIMARY KEY,
				name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
				script_path TEXT NOT NULL UNIQUE CHECK (char_length(script_path) BETWEEN 1 AND 1024),
				process_name TEXT NOT NULL CHECK (char_length(process_name) BETWEEN 5 AND 128),
				icon_mime TEXT NOT NULL DEFAULT '',
				icon_data BYTEA NOT NULL DEFAULT ''::bytea CHECK (octet_length(icon_data) <= 262144),
				enabled BOOLEAN NOT NULL DEFAULT TRUE,
				is_default BOOLEAN NOT NULL DEFAULT FALSE,
				created_by_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS miners_single_default_idx ON miners(is_default) WHERE is_default`,
			`CREATE INDEX IF NOT EXISTS miners_enabled_idx ON miners(enabled, name)`,
		},
	},
	{
		version: 10,
		name:    "generation_media_24h_retention",
		statements: []string{
			`ALTER TABLE content_media ALTER COLUMN expires_at SET DEFAULT (now() + interval '24 hours')`,
			`ALTER TABLE comfy_output_ownership ALTER COLUMN expires_at SET DEFAULT (now() + interval '24 hours')`,
			`UPDATE content_media m SET expires_at = LEAST(m.expires_at, m.created_at + interval '24 hours')
			  FROM content_events e WHERE e.id=m.event_id AND e.service='comfyui'`,
			`UPDATE comfy_output_ownership SET expires_at = LEAST(expires_at, created_at + interval '24 hours')`,
		},
	},
	{
		version: 11,
		name:    "content_media_hash_for_matched_output_cleanup",
		statements: []string{
			`ALTER TABLE content_media ADD COLUMN IF NOT EXISTS content_hash TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS content_media_expired_comfy_idx ON content_media(expires_at,event_id)`,
		},
	},
	{
		version: 12,
		name:    "profile_hidden_generation_media",
		statements: []string{
			`ALTER TABLE content_media ADD COLUMN IF NOT EXISTS profile_hidden_at TIMESTAMPTZ NULL`,
			`CREATE INDEX IF NOT EXISTS content_media_profile_visible_idx ON content_media(event_id,expires_at) WHERE profile_hidden_at IS NULL`,
		},
	},
	{
		version: 13,
		name:    "case_insensitive_unique_user_emails",
		statements: []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_unique_idx
			 ON users (LOWER(email)) WHERE email IS NOT NULL AND email <> ''`,
		},
	},
	{
		version: 14,
		name:    "quick_generation_access_and_quotas",
		statements: []string{
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS can_use_quick_generation BOOLEAN NOT NULL DEFAULT TRUE`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS generation_daily_limit INT NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS generation_total_limit BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS generation_total_used BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS grant_quick_generation BOOLEAN NOT NULL DEFAULT TRUE`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS generation_daily_limit INT NOT NULL DEFAULT 0`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS generation_total_limit BIGINT NOT NULL DEFAULT 0`,
			`CREATE TABLE IF NOT EXISTS quick_generation_daily_usage (
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				usage_date DATE NOT NULL,
				used_count INT NOT NULL DEFAULT 0 CHECK (used_count >= 0),
				PRIMARY KEY (user_id, usage_date)
			)`,
			`ALTER TABLE users DROP CONSTRAINT IF EXISTS users_generation_daily_limit_valid`,
			`ALTER TABLE users ADD CONSTRAINT users_generation_daily_limit_valid CHECK (generation_daily_limit >= 0)`,
			`ALTER TABLE users DROP CONSTRAINT IF EXISTS users_generation_total_limit_valid`,
			`ALTER TABLE users ADD CONSTRAINT users_generation_total_limit_valid CHECK (generation_total_limit >= 0 AND generation_total_used >= 0)`,
			`ALTER TABLE invites DROP CONSTRAINT IF EXISTS invites_generation_limits_valid`,
			`ALTER TABLE invites ADD CONSTRAINT invites_generation_limits_valid CHECK (generation_daily_limit >= 0 AND generation_total_limit >= 0)`,
		},
	},
	{
		version: 15,
		name:    "trusted_mining_access",
		statements: []string{
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS can_manage_mining BOOLEAN NOT NULL DEFAULT FALSE`,
		},
	},
	{
		version: 16,
		name:    "quick_generation_mining_pool",
		statements: []string{
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS pause_mining_for_quick_generation BOOLEAN NOT NULL DEFAULT FALSE`,
			`CREATE TABLE IF NOT EXISTS quick_generation_mining_leases (
				id TEXT PRIMARY KEY CHECK (char_length(id) BETWEEN 1 AND 128),
				prompt_id TEXT NULL UNIQUE CHECK (prompt_id IS NULL OR char_length(prompt_id) BETWEEN 1 AND 128),
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				miner_id BIGINT NOT NULL REFERENCES miners(id) ON DELETE RESTRICT,
				script_path TEXT NOT NULL CHECK (char_length(script_path) BETWEEN 1 AND 1024),
				process_name TEXT NOT NULL CHECK (char_length(process_name) BETWEEN 1 AND 128),
				resume_mining BOOLEAN NOT NULL DEFAULT TRUE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE INDEX IF NOT EXISTS quick_generation_mining_leases_created_idx ON quick_generation_mining_leases(created_at)`,
		},
	},
	{
		version: 17,
		name:    "prompt_assistant_content_audit",
		statements: []string{
			`ALTER TABLE content_events DROP CONSTRAINT IF EXISTS content_events_kind_check`,
			`ALTER TABLE content_events ADD CONSTRAINT content_events_kind_check CHECK (kind IN ('comfyui_prompt','openwebui_chat','prompt_assistant'))`,
		},
	},
	{
		version: 18,
		name:    "host_monitoring_history",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS host_metrics (
				id BIGSERIAL PRIMARY KEY,
				recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				cpu_percent DOUBLE PRECISION NOT NULL CHECK (cpu_percent >= 0 AND cpu_percent <= 100),
				memory_used_bytes BIGINT NOT NULL CHECK (memory_used_bytes >= 0),
				memory_total_bytes BIGINT NOT NULL CHECK (memory_total_bytes >= 0),
				gpu_available BOOLEAN NOT NULL DEFAULT FALSE,
				gpu_name TEXT NOT NULL DEFAULT '',
				gpu_percent DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (gpu_percent >= 0 AND gpu_percent <= 100),
				gpu_memory_used_bytes BIGINT NOT NULL DEFAULT 0 CHECK (gpu_memory_used_bytes >= 0),
				gpu_memory_total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (gpu_memory_total_bytes >= 0)
			)`,
			`CREATE INDEX IF NOT EXISTS host_metrics_recorded_at_idx ON host_metrics(recorded_at DESC)`,
		},
	},
	{
		version: 19,
		name:    "quick_generation_type_access",
		statements: []string{
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS can_generate_text_to_image BOOLEAN NOT NULL DEFAULT TRUE`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS can_generate_image_to_image BOOLEAN NOT NULL DEFAULT FALSE`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS can_generate_video BOOLEAN NOT NULL DEFAULT FALSE`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS grant_text_to_image BOOLEAN NOT NULL DEFAULT TRUE`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS grant_image_to_image BOOLEAN NOT NULL DEFAULT FALSE`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS grant_video BOOLEAN NOT NULL DEFAULT FALSE`,
		},
	},
	{
		version: 20,
		name:    "generation_request_recovery",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS generation_requests (
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				request_id TEXT NOT NULL,
				prompt_id TEXT NULL UNIQUE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				PRIMARY KEY (user_id, request_id)
			)`,
			`CREATE INDEX IF NOT EXISTS generation_requests_created_at_idx ON generation_requests(created_at DESC)`,
		},
	},
	{
		version: 21,
		name:    "sensitive_generation_content",
		statements: []string{
			`ALTER TABLE content_events ADD COLUMN IF NOT EXISTS is_sensitive BOOLEAN NOT NULL DEFAULT FALSE`,
			`ALTER TABLE content_events ADD COLUMN IF NOT EXISTS sensitivity_classified_at TIMESTAMPTZ NULL`,
			`CREATE INDEX IF NOT EXISTS content_events_sensitive_idx ON content_events(is_sensitive, created_at DESC) WHERE is_sensitive`,
			`CREATE INDEX IF NOT EXISTS content_events_sensitivity_pending_idx ON content_events(created_at ASC) WHERE sensitivity_classified_at IS NULL`,
		},
	},
	{
		version: 22,
		name:    "visual_sensitive_media_classification",
		statements: []string{
			`ALTER TABLE content_media ADD COLUMN IF NOT EXISTS visual_sensitivity_classified_at TIMESTAMPTZ NULL`,
			`CREATE INDEX IF NOT EXISTS content_media_visual_sensitivity_pending_idx ON content_media(created_at ASC) WHERE media_type='image' AND visual_sensitivity_classified_at IS NULL`,
		},
	},
}

func Migrate(ctx context.Context, db *sql.DB) error {
	if err := validateMigrations(migrationCatalog); err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	applied, err := appliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	catalog := make(map[int64]migration, len(migrationCatalog))
	for _, item := range migrationCatalog {
		catalog[item.version] = item
	}
	for version := range applied {
		if _, ok := catalog[version]; !ok {
			return fmt.Errorf("database schema version %d is newer than this binary", version)
		}
	}

	for _, item := range migrationCatalog {
		checksum := migrationChecksum(item)
		if stored, ok := applied[item.version]; ok {
			if stored != checksum {
				compatible, err := canReplaceRedactedMigrationChecksum(ctx, tx, item)
				if err != nil {
					return err
				}
				if compatible {
					if _, err := tx.ExecContext(ctx, `UPDATE schema_migrations SET checksum = $2 WHERE version = $1`, item.version, checksum); err != nil {
						return fmt.Errorf("update redacted migration %d (%s) checksum: %w", item.version, item.name, err)
					}
					continue
				}
				return fmt.Errorf("migration %d (%s) checksum mismatch", item.version, item.name)
			}
			continue
		}
		for statementIndex, statement := range item.statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply migration %d (%s), statement %d: %w", item.version, item.name, statementIndex+1, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, name, checksum) VALUES ($1,$2,$3)
		`, item.version, item.name, checksum); err != nil {
			return fmt.Errorf("record migration %d (%s): %w", item.version, item.name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func canReplaceRedactedMigrationChecksum(ctx context.Context, tx *sql.Tx, item migration) (bool, error) {
	if !isRedactedMigration(item) {
		return false, nil
	}

	// Migration 9 was redacted to remove a machine-specific default profile.
	// Verify its actual DDL before replacing the old checksum in an existing DB.
	var compatible bool
	err := tx.QueryRowContext(ctx, `
		SELECT
			to_regclass('public.miners') IS NOT NULL
			AND (SELECT count(*) FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = 'miners'
				AND column_name IN ('id', 'name', 'script_path', 'process_name', 'enabled', 'is_default', 'created_by_user_id')) = 7
			AND EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'miners' AND indexname = 'miners_single_default_idx')
			AND EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'miners' AND indexname = 'miners_enabled_idx')
	`).Scan(&compatible)
	if err != nil {
		return false, fmt.Errorf("verify redacted migration %d (%s): %w", item.version, item.name, err)
	}
	return compatible, nil
}

func isRedactedMigration(item migration) bool {
	return item.version == 9 && item.name == "mining_profiles"
}

func appliedMigrations(ctx context.Context, tx *sql.Tx) (map[int64]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[int64]string)
	for rows.Next() {
		var version int64
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

func migrationChecksum(item migration) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%d\x00%s\x00", item.version, item.name)
	for _, statement := range item.statements {
		hash.Write([]byte(strings.TrimSpace(statement)))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validateMigrations(items []migration) error {
	if len(items) == 0 {
		return fmt.Errorf("migration catalog is empty")
	}
	versions := make([]int64, 0, len(items))
	seenNames := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.version <= 0 || strings.TrimSpace(item.name) == "" || len(item.statements) == 0 {
			return fmt.Errorf("invalid migration definition at version %d", item.version)
		}
		if _, exists := seenNames[item.name]; exists {
			return fmt.Errorf("duplicate migration name %q", item.name)
		}
		seenNames[item.name] = struct{}{}
		versions = append(versions, item.version)
	}
	if !sort.SliceIsSorted(versions, func(i, j int) bool { return versions[i] < versions[j] }) {
		return fmt.Errorf("migration versions must be strictly increasing")
	}
	for index, version := range versions {
		if version != int64(index+1) {
			return fmt.Errorf("migration versions must be contiguous: got %d at position %d", version, index+1)
		}
	}
	return nil
}
