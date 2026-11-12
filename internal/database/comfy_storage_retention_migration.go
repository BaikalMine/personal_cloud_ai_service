package database

var comfyStorageRetentionMigration = migration{
	version: 91, name: "comfy_storage_retention", statements: []string{
		`ALTER TABLE comfy_storage_operations ADD COLUMN origin text NOT NULL DEFAULT 'manual'
		 CHECK(origin IN ('manual','retention'))`,
		`ALTER TABLE comfy_storage_operations ADD CONSTRAINT comfy_storage_retention_actor
		 CHECK(origin<>'retention' OR (actor_id IS NULL AND actor_name=''))`,
	},
}
