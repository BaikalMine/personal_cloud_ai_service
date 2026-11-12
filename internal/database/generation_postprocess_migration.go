package database

var generationPostprocessMigration = migration{
	version: 80, name: "generation_postprocess_receipts", statements: []string{
		`CREATE TABLE generation_postprocess_receipts (
		 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		 request_id TEXT NOT NULL CHECK(length(request_id) BETWEEN 16 AND 96),
		 state TEXT NOT NULL CHECK(state IN ('quoted','accepted','cancelled')),
		 version INTEGER NOT NULL DEFAULT 1 CHECK(version=1),
		 confirmation_sha256 TEXT NOT NULL DEFAULT '',
		 payload_cipher BYTEA NOT NULL DEFAULT ''::bytea,
		 sources JSONB NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(sources)='array'),
		 sensitive BOOLEAN NOT NULL DEFAULT false,
		 job_id BIGINT UNIQUE REFERENCES generation_jobs(id) ON DELETE SET NULL,
		 job_public_id TEXT NOT NULL DEFAULT '',
		 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
		 expires_at TIMESTAMPTZ NOT NULL DEFAULT (clock_timestamp()+interval '10 minutes'),
		 CHECK ((state='accepted' AND length(job_public_id) BETWEEN 16 AND 96) OR (state<>'accepted' AND job_id IS NULL AND job_public_id='')),
		 CHECK ((state='cancelled' AND confirmation_sha256='' AND octet_length(payload_cipher)=0 AND sources='[]'::jsonb) OR
		 (state<>'cancelled' AND confirmation_sha256 ~ '^[a-f0-9]{64}$' AND octet_length(payload_cipher) BETWEEN 1 AND 4194304 AND jsonb_array_length(sources) BETWEEN 1 AND 2)),
		 PRIMARY KEY(user_id,request_id)
		)`,
	},
}
