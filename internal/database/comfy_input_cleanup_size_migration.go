package database

var comfyInputCleanupSizeMigration = migration{
	version: 87, name: "comfy_input_cleanup_size", statements: []string{
		`ALTER TABLE comfy_input_cleanup_receipts DROP CONSTRAINT comfy_input_cleanup_receipts_size_bytes_check`,
		`ALTER TABLE comfy_input_cleanup_receipts ADD CONSTRAINT comfy_input_cleanup_receipts_size_bytes_check CHECK (size_bytes BETWEEN 0 AND 536870912)`,
	},
}
