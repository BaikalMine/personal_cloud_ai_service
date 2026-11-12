package database

var comfyCleanupReceiptsMigration = migration{
	version: 84, name: "comfy_output_cleanup_receipts", statements: []string{
		`ALTER TABLE comfy_output_cleanup_tombstones
		 ADD COLUMN eligible_at TIMESTAMPTZ,
		 ADD COLUMN cleanup_reason TEXT NOT NULL DEFAULT 'retention' CHECK (cleanup_reason IN ('retention','profile_hidden')),
		 ADD COLUMN last_outcome TEXT NOT NULL DEFAULT 'pending' CHECK (last_outcome IN ('pending','legacy_retry','mismatched','rejected','host_unavailable','unconfirmed')),
		 ADD COLUMN last_attempt_at TIMESTAMPTZ`,
		`UPDATE comfy_output_cleanup_tombstones SET eligible_at=next_attempt_at,
		 last_outcome=CASE WHEN attempt_count>0 THEN 'legacy_retry' ELSE 'pending' END`,
		`ALTER TABLE comfy_output_cleanup_tombstones ALTER COLUMN eligible_at SET NOT NULL,
		 ALTER COLUMN eligible_at SET DEFAULT now()`,
		`CREATE TABLE comfy_output_cleanup_receipts (
		 tombstone_id BIGINT PRIMARY KEY CHECK (tombstone_id>0),
		 filename TEXT NOT NULL,
		 subfolder TEXT NOT NULL,
		 storage_type TEXT NOT NULL CHECK (storage_type='output'),
		 size_bytes BIGINT NOT NULL CHECK (size_bytes>=0 AND size_bytes<=2147483648),
		 content_hash TEXT NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
		 outcome TEXT NOT NULL CHECK (outcome IN ('deleted','missing','legacy_confirmed')),
		 cleanup_reason TEXT NOT NULL CHECK (cleanup_reason IN ('retention','profile_hidden')),
		 freed_bytes BIGINT NOT NULL CHECK (freed_bytes>=0),
		 attempt_count INTEGER NOT NULL CHECK (attempt_count>0),
		 queued_at TIMESTAMPTZ NOT NULL,
		 eligible_at TIMESTAMPTZ NOT NULL,
		 confirmed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		 CHECK ((outcome='deleted' AND freed_bytes=size_bytes) OR (outcome<>'deleted' AND freed_bytes=0))
		)`,
		`CREATE INDEX comfy_output_cleanup_receipts_time_idx ON comfy_output_cleanup_receipts(confirmed_at,tombstone_id)`,
	},
}
