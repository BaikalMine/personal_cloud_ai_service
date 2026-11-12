package database

var promptAssistantLongTransportMigration = migration{
	version: 101, name: "prompt_assistant_long_video_transport", statements: []string{
		`ALTER TABLE gpu_inference_progress DROP CONSTRAINT gpu_inference_progress_cipher_check,
		 ADD CONSTRAINT gpu_inference_progress_cipher_check CHECK(octet_length(cipher) BETWEEN 1 AND 524352)`,
		`ALTER TABLE generation_drafts DROP CONSTRAINT generation_drafts_payload_cipher_check,
		 ADD CONSTRAINT generation_drafts_payload_cipher_check CHECK(octet_length(payload_cipher) BETWEEN 1 AND 1048640)`,
		`ALTER TABLE generation_media_collections DROP CONSTRAINT generation_collection_context_consistent,
		 ADD CONSTRAINT generation_collection_context_consistent CHECK(
		 (context_cipher IS NULL AND context_saved_at IS NULL) OR
		 (context_cipher IS NOT NULL AND context_saved_at IS NOT NULL AND octet_length(context_cipher) BETWEEN 1 AND 1048640))`,
	},
}
