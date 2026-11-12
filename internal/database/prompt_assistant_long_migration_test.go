package database

import "testing"

func TestPromptAssistantLongBudgetMigration(t *testing.T) {
	db, _ := runtimeTestDatabase(t, false)
	if _, err := db.Exec(`CREATE TABLE prompt_assistant_runs (
	 num_predict INT NOT NULL CHECK(num_predict BETWEEN 1 AND 8192),
	 timeout_ms BIGINT NOT NULL CHECK(timeout_ms BETWEEN 1000 AND 900000));
	 INSERT INTO prompt_assistant_runs VALUES(4096,240000)`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range promptAssistantLongMigration.statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]int{{5632, 600000}, {19456, 1680000}, {27648, 1680000}} {
		if _, err := db.Exec(`INSERT INTO prompt_assistant_runs VALUES($1,$2)`, pair[0], pair[1]); err != nil {
			t.Fatal("long assistant budget cannot be recorded", err)
		}
	}
	for _, pair := range [][2]int{{32769, 1000}, {1, 3600001}, {0, 1000}} {
		if _, err := db.Exec(`INSERT INTO prompt_assistant_runs VALUES($1,$2)`, pair[0], pair[1]); err == nil {
			t.Fatal("invalid budget accepted", pair)
		}
	}
}

func TestPromptAssistantLongTransportMigration(t *testing.T) {
	db, _ := runtimeTestDatabase(t, false)
	if _, err := db.Exec(`CREATE TABLE gpu_inference_progress(cipher BYTEA CHECK(octet_length(cipher) BETWEEN 1 AND 65600));
	 CREATE TABLE generation_drafts(payload_cipher BYTEA CHECK(octet_length(payload_cipher) BETWEEN 1 AND 262144));
	 CREATE TABLE generation_media_collections(context_cipher BYTEA, context_saved_at TIMESTAMPTZ,
	 CONSTRAINT generation_collection_context_consistent CHECK(
	 (context_cipher IS NULL AND context_saved_at IS NULL) OR
	 (context_cipher IS NOT NULL AND context_saved_at IS NOT NULL AND octet_length(context_cipher) BETWEEN 1 AND 196608)))`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range promptAssistantLongTransportMigration.statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO gpu_inference_progress VALUES(decode(repeat('ab',100000),'hex'))`,
		`INSERT INTO generation_drafts VALUES(decode(repeat('ab',700000),'hex'))`,
		`INSERT INTO generation_media_collections VALUES(decode(repeat('ab',700000),'hex'),now())`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal("full long-video state cannot be stored", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO generation_media_collections VALUES(NULL,now())`); err == nil {
		t.Fatal("context consistency check was lost")
	}
}
