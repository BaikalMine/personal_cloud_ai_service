package database

var generationRepeatMigration = migration{
	version: 77, name: "checked_generation_repeats", statements: []string{
		`CREATE TABLE generation_repeats (
		 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		 request_id TEXT NOT NULL CHECK(length(request_id) BETWEEN 16 AND 96),
		 source_public_id TEXT NOT NULL,
		 source_execution_sha256 TEXT NOT NULL CHECK(source_execution_sha256='' OR source_execution_sha256 ~ '^[a-f0-9]{64}$'),
		 confirmation_sha256 TEXT NOT NULL CHECK(confirmation_sha256 ~ '^[a-f0-9]{64}$'),
		 job_id BIGINT UNIQUE REFERENCES generation_jobs(id) ON DELETE SET NULL,
		 job_public_id TEXT NOT NULL,
		 cancelled BOOLEAN NOT NULL DEFAULT false,
		 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
		 CHECK ((cancelled AND job_id IS NULL AND job_public_id='' AND source_execution_sha256='') OR (NOT cancelled AND job_public_id<>'')),
		 PRIMARY KEY(user_id,request_id)
		)`,
	},
}
