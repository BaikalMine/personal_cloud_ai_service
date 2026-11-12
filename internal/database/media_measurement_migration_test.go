package database

import (
	"context"
	"os"
	"testing"
)

func TestMediaMeasurementMigrationPreservesMedia(t *testing.T) {
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
	if err := applyMigrationsThroughForTest(ctx, db, 77); err != nil {
		t.Fatal(err)
	}
	var eventID, mediaID int64
	if err := db.QueryRow(`INSERT INTO content_events(service,kind,prompt_cipher,response_cipher,metadata_cipher) VALUES('comfyui','comfyui_prompt','x','x','x') RETURNING id`).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,payload_cipher,size_bytes) VALUES($1,'image','image/png','old.png','original-cipher',10) RETURNING id`, eventID).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var cipher string
	if err := db.QueryRow(`SELECT count(*) FROM content_media_measurements`).Scan(&count); err != nil || count != 0 {
		t.Fatal("invented measurements", count, err)
	}
	if err := db.QueryRow(`SELECT convert_from(payload_cipher,'UTF8') FROM content_media WHERE id=$1`, mediaID).Scan(&cipher); err != nil || cipher != "original-cipher" {
		t.Fatal(cipher, err)
	}
	for _, query := range []string{
		`INSERT INTO content_media_measurements(media_id,content_sha256,size_bytes,payload_cipher) VALUES($1,'bad',10,'x')`,
		`INSERT INTO content_media_measurements(media_id,content_sha256,size_bytes,payload_cipher) VALUES($1,repeat('a',64),0,'x')`,
		`INSERT INTO content_media_measurements(media_id,content_sha256,size_bytes,payload_cipher) VALUES($1,repeat('a',64),10,''::bytea)`,
	} {
		if _, err := db.Exec(query, mediaID); err == nil {
			t.Fatal("constraint missing", query)
		}
	}
	if _, err := db.Exec(`INSERT INTO content_media_measurements(media_id,content_sha256,size_bytes,payload_cipher) VALUES($1,repeat('a',64),10,'x')`, mediaID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM content_media WHERE id=$1`, mediaID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM content_media_measurements`).Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan measurement", count, err)
	}
}
