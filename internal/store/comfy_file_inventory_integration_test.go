package store_test

import (
	"context"
	"strings"
	"testing"

	"ai-access-gateway/internal/updates"
)

func TestComfyFileRegistrations(t *testing.T) {
	db, s, user := comfyStorageDBFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO comfy_input_assets(id,user_id,filename,subfolder,size_bytes,content_hash,state) VALUES($1,$2,'Reserved.PNG','Owner/Sub',12,repeat('a',64),'reserved')`, strings.Repeat("a", 32), user); err != nil {
		t.Fatal(err)
	}
	var event int64
	if err := db.QueryRow(`INSERT INTO content_events(user_id,service,kind,prompt_cipher,response_cipher,metadata_cipher) VALUES($1,'comfyui','comfyui_prompt','x','x','x') RETURNING id`, user).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO comfy_output_ownership(event_id,user_id,prompt_id,filename,subfolder,storage_type,media_type) VALUES($1,$2,'prompt','owned.png','owner/sub','output','image')`, event, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO comfy_output_cleanup_tombstones(filename,subfolder,storage_type,size_bytes,content_hash) VALUES('deleted.png','owner/sub','output',12,repeat('a',64))`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO content_media(event_id,media_type,mime_type,original_name,subfolder,storage_type,payload_cipher,size_bytes,content_hash) VALUES($1,'image','image/png','media.png','owner/sub','output','x',12,repeat('a',64))`, event); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"input", "output"} {
		items := []updates.ComfyFileInventoryItem{}
		for _, name := range []string{"reserved.png", "owned.png", "deleted.png", "media.png", "unknown.png"} {
			items = append(items, updates.ComfyFileInventoryItem{ID: updates.ComfyFileInventoryID(kind, "OWNER/SUB", name), Filename: name, Subfolder: "OWNER/SUB"})
		}
		known, err := s.ComfyFileRegistrations(ctx, kind, items)
		if err != nil || len(known) != 5 {
			t.Fatal(known, err)
		}
		for i, item := range items {
			want := kind == "input" && i == 0 || kind == "output" && i > 0 && i < 4
			if known[item.ID] != want {
				t.Fatal(kind, item, known)
			}
		}
		if _, err := s.ComfyFileRegistrations(ctx, kind, append(items, items[0])); err == nil {
			t.Fatal("accepted duplicate file identity")
		}
	}
}
