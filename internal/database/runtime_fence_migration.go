package database

var runtimeFenceMigration = migration{
	version: 93, name: "release_runtime_fence", statements: []string{
		`CREATE TABLE release_runtime_control (
		 singleton SMALLINT PRIMARY KEY CHECK(singleton=1),
		 protocol_version INTEGER NOT NULL DEFAULT 1 CHECK(protocol_version=1),
		 epoch BIGINT NOT NULL DEFAULT 0 CHECK(epoch>=0),
		 active BOOLEAN NOT NULL DEFAULT false,
		 operation_id TEXT NOT NULL DEFAULT '',
		 token_hash TEXT NOT NULL DEFAULT '',
		 previous_manifest_hash TEXT NOT NULL DEFAULT '',
		 candidate_manifest_hash TEXT NOT NULL DEFAULT '',
		 requested_at TIMESTAMPTZ,
		 finished_at TIMESTAMPTZ,
		 CHECK((epoch=0 AND NOT active AND operation_id='' AND token_hash=''
		  AND previous_manifest_hash='' AND candidate_manifest_hash=''
		  AND requested_at IS NULL AND finished_at IS NULL) OR
		 (epoch>0 AND operation_id ~ '^[0-9a-f]{32}$' AND token_hash ~ '^[0-9a-f]{64}$'
		  AND previous_manifest_hash ~ '^[0-9a-f]{64}$' AND candidate_manifest_hash ~ '^[0-9a-f]{64}$'
		  AND requested_at IS NOT NULL AND ((active AND finished_at IS NULL) OR
		  (NOT active AND finished_at IS NOT NULL AND finished_at>=requested_at))))
		)`,
		`INSERT INTO release_runtime_control(singleton) VALUES(1)`,
	},
}
