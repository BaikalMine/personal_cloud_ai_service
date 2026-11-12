package database

var galleryDatasetImportMigration = migration{version: 79, name: "gallery_dataset_import_receipts", statements: []string{
	`CREATE TABLE gallery_dataset_imports (
		user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		request_id TEXT NOT NULL CHECK(length(request_id) BETWEEN 16 AND 96),
		request_sha256 TEXT NOT NULL CHECK(request_sha256='' OR request_sha256 ~ '^[a-f0-9]{64}$'),
		media_id BIGINT NOT NULL CHECK(media_id>0),
		dataset_id TEXT NOT NULL,
		image_id TEXT NOT NULL,
		revision BIGINT NOT NULL,
		already_present BOOLEAN NOT NULL DEFAULT FALSE,
		cancelled BOOLEAN NOT NULL DEFAULT FALSE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY(user_id,request_id)
	)`,
}}
