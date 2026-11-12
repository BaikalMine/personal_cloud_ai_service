package database

var promptAssistantLongMigration = migration{
	version: 100, name: "prompt_assistant_long_video_budgets", statements: []string{
		`ALTER TABLE prompt_assistant_runs
		 DROP CONSTRAINT prompt_assistant_runs_num_predict_check,
		 DROP CONSTRAINT prompt_assistant_runs_timeout_ms_check,
		 ADD CONSTRAINT prompt_assistant_runs_num_predict_check CHECK(num_predict BETWEEN 1 AND 32768),
		 ADD CONSTRAINT prompt_assistant_runs_timeout_ms_check CHECK(timeout_ms BETWEEN 1000 AND 3600000)`,
	},
}
