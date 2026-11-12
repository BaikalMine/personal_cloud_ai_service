package database

var miningFavoritesMigration = migration{version: 98, name: "mining_script_favorites", statements: []string{
	`CREATE TABLE mining_script_favorites (
	 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	 path_key TEXT NOT NULL CHECK (char_length(path_key) BETWEEN 1 AND 1024),
	 script_path TEXT NOT NULL CHECK (char_length(script_path) BETWEEN 1 AND 1024),
	 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	 PRIMARY KEY(user_id,path_key))`,
}}
