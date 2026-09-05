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
	{
		version: 23,
		name:    "recheck_live_media_with_strict_visual_policy",
		statements: []string{
			`UPDATE content_media SET visual_sensitivity_classified_at=NULL WHERE media_type='image' AND expires_at > now()`,
		},
	},
	{
		version: 24,
		name:    "recheck_live_content_with_expanded_sensitive_terms",
		statements: []string{
			`UPDATE content_events SET sensitivity_classified_at=NULL WHERE expires_at > now()`,
		},
	},
	{
		version: 25,
		name:    "recheck_live_media_with_dedicated_nsfw_classifier",
		statements: []string{
			`UPDATE content_media SET visual_sensitivity_classified_at=NULL WHERE media_type='image' AND expires_at > now()`,
		},
	},
	{
		version: 26,
		name:    "recheck_live_media_with_high_recall_nsfw_threshold",
		statements: []string{
			`UPDATE content_media SET visual_sensitivity_classified_at=NULL WHERE media_type='image' AND expires_at > now()`,
		},
	},
	{
		version: 27,
		name:    "recheck_live_media_with_balanced_nsfw_threshold",
		statements: []string{
			`UPDATE content_media SET visual_sensitivity_classified_at=NULL WHERE media_type='image' AND expires_at > now()`,
		},
	},
	{
		version: 28,
		name:    "quick_generation_recipes_and_variants",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS quick_generation_recipes (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
				template_id TEXT NOT NULL DEFAULT '',
				workflow_id TEXT NOT NULL DEFAULT '',
				payload_cipher BYTEA NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE INDEX IF NOT EXISTS quick_generation_recipes_user_updated_idx ON quick_generation_recipes(user_id,updated_at DESC)`,
			`CREATE TABLE IF NOT EXISTS quick_generation_variants (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				prompt_id TEXT NOT NULL UNIQUE,
				template_id TEXT NOT NULL DEFAULT '',
				workflow_id TEXT NOT NULL DEFAULT '',
				model_name TEXT NOT NULL DEFAULT '',
				seed BIGINT NOT NULL,
				payload_cipher BYTEA NOT NULL,
				state TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','completed','error','cancelled')),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				finished_at TIMESTAMPTZ NULL
			)`,
			`CREATE INDEX IF NOT EXISTS quick_generation_variants_user_created_idx ON quick_generation_variants(user_id,created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS quick_generation_variants_completed_idx ON quick_generation_variants(finished_at DESC) WHERE state='completed'`,
		},
	},
	{
		version: 29,
		name:    "content_generation_status_and_three_day_media_retention",
		statements: []string{
			`ALTER TABLE content_events ADD COLUMN IF NOT EXISTS generation_state TEXT NOT NULL DEFAULT '' CHECK (generation_state IN ('','queued','running','completed','error','cancelled'))`,
			`ALTER TABLE content_events ADD COLUMN IF NOT EXISTS generated_media_count BIGINT NOT NULL DEFAULT 0 CHECK (generated_media_count >= 0)`,
			`ALTER TABLE content_events ADD COLUMN IF NOT EXISTS media_expires_at TIMESTAMPTZ NULL`,
			`CREATE INDEX IF NOT EXISTS content_events_generation_state_idx ON content_events(generation_state,created_at DESC) WHERE generation_state <> ''`,
			`ALTER TABLE content_media ALTER COLUMN expires_at SET DEFAULT (now() + interval '3 days')`,
			`UPDATE content_media SET expires_at=GREATEST(expires_at,created_at + interval '3 days') WHERE expires_at > now()`,
			`UPDATE content_events e
			 SET generated_media_count=summary.media_count,media_expires_at=summary.last_media_expiry
			 FROM (
			   SELECT event_id,COUNT(*)::BIGINT AS media_count,MAX(expires_at) AS last_media_expiry
			   FROM content_media GROUP BY event_id
			 ) summary
			 WHERE e.id=summary.event_id`,
			`UPDATE content_events SET generation_state='completed'
			 WHERE service='comfyui' AND kind='comfyui_prompt' AND generation_state='' AND generated_media_count > 0`,
		},
	},
	{
		version: 30,
		name:    "quick_generation_policies_and_durable_state",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS quick_generation_access_policies (
				user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
				preset_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
				model_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
				krea_lora_groups JSONB NOT NULL DEFAULT '[]'::jsonb,
				flux_lora_groups JSONB NOT NULL DEFAULT '[]'::jsonb,
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`ALTER TABLE quick_generation_variants ADD COLUMN IF NOT EXISTS state_changed_at TIMESTAMPTZ NOT NULL DEFAULT now()`,
			`ALTER TABLE quick_generation_variants ADD COLUMN IF NOT EXISTS error_message TEXT NOT NULL DEFAULT ''`,
			`UPDATE quick_generation_variants SET state_changed_at=COALESCE(finished_at,created_at)`,
			`CREATE INDEX IF NOT EXISTS quick_generation_variants_active_idx ON quick_generation_variants(created_at ASC) WHERE state IN ('queued','running')`,
		},
	},
	{
		version: 31,
		name:    "temporary_invite_accounts",
		statements: []string{
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS account_lifetime_seconds BIGINT NOT NULL DEFAULT 0 CHECK (account_lifetime_seconds >= 0 AND account_lifetime_seconds <= 31536000)`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS account_expires_at TIMESTAMPTZ NULL`,
			`CREATE INDEX IF NOT EXISTS users_temporary_expiry_idx ON users(account_expires_at) WHERE account_expires_at IS NOT NULL`,
			`ALTER TABLE content_events DROP CONSTRAINT IF EXISTS content_events_user_id_fkey`,
			`ALTER TABLE content_events ALTER COLUMN user_id DROP NOT NULL`,
			`ALTER TABLE content_events ADD CONSTRAINT content_events_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL`,
		},
	},
	{
		version: 32,
		name:    "feature_suggestions_with_virustotal_scans",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS feature_suggestions (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
				username TEXT NOT NULL DEFAULT '',
				title TEXT NOT NULL CHECK (char_length(title) BETWEEN 3 AND 120),
				description_cipher BYTEA NOT NULL,
				links_cipher BYTEA NOT NULL,
				json_name TEXT NOT NULL DEFAULT '',
				json_cipher BYTEA NOT NULL,
				status TEXT NOT NULL DEFAULT 'scanning' CHECK (status IN ('scanning','clean','flagged','error')),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE INDEX IF NOT EXISTS feature_suggestions_created_idx ON feature_suggestions(created_at DESC)`,
			`CREATE TABLE IF NOT EXISTS feature_suggestion_scans (
				id BIGSERIAL PRIMARY KEY,
				suggestion_id BIGINT NOT NULL REFERENCES feature_suggestions(id) ON DELETE CASCADE,
				kind TEXT NOT NULL CHECK (kind IN ('url','json')),
				source_name TEXT NOT NULL,
				analysis_id TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','in-progress','completed','error')),
				malicious INT NOT NULL DEFAULT 0 CHECK (malicious >= 0),
				suspicious INT NOT NULL DEFAULT 0 CHECK (suspicious >= 0),
				harmless INT NOT NULL DEFAULT 0 CHECK (harmless >= 0),
				undetected INT NOT NULL DEFAULT 0 CHECK (undetected >= 0),
				timeout INT NOT NULL DEFAULT 0 CHECK (timeout >= 0),
				error_message TEXT NOT NULL DEFAULT '',
				checked_at TIMESTAMPTZ NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE INDEX IF NOT EXISTS feature_suggestion_scans_pending_idx ON feature_suggestion_scans(status,created_at ASC) WHERE status IN ('queued','in-progress')`,
		},
	},
	{
		version: 33,
		name:    "prompt_assistant_content_service",
		statements: []string{
			`ALTER TABLE content_events DROP CONSTRAINT IF EXISTS content_events_service_check`,
			`ALTER TABLE content_events ADD CONSTRAINT content_events_service_check CHECK (service IN ('comfyui','openwebui','ollama'))`,
		},
	},
	{
		version: 34,
		name:    "tracked_comfy_input_assets",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS comfy_input_assets (
				id TEXT PRIMARY KEY CHECK (char_length(id) BETWEEN 32 AND 128),
				user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
				filename TEXT NOT NULL DEFAULT '',
				subfolder TEXT NOT NULL DEFAULT '',
				storage_type TEXT NOT NULL DEFAULT 'input' CHECK (storage_type = 'input'),
				size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0 AND size_bytes <= 335544320),
				content_hash TEXT NOT NULL CHECK (char_length(content_hash) = 64),
				state TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','stored')),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '15 minutes'),
				cleanup_retry_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				cleanup_attempts INTEGER NOT NULL DEFAULT 0 CHECK (cleanup_attempts >= 0)
			)`,
			`CREATE INDEX IF NOT EXISTS comfy_input_assets_user_live_idx ON comfy_input_assets(user_id,expires_at) WHERE user_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS comfy_input_assets_expiry_idx ON comfy_input_assets(cleanup_retry_at,expires_at)`,
		},
	},
	{
		version: 35,
		name:    "comfy_output_cleanup_tombstones",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS comfy_output_cleanup_tombstones (
				id BIGSERIAL PRIMARY KEY,
				filename TEXT NOT NULL,
				subfolder TEXT NOT NULL DEFAULT '',
				storage_type TEXT NOT NULL DEFAULT 'output' CHECK (storage_type = 'output'),
				size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0 AND size_bytes <= 2147483648),
				content_hash TEXT NOT NULL CHECK (char_length(content_hash) = 64),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
				UNIQUE(filename,subfolder,storage_type,size_bytes,content_hash)
			)`,
			`CREATE INDEX IF NOT EXISTS comfy_output_cleanup_due_idx ON comfy_output_cleanup_tombstones(next_attempt_at,id)`,
		},
	},
	{
		version: 36,
		name:    "content_live_revision",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS content_change_revision (
				id SMALLINT PRIMARY KEY CHECK (id = 1),
				revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
				changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`INSERT INTO content_change_revision(id,revision) VALUES (1,1) ON CONFLICT (id) DO NOTHING`,
			`CREATE OR REPLACE FUNCTION bump_content_change_revision() RETURNS trigger AS $$
			BEGIN
				UPDATE content_change_revision SET revision=revision+1,changed_at=now() WHERE id=1;
				RETURN NULL;
			END;
			$$ LANGUAGE plpgsql`,
			`DROP TRIGGER IF EXISTS content_events_change_revision ON content_events`,
			`CREATE TRIGGER content_events_change_revision
				AFTER INSERT OR UPDATE OR DELETE ON content_events
				FOR EACH STATEMENT EXECUTE FUNCTION bump_content_change_revision()`,
			`DROP TRIGGER IF EXISTS content_media_change_revision ON content_media`,
			`CREATE TRIGGER content_media_change_revision
				AFTER INSERT OR UPDATE OR DELETE ON content_media
				FOR EACH STATEMENT EXECUTE FUNCTION bump_content_change_revision()`,
		},
	},
	{
		version: 37,
		name:    "unified_content_retention_defaults",
		statements: []string{
			`ALTER TABLE content_events ALTER COLUMN expires_at SET DEFAULT (now() + interval '7 days')`,
			`ALTER TABLE content_media ALTER COLUMN expires_at SET DEFAULT (now() + interval '24 hours')`,
			`ALTER TABLE comfy_output_ownership ALTER COLUMN expires_at SET DEFAULT (now() + interval '24 hours')`,
			`UPDATE content_media SET expires_at=LEAST(expires_at,created_at + interval '24 hours') WHERE expires_at > now()`,
			`UPDATE comfy_output_ownership SET expires_at=LEAST(expires_at,created_at + interval '24 hours') WHERE expires_at > now()`,
			`UPDATE content_events e
			 SET media_expires_at=summary.last_media_expiry
			 FROM (
			   SELECT event_id,MAX(expires_at) AS last_media_expiry
			   FROM content_media GROUP BY event_id
			 ) summary
			 WHERE e.id=summary.event_id`,
		},
	},
	{
		version: 38,
		name:    "quick_generation_request_telemetry",
		statements: []string{
			`CREATE UNIQUE INDEX IF NOT EXISTS proxy_requests_quick_generation_request_idx
			 ON proxy_requests(user_id,path)
			 WHERE service='comfyui' AND method='POST' AND path LIKE '/generate/run/%'`,
			`INSERT INTO proxy_requests
				(user_id,service,method,path,status_code,duration_ms,bytes_in,bytes_out,is_websocket,client_ip,user_agent,created_at)
			 SELECT gr.user_id,'comfyui','POST','/generate/run/' || gr.request_id,202,0,0,0,false,'','',gr.created_at
			 FROM generation_requests gr
			 WHERE gr.prompt_id IS NOT NULL
			   AND NOT EXISTS (
				 SELECT 1 FROM proxy_requests pr
				 WHERE pr.user_id=gr.user_id AND pr.service='comfyui' AND pr.method='POST'
				   AND pr.path='/generate/run/' || gr.request_id
			   )`,
		},
	},
	{
		version: 39,
		name:    "bounded_database_retention",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS database_cleanup_state (
				id SMALLINT PRIMARY KEY CHECK (id = 1),
				last_started_at TIMESTAMPTZ NULL,
				last_finished_at TIMESTAMPTZ NULL,
				last_success_at TIMESTAMPTZ NULL,
				status TEXT NOT NULL DEFAULT 'never' CHECK (status IN ('never','ok','partial','error')),
				deleted_rows JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(deleted_rows) = 'object'),
				errors JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(errors) = 'object'),
				duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`INSERT INTO database_cleanup_state(id) VALUES (1) ON CONFLICT (id) DO NOTHING`,
			`CREATE INDEX IF NOT EXISTS proxy_requests_retention_idx ON proxy_requests(created_at,id)`,
			`CREATE INDEX IF NOT EXISTS websocket_sessions_retention_idx ON websocket_sessions(closed_at,id) WHERE closed_at IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS generation_requests_retention_idx ON generation_requests(updated_at,user_id,request_id)`,
			`CREATE INDEX IF NOT EXISTS quick_generation_daily_usage_retention_idx ON quick_generation_daily_usage(usage_date,user_id)`,
			`CREATE INDEX IF NOT EXISTS invites_retention_idx ON invites(expires_at,id)`,
			`CREATE INDEX IF NOT EXISTS audit_log_retention_idx ON audit_log(created_at,id)`,
			`CREATE INDEX IF NOT EXISTS host_metrics_retention_idx ON host_metrics(recorded_at,id)`,
			`CREATE INDEX IF NOT EXISTS quick_generation_variants_retention_idx
			 ON quick_generation_variants((COALESCE(finished_at,created_at)),id)
			 WHERE state NOT IN ('queued','running')`,
			`CREATE INDEX IF NOT EXISTS comfy_output_ownership_retention_idx ON comfy_output_ownership(expires_at,id)`,
		},
	},
	{
		version: 40,
		name:    "durable_generation_jobs",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS generation_jobs (
				id BIGSERIAL PRIMARY KEY,
				public_id TEXT NOT NULL UNIQUE CHECK (char_length(public_id) BETWEEN 16 AND 96 AND public_id ~ '^[A-Za-z0-9_-]+$'),
				user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
				username_snapshot TEXT NOT NULL DEFAULT '' CHECK (char_length(username_snapshot) <= 128),
				request_id TEXT NOT NULL CHECK (char_length(request_id) BETWEEN 1 AND 128),
				parent_job_id BIGINT NULL REFERENCES generation_jobs(id) ON DELETE SET NULL,
				prompt_id TEXT NULL UNIQUE CHECK (prompt_id IS NULL OR char_length(prompt_id) BETWEEN 1 AND 128),
				template_id TEXT NOT NULL DEFAULT '' CHECK (char_length(template_id) <= 256),
				workflow_id TEXT NOT NULL DEFAULT '' CHECK (char_length(workflow_id) <= 256),
				model_name TEXT NOT NULL DEFAULT '' CHECK (char_length(model_name) <= 512),
				seed BIGINT NOT NULL DEFAULT -1,
				payload_cipher BYTEA NULL,
				state TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft','preparing','uploading','waiting_for_resources','queued','running','postprocessing','archiving','completed','failed','cancelled','expired')),
				status_message TEXT NOT NULL DEFAULT '' CHECK (char_length(status_message) <= 500),
				error_code TEXT NOT NULL DEFAULT '' CHECK (char_length(error_code) <= 80),
				error_message TEXT NOT NULL DEFAULT '' CHECK (char_length(error_message) <= 4000),
				attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt BETWEEN 1 AND 100),
				dependencies JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(dependencies) = 'array'),
				input_count INTEGER NOT NULL DEFAULT 0 CHECK (input_count BETWEEN 0 AND 16),
				state_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				started_at TIMESTAMPTZ NULL,
				finished_at TIMESTAMPTZ NULL,
				resources_released_at TIMESTAMPTZ NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS generation_jobs_user_request_idx ON generation_jobs(user_id,request_id) WHERE user_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS generation_jobs_user_created_idx ON generation_jobs(user_id,created_at DESC,id DESC)`,
			`CREATE INDEX IF NOT EXISTS generation_jobs_active_idx ON generation_jobs(state_changed_at,id) WHERE state NOT IN ('completed','failed','cancelled','expired')`,
			`CREATE INDEX IF NOT EXISTS generation_jobs_retention_idx ON generation_jobs(finished_at,id) WHERE state IN ('completed','failed','cancelled','expired')`,
			`INSERT INTO generation_jobs
				(public_id,user_id,username_snapshot,request_id,prompt_id,template_id,workflow_id,model_name,seed,payload_cipher,state,status_message,error_code,error_message,attempt,dependencies,input_count,state_changed_at,started_at,finished_at,resources_released_at,created_at,updated_at)
			 SELECT 'legacy-' || lpad(v.id::text,20,'0'),v.user_id,COALESCE(u.username,''),
				COALESCE(gr.request_id,'legacy-' || lpad(v.id::text,20,'0')),v.prompt_id,
				left(v.template_id,256),left(v.workflow_id,256),left(v.model_name,512),v.seed,v.payload_cipher,
				CASE v.state WHEN 'error' THEN 'failed' ELSE v.state END,
				CASE v.state WHEN 'queued' THEN 'Генерация ожидает запуска' WHEN 'running' THEN 'ComfyUI выполняет workflow' WHEN 'completed' THEN 'Готово' WHEN 'cancelled' THEN 'Генерация отменена' ELSE 'Генерация завершилась с ошибкой' END,
				CASE WHEN v.state='error' THEN 'legacy_generation_error' ELSE '' END,left(v.error_message,4000),1,'["comfyui"]'::jsonb,0,
				v.state_changed_at,CASE WHEN v.state IN ('running','completed','error','cancelled') THEN v.created_at ELSE NULL END,
				v.finished_at,CASE WHEN v.state IN ('completed','error','cancelled') THEN COALESCE(v.finished_at,v.state_changed_at,v.created_at) ELSE NULL END,
				v.created_at,COALESCE(v.finished_at,v.state_changed_at,v.created_at)
			 FROM quick_generation_variants v
			 LEFT JOIN users u ON u.id=v.user_id
			 LEFT JOIN LATERAL (
				SELECT r.request_id FROM generation_requests r
				WHERE r.user_id=v.user_id AND r.prompt_id=v.prompt_id
				ORDER BY r.created_at LIMIT 1
			 ) gr ON true
			 ON CONFLICT (prompt_id) DO NOTHING`,
			`INSERT INTO generation_jobs
				(public_id,user_id,username_snapshot,request_id,prompt_id,state,status_message,error_code,error_message,attempt,dependencies,state_changed_at,started_at,finished_at,resources_released_at,created_at,updated_at)
			 SELECT 'request-' || substr(md5(r.user_id::text || ':' || r.request_id),1,32),r.user_id,COALESCE(u.username,''),r.request_id,r.prompt_id,
				CASE WHEN r.prompt_id IS NOT NULL THEN 'queued' WHEN r.updated_at < now()-interval '5 minutes' THEN 'expired' ELSE 'preparing' END,
				CASE WHEN r.prompt_id IS NOT NULL THEN 'Генерация ожидает запуска' WHEN r.updated_at < now()-interval '5 minutes' THEN 'Подтверждение запуска истекло' ELSE 'Подтверждаем запуск' END,
				CASE WHEN r.prompt_id IS NULL AND r.updated_at < now()-interval '5 minutes' THEN 'legacy_submission_unconfirmed' ELSE '' END,
				CASE WHEN r.prompt_id IS NULL AND r.updated_at < now()-interval '5 minutes' THEN 'Gateway не получил prompt_id до перезапуска' ELSE '' END,
				1,'["comfyui"]'::jsonb,r.updated_at,NULL,
				CASE WHEN r.prompt_id IS NULL AND r.updated_at < now()-interval '5 minutes' THEN r.updated_at ELSE NULL END,
				CASE WHEN r.prompt_id IS NULL AND r.updated_at < now()-interval '5 minutes' THEN r.updated_at ELSE NULL END,
				r.created_at,r.updated_at
			 FROM generation_requests r
			 LEFT JOIN users u ON u.id=r.user_id
			 WHERE NOT EXISTS (
				SELECT 1 FROM generation_jobs j
				WHERE j.user_id=r.user_id AND j.request_id=r.request_id
			 )
			 ON CONFLICT DO NOTHING`,
			`CREATE TABLE IF NOT EXISTS generation_job_transitions (
				id BIGSERIAL PRIMARY KEY,
				job_id BIGINT NOT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE,
				from_state TEXT NOT NULL DEFAULT '' CHECK (from_state IN ('','draft','preparing','uploading','waiting_for_resources','queued','running','postprocessing','archiving','completed','failed','cancelled','expired')),
				to_state TEXT NOT NULL CHECK (to_state IN ('draft','preparing','uploading','waiting_for_resources','queued','running','postprocessing','archiving','completed','failed','cancelled','expired')),
				message TEXT NOT NULL DEFAULT '' CHECK (char_length(message) <= 500),
				error_code TEXT NOT NULL DEFAULT '' CHECK (char_length(error_code) <= 80),
				error_message TEXT NOT NULL DEFAULT '' CHECK (char_length(error_message) <= 4000),
				attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt BETWEEN 1 AND 100),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE INDEX IF NOT EXISTS generation_job_transitions_job_created_idx ON generation_job_transitions(job_id,created_at,id)`,
			`INSERT INTO generation_job_transitions(job_id,from_state,to_state,message,error_code,error_message,attempt,created_at)
			 SELECT id,'',state,status_message,error_code,error_message,attempt,state_changed_at
			 FROM generation_jobs j
			 WHERE NOT EXISTS (SELECT 1 FROM generation_job_transitions t WHERE t.job_id=j.id)`,
			`ALTER TABLE generation_requests ADD COLUMN IF NOT EXISTS job_id BIGINT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE`,
			`UPDATE generation_requests r SET job_id=j.id FROM generation_jobs j
			 WHERE r.job_id IS NULL AND j.user_id=r.user_id AND j.request_id=r.request_id`,
			`CREATE UNIQUE INDEX IF NOT EXISTS generation_requests_job_idx ON generation_requests(job_id) WHERE job_id IS NOT NULL`,
			`ALTER TABLE quick_generation_variants ADD COLUMN IF NOT EXISTS job_id BIGINT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE`,
			`UPDATE quick_generation_variants v SET job_id=j.id FROM generation_jobs j
			 WHERE v.job_id IS NULL AND j.prompt_id=v.prompt_id`,
			`CREATE UNIQUE INDEX IF NOT EXISTS quick_generation_variants_job_idx ON quick_generation_variants(job_id) WHERE job_id IS NOT NULL`,
			`ALTER TABLE content_events ADD COLUMN IF NOT EXISTS generation_job_id BIGINT NULL REFERENCES generation_jobs(id) ON DELETE SET NULL`,
			`UPDATE content_events e SET generation_job_id=j.id FROM generation_jobs j
			 WHERE e.generation_job_id IS NULL AND e.service='comfyui' AND e.kind='comfyui_prompt' AND e.external_id=j.prompt_id
			   AND (j.user_id IS NULL OR e.user_id=j.user_id)`,
			`CREATE INDEX IF NOT EXISTS content_events_generation_job_idx ON content_events(generation_job_id) WHERE generation_job_id IS NOT NULL`,
			`ALTER TABLE quick_generation_mining_leases ADD COLUMN IF NOT EXISTS generation_job_id BIGINT NULL REFERENCES generation_jobs(id) ON DELETE SET NULL`,
			`UPDATE quick_generation_mining_leases l SET generation_job_id=j.id FROM generation_jobs j
			 WHERE l.generation_job_id IS NULL AND l.prompt_id=j.prompt_id`,
			`CREATE INDEX IF NOT EXISTS quick_generation_mining_leases_job_idx ON quick_generation_mining_leases(generation_job_id) WHERE generation_job_id IS NOT NULL`,
			`CREATE TABLE IF NOT EXISTS generation_job_revision (
				id SMALLINT PRIMARY KEY CHECK (id=1),
				revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0),
				changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`INSERT INTO generation_job_revision(id,revision) VALUES (1,1) ON CONFLICT (id) DO NOTHING`,
		},
	},
	{
		version: 41,
		name:    "generation_job_execution_resources",
		statements: []string{
			`ALTER TABLE generation_jobs ADD COLUMN IF NOT EXISTS quota_reserved_on DATE NULL`,
			`ALTER TABLE generation_jobs ADD COLUMN IF NOT EXISTS quota_committed_at TIMESTAMPTZ NULL`,
			`ALTER TABLE generation_jobs ADD COLUMN IF NOT EXISTS cancellation_requested_at TIMESTAMPTZ NULL`,
			`ALTER TABLE generation_jobs ADD COLUMN IF NOT EXISTS cancellation_confirmed_at TIMESTAMPTZ NULL`,
			`WITH duplicates AS (
				SELECT id,row_number() OVER (PARTITION BY generation_job_id ORDER BY id) AS position
				FROM content_events
				WHERE generation_job_id IS NOT NULL AND service='comfyui' AND kind='comfyui_prompt'
			 )
			 UPDATE content_events e SET generation_job_id=NULL
			 FROM duplicates d WHERE e.id=d.id AND d.position>1`,
			`CREATE UNIQUE INDEX IF NOT EXISTS content_events_generation_job_prompt_idx
			 ON content_events(generation_job_id)
			 WHERE generation_job_id IS NOT NULL AND service='comfyui' AND kind='comfyui_prompt'`,
			`CREATE INDEX IF NOT EXISTS generation_jobs_cancellation_idx ON generation_jobs(cancellation_requested_at,id)
			 WHERE cancellation_requested_at IS NOT NULL AND state NOT IN ('completed','failed','cancelled','expired')`,
		},
	},
	{
		version: 42,
		name:    "end_to_end_observability",
		statements: []string{
			`ALTER TABLE generation_jobs ADD COLUMN IF NOT EXISTS correlation_id TEXT NOT NULL DEFAULT ''
			 CHECK (correlation_id='' OR (char_length(correlation_id) BETWEEN 16 AND 96 AND correlation_id ~ '^[A-Za-z0-9_-]+$'))`,
			`UPDATE generation_jobs SET correlation_id=public_id WHERE correlation_id=''`,
			`CREATE INDEX IF NOT EXISTS generation_jobs_correlation_idx ON generation_jobs(correlation_id)`,
			`ALTER TABLE generation_requests ADD COLUMN IF NOT EXISTS correlation_id TEXT NOT NULL DEFAULT ''
			 CHECK (correlation_id='' OR (char_length(correlation_id) BETWEEN 16 AND 96 AND correlation_id ~ '^[A-Za-z0-9_-]+$'))`,
			`UPDATE generation_requests r SET correlation_id=j.correlation_id
			 FROM generation_jobs j WHERE r.job_id=j.id AND r.correlation_id=''`,
			`CREATE INDEX IF NOT EXISTS generation_requests_correlation_idx ON generation_requests(correlation_id) WHERE correlation_id<>''`,
			`ALTER TABLE generation_job_transitions ADD COLUMN IF NOT EXISTS correlation_id TEXT NOT NULL DEFAULT ''
			 CHECK (correlation_id='' OR (char_length(correlation_id) BETWEEN 16 AND 96 AND correlation_id ~ '^[A-Za-z0-9_-]+$'))`,
			`ALTER TABLE generation_job_transitions ADD COLUMN IF NOT EXISTS duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (duration_ms >= 0)`,
			`UPDATE generation_job_transitions t SET correlation_id=j.correlation_id
			 FROM generation_jobs j WHERE t.job_id=j.id AND t.correlation_id=''`,
			`WITH ordered AS (
			 SELECT id,lag(created_at) OVER (PARTITION BY job_id ORDER BY created_at,id) AS previous_at
			 FROM generation_job_transitions
			 )
			 UPDATE generation_job_transitions t
			 SET duration_ms=GREATEST(0,(EXTRACT(EPOCH FROM (t.created_at-o.previous_at))*1000)::bigint)
			 FROM ordered o WHERE t.id=o.id AND o.previous_at IS NOT NULL AND t.duration_ms=0`,
			`CREATE INDEX IF NOT EXISTS generation_job_transitions_correlation_idx ON generation_job_transitions(correlation_id,created_at,id)`,
			`ALTER TABLE quick_generation_mining_leases ADD COLUMN IF NOT EXISTS correlation_id TEXT NOT NULL DEFAULT ''
			 CHECK (correlation_id='' OR (char_length(correlation_id) BETWEEN 16 AND 96 AND correlation_id ~ '^[A-Za-z0-9_-]+$'))`,
			`UPDATE quick_generation_mining_leases l SET correlation_id=j.correlation_id
			 FROM generation_jobs j WHERE l.generation_job_id=j.id AND l.correlation_id=''`,
			`CREATE INDEX IF NOT EXISTS quick_generation_mining_leases_correlation_idx ON quick_generation_mining_leases(correlation_id) WHERE correlation_id<>''`,
			`ALTER TABLE content_events ADD COLUMN IF NOT EXISTS correlation_id TEXT NOT NULL DEFAULT ''
			 CHECK (correlation_id='' OR (char_length(correlation_id) BETWEEN 16 AND 96 AND correlation_id ~ '^[A-Za-z0-9_-]+$'))`,
			`UPDATE content_events e SET correlation_id=j.correlation_id
			 FROM generation_jobs j WHERE e.generation_job_id=j.id AND e.correlation_id=''`,
			`CREATE INDEX IF NOT EXISTS content_events_correlation_idx ON content_events(correlation_id,created_at DESC) WHERE correlation_id<>''`,
			`ALTER TABLE proxy_requests ADD COLUMN IF NOT EXISTS request_id TEXT NOT NULL DEFAULT '' CHECK (char_length(request_id) <= 96)`,
			`ALTER TABLE proxy_requests ADD COLUMN IF NOT EXISTS correlation_id TEXT NOT NULL DEFAULT ''
			 CHECK (correlation_id='' OR (char_length(correlation_id) BETWEEN 16 AND 96 AND correlation_id ~ '^[A-Za-z0-9_-]+$'))`,
			`ALTER TABLE proxy_requests ADD COLUMN IF NOT EXISTS generation_job_id BIGINT NULL REFERENCES generation_jobs(id) ON DELETE SET NULL`,
			`UPDATE proxy_requests pr SET generation_job_id=j.id,correlation_id=j.correlation_id,request_id=j.request_id
			 FROM generation_jobs j
			 WHERE pr.user_id=j.user_id AND pr.service='comfyui' AND pr.method='POST'
			   AND pr.path='/generate/run/' || j.request_id AND pr.generation_job_id IS NULL`,
			`CREATE INDEX IF NOT EXISTS proxy_requests_correlation_idx ON proxy_requests(correlation_id,created_at DESC) WHERE correlation_id<>''`,
			`CREATE INDEX IF NOT EXISTS proxy_requests_generation_job_idx ON proxy_requests(generation_job_id,created_at) WHERE generation_job_id IS NOT NULL`,
			`ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS request_id TEXT NOT NULL DEFAULT '' CHECK (char_length(request_id) <= 96)`,
			`ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS correlation_id TEXT NOT NULL DEFAULT ''
			 CHECK (correlation_id='' OR (char_length(correlation_id) BETWEEN 16 AND 96 AND correlation_id ~ '^[A-Za-z0-9_-]+$'))`,
			`ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS generation_job_id BIGINT NULL REFERENCES generation_jobs(id) ON DELETE SET NULL`,
			`CREATE INDEX IF NOT EXISTS audit_log_correlation_idx ON audit_log(correlation_id,created_at DESC) WHERE correlation_id<>''`,
			`CREATE INDEX IF NOT EXISTS audit_log_generation_job_idx ON audit_log(generation_job_id,created_at) WHERE generation_job_id IS NOT NULL`,
			`CREATE TABLE IF NOT EXISTS service_observations (
			 id BIGSERIAL PRIMARY KEY,
			 component TEXT NOT NULL CHECK (char_length(component) BETWEEN 1 AND 80),
			 operation TEXT NOT NULL DEFAULT 'probe' CHECK (char_length(operation) BETWEEN 1 AND 120),
			 outcome TEXT NOT NULL CHECK (outcome IN ('ok','degraded','error','timeout','misconfigured')),
			 latency_ms BIGINT NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
			 generation_job_id BIGINT NULL REFERENCES generation_jobs(id) ON DELETE SET NULL,
			 correlation_id TEXT NOT NULL DEFAULT '' CHECK (correlation_id='' OR (char_length(correlation_id) BETWEEN 16 AND 96 AND correlation_id ~ '^[A-Za-z0-9_-]+$')),
			 error_code TEXT NOT NULL DEFAULT '' CHECK (char_length(error_code) <= 80),
			 detail TEXT NOT NULL DEFAULT '' CHECK (char_length(detail) <= 1000),
			 observed_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE INDEX IF NOT EXISTS service_observations_component_time_idx ON service_observations(component,observed_at DESC,id DESC)`,
			`CREATE INDEX IF NOT EXISTS service_observations_job_idx ON service_observations(generation_job_id,observed_at,id) WHERE generation_job_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS service_observations_retention_idx ON service_observations(observed_at,id)`,
			`CREATE TABLE IF NOT EXISTS gateway_observations (
			 id BIGSERIAL PRIMARY KEY,
			 database_bytes BIGINT NOT NULL DEFAULT 0 CHECK (database_bytes >= 0),
			 active_jobs INTEGER NOT NULL DEFAULT 0 CHECK (active_jobs >= 0),
			 overdue_jobs INTEGER NOT NULL DEFAULT 0 CHECK (overdue_jobs >= 0),
			 active_leases INTEGER NOT NULL DEFAULT 0 CHECK (active_leases >= 0),
			 content_moderation_backlog INTEGER NOT NULL DEFAULT 0 CHECK (content_moderation_backlog >= 0),
			 media_moderation_backlog INTEGER NOT NULL DEFAULT 0 CHECK (media_moderation_backlog >= 0),
			 cleanup_status TEXT NOT NULL DEFAULT 'never' CHECK (cleanup_status IN ('never','ok','partial','error')),
			 cleanup_age_seconds BIGINT NOT NULL DEFAULT 0 CHECK (cleanup_age_seconds >= 0),
			 recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE INDEX IF NOT EXISTS gateway_observations_retention_idx ON gateway_observations(recorded_at,id)`,
		},
	},
	{
		version: 43,
		name:    "chunked_content_media",
		statements: []string{
			`ALTER TABLE content_media ADD COLUMN IF NOT EXISTS storage_format TEXT NOT NULL DEFAULT 'inline_v1'`,
			`DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='content_media_storage_format_check' AND conrelid='content_media'::regclass) THEN
					ALTER TABLE content_media ADD CONSTRAINT content_media_storage_format_check CHECK (storage_format IN ('inline_v1','chunked_v1'));
				END IF;
			END $$`,
			`CREATE TABLE IF NOT EXISTS content_media_chunks (
				media_id BIGINT NOT NULL REFERENCES content_media(id) ON DELETE CASCADE,
				chunk_index INTEGER NOT NULL CHECK (chunk_index >= 0),
				payload_cipher BYTEA NOT NULL CHECK (octet_length(payload_cipher) > 0),
				plain_size INTEGER NOT NULL CHECK (plain_size BETWEEN 1 AND 1048576),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				PRIMARY KEY(media_id,chunk_index)
			)`,
		},
	},
	{
		version: 44,
		name:    "generation_media_library",
		statements: []string{
			`ALTER TABLE content_media ADD COLUMN IF NOT EXISTS favorite_at TIMESTAMPTZ NULL`,
			`ALTER TABLE content_media ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMPTZ NULL`,
			`CREATE INDEX IF NOT EXISTS content_media_favorite_idx ON content_media(favorite_at DESC,id DESC) WHERE favorite_at IS NOT NULL AND profile_hidden_at IS NULL`,
			`CREATE INDEX IF NOT EXISTS content_media_pinned_idx ON content_media(pinned_at DESC,id DESC) WHERE pinned_at IS NOT NULL AND profile_hidden_at IS NULL`,
			`CREATE TABLE IF NOT EXISTS generation_media_collections (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
				name_key TEXT NOT NULL CHECK (char_length(name_key) BETWEEN 1 AND 80),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				UNIQUE(user_id,name_key)
			)`,
			`CREATE TABLE IF NOT EXISTS generation_media_collection_items (
				collection_id BIGINT NOT NULL REFERENCES generation_media_collections(id) ON DELETE CASCADE,
				media_id BIGINT NOT NULL REFERENCES content_media(id) ON DELETE CASCADE,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				PRIMARY KEY(collection_id,media_id)
			)`,
			`CREATE INDEX IF NOT EXISTS generation_media_collection_items_media_idx ON generation_media_collection_items(media_id,collection_id)`,
			`CREATE TABLE IF NOT EXISTS generation_media_tags (
				media_id BIGINT NOT NULL REFERENCES content_media(id) ON DELETE CASCADE,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				tag TEXT NOT NULL CHECK (char_length(btrim(tag)) BETWEEN 1 AND 32),
				tag_key TEXT NOT NULL CHECK (char_length(tag_key) BETWEEN 1 AND 32),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				PRIMARY KEY(media_id,tag_key)
			)`,
			`CREATE INDEX IF NOT EXISTS generation_media_tags_user_idx ON generation_media_tags(user_id,tag_key,media_id)`,
			`CREATE TABLE IF NOT EXISTS generation_media_references (
				id BIGSERIAL PRIMARY KEY,
				source_media_id BIGINT NULL REFERENCES content_media(id) ON DELETE SET NULL,
				source_media_name TEXT NOT NULL DEFAULT '' CHECK (char_length(source_media_name) <= 255),
				target_job_id BIGINT NOT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE,
				reference_number SMALLINT NOT NULL CHECK (reference_number BETWEEN 1 AND 4),
				reference_role TEXT NOT NULL DEFAULT 'details' CHECK (char_length(reference_role) BETWEEN 1 AND 40),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				UNIQUE(target_job_id,reference_number)
			)`,
			`CREATE INDEX IF NOT EXISTS generation_media_references_source_idx ON generation_media_references(source_media_id,created_at DESC,id DESC) WHERE source_media_id IS NOT NULL`,
			`DROP TRIGGER IF EXISTS generation_media_collections_change_revision ON generation_media_collections`,
			`CREATE TRIGGER generation_media_collections_change_revision AFTER INSERT OR UPDATE OR DELETE ON generation_media_collections FOR EACH STATEMENT EXECUTE FUNCTION bump_content_change_revision()`,
			`DROP TRIGGER IF EXISTS generation_media_collection_items_change_revision ON generation_media_collection_items`,
			`CREATE TRIGGER generation_media_collection_items_change_revision AFTER INSERT OR UPDATE OR DELETE ON generation_media_collection_items FOR EACH STATEMENT EXECUTE FUNCTION bump_content_change_revision()`,
			`DROP TRIGGER IF EXISTS generation_media_tags_change_revision ON generation_media_tags`,
			`CREATE TRIGGER generation_media_tags_change_revision AFTER INSERT OR UPDATE OR DELETE ON generation_media_tags FOR EACH STATEMENT EXECUTE FUNCTION bump_content_change_revision()`,
			`DROP TRIGGER IF EXISTS generation_media_references_change_revision ON generation_media_references`,
			`CREATE TRIGGER generation_media_references_change_revision AFTER INSERT OR UPDATE OR DELETE ON generation_media_references FOR EACH STATEMENT EXECUTE FUNCTION bump_content_change_revision()`,
		},
	},
	{
		version: 45,
		name:    "unified_content_tasks",
		statements: []string{
			`ALTER TABLE content_events ADD COLUMN IF NOT EXISTS username_snapshot TEXT NOT NULL DEFAULT '' CHECK (char_length(username_snapshot) <= 128)`,
			`UPDATE content_events e SET username_snapshot=u.username FROM users u WHERE e.user_id=u.id AND e.username_snapshot=''`,
			`ALTER TABLE content_events ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now()`,
			`CREATE OR REPLACE FUNCTION touch_content_event_updated_at() RETURNS trigger AS $$
			BEGIN
				NEW.updated_at=now();
				RETURN NEW;
			END;
			$$ LANGUAGE plpgsql`,
			`DROP TRIGGER IF EXISTS content_events_touch_updated_at ON content_events`,
			`CREATE TRIGGER content_events_touch_updated_at BEFORE UPDATE ON content_events FOR EACH ROW EXECUTE FUNCTION touch_content_event_updated_at()`,
			`CREATE INDEX IF NOT EXISTS content_events_job_created_idx ON content_events(generation_job_id,created_at,id) WHERE generation_job_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS content_events_correlation_kind_idx ON content_events(correlation_id,kind,created_at,id) WHERE correlation_id<>''`,
			`DROP TRIGGER IF EXISTS generation_jobs_content_revision ON generation_jobs`,
			`CREATE TRIGGER generation_jobs_content_revision AFTER INSERT OR UPDATE OR DELETE ON generation_jobs FOR EACH STATEMENT EXECUTE FUNCTION bump_content_change_revision()`,
			`DROP TRIGGER IF EXISTS generation_job_transitions_content_revision ON generation_job_transitions`,
			`CREATE TRIGGER generation_job_transitions_content_revision AFTER INSERT OR UPDATE OR DELETE ON generation_job_transitions FOR EACH STATEMENT EXECUTE FUNCTION bump_content_change_revision()`,
		},
	},
	{
		version: 46,
		name:    "generation_notifications",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS user_notification_preferences (
				user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
				in_app_enabled BOOLEAN NOT NULL DEFAULT true,
				success_enabled BOOLEAN NOT NULL DEFAULT true,
				browser_enabled BOOLEAN NOT NULL DEFAULT false,
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE TABLE IF NOT EXISTS user_notifications (
				id BIGSERIAL PRIMARY KEY,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				generation_job_id BIGINT NOT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE,
				kind TEXT NOT NULL CHECK (kind IN ('generation_completed','generation_failed')),
				title TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
				message TEXT NOT NULL DEFAULT '' CHECK (char_length(message) <= 500),
				href TEXT NOT NULL CHECK (char_length(href) BETWEEN 1 AND 512),
				read_at TIMESTAMPTZ NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				UNIQUE(generation_job_id)
			)`,
			`CREATE INDEX IF NOT EXISTS user_notifications_user_created_idx ON user_notifications(user_id,created_at DESC,id DESC)`,
			`CREATE INDEX IF NOT EXISTS user_notifications_user_unread_idx ON user_notifications(user_id,created_at DESC,id DESC) WHERE read_at IS NULL`,
			`CREATE TABLE IF NOT EXISTS user_notification_revision (
				user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
				revision BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
				changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
		},
	},
	{
		version: 47,
		name:    "prompt_assistant_quality",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS prompt_assistant_runs (
				id BIGSERIAL PRIMARY KEY,
				content_event_id BIGINT NOT NULL UNIQUE REFERENCES content_events(id) ON DELETE CASCADE,
				user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
				correlation_id TEXT NOT NULL UNIQUE CHECK (char_length(correlation_id) BETWEEN 8 AND 128),
				mode TEXT NOT NULL CHECK (mode IN ('text-to-image','image-to-image','minimax-h3-video')),
				profile TEXT NOT NULL CHECK (char_length(profile) BETWEEN 1 AND 64),
				model TEXT NOT NULL CHECK (char_length(model) BETWEEN 1 AND 256),
				status TEXT NOT NULL CHECK (status IN ('completed','error','blocked')),
				decision TEXT NULL CHECK (decision IN ('applied','edited_after_apply','kept_original')),
				latency_ms BIGINT NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
				prompt_tokens INT NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
				completion_tokens INT NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
				total_duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (total_duration_ms >= 0),
				load_duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (load_duration_ms >= 0),
				eval_duration_ms BIGINT NOT NULL DEFAULT 0 CHECK (eval_duration_ms >= 0),
				num_predict INT NOT NULL CHECK (num_predict BETWEEN 1 AND 8192),
				timeout_ms BIGINT NOT NULL CHECK (timeout_ms BETWEEN 1000 AND 900000),
				keep_alive TEXT NOT NULL CHECK (char_length(keep_alive) BETWEEN 1 AND 32),
				reference_count INT NOT NULL DEFAULT 0 CHECK (reference_count BETWEEN 0 AND 6),
				error_code TEXT NOT NULL DEFAULT '' CHECK (char_length(error_code) <= 64),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				decided_at TIMESTAMPTZ NULL
			)`,
			`CREATE INDEX IF NOT EXISTS prompt_assistant_runs_model_created_idx ON prompt_assistant_runs(model,created_at DESC,id DESC)`,
			`CREATE INDEX IF NOT EXISTS prompt_assistant_runs_profile_created_idx ON prompt_assistant_runs(profile,created_at DESC,id DESC)`,
			`CREATE INDEX IF NOT EXISTS prompt_assistant_runs_decision_created_idx ON prompt_assistant_runs(decision,created_at DESC,id DESC) WHERE decision IS NOT NULL`,
		},
	},
	{
		version: 48,
		name:    "controlled_generation_batches",
		statements: []string{
			`CREATE TABLE IF NOT EXISTS generation_batches (
				id BIGSERIAL PRIMARY KEY,
				public_id TEXT NOT NULL UNIQUE CHECK (char_length(public_id) BETWEEN 16 AND 96 AND public_id ~ '^[A-Za-z0-9_-]+$'),
				user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
				username_snapshot TEXT NOT NULL DEFAULT '' CHECK (char_length(username_snapshot) <= 128),
				request_id TEXT NOT NULL CHECK (char_length(request_id) BETWEEN 16 AND 96 AND request_id ~ '^[A-Za-z0-9_-]+$'),
				parent_batch_id BIGINT NULL REFERENCES generation_batches(id) ON DELETE SET NULL,
				source_job_id BIGINT NULL REFERENCES generation_jobs(id) ON DELETE SET NULL,
				winner_job_id BIGINT NULL REFERENCES generation_jobs(id) ON DELETE SET NULL,
				template_id TEXT NOT NULL CHECK (char_length(template_id) BETWEEN 1 AND 256),
				workflow_id TEXT NOT NULL CHECK (char_length(workflow_id) BETWEEN 1 AND 256),
				model_name TEXT NOT NULL CHECK (char_length(model_name) BETWEEN 1 AND 512),
				experiment_mode TEXT NOT NULL CHECK (experiment_mode IN ('seeds','parameter')),
				parameter_name TEXT NOT NULL DEFAULT '' CHECK (char_length(parameter_name) <= 64),
				parameter_values JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(parameter_values)='array'),
				seed_locked BOOLEAN NOT NULL DEFAULT false,
				total_count INTEGER NOT NULL CHECK (total_count BETWEEN 2 AND 20),
				max_parallel INTEGER NOT NULL DEFAULT 1 CHECK (max_parallel BETWEEN 1 AND 4),
				cancellation_requested_at TIMESTAMPTZ NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				CHECK ((experiment_mode='seeds' AND parameter_name='') OR (experiment_mode='parameter' AND char_length(parameter_name) BETWEEN 1 AND 64))
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS generation_batches_user_request_idx ON generation_batches(user_id,request_id) WHERE user_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS generation_batches_user_created_idx ON generation_batches(user_id,created_at DESC,id DESC)`,
			`CREATE INDEX IF NOT EXISTS generation_batches_cancellation_idx ON generation_batches(cancellation_requested_at,id) WHERE cancellation_requested_at IS NOT NULL`,
			`ALTER TABLE generation_jobs ADD COLUMN IF NOT EXISTS batch_id BIGINT NULL REFERENCES generation_batches(id) ON DELETE SET NULL`,
			`ALTER TABLE generation_jobs ADD COLUMN IF NOT EXISTS batch_position INTEGER NOT NULL DEFAULT 0 CHECK (batch_position BETWEEN 0 AND 20)`,
			`ALTER TABLE generation_jobs ADD COLUMN IF NOT EXISTS experiment_value TEXT NOT NULL DEFAULT '' CHECK (char_length(experiment_value) <= 128)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS generation_jobs_batch_position_idx ON generation_jobs(batch_id,batch_position) WHERE batch_id IS NOT NULL`,
			`CREATE INDEX IF NOT EXISTS generation_jobs_batch_state_idx ON generation_jobs(batch_id,state,batch_position) WHERE batch_id IS NOT NULL`,
		},
	},
	{
		version: 49,
		name:    "feature_suggestion_review_workflow",
		statements: []string{
			`ALTER TABLE feature_suggestions ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'other' CHECK (kind IN ('lora','model','workflow','other'))`,
			`ALTER TABLE feature_suggestions ADD COLUMN IF NOT EXISTS json_size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (json_size_bytes >= 0 AND json_size_bytes <= 5242880)`,
			`UPDATE feature_suggestions SET json_size_bytes=GREATEST(octet_length(json_cipher)-28,0) WHERE json_name <> '' AND json_size_bytes=0`,
			`ALTER TABLE feature_suggestions ADD COLUMN IF NOT EXISTS scan_status TEXT NOT NULL DEFAULT 'none' CHECK (scan_status IN ('none','queued','scanning','clean','flagged','error'))`,
			`ALTER TABLE feature_suggestions ADD COLUMN IF NOT EXISTS review_comment_cipher BYTEA NOT NULL DEFAULT '\x'::bytea`,
			`ALTER TABLE feature_suggestions ADD COLUMN IF NOT EXISTS reviewed_by BIGINT NULL REFERENCES users(id) ON DELETE SET NULL`,
			`ALTER TABLE feature_suggestions ADD COLUMN IF NOT EXISTS reviewed_by_username TEXT NOT NULL DEFAULT '' CHECK (char_length(reviewed_by_username) <= 128)`,
			`ALTER TABLE feature_suggestions ADD COLUMN IF NOT EXISTS submitted_at TIMESTAMPTZ NULL`,
			`ALTER TABLE feature_suggestions ADD COLUMN IF NOT EXISTS reviewed_at TIMESTAMPTZ NULL`,
			`ALTER TABLE feature_suggestions DROP CONSTRAINT IF EXISTS feature_suggestions_status_check`,
			`UPDATE feature_suggestions SET
				scan_status=CASE status WHEN 'clean' THEN 'clean' WHEN 'flagged' THEN 'flagged' WHEN 'error' THEN 'error' ELSE 'scanning' END,
				status=CASE status WHEN 'scanning' THEN 'scanning' ELSE 'review' END,
				submitted_at=COALESCE(submitted_at,created_at)`,
			`ALTER TABLE feature_suggestions ALTER COLUMN status SET DEFAULT 'draft'`,
			`ALTER TABLE feature_suggestions ADD CONSTRAINT feature_suggestions_status_check CHECK (status IN ('draft','submitted','scanning','review','accepted','rejected'))`,
			`CREATE INDEX IF NOT EXISTS feature_suggestions_status_created_idx ON feature_suggestions(status,created_at DESC,id DESC)`,
			`CREATE INDEX IF NOT EXISTS feature_suggestions_user_created_idx ON feature_suggestions(user_id,created_at DESC,id DESC) WHERE user_id IS NOT NULL`,
			`ALTER TABLE feature_suggestion_scans ADD COLUMN IF NOT EXISTS source_index INTEGER NOT NULL DEFAULT 0`,
			`WITH ranked AS (
				SELECT id,(row_number() OVER (PARTITION BY suggestion_id,kind ORDER BY id)-1)::integer AS source_index
				FROM feature_suggestion_scans
			) UPDATE feature_suggestion_scans scan SET source_index=ranked.source_index FROM ranked WHERE scan.id=ranked.id`,
			`ALTER TABLE feature_suggestion_scans DROP CONSTRAINT IF EXISTS feature_suggestion_scans_source_index_check`,
			`ALTER TABLE feature_suggestion_scans ADD CONSTRAINT feature_suggestion_scans_source_index_check CHECK (source_index BETWEEN 0 AND 15)`,
			`ALTER TABLE feature_suggestion_scans ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 100)`,
			`ALTER TABLE feature_suggestion_scans ADD COLUMN IF NOT EXISTS lease_token TEXT NOT NULL DEFAULT '' CHECK (char_length(lease_token) <= 96)`,
			`ALTER TABLE feature_suggestion_scans ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ NULL`,
			`CREATE UNIQUE INDEX IF NOT EXISTS feature_suggestion_scans_source_idx ON feature_suggestion_scans(suggestion_id,kind,source_index)`,
			`DROP INDEX IF EXISTS feature_suggestion_scans_pending_idx`,
			`CREATE INDEX feature_suggestion_scans_pending_idx ON feature_suggestion_scans(status,lease_expires_at,created_at,id) WHERE status IN ('queued','in-progress')`,
		},
	},
	{
		version: 50,
		name:    "separate_generation_quotas_and_invite_controls",
		statements: []string{
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS video_generation_daily_limit INT NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS video_generation_total_limit BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS video_generation_total_used BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS can_use_advanced_generation_settings BOOLEAN NOT NULL DEFAULT TRUE`,
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS max_video_generation_quality INT NOT NULL DEFAULT 1440`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS video_generation_daily_limit INT NOT NULL DEFAULT 0`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS video_generation_total_limit BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS grant_advanced_generation_settings BOOLEAN NOT NULL DEFAULT TRUE`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS pause_mining_for_quick_generation BOOLEAN NOT NULL DEFAULT FALSE`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS max_video_generation_quality INT NOT NULL DEFAULT 1440`,
			`ALTER TABLE quick_generation_daily_usage ADD COLUMN IF NOT EXISTS video_used_count INT NOT NULL DEFAULT 0`,
			`UPDATE users SET video_generation_daily_limit=generation_daily_limit, video_generation_total_limit=generation_total_limit`,
			`UPDATE invites SET video_generation_daily_limit=generation_daily_limit, video_generation_total_limit=generation_total_limit`,
			`WITH video_usage AS (
				SELECT user_id, COUNT(*)::bigint AS used
				FROM generation_jobs
				WHERE user_id IS NOT NULL AND template_id='minimax-h3-video' AND quota_reserved_on IS NOT NULL
				GROUP BY user_id
			) UPDATE users u SET
				video_generation_total_used=LEAST(u.generation_total_used,video_usage.used),
				generation_total_used=GREATEST(u.generation_total_used-video_usage.used,0)
			FROM video_usage WHERE u.id=video_usage.user_id`,
			`WITH video_usage AS (
				SELECT user_id, timezone('Europe/Moscow',quota_reserved_on)::date AS usage_date, COUNT(*)::int AS used
				FROM generation_jobs
				WHERE user_id IS NOT NULL AND template_id='minimax-h3-video' AND quota_reserved_on IS NOT NULL
				GROUP BY user_id, timezone('Europe/Moscow',quota_reserved_on)::date
			) UPDATE quick_generation_daily_usage d SET
				video_used_count=LEAST(d.used_count,video_usage.used),
				used_count=GREATEST(d.used_count-video_usage.used,0)
			FROM video_usage WHERE d.user_id=video_usage.user_id AND d.usage_date=video_usage.usage_date`,
			`ALTER TABLE users DROP CONSTRAINT IF EXISTS users_video_generation_limits_valid`,
			`ALTER TABLE users ADD CONSTRAINT users_video_generation_limits_valid CHECK (video_generation_daily_limit >= 0 AND video_generation_total_limit >= 0 AND video_generation_total_used >= 0)`,
			`ALTER TABLE users DROP CONSTRAINT IF EXISTS users_max_video_generation_quality_valid`,
			`ALTER TABLE users ADD CONSTRAINT users_max_video_generation_quality_valid CHECK (max_video_generation_quality IN (480,720,1080,1440))`,
			`ALTER TABLE invites DROP CONSTRAINT IF EXISTS invites_video_generation_limits_valid`,
			`ALTER TABLE invites ADD CONSTRAINT invites_video_generation_limits_valid CHECK (video_generation_daily_limit >= 0 AND video_generation_total_limit >= 0)`,
			`ALTER TABLE invites DROP CONSTRAINT IF EXISTS invites_max_video_generation_quality_valid`,
			`ALTER TABLE invites ADD CONSTRAINT invites_max_video_generation_quality_valid CHECK (max_video_generation_quality IN (480,720,1080,1440))`,
			`ALTER TABLE quick_generation_daily_usage DROP CONSTRAINT IF EXISTS quick_generation_daily_usage_video_used_valid`,
			`ALTER TABLE quick_generation_daily_usage ADD CONSTRAINT quick_generation_daily_usage_video_used_valid CHECK (video_used_count >= 0)`,
		},
	},
	{
		version: 51,
		name:    "image_lora_training",
		statements: []string{
			`ALTER TABLE users ADD COLUMN IF NOT EXISTS can_train_image_lora BOOLEAN NOT NULL DEFAULT FALSE`,
			`ALTER TABLE invites ADD COLUMN IF NOT EXISTS grant_train_image_lora BOOLEAN NOT NULL DEFAULT FALSE`,
			`CREATE TABLE IF NOT EXISTS lora_training_jobs (
				id BIGSERIAL PRIMARY KEY,
				public_id TEXT NOT NULL UNIQUE CHECK (char_length(public_id) BETWEEN 16 AND 96 AND public_id ~ '^[A-Za-z0-9_-]+$'),
				user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
				username_snapshot TEXT NOT NULL DEFAULT '' CHECK (char_length(username_snapshot) <= 128),
				request_id TEXT NOT NULL CHECK (char_length(request_id) BETWEEN 16 AND 96 AND request_id ~ '^[A-Za-z0-9_-]+$'),
				profile_id TEXT NOT NULL CHECK (char_length(profile_id) BETWEEN 1 AND 96),
				family TEXT NOT NULL CHECK (family IN ('krea2','flux2-klein')),
				base_model TEXT NOT NULL CHECK (char_length(base_model) BETWEEN 1 AND 256),
				name TEXT NOT NULL CHECK (char_length(name) BETWEEN 3 AND 80),
				output_name TEXT NOT NULL CHECK (char_length(output_name) BETWEEN 3 AND 64 AND output_name ~ '^[A-Za-z0-9][A-Za-z0-9_-]+$'),
				trigger_word TEXT NOT NULL CHECK (char_length(trigger_word) BETWEEN 2 AND 80),
				concept_type TEXT NOT NULL CHECK (concept_type IN ('character','style','object','product')),
				preset TEXT NOT NULL CHECK (preset IN ('quick','balanced','detailed')),
				resolution INTEGER NOT NULL CHECK (resolution IN (512,768,1024)),
				max_train_steps INTEGER NOT NULL CHECK (max_train_steps BETWEEN 100 AND 5000),
				network_dim INTEGER NOT NULL CHECK (network_dim IN (8,16,32,64)),
				network_alpha INTEGER NOT NULL CHECK (network_alpha BETWEEN 1 AND 64),
				learning_rate DOUBLE PRECISION NOT NULL CHECK (learning_rate >= 0.000001 AND learning_rate <= 0.001),
				seed BIGINT NOT NULL CHECK (seed >= 0),
				sample_count INTEGER NOT NULL CHECK (sample_count BETWEEN 5 AND 100),
				dataset_bytes BIGINT NOT NULL CHECK (dataset_bytes BETWEEN 1 AND 536870912),
				dataset_path TEXT NOT NULL CHECK (char_length(dataset_path) <= 1024),
				state TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','uploading','preparing','caching','running','installing','completed','failed','cancelled')),
				stage TEXT NOT NULL DEFAULT 'В очереди' CHECK (char_length(stage) <= 120),
				progress INTEGER NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
				message TEXT NOT NULL DEFAULT '' CHECK (char_length(message) <= 1000),
				error_message TEXT NOT NULL DEFAULT '' CHECK (char_length(error_message) <= 2000),
				agent_job_id TEXT NOT NULL DEFAULT '' CHECK (char_length(agent_job_id) <= 96),
				artifact_name TEXT NOT NULL DEFAULT '' CHECK (char_length(artifact_name) <= 255),
				artifact_bytes BIGINT NOT NULL DEFAULT 0 CHECK (artifact_bytes >= 0),
				cancellation_requested_at TIMESTAMPTZ NULL,
				started_at TIMESTAMPTZ NULL,
				finished_at TIMESTAMPTZ NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS lora_training_jobs_user_active_idx ON lora_training_jobs(user_id) WHERE user_id IS NOT NULL AND state IN ('queued','uploading','preparing','caching','running','installing')`,
			`CREATE INDEX IF NOT EXISTS lora_training_jobs_user_created_idx ON lora_training_jobs(user_id,created_at DESC,id DESC)`,
			`CREATE INDEX IF NOT EXISTS lora_training_jobs_dispatch_idx ON lora_training_jobs(state,created_at,id)`,
			`ALTER TABLE quick_generation_mining_leases ADD COLUMN IF NOT EXISTS lora_training_job_id BIGINT NULL REFERENCES lora_training_jobs(id) ON DELETE SET NULL`,
			`CREATE INDEX IF NOT EXISTS quick_generation_mining_leases_lora_training_idx ON quick_generation_mining_leases(lora_training_job_id) WHERE lora_training_job_id IS NOT NULL`,
		},
	},
	{
		version: 52,
		name:    "failed_lora_training_retention",
		statements: []string{
			`CREATE INDEX IF NOT EXISTS lora_training_jobs_failed_cleanup_idx ON lora_training_jobs((COALESCE(finished_at,updated_at)),id) WHERE state='failed'`,
		},
	},
	{
		version: 53,
		name:    "generation_drafts",
		statements: []string{
			`CREATE SEQUENCE generation_draft_revision_seq`,
			`CREATE TABLE generation_drafts (
				user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
				revision BIGINT NOT NULL DEFAULT nextval('generation_draft_revision_seq'),
				payload_cipher BYTEA NOT NULL CHECK (octet_length(payload_cipher) BETWEEN 1 AND 262144),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '30 days'
			)`,
			`CREATE INDEX generation_drafts_expiry_idx ON generation_drafts(expires_at)`,
			`CREATE INDEX comfy_input_assets_owner_path_idx ON comfy_input_assets(user_id,subfolder,filename) WHERE state='stored'`,
		},
	},
	{
		version: 54,
		name:    "lora_datasets",
		statements: []string{
			`CREATE SEQUENCE lora_dataset_revision_seq`,
			`CREATE TABLE lora_datasets (
				id TEXT PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				name TEXT NOT NULL DEFAULT '', revision BIGINT NOT NULL DEFAULT nextval('lora_dataset_revision_seq'),
				manifest_cipher BYTEA NOT NULL CHECK(octet_length(manifest_cipher) BETWEEN 1 AND 524352),
				image_count INTEGER NOT NULL DEFAULT 0 CHECK(image_count BETWEEN 0 AND 100),
				size_bytes BIGINT NOT NULL DEFAULT 0 CHECK(size_bytes BETWEEN 0 AND 536870912),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				expires_at TIMESTAMPTZ NOT NULL DEFAULT now()+interval '30 days'
			)`,
			`CREATE INDEX lora_datasets_owner_idx ON lora_datasets(user_id,updated_at DESC)`,
			`CREATE TABLE lora_dataset_assets (
				id TEXT PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				name TEXT NOT NULL, content_hash TEXT NOT NULL CHECK(length(content_hash)=64), mime_type TEXT NOT NULL,
				size_bytes BIGINT NOT NULL CHECK(size_bytes BETWEEN 1 AND 25165824),
				width INTEGER NOT NULL CHECK(width BETWEEN 256 AND 16384), height INTEGER NOT NULL CHECK(height BETWEEN 256 AND 16384),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_used_at TIMESTAMPTZ NOT NULL DEFAULT now(), UNIQUE(user_id,content_hash)
			)`,
			`CREATE TABLE lora_dataset_asset_chunks (
				asset_id TEXT NOT NULL REFERENCES lora_dataset_assets(id) ON DELETE CASCADE,
				chunk_index INTEGER NOT NULL CHECK(chunk_index>=0), plain_size INTEGER NOT NULL CHECK(plain_size BETWEEN 1 AND 1048576),
				payload_cipher BYTEA NOT NULL CHECK(octet_length(payload_cipher) BETWEEN 1 AND 1048640), PRIMARY KEY(asset_id,chunk_index)
			)`,
			`CREATE TABLE lora_dataset_snapshots (
				id TEXT PRIMARY KEY, dataset_id TEXT REFERENCES lora_datasets(id) ON DELETE SET NULL,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE, name TEXT NOT NULL, revision BIGINT NOT NULL,
				manifest_cipher BYTEA NOT NULL CHECK(octet_length(manifest_cipher) BETWEEN 1 AND 524352),
				manifest_hash TEXT NOT NULL CHECK(length(manifest_hash)=64), image_count INTEGER NOT NULL, size_bytes BIGINT NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ NOT NULL DEFAULT now()+interval '30 days',
				UNIQUE(dataset_id,revision)
			)`,
			`CREATE INDEX lora_dataset_snapshots_owner_idx ON lora_dataset_snapshots(user_id,created_at DESC)`,
			`CREATE TABLE lora_dataset_asset_refs (
				asset_id TEXT NOT NULL REFERENCES lora_dataset_assets(id) ON DELETE CASCADE,
				dataset_id TEXT REFERENCES lora_datasets(id) ON DELETE CASCADE,
				snapshot_id TEXT REFERENCES lora_dataset_snapshots(id) ON DELETE CASCADE,
				CHECK(num_nonnulls(dataset_id,snapshot_id)=1), UNIQUE(dataset_id,asset_id), UNIQUE(snapshot_id,asset_id)
			)`,
			`CREATE INDEX lora_dataset_asset_refs_asset_idx ON lora_dataset_asset_refs(asset_id)`,
			`ALTER TABLE lora_training_jobs ADD COLUMN dataset_snapshot_id TEXT REFERENCES lora_dataset_snapshots(id) ON DELETE SET NULL`,
			`ALTER TABLE lora_training_jobs ADD COLUMN dataset_snapshot_hash TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX lora_training_jobs_snapshot_idx ON lora_training_jobs(dataset_snapshot_id) WHERE dataset_snapshot_id IS NOT NULL`,
		},
	},
	{
		version: 55, name: "durable_lora_caption_jobs", statements: []string{
			`CREATE TABLE lora_caption_jobs (
				id TEXT PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				dataset_id TEXT REFERENCES lora_datasets(id) ON DELETE CASCADE, image_id TEXT NOT NULL DEFAULT '',
				asset_id TEXT NOT NULL REFERENCES lora_dataset_assets(id) ON DELETE CASCADE,
				request_key TEXT NOT NULL CHECK(length(request_key)=64), instruction_version TEXT NOT NULL,
				input_cipher BYTEA NOT NULL CHECK(octet_length(input_cipher) BETWEEN 1 AND 65536), result_cipher BYTEA NOT NULL DEFAULT '',
				state TEXT NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','completed','failed','cancelled')),
				status TEXT NOT NULL DEFAULT 'Описание поставлено в очередь', error TEXT NOT NULL DEFAULT '',
				attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts>=0), run_token TEXT NOT NULL DEFAULT '',
				cancel_requested BOOLEAN NOT NULL DEFAULT false, available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				expires_at TIMESTAMPTZ NOT NULL DEFAULT now()+interval '30 days', UNIQUE(user_id,request_key)
			)`,
			`CREATE UNIQUE INDEX lora_caption_one_active_image_idx ON lora_caption_jobs(dataset_id,image_id) WHERE dataset_id IS NOT NULL AND state IN ('queued','running')`,
			`CREATE INDEX lora_caption_queue_idx ON lora_caption_jobs(available_at,created_at) WHERE state='queued'`,
			`CREATE INDEX lora_caption_dataset_idx ON lora_caption_jobs(user_id,dataset_id,created_at)`,
			`ALTER TABLE lora_dataset_asset_refs ADD COLUMN caption_job_id TEXT REFERENCES lora_caption_jobs(id) ON DELETE CASCADE`,
			`ALTER TABLE lora_dataset_asset_refs DROP CONSTRAINT lora_dataset_asset_refs_check`,
			`ALTER TABLE lora_dataset_asset_refs ADD CONSTRAINT lora_dataset_asset_refs_check CHECK(num_nonnulls(dataset_id,snapshot_id,caption_job_id)=1)`,
			`CREATE UNIQUE INDEX lora_dataset_caption_asset_idx ON lora_dataset_asset_refs(caption_job_id,asset_id) WHERE caption_job_id IS NOT NULL`,
		},
	},
	{
		version: 56, name: "gpu_admission_and_evidence", statements: []string{
			`CREATE TABLE gpu_resources (
				id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 1 AND 64), revision BIGINT NOT NULL DEFAULT 1,
				observation TEXT NOT NULL DEFAULT 'unknown' CHECK(observation IN ('idle','busy','unknown')),
				message TEXT NOT NULL DEFAULT '' CHECK(length(message)<=1000),
				observed_at TIMESTAMPTZ NOT NULL DEFAULT now(), valid_until TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE TABLE gpu_work (
				id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 1 AND 96), resource_id TEXT NOT NULL REFERENCES gpu_resources(id),
				kind TEXT NOT NULL CHECK(kind IN ('generation','lora_training','lora_caption','prompt_assistant')),
				job_key TEXT NOT NULL CHECK(length(job_key) BETWEEN 1 AND 96), user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
				priority BOOLEAN NOT NULL DEFAULT false, state TEXT NOT NULL DEFAULT 'waiting' CHECK(state IN ('waiting','held','uncertain','released','cancelled')),
				phase TEXT NOT NULL DEFAULT 'reserved' CHECK(phase IN ('reserved','dispatching','running')),
				lease_token TEXT NOT NULL DEFAULT '', lease_until TIMESTAMPTZ, external_id TEXT NOT NULL DEFAULT '' CHECK(length(external_id)<=200),
				cancellation_requested BOOLEAN NOT NULL DEFAULT false, ready_until TIMESTAMPTZ NOT NULL DEFAULT now()+interval '5 minutes',
				queued_at TIMESTAMPTZ NOT NULL DEFAULT now(), created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
				held_at TIMESTAMPTZ, released_at TIMESTAMPTZ, UNIQUE(kind,job_key),
				CHECK((state IN ('held','uncertain') AND lease_token<>'' AND lease_until IS NOT NULL) OR (state NOT IN ('held','uncertain') AND lease_token='' AND lease_until IS NULL))
			)`,
			`CREATE UNIQUE INDEX gpu_work_one_holder_idx ON gpu_work(resource_id) WHERE state IN ('held','uncertain')`,
			`CREATE INDEX gpu_work_queue_idx ON gpu_work(resource_id,queued_at,id) WHERE state='waiting'`,
			`CREATE INDEX gpu_work_owner_idx ON gpu_work(user_id,created_at)`,
			`CREATE TABLE gpu_work_events (
				id BIGSERIAL PRIMARY KEY, work_id TEXT NOT NULL REFERENCES gpu_work(id) ON DELETE CASCADE,
				state TEXT NOT NULL, code TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '' CHECK(length(detail)<=1000), created_at TIMESTAMPTZ NOT NULL DEFAULT now()
			)`,
			`CREATE INDEX gpu_work_events_work_idx ON gpu_work_events(work_id,id)`,
		},
	},
	{
		version: 57, name: "lora_training_durable_handoff", statements: []string{
			`ALTER TABLE lora_training_jobs ADD COLUMN agent_submission_started_at TIMESTAMPTZ`,
			`UPDATE lora_training_jobs SET agent_submission_started_at=updated_at WHERE state='uploading' AND agent_job_id=''`,
		},
	},
	{
		version: 58, name: "independent_queue_priority", statements: []string{
			`ALTER TABLE users ADD COLUMN queue_priority BOOLEAN NOT NULL DEFAULT FALSE`,
			`ALTER TABLE invites ADD COLUMN queue_priority BOOLEAN NOT NULL DEFAULT FALSE`,
			`UPDATE users SET queue_priority=pause_mining_for_quick_generation`,
			`UPDATE invites SET queue_priority=pause_mining_for_quick_generation`,
		},
	},
	{
		version: 59, name: "mining_resume_completion_evidence", statements: []string{
			`ALTER TABLE quick_generation_mining_leases ADD COLUMN resume_ready BOOLEAN NOT NULL DEFAULT FALSE`,
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
