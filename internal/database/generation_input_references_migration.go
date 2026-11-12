package database

var generationInputReferencesMigration = migration{
	version: 89, name: "generation_input_references", statements: []string{
		`ALTER TABLE generation_jobs ADD COLUMN input_references_verified boolean NOT NULL DEFAULT false`,
		`CREATE FUNCTION invalidate_generation_input_manifest() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN PERFORM pg_advisory_xact_lock(743629835); NEW.input_references_verified := false; RETURN NEW; END $$`,
		`CREATE TRIGGER generation_input_manifest_payload BEFORE UPDATE OF payload_cipher ON generation_jobs
		 FOR EACH ROW EXECUTE FUNCTION invalidate_generation_input_manifest()`,
		`CREATE TABLE generation_input_references (
		 job_id bigint NOT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE,
		 input_id text NOT NULL,
		 input_name text NOT NULL CHECK (length(input_name) BETWEEN 1 AND 1024),
		 created_at timestamptz NOT NULL DEFAULT now(),
		 PRIMARY KEY(job_id,input_id), UNIQUE(job_id,input_name)
		)`,
		`CREATE INDEX generation_input_references_input_idx ON generation_input_references(input_id,job_id)`,
		`CREATE INDEX comfy_input_assets_path_idx ON comfy_input_assets(subfolder,filename)`,
		`CREATE INDEX generation_jobs_unknown_inputs_idx ON generation_jobs(id) WHERE NOT input_references_verified`,
	},
}
