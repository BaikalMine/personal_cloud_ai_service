package database

var comfyStorageInventoryMigration = migration{
	version: 88, name: "comfy_storage_inventory", statements: []string{
		`CREATE INDEX content_media_output_path_idx ON content_media(original_name,subfolder,id DESC) WHERE storage_type='output'`,
	},
}
