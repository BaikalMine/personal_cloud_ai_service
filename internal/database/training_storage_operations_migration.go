package database

var trainingStorageOperationsMigration = migration{
	version: 86, name: "training_storage_operations", statements: []string{
		`CREATE TABLE training_storage_operations (
		 id TEXT PRIMARY KEY CHECK(id ~ '^[A-Za-z0-9_-]{32,96}$'),
		 resource_id TEXT NOT NULL REFERENCES gpu_resources(id),
		 actor_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
		 actor_name TEXT NOT NULL,
		 host_key TEXT NOT NULL CHECK(host_key ~ '^[0-9a-f]{64}$'),
		 request_hash TEXT NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
		 request JSONB,
		 preview JSONB,
		 result JSONB,
		 state TEXT NOT NULL DEFAULT 'preview' CHECK(state IN ('preview','dispatching','unconfirmed','completed','partial','rejected','closed_unconfirmed','cancelled','expired')),
		 item_count INTEGER NOT NULL CHECK(item_count BETWEEN 1 AND 50),
		 expected_bytes BIGINT NOT NULL CHECK(expected_bytes>=0),
		 freed_bytes BIGINT NOT NULL DEFAULT 0 CHECK(freed_bytes>=0 AND freed_bytes<=expected_bytes),
		 deleted_files BIGINT NOT NULL DEFAULT 0 CHECK(deleted_files>=0),
		 last_code TEXT NOT NULL DEFAULT '' CHECK(char_length(last_code)<=80),
		 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		 expires_at TIMESTAMPTZ NOT NULL DEFAULT (now()+interval '15 minutes'),
		 dispatched_at TIMESTAMPTZ,
		 finished_at TIMESTAMPTZ,
		 checked_at TIMESTAMPTZ,
		 pruned_at TIMESTAMPTZ,
		 CHECK((state IN ('preview','dispatching','unconfirmed'))=(finished_at IS NULL)),
		 CHECK((pruned_at IS NULL AND request IS NOT NULL AND preview IS NOT NULL) OR
		       (pruned_at IS NOT NULL AND finished_at IS NOT NULL AND request IS NULL AND preview IS NULL AND result IS NULL)),
		 CHECK(request IS NULL OR (jsonb_typeof(request)='object' AND octet_length(request::text)<=65536)),
		 CHECK(preview IS NULL OR (jsonb_typeof(preview)='object' AND octet_length(preview::text)<=262144)),
		 CHECK(result IS NULL OR (jsonb_typeof(result)='object' AND octet_length(result::text)<=1048576)),
		 CHECK(state NOT IN ('dispatching','unconfirmed') OR dispatched_at IS NOT NULL),
		 CHECK(state NOT IN ('preview','dispatching','unconfirmed','cancelled','expired') OR (freed_bytes=0 AND deleted_files=0))
		)`,
		`CREATE UNIQUE INDEX training_storage_active_resource_idx ON training_storage_operations(resource_id) WHERE state IN ('dispatching','unconfirmed')`,
		`CREATE INDEX training_storage_operations_history_idx ON training_storage_operations(created_at DESC,id)`,
		`CREATE INDEX training_storage_operations_retention_idx ON training_storage_operations(finished_at) WHERE pruned_at IS NULL AND finished_at IS NOT NULL`,
	},
}
