package database

var inferenceProgressMigration = migration{version: 97, name: "encrypted_assistant_inference_progress", statements: []string{
	`CREATE TABLE gpu_inference_progress (
	 id TEXT PRIMARY KEY REFERENCES gpu_inference_requests(id) ON DELETE CASCADE,
	 run_token TEXT NOT NULL, cipher BYTEA NOT NULL CHECK(octet_length(cipher) BETWEEN 1 AND 65600),
	 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ NOT NULL)`,
	`CREATE INDEX gpu_inference_progress_expiry_idx ON gpu_inference_progress(expires_at)`,
}}
