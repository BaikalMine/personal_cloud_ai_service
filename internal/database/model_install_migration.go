package database

var modelInstallMigration = migration{version: 96, name: "model_install_jobs", statements: []string{
	`CREATE TABLE model_install_requests (id TEXT PRIMARY KEY CHECK(id ~ '^[a-f0-9]{32}$'), owner_id BIGINT REFERENCES users(id) ON DELETE SET NULL, closed BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	`CREATE TABLE model_install_jobs (
	 id TEXT PRIMARY KEY CHECK(id ~ '^[a-f0-9]{32}$'), owner_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
	 model_id BIGINT NOT NULL CHECK(model_id>0), version_id BIGINT NOT NULL CHECK(version_id>0),
	 kind TEXT NOT NULL CHECK(kind IN ('loras','checkpoints')), name TEXT NOT NULL, filename TEXT NOT NULL,
	 sha256 TEXT NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'), size_bytes BIGINT NOT NULL CHECK(size_bytes>0), request JSONB NOT NULL,
	 state TEXT NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','reconciling','completed','failed','cancelled')),
	 progress DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK(progress>=0 AND progress<=100), message TEXT NOT NULL DEFAULT '',
	 verification_after TIMESTAMPTZ NOT NULL DEFAULT now(),
	 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	`CREATE UNIQUE INDEX model_install_active_version ON model_install_jobs(version_id) WHERE state IN ('queued','running','reconciling')`,
}}
