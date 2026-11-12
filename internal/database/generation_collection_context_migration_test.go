package database

import (
	"context"
	"os"
	"testing"
)

func TestGenerationCollectionContextMigration(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 93); err != nil {
		t.Fatal(err)
	}
	var owner, collection int64
	if err := db.QueryRow(`INSERT INTO users(username,password_hash,role) VALUES('collection-migration-owner','hash','user') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO generation_media_collections(user_id,name,name_key) VALUES($1,'Existing','existing') RETURNING id`, owner).Scan(&collection); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var revision int64
	var name string
	var empty bool
	if err := db.QueryRow(`SELECT name,revision,context_cipher IS NULL AND context_saved_at IS NULL FROM generation_media_collections WHERE id=$1`, collection).Scan(&name, &revision, &empty); err != nil || name != "Existing" || revision <= 0 || !empty {
		t.Fatal(name, revision, empty, err)
	}
	for _, update := range []string{
		`context_cipher='x'`,
		`context_saved_at=now()`,
		`context_cipher=''::bytea,context_saved_at=now()`,
		`context_cipher=decode(repeat('aa',196609),'hex'),context_saved_at=now()`,
	} {
		if _, err := db.Exec(`UPDATE generation_media_collections SET `+update+` WHERE id=$1`, collection); err == nil {
			t.Fatal("invalid context accepted", update)
		}
	}
	if _, err := db.Exec(`UPDATE generation_media_collections SET context_cipher='encrypted',context_saved_at=now() WHERE id=$1`, collection); err != nil {
		t.Fatal(err)
	}
}
