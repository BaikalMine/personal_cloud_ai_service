package database

var comfyStorageOperationsMigration = migration{
	version: 90, name: "comfy_storage_operations", statements: []string{
		`CREATE TABLE comfy_storage_operations (
		 id text PRIMARY KEY CHECK(id ~ '^[A-Za-z0-9_-]{32,96}$'),
		 resource_id text NOT NULL REFERENCES gpu_resources(id),
		 actor_id bigint REFERENCES users(id) ON DELETE SET NULL,
		 actor_name text NOT NULL,
		 host_key text NOT NULL CHECK(host_key ~ '^[0-9a-f]{64}$'),
		 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
		 request jsonb,
		 selection jsonb,
		 result jsonb,
		 state text NOT NULL DEFAULT 'preview' CHECK(state IN ('preview','dispatching','unconfirmed','completed','partial','rejected','closed_unconfirmed','cancelled','expired')),
		 item_count integer NOT NULL CHECK(item_count BETWEEN 1 AND 50),
		 expected_bytes bigint NOT NULL CHECK(expected_bytes>=0),
		 freed_bytes bigint NOT NULL DEFAULT 0 CHECK(freed_bytes>=0 AND freed_bytes<=expected_bytes),
		 deleted_files integer NOT NULL DEFAULT 0 CHECK(deleted_files>=0 AND deleted_files<=item_count),
		 last_code text NOT NULL DEFAULT '' CHECK(length(last_code)<=80),
		 created_at timestamptz NOT NULL DEFAULT now(),
		 expires_at timestamptz NOT NULL DEFAULT (now()+interval '15 minutes'),
		 dispatched_at timestamptz,
		 finished_at timestamptz,
		 checked_at timestamptz,
		 pruned_at timestamptz,
		 CHECK((state IN ('preview','dispatching','unconfirmed'))=(finished_at IS NULL)),
		 CHECK((pruned_at IS NULL AND request IS NOT NULL AND selection IS NOT NULL) OR
		 (pruned_at IS NOT NULL AND finished_at IS NOT NULL AND request IS NULL AND selection IS NULL AND result IS NULL)),
		 CHECK(request IS NULL OR (jsonb_typeof(request)='object' AND octet_length(request::text)<=65536)),
		 CHECK(selection IS NULL OR (jsonb_typeof(selection)='array' AND jsonb_array_length(selection)=item_count AND octet_length(selection::text)<=65536)),
		 CHECK(result IS NULL OR (jsonb_typeof(result)='object' AND octet_length(result::text)<=1048576)),
		 CHECK(state NOT IN ('dispatching','unconfirmed') OR dispatched_at IS NOT NULL),
		 CHECK(state NOT IN ('preview','dispatching','unconfirmed','cancelled','expired') OR (freed_bytes=0 AND deleted_files=0))
		)`,
		`CREATE UNIQUE INDEX comfy_storage_active_resource_idx ON comfy_storage_operations(resource_id) WHERE state IN ('dispatching','unconfirmed')`,
		`CREATE INDEX comfy_storage_operations_history_idx ON comfy_storage_operations(created_at DESC,id)`,
		`CREATE INDEX comfy_storage_operations_retention_idx ON comfy_storage_operations(finished_at) WHERE finished_at IS NOT NULL AND pruned_at IS NULL`,
	},
}
