package database

var generationSubmissionMigration = migration{
	version: 92, name: "generation_submission_receipts", statements: []string{
		`CREATE TABLE generation_submissions (
		 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		 kind TEXT NOT NULL CHECK(kind IN ('single','batch')),
		 request_id TEXT NOT NULL CHECK(char_length(request_id) BETWEEN 1 AND 128),
		 public_id TEXT NOT NULL DEFAULT '' CHECK(public_id='' OR (char_length(public_id) BETWEEN 16 AND 96 AND public_id ~ '^[A-Za-z0-9_-]+$')),
		 closed BOOLEAN NOT NULL DEFAULT false,
		 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
		 CHECK(NOT closed OR public_id=''),
		 PRIMARY KEY(user_id,kind,request_id)
		)`,
		`INSERT INTO generation_submissions(user_id,kind,request_id,public_id,created_at)
		 SELECT user_id,'single',request_id,public_id,created_at FROM generation_jobs WHERE user_id IS NOT NULL`,
		`INSERT INTO generation_submissions(user_id,kind,request_id,public_id,created_at)
		 SELECT user_id,'batch',request_id,public_id,created_at FROM generation_batches WHERE user_id IS NOT NULL`,
	},
}
