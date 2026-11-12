package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestHarnessOnlyInviteIntegration(t *testing.T) {
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
	s := store.New(db)
	if err = s.EnsureBootstrapAdmin(ctx, "harness-invite-admin", "hash"); err != nil {
		t.Fatal(err)
	}
	admin, _, err := s.FindUserWithPassword(ctx, "harness-invite-admin")
	if err != nil {
		t.Fatal(err)
	}
	params := store.CreateInviteParams{TokenHash: "harness-only-fixture-token", CreatedByUserID: admin.ID, MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour)}
	params.ApplyAccess(domain.AccessSettings{Harness: true, MaxVideoQuality: 720})
	if _, err = s.CreateInvite(ctx, params); err != nil {
		t.Fatal(err)
	}
	invitation, err := s.AvailableInvite(ctx, params.TokenHash)
	if err != nil || !invitation.GrantHarness || invitation.GrantQuickGeneration {
		t.Fatalf("invite=%+v err=%v", invitation, err)
	}
	id, _, err := s.RegisterFromInvite(ctx, store.RegisterFromInviteParams{TokenHash: params.TokenHash, Username: "harness-only-user", PasswordHash: "hash", IP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.UserByID(ctx, id)
	if err != nil || !user.CanUseHarness || user.CanUseQuickGeneration || user.CanManageMining || user.Role != "user" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	loaded, _, err := s.FindUserWithPassword(ctx, "harness-only-user")
	if err != nil || !loaded.CanUseHarness {
		t.Fatal("login lost Harness flag", err)
	}
	users, err := s.ListUsers(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range users {
		if row.ID == id {
			found = row.CanUseHarness
		}
	}
	if !found {
		t.Fatal("user list lost Harness flag")
	}
	invites, err := s.ListInvites(ctx, 100)
	if err != nil || len(invites) != 1 || !invites[0].GrantHarness {
		t.Fatal("invite list lost Harness flag", err)
	}
	update := store.SetServiceAccessParams{}
	update.ApplyAccess(domain.AccessSettings{MaxVideoQuality: 720})
	changed, err := s.SetServiceAccess(ctx, id, update)
	if err != nil || !changed {
		t.Fatal("revoke", err)
	}
	user, err = s.UserByID(ctx, id)
	if err != nil || user.CanUseHarness {
		t.Fatal("revocation lost", err)
	}
}
