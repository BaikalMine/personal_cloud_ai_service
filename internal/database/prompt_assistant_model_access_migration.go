package database

var promptAssistantModelAccessMigration = migration{
	version: 95, name: "prompt_assistant_model_access", statements: []string{
		`CREATE TABLE prompt_assistant_model_grants (
		 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		 model_id TEXT NOT NULL CHECK (model_id ~ '^[a-z][a-z0-9-]{0,63}$'),
		 granted_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
		 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		 PRIMARY KEY(user_id,model_id))`,
		`ALTER TABLE prompt_assistant_jobs ADD COLUMN model_id TEXT NOT NULL DEFAULT ''
		 CHECK (model_id = '' OR model_id ~ '^[a-z][a-z0-9-]{0,63}$')`,
	},
}
