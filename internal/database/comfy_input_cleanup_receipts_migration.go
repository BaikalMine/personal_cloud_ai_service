package database

var comfyInputCleanupReceiptsMigration = migration{
	version: 85, name: "comfy_input_cleanup_receipts", statements: []string{
		`ALTER TABLE comfy_input_assets
		 ADD COLUMN cleanup_last_outcome TEXT NOT NULL DEFAULT 'pending' CHECK (cleanup_last_outcome IN ('pending','legacy_retry','mismatched','rejected','host_unavailable','unconfirmed')),
		 ADD COLUMN cleanup_last_attempt_at TIMESTAMPTZ`,
		`UPDATE comfy_input_assets SET cleanup_last_outcome='legacy_retry' WHERE cleanup_attempts>0`,
		`CREATE TABLE comfy_input_cleanup_receipts (
		 input_id TEXT PRIMARY KEY CHECK (char_length(input_id) BETWEEN 32 AND 128),
		 filename TEXT NOT NULL,
		 subfolder TEXT NOT NULL,
		 storage_type TEXT NOT NULL CHECK (storage_type='input'),
		 size_bytes BIGINT NOT NULL CHECK (size_bytes>=0 AND size_bytes<=335544320),
		 content_hash TEXT NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
		 state TEXT NOT NULL CHECK (state IN ('reserved','stored')),
		 observed_size_bytes BIGINT NOT NULL CHECK (observed_size_bytes>=0 AND observed_size_bytes<=2147483648),
		 observed_hash TEXT NOT NULL CHECK (observed_hash ~ '^[0-9a-f]{64}$'),
		 outcome TEXT NOT NULL CHECK (outcome IN ('deleted','missing','legacy_confirmed')),
		 freed_bytes BIGINT NOT NULL CHECK (freed_bytes>=0),
		 attempt_count INTEGER NOT NULL CHECK (attempt_count>0),
		 queued_at TIMESTAMPTZ NOT NULL,
		 eligible_at TIMESTAMPTZ NOT NULL,
		 confirmed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		 CHECK ((outcome='deleted' AND freed_bytes=observed_size_bytes) OR (outcome<>'deleted' AND freed_bytes=0)),
		 CHECK (state='reserved' OR (size_bytes=observed_size_bytes AND content_hash=observed_hash))
		)`,
		`CREATE INDEX comfy_input_cleanup_receipts_time_idx ON comfy_input_cleanup_receipts(confirmed_at,input_id)`,
	},
}
