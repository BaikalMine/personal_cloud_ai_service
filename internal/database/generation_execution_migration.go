package database

var generationExecutionMigration = migration{
	version: 76, name: "generation_execution_evidence", statements: []string{
		`CREATE TABLE generation_job_executions (
		 job_id BIGINT PRIMARY KEY REFERENCES generation_jobs(id) ON DELETE CASCADE,
		 payload_cipher BYTEA NOT NULL CHECK(octet_length(payload_cipher) BETWEEN 1 AND 2097152),
		 payload_sha256 TEXT NOT NULL CHECK(payload_sha256 ~ '^[a-f0-9]{64}$'),
		 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
		)`,
	},
}
