package database

var harnessUltraMigration = migration{version: 102, name: "harness_ultra_gpu_namespace", statements: []string{
	`ALTER TABLE gpu_work DROP CONSTRAINT gpu_work_kind_check,
	 ADD CONSTRAINT gpu_work_kind_check CHECK(kind IN ('generation','lora_training','lora_caption','prompt_assistant','harness_ultra'))`,
}}
