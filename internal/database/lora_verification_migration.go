package database

var loraVerificationMigration = migration{
	version: 74, name: "persisted_lora_verification_batches", statements: []string{
		`ALTER TABLE generation_batches DROP CONSTRAINT generation_batches_experiment_mode_check,
   ADD CONSTRAINT generation_batches_experiment_mode_check CHECK(experiment_mode IN ('seeds','parameter','lora_verification'))`,
		`ALTER TABLE generation_batches DROP CONSTRAINT generation_batches_check,
   ADD CONSTRAINT generation_batches_check CHECK(
    (experiment_mode IN ('seeds','lora_verification') AND parameter_name='') OR
    (experiment_mode='parameter' AND char_length(parameter_name) BETWEEN 1 AND 64))`,
		`CREATE TABLE lora_verifications (
   batch_id BIGINT PRIMARY KEY REFERENCES generation_batches(id) ON DELETE CASCADE,
   user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
   training_job_id BIGINT REFERENCES lora_training_jobs(id) ON DELETE SET NULL,
   training_public_id TEXT NOT NULL CHECK(char_length(training_public_id) BETWEEN 1 AND 96),
   training_name TEXT NOT NULL,
   family TEXT NOT NULL CHECK(family IN ('krea2','flux2-klein')),
   profile_sha256 TEXT NOT NULL CHECK(profile_sha256 ~ '^[a-f0-9]{64}$'),
   artifact_name TEXT NOT NULL CHECK(char_length(artifact_name) BETWEEN 1 AND 255),
   artifact_bytes BIGINT NOT NULL CHECK(artifact_bytes>0),
   artifact_sha256 TEXT NOT NULL CHECK(artifact_sha256 ~ '^[a-f0-9]{64}$'),
   training_provenance JSONB NOT NULL CHECK(jsonb_typeof(training_provenance)='object' AND octet_length(training_provenance::text)<=16384),
   dataset_snapshot_id TEXT NOT NULL DEFAULT '',
   dataset_snapshot_hash TEXT NOT NULL DEFAULT '' CHECK(dataset_snapshot_hash='' OR dataset_snapshot_hash ~ '^[a-f0-9]{64}$'),
   model_id TEXT NOT NULL CHECK(char_length(model_id) BETWEEN 1 AND 3000),
   request_sha256 TEXT NOT NULL CHECK(request_sha256 ~ '^[a-f0-9]{64}$'),
   plan_cipher BYTEA NOT NULL CHECK(octet_length(plan_cipher) BETWEEN 1 AND 4194304),
   created_at TIMESTAMPTZ NOT NULL DEFAULT now()
  )`,
		`CREATE INDEX lora_verifications_owner_source_idx ON lora_verifications(user_id,training_public_id,batch_id DESC)`,
		`CREATE TABLE lora_verification_members (
   batch_id BIGINT NOT NULL REFERENCES lora_verifications(batch_id) ON DELETE CASCADE,
   position INTEGER NOT NULL CHECK(position BETWEEN 1 AND 20),
   job_id BIGINT UNIQUE REFERENCES generation_jobs(id) ON DELETE SET NULL,
   job_public_id TEXT NOT NULL UNIQUE CHECK(char_length(job_public_id) BETWEEN 16 AND 96),
   case_index INTEGER NOT NULL CHECK(case_index BETWEEN 0 AND 9),
   variant_index INTEGER NOT NULL CHECK(variant_index BETWEEN 0 AND 4),
   seed BIGINT NOT NULL CHECK(seed BETWEEN 0 AND 1125899906842624),
   weight TEXT NOT NULL CHECK(weight ~ '^-?([0-3](\.[0-9]{1,2})?|4)$' AND weight::numeric BETWEEN -4 AND 4 AND weight::numeric*20=trunc(weight::numeric*20)),
   PRIMARY KEY(batch_id,position), UNIQUE(batch_id,case_index,variant_index),
   CHECK((variant_index=0 AND weight='0') OR (variant_index>0 AND weight::numeric<>0))
  )`,
	},
}
