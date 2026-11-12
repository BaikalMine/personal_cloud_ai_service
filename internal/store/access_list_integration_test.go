package store_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/store"
)

func TestAccessListsIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	r := store.New(db)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(username,password_hash,role,created_at,can_use_quick_generation,can_generate_text_to_image,can_generate_image_to_image,can_generate_video,can_train_image_lora,can_use_comfyui,can_use_openwebui)
 SELECT 'list-'||lpad(n::text,3,'0'),'hash','user','2026-01-01',true,true,false,n%2=0,n%3=0,false,false FROM generate_series(1,125) n`)
	var firstID int64
	if err = db.QueryRow(`SELECT id FROM users WHERE username='list-001'`).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE users SET email='literal%_@example.test',generation_daily_limit=12,generation_total_limit=50,generation_total_used=7,video_generation_daily_limit=2,video_generation_total_limit=8,video_generation_total_used=1 WHERE id=$1`, firstID)
	exec(`INSERT INTO quick_generation_daily_usage(user_id,usage_date,used_count,video_used_count) VALUES($1,timezone('Europe/Moscow',now())::date,3,1),($1,timezone('Europe/Moscow',now())::date-1,99,99)`, firstID)
	seen := map[int64]bool{}
	for _, offset := range []int{0, 50, 100} {
		p, err := r.ListUsersPage(ctx, store.AccessListFilter{Offset: offset, Sort: "name"})
		if err != nil || p.Total != 125 || p.Filter.Offset != offset || p.Now.IsZero() {
			t.Fatalf("user page %+v %v", p, err)
		}
		for _, row := range p.Items {
			if seen[row.ID] {
				t.Fatal("duplicate across pages", row.ID)
			}
			seen[row.ID] = true
			if row.ID == firstID && (row.ImageUsedToday != 3 || row.VideoUsedToday != 1 || row.GenerationTotalUsed != 7 || row.GenerationDailyLimit != 12) {
				t.Fatal("quota fields", row)
			}
		}
	}
	if len(seen) != 125 {
		t.Fatal("missing users", len(seen))
	}
	p, err := r.ListUsersPage(ctx, store.AccessListFilter{Offset: 999, Sort: "name"})
	if err != nil || p.Filter.Offset != 100 || len(p.Items) != 25 {
		t.Fatal("clamp", p, err)
	}
	for _, q := range []string{"%_", "literal%_@example.test"} {
		p, err := r.ListUsersPage(ctx, store.AccessListFilter{Search: q})
		if err != nil || p.Total != 1 || p.Items[0].ID != firstID {
			t.Fatal("literal/id search", q, p, err)
		}
	}
	idPage, err := r.ListUsersPage(ctx, store.AccessListFilter{Search: strconv.FormatInt(firstID, 10)})
	if err != nil {
		t.Fatal(err)
	}
	foundID := false
	for _, row := range idPage.Items {
		foundID = foundID || row.ID == firstID
	}
	if !foundID {
		t.Fatal("ID search did not include matching user")
	}
	exec(`UPDATE users SET account_expires_at=now()+interval '1 day' WHERE username='list-002'`)
	exec(`UPDATE users SET account_expires_at=now()-interval '1 day' WHERE username='list-003'`)
	exec(`UPDATE users SET disabled=true,account_expires_at=now()-interval '1 day' WHERE username='list-004'`)
	exec(`UPDATE users SET locked_until=now()+interval '1 hour' WHERE username='list-005'`)
	exec(`UPDATE users SET role='admin',can_use_quick_generation=false,can_generate_text_to_image=false,can_generate_video=false,can_train_image_lora=false WHERE username='list-006'`)
	for _, tc := range []struct {
		f     store.AccessListFilter
		count int64
	}{
		{store.AccessListFilter{AccountType: "temporary"}, 3}, {store.AccessListFilter{ExpiringSoon: true}, 1},
		{store.AccessListFilter{State: "disabled"}, 1}, {store.AccessListFilter{State: "expired"}, 1},
		{store.AccessListFilter{State: "locked"}, 1}, {store.AccessListFilter{State: "active"}, 122},
		{store.AccessListFilter{Access: "none"}, 2}, {store.AccessListFilter{State: "disabled", Access: "photo"}, 0},
		{store.AccessListFilter{Search: "list-006", Access: "video"}, 1}, {store.AccessListFilter{State: "locked", Access: "photo"}, 1},
	} {
		p, err := r.ListUsersPage(ctx, tc.f)
		if err != nil || p.Total != tc.count {
			t.Fatal("user filters", tc, p.Total, err)
		}
	}
	for _, sort := range []string{"newest", "oldest", "name", "login", "expires"} {
		p, err := r.ListUsersPage(ctx, store.AccessListFilter{Sort: sort})
		if err != nil || len(p.Items) != 50 {
			t.Fatal(sort, err)
		}
		again, err := r.ListUsersPage(ctx, store.AccessListFilter{Sort: sort})
		if err != nil {
			t.Fatal(err)
		}
		for i := range p.Items {
			if p.Items[i].ID != again.Items[i].ID {
				t.Fatal("unstable tie", sort)
			}
		}
	}
	exec(`INSERT INTO invites(token_hash,created_by_user_id,max_uses,used_count,expires_at,created_at,grant_quick_generation,grant_text_to_image,grant_video,account_lifetime_seconds)
 SELECT 'list-token-'||n,$1,3,0,now()+interval '10 days','2026-01-01',true,true,n%2=0,CASE WHEN n%2=0 THEN 3600 ELSE 0 END FROM generate_series(1,225)n`, firstID)
	seen = map[int64]bool{}
	for offset := 0; offset < 225; offset += 50 {
		p, err := r.ListInvitesPage(ctx, store.AccessListFilter{Offset: offset})
		if err != nil || p.Total != 225 {
			t.Fatal("invite page", p.Total, err)
		}
		for _, row := range p.Items {
			if seen[row.ID] {
				t.Fatal("duplicate invite")
			}
			seen[row.ID] = true
		}
	}
	if len(seen) != 225 {
		t.Fatal("invitations truncated", len(seen))
	}
	exec(`UPDATE invites SET expires_at=now()+interval '1 day' WHERE token_hash='list-token-1'`)
	exec(`UPDATE invites SET expires_at=now()-interval '1 day' WHERE token_hash='list-token-2'`)
	exec(`UPDATE invites SET revoked=true,expires_at=now()-interval '1 day' WHERE token_hash='list-token-3'`)
	exec(`UPDATE invites SET used_count=max_uses WHERE token_hash='list-token-4'`)
	for _, tc := range []struct {
		f     store.AccessListFilter
		count int64
	}{
		{store.AccessListFilter{AccountType: "temporary"}, 112}, {store.AccessListFilter{ExpiringSoon: true}, 1},
		{store.AccessListFilter{State: "revoked"}, 1}, {store.AccessListFilter{State: "expired"}, 1},
		{store.AccessListFilter{State: "used"}, 1}, {store.AccessListFilter{State: "active"}, 222},
		{store.AccessListFilter{Access: "video"}, 112}, {store.AccessListFilter{State: "expired", Access: "video"}, 1},
		{store.AccessListFilter{Search: "list-token"}, 0}, {store.AccessListFilter{Search: "%"}, 0},
	} {
		p, err := r.ListInvitesPage(ctx, tc.f)
		if err != nil || p.Total != tc.count {
			t.Fatal("invite filters", tc, p.Total, err)
		}
	}
	ip, err := r.ListInvitesPage(ctx, store.AccessListFilter{Offset: 999, Sort: "oldest"})
	if err != nil || ip.Filter.Offset != 200 || len(ip.Items) != 25 {
		t.Fatal("invite clamp", ip, err)
	}
	for _, sort := range []string{"newest", "oldest", "creator", "uses", "expires"} {
		p, err := r.ListInvitesPage(ctx, store.AccessListFilter{Sort: sort})
		if err != nil || len(p.Items) != 50 {
			t.Fatal(sort, err)
		}
		again, err := r.ListInvitesPage(ctx, store.AccessListFilter{Sort: sort})
		if err != nil {
			t.Fatal(err)
		}
		for i := range p.Items {
			if p.Items[i].ID != again.Items[i].ID {
				t.Fatal("unstable invite tie", sort)
			}
		}
	}
	exec(`DELETE FROM users WHERE id=$1`, firstID)
	ip, err = r.ListInvitesPage(ctx, store.AccessListFilter{})
	if err != nil || ip.Total != 225 || ip.Items[0].CreatedBy != "" {
		t.Fatal("deleted creator", ip, err)
	}
	for _, f := range []store.AccessListFilter{{Offset: -1}, {Offset: 1000000001}, {Sort: "username; DROP TABLE users"}, {State: "wrong"}, {Access: "invalid"}, {Search: "\x00"}} {
		if _, err := r.ListUsersPage(ctx, f); !errors.Is(err, store.ErrAccessListFilter) {
			t.Fatal("invalid users filter", f, err)
		}
		if _, err := r.ListInvitesPage(ctx, f); !errors.Is(err, store.ErrAccessListFilter) {
			t.Fatal("invalid invites filter", f, err)
		}
	}
}
