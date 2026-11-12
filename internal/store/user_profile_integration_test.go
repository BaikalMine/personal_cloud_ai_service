package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/store"
)

func TestUserProfilePagesIntegration(t *testing.T) {
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
	exec(`INSERT INTO users(username,password_hash,role) VALUES('profile-owner','hash','user'),('profile-other','hash','user')`)
	var owner, other int64
	if err = db.QueryRow(`SELECT id FROM users WHERE username='profile-owner'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT id FROM users WHERE username='profile-other'`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO sessions(user_id,token_hash,expires_at,last_seen_at,ip,user_agent)
	 SELECT $1,'profile-token-'||n,now()+interval '1 day',now()-interval '1 minute','192.0.2.'||n,'Browser '||n FROM generate_series(1,125)n`, owner)
	exec(`INSERT INTO sessions(user_id,token_hash,expires_at,last_seen_at) VALUES
	 ($1,'profile-expired',now()-interval '1 second',now()),($1,'profile-idle',now()+interval '1 day',now()-interval '2 hours'),($2,'profile-foreign',now()+interval '1 day',now())`, owner, other)
	exec(`UPDATE sessions SET user_agent='literal%_device' WHERE token_hash='profile-token-1'`)
	seen := map[int64]bool{}
	for _, offset := range []int{0, 50, 100} {
		p, err := r.ListUserSessionsPage(ctx, owner, "profile-token-1", time.Hour, store.UserProfileFilter{Offset: offset})
		if err != nil || p.Total != 125 || p.Now.IsZero() || p.Filter.Offset != offset {
			t.Fatalf("session page %+v %v", p, err)
		}
		for _, item := range p.Items {
			if seen[item.ID] {
				t.Fatal("duplicate session", item.ID)
			}
			seen[item.ID] = true
			if item.UserAgent == "literal%_device" && !item.Current {
				t.Fatal("lost current session")
			}
		}
	}
	if len(seen) != 125 {
		t.Fatal("session truncation", len(seen))
	}
	for _, tc := range []struct {
		f      store.UserProfileFilter
		total  int64
		offset int
	}{
		{store.UserProfileFilter{Search: "%_"}, 1, 0}, {store.UserProfileFilter{Result: "current"}, 1, 0},
		{store.UserProfileFilter{Result: "others"}, 124, 0}, {store.UserProfileFilter{Search: "missing", Offset: 500}, 0, 0},
		{store.UserProfileFilter{Offset: 999}, 125, 100},
	} {
		p, err := r.ListUserSessionsPage(ctx, owner, "profile-token-1", time.Hour, tc.f)
		if err != nil || p.Total != tc.total || p.Filter.Offset != tc.offset {
			t.Fatal("session filter", tc, p, err)
		}
	}
	oldest, err := r.ListUserSessionsPage(ctx, owner, "", time.Hour, store.UserProfileFilter{Sort: "oldest"})
	if err != nil || oldest.Items[0].UserAgent != "literal%_device" {
		t.Fatal("stable session sort", oldest, err)
	}
	currentID := oldest.Items[0].ID
	if _, revoked, err := r.RevokeOwnedSessionWithHash(ctx, currentID, owner, "profile-token-1"); err != nil || revoked {
		t.Fatal("current session revoked", revoked, err)
	}
	if _, revoked, err := r.RevokeOwnedSessionWithHash(ctx, currentID, other, ""); err != nil || revoked {
		t.Fatal("foreign session revoked", revoked, err)
	}
	if hash, revoked, err := r.RevokeOwnedSessionWithHash(ctx, currentID, owner, ""); err != nil || !revoked || hash != "profile-token-1" {
		t.Fatal("selected revoke", hash, revoked, err)
	}

	exec(`INSERT INTO proxy_requests(user_id,service,method,path,status_code,duration_ms,created_at)
	 SELECT $1,CASE WHEN n%2=0 THEN 'openwebui' ELSE 'comfyui' END,'POST','/request/'||n,CASE WHEN n%5=0 THEN 500 ELSE 200 END,10,'2026-01-01' FROM generate_series(1,125)n`, owner)
	exec(`INSERT INTO proxy_requests(user_id,service,method,path,status_code,duration_ms) VALUES($1,'comfyui','POST','/foreign',200,10)`, other)
	exec(`UPDATE proxy_requests SET path='/literal%_path' WHERE user_id=$1 AND path='/request/1'`, owner)
	paths := map[string]bool{}
	for _, offset := range []int{0, 50, 100} {
		p, err := r.ListUserActivityPage(ctx, owner, store.UserProfileFilter{Offset: offset})
		if err != nil || p.Total != 125 || p.Filter.Offset != offset || p.Now.IsZero() {
			t.Fatal("activity page", p, err)
		}
		for _, item := range p.Items {
			if paths[item.Path] {
				t.Fatal("duplicate request", item.Path)
			}
			paths[item.Path] = true
		}
	}
	if len(paths) != 125 || paths["/foreign"] {
		t.Fatal("activity scope/page loss", len(paths))
	}
	for _, tc := range []struct {
		f      store.UserProfileFilter
		total  int64
		offset int
	}{
		{store.UserProfileFilter{Search: "%_"}, 1, 0}, {store.UserProfileFilter{Service: "openwebui"}, 62, 0},
		{store.UserProfileFilter{Result: "error"}, 25, 0}, {store.UserProfileFilter{Result: "success"}, 100, 0},
		{store.UserProfileFilter{Search: "missing", Offset: 500}, 0, 0}, {store.UserProfileFilter{Offset: 999}, 125, 100},
	} {
		p, err := r.ListUserActivityPage(ctx, owner, tc.f)
		if err != nil || p.Total != tc.total || p.Filter.Offset != tc.offset {
			t.Fatal("activity filter", tc, p, err)
		}
	}
	activity, err := r.ListUserActivityPage(ctx, owner, store.UserProfileFilter{Sort: "oldest"})
	if err != nil || activity.Items[0].Path != "/literal%_path" {
		t.Fatal("stable activity sort", activity, err)
	}
	for _, f := range []store.UserProfileFilter{{Offset: -1}, {Offset: 1000000001}, {Search: string([]byte{0})}, {Service: "unknown"}, {Result: "unknown"}, {Sort: "id;DROP"}} {
		if _, err := r.ListUserActivityPage(ctx, owner, f); !errors.Is(err, store.ErrUserProfileFilter) {
			t.Fatal("invalid activity filter", f, err)
		}
		if _, err := r.ListUserSessionsPage(ctx, owner, "", time.Hour, f); !errors.Is(err, store.ErrUserProfileFilter) {
			t.Fatal("invalid session filter", f, err)
		}
	}
}
