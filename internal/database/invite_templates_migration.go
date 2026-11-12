package database

var inviteTemplatesMigration = migration{
	version: 83, name: "invitation_templates", statements: []string{
		`CREATE TABLE invite_templates (
		 id BIGSERIAL PRIMARY KEY,
		 name TEXT NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
		 revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
		 settings JSONB NOT NULL CHECK (jsonb_typeof(settings)='object' AND COALESCE(settings->'version'='1'::jsonb,false) AND octet_length(settings::text)<=8192),
		 created_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
		 updated_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
		 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE UNIQUE INDEX invite_templates_name_idx ON invite_templates (lower(btrim(name)))`,
	},
}
