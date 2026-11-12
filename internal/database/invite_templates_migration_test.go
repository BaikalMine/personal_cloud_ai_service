package database

import (
	"context"
	"os"
	"testing"
)

func TestInviteTemplatesMigration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	resetMigrationIntegrationSchema(t, ctx, db)
	defer resetMigrationIntegrationSchema(t, ctx, db)
	if err := applyMigrationsThroughForTest(ctx, db, 82); err != nil {
		t.Fatal(err)
	}
	var uid int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('template-migration','hash','admin') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO invites(token_hash,created_by_user_id,max_uses,expires_at,grant_comfyui) VALUES('retained-invite',$1,2,now()+interval '1 hour',true)`, uid); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var uses int
	if err := db.QueryRow(`SELECT max_uses FROM invites WHERE token_hash='retained-invite' AND grant_comfyui`).Scan(&uses); err != nil || uses != 2 {
		t.Fatalf("existing invite changed: %d %v", uses, err)
	}
	for _, raw := range []string{`{}`, `null`, `[]`, `{"version":2}`, `{"version":"1"}`} {
		if _, err := db.Exec(`INSERT INTO invite_templates(name,settings) VALUES('invalid',$1)`, raw); err == nil {
			t.Fatalf("accepted invalid envelope: %s", raw)
		}
	}
	if _, err := db.Exec(`INSERT INTO invite_templates(name,settings,created_by_user_id,updated_by_user_id) VALUES('Retained template','{"version":1}',$1,$1)`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM invite_templates WHERE created_by_user_id IS NULL AND updated_by_user_id IS NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("template after creator removal: %d %v", count, err)
	}
}
