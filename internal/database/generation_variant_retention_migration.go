package database

var generationVariantRetentionMigration = migration{
	version: 82, name: "generation_variant_retained_provenance", statements: []string{
		`ALTER TABLE quick_generation_variants DROP CONSTRAINT quick_generation_variants_job_id_fkey`,
		`ALTER TABLE quick_generation_variants ADD CONSTRAINT quick_generation_variants_job_id_fkey
		 FOREIGN KEY (job_id) REFERENCES generation_jobs(id) ON DELETE SET NULL`,
	},
}
