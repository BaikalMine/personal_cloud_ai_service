package database

var generationPostprocessInputsMigration = migration{
	version: 81, name: "generation_postprocess_inputs", statements: []string{
		`ALTER TABLE comfy_input_assets DROP CONSTRAINT comfy_input_assets_size_bytes_check`,
		`ALTER TABLE comfy_input_assets ADD CONSTRAINT comfy_input_assets_size_bytes_check CHECK (size_bytes BETWEEN 0 AND 536870912)`,
		`CREATE TABLE generation_postprocess_inputs (
		 input_id TEXT PRIMARY KEY REFERENCES comfy_input_assets(id) ON DELETE CASCADE,
		 job_id BIGINT REFERENCES generation_jobs(id) ON DELETE SET NULL,
		 source_position SMALLINT NOT NULL CHECK (source_position BETWEEN 0 AND 1),
		 dispatch_token TEXT NOT NULL CHECK (char_length(dispatch_token) BETWEEN 16 AND 96),
		 cleanup_blocked BOOLEAN NOT NULL DEFAULT true,
		 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		 UNIQUE(job_id,dispatch_token,source_position)
		)`,
		`CREATE INDEX generation_postprocess_inputs_job_idx ON generation_postprocess_inputs(job_id,source_position,created_at DESC) WHERE job_id IS NOT NULL`,
	},
}
