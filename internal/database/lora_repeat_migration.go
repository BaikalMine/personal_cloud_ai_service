package database

var loraRepeatMigration = migration{
	version: 75, name: "durable_exact_lora_training_repeats", statements: []string{
		`CREATE TABLE lora_training_repeats (
   user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
   request_id TEXT NOT NULL CHECK(request_id ~ '^[A-Za-z0-9_-]{16,96}$'),
   request_sha256 TEXT NOT NULL CHECK(request_sha256 ~ '^[a-f0-9]{64}$'),
   source_job_id BIGINT REFERENCES lora_training_jobs(id) ON DELETE SET NULL,
   source_public_id TEXT NOT NULL CHECK(source_public_id ~ '^[A-Za-z0-9_-]{16,96}$'),
   source_name TEXT NOT NULL,
   source_sha256 TEXT NOT NULL CHECK(source_sha256 ~ '^[a-f0-9]{64}$'),
   job_id BIGINT UNIQUE REFERENCES lora_training_jobs(id) ON DELETE SET NULL,
   job_public_id TEXT NOT NULL UNIQUE CHECK(job_public_id ~ '^[A-Za-z0-9_-]{16,96}$'),
   dataset_snapshot_id TEXT NOT NULL CHECK(dataset_snapshot_id ~ '^[A-Za-z0-9_-]{16,96}$'),
   dataset_snapshot_hash TEXT NOT NULL CHECK(dataset_snapshot_hash ~ '^[a-f0-9]{64}$'),
   execution_constraint JSONB NOT NULL CHECK(jsonb_typeof(execution_constraint)='object' AND octet_length(execution_constraint::text)<=16384),
   created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
   PRIMARY KEY(user_id, request_id),
   CHECK(source_job_id IS NULL OR job_id IS NULL OR source_job_id<>job_id)
  )`,
		`CREATE INDEX lora_training_repeats_owner_source_idx ON lora_training_repeats(user_id,source_public_id,created_at DESC)`,
	},
}
