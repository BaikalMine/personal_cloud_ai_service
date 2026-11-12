package database

var harnessUsersMigration = migration{version: 103, name: "harness_user_access", statements: []string{
	`ALTER TABLE users ADD COLUMN can_use_harness BOOLEAN NOT NULL DEFAULT false`,
	`ALTER TABLE invites ADD COLUMN grant_harness BOOLEAN NOT NULL DEFAULT false`,
}}
