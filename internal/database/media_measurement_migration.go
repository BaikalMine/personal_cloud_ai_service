package database

var mediaMeasurementMigration = migration{
	version: 78, name: "completed_media_measurements", statements: []string{
		`CREATE TABLE content_media_measurements (
		 media_id BIGINT PRIMARY KEY REFERENCES content_media(id) ON DELETE CASCADE,
		 content_sha256 TEXT NOT NULL CHECK(content_sha256 ~ '^[a-f0-9]{64}$'),
		 size_bytes BIGINT NOT NULL CHECK(size_bytes>0),
		 payload_cipher BYTEA NOT NULL CHECK(octet_length(payload_cipher) BETWEEN 1 AND 16384),
		 recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
		)`,
	},
}
