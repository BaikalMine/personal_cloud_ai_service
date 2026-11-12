package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestInviteTemplatesIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	r := store.New(db)
	if err := r.EnsureBootstrapAdmin(ctx, "template-admin", "hash"); err != nil {
		t.Fatal(err)
	}
	admin, _, err := r.FindUserWithPassword(ctx, "template-admin")
	if err != nil {
		t.Fatal(err)
	}
	settings := domain.InviteTemplateSettings{Version: 1, Access: domain.AccessSettings{QuickGeneration: true, TextToImage: true, ImageToImage: true, Video: true, Training: true, AdvancedSettings: true, PauseMining: true, QueuePriority: true, ImageDailyLimit: 12, ImageTotalLimit: 50, VideoDailyLimit: 2, VideoTotalLimit: 8, MaxVideoQuality: 720}, MaxUses: 1, LinkLifetimeHours: 1, AccountLifetimeSeconds: 7 * 24 * 3600}
	item, err := r.SaveInviteTemplate(ctx, admin.ID, domain.InviteTemplate{Name: "Photo and video", Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if item.ID <= 0 || item.Revision != 1 {
		t.Fatalf("created: %+v", item)
	}
	loaded, err := r.InviteTemplate(ctx, admin.ID, item.ID)
	if err != nil || loaded.Settings != settings {
		t.Fatalf("readback: %+v %v", loaded, err)
	}
	if _, err := r.SaveInviteTemplate(ctx, admin.ID, domain.InviteTemplate{Name: "  PHOTO AND VIDEO  ", Settings: settings}); !errors.Is(err, store.ErrInviteTemplateName) {
		t.Fatalf("duplicate: %v", err)
	}
	params := store.CreateInviteParams{TokenHash: "template-invite-hash", CreatedByUserID: admin.ID, MaxUses: settings.MaxUses, ExpiresAt: time.Now().Add(time.Duration(settings.LinkLifetimeHours) * time.Hour), AccountLifetimeSeconds: settings.AccountLifetimeSeconds}
	params.ApplyAccess(loaded.Settings.Access)
	if _, err := r.CreateInvite(ctx, params); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			copy := loaded
			copy.Name = fmt.Sprintf("Updated %d", i)
			copy.Settings.Access.ImageDailyLimit = 3
			_, err := r.SaveInviteTemplate(ctx, admin.ID, copy)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, store.ErrInviteTemplateConflict) {
				t.Errorf("update race: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("winning edits: %d", successes.Load())
	}
	updated, err := r.InviteTemplate(ctx, admin.ID, item.ID)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("revision: %+v %v", updated, err)
	}
	if err := r.DeleteInviteTemplate(ctx, admin.ID, item.ID, 1); !errors.Is(err, store.ErrInviteTemplateConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := r.DeleteInviteTemplate(ctx, admin.ID, item.ID, updated.Revision); err != nil {
		t.Fatal(err)
	}
	access, err := r.AvailableInvite(ctx, params.TokenHash)
	if err != nil || access.AccessSettings() != settings.Access || access.AccountLifetimeSeconds != settings.AccountLifetimeSeconds {
		t.Fatalf("template edit/delete mutated invite: %+v %v", access, err)
	}
	uid, _, err := r.RegisterFromInvite(ctx, store.RegisterFromInviteParams{TokenHash: params.TokenHash, Username: "template-recipient", PasswordHash: "hash", IP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := r.UserByID(ctx, uid)
	if err != nil || u.AccessSettings() != settings.Access || !u.AccountExpiresAt.Valid || time.Until(u.AccountExpiresAt.Time) < 6*24*time.Hour {
		t.Fatalf("registration snapshot: %+v %v", u, err)
	}
	manual := store.SetServiceAccessParams{}
	changed := settings.Access
	changed.Video = false
	changed.ImageDailyLimit = 4
	manual.ApplyAccess(changed)
	if ok, err := r.SetServiceAccess(ctx, uid, manual); err != nil || !ok {
		t.Fatalf("manual override: %v %v", ok, err)
	}
	u, err = r.UserByID(ctx, uid)
	if err != nil || u.AccessSettings() != changed {
		t.Fatalf("manual readback: %+v %v", u, err)
	}
	for _, actor := range []int64{uid, 999999} {
		if _, err := r.SaveInviteTemplate(ctx, actor, domain.InviteTemplate{Name: "Denied", Settings: settings}); !errors.Is(err, store.ErrInviteTemplateDenied) {
			t.Fatalf("denied save: %v", err)
		}
		if _, err := r.ListInviteTemplates(ctx, actor, "", 0); !errors.Is(err, store.ErrInviteTemplateDenied) {
			t.Fatalf("denied list: %v", err)
		}
		if _, err := r.InviteTemplate(ctx, actor, 1); !errors.Is(err, store.ErrInviteTemplateDenied) {
			t.Fatalf("denied read: %v", err)
		}
		if err := r.DeleteInviteTemplate(ctx, actor, 1, 1); !errors.Is(err, store.ErrInviteTemplateDenied) {
			t.Fatalf("denied delete: %v", err)
		}
	}
	for i := 0; i < 55; i++ {
		if _, err := r.SaveInviteTemplate(ctx, admin.ID, domain.InviteTemplate{Name: fmt.Sprintf("Saved %02d", i), Settings: settings}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := r.ListInviteTemplates(ctx, admin.ID, "saved", 0)
	if err != nil || first.Total != 55 || len(first.Items) != 50 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	last, err := r.ListInviteTemplates(ctx, admin.ID, "saved", 50)
	if err != nil || len(last.Items) != 5 || last.Total != 55 {
		t.Fatalf("last page: %+v %v", last, err)
	}
	for _, a := range first.Items {
		for _, b := range last.Items {
			if a.ID == b.ID {
				t.Fatal("pagination overlap")
			}
		}
	}
	clamped, err := r.ListInviteTemplates(ctx, admin.ID, "saved", 1000)
	if err != nil || clamped.Offset != 50 {
		t.Fatalf("clamp: %+v %v", clamped, err)
	}
	literal, err := r.ListInviteTemplates(ctx, admin.ID, "%_", 0)
	if err != nil || literal.Total != 0 {
		t.Fatalf("wildcard search: %+v %v", literal, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET disabled=true WHERE id=$1`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ListInviteTemplates(ctx, admin.ID, "", 0); !errors.Is(err, store.ErrInviteTemplateDenied) {
		t.Fatalf("disabled admin read: %v", err)
	}
}
