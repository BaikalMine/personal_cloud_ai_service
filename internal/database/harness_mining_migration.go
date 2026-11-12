package database

var harnessMiningMigration = migration{version: 99, name: "harness_mining_lifecycle", statements: []string{
	`ALTER TABLE quick_generation_mining_leases
	 ADD COLUMN executor_permit_id TEXT NOT NULL DEFAULT '',
	 ADD COLUMN executor_owner_id TEXT NOT NULL DEFAULT '',
	 ADD COLUMN executor_generation BIGINT NOT NULL DEFAULT 0,
	 ADD CONSTRAINT mining_executor_binding_check CHECK (
	 (executor_permit_id='' AND executor_owner_id='' AND executor_generation=0) OR
	 (executor_permit_id ~ '^[a-f0-9]{32}$' AND executor_owner_id ~ '^[a-f0-9]{64}$' AND executor_generation>0
	 AND gpu_work_id IS NULL AND generation_job_id IS NULL AND lora_training_job_id IS NULL AND prompt_id IS NULL))`,
	`CREATE UNIQUE INDEX mining_executor_permit_idx ON quick_generation_mining_leases(executor_permit_id) WHERE executor_permit_id<>''`,
}}
