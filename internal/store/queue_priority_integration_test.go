package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestQueuePriorityPolicyIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	repository := store.New(db)
	var adminID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('policy-admin','hash','admin') RETURNING id`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	assertPolicy := func(user domain.User, priority, pause bool) {
		t.Helper()
		if user.QueuePriority != priority || user.PauseMiningForQuickGeneration != pause {
			t.Fatalf("user %d priority=%v pause=%v want %v/%v", user.ID, user.QueuePriority, user.PauseMiningForQuickGeneration, priority, pause)
		}
	}
	for _, priority := range []bool{false, true} {
		for _, pause := range []bool{false, true} {
			name := fmt.Sprintf("policy-%v-%v", priority, pause)
			inviteID, err := repository.CreateInvite(ctx, store.CreateInviteParams{TokenHash: name, CreatedByUserID: adminID, MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour), GrantQuickGeneration: true, GrantTextToImage: true, GrantTrainImageLora: true, QueuePriority: priority, PauseMiningForQuickGeneration: pause})
			if err != nil {
				t.Fatal(err)
			}
			access, err := repository.AvailableInvite(ctx, name)
			if err != nil || access.QueuePriority != priority || access.PauseMiningForQuickGeneration != pause {
				t.Fatalf("invite access %+v err=%v", access, err)
			}
			invites, err := repository.ListInvites(ctx, 200)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, invite := range invites {
				if invite.ID == inviteID {
					found = true
					if invite.QueuePriority != priority || invite.PauseMiningForQuickGeneration != pause {
						t.Fatalf("listed invite %+v", invite)
					}
				}
			}
			if !found {
				t.Fatal("invite not listed")
			}
			userID, _, err := repository.RegisterFromInvite(ctx, store.RegisterFromInviteParams{TokenHash: name, Username: name, PasswordHash: "hash"})
			if err != nil {
				t.Fatal(err)
			}
			checkReaders := func(priority, pause bool) {
				t.Helper()
				user, err := repository.UserByID(ctx, userID)
				if err != nil {
					t.Fatal(err)
				}
				assertPolicy(user, priority, pause)
				user, _, err = repository.FindUserWithPassword(ctx, name)
				if err != nil {
					t.Fatal(err)
				}
				assertPolicy(user, priority, pause)
				user, err = repository.UserBySessionHash(ctx, name, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				assertPolicy(user, priority, pause)
				rows, err := repository.ListUsers(ctx, name)
				if err != nil || len(rows) != 1 || rows[0].QueuePriority != priority || rows[0].PauseMiningForQuickGeneration != pause {
					t.Fatalf("listed users %+v err=%v", rows, err)
				}
			}
			if err := repository.CreateSession(ctx, userID, name, time.Now().Add(time.Hour), "test", ""); err != nil {
				t.Fatal(err)
			}
			checkReaders(priority, pause)
			updated, err := repository.SetServiceAccess(ctx, userID, store.SetServiceAccessParams{QuickGeneration: true, TextToImage: true, TrainImageLora: true, QueuePriority: !priority, PauseMiningForQuickGeneration: pause})
			if err != nil || !updated {
				t.Fatalf("set priority updated=%v err=%v", updated, err)
			}
			checkReaders(!priority, pause)
			updated, err = repository.SetServiceAccess(ctx, userID, store.SetServiceAccessParams{QuickGeneration: true, TextToImage: true, TrainImageLora: true, QueuePriority: !priority, PauseMiningForQuickGeneration: !pause})
			if err != nil || !updated {
				t.Fatalf("set mining updated=%v err=%v", updated, err)
			}
			checkReaders(!priority, !pause)
			if updated, err := repository.SetAdminQueuePriority(ctx, userID, priority); err != nil || updated {
				t.Fatalf("regular user admin priority updated=%v err=%v", updated, err)
			}
			if updated, err := repository.SetAdminGenerationMiningPolicy(ctx, userID, pause); err != nil || updated {
				t.Fatalf("regular user admin mining updated=%v err=%v", updated, err)
			}
		}
	}
	for _, priority := range []bool{true, false} {
		if updated, err := repository.SetAdminQueuePriority(ctx, adminID, priority); err != nil || !updated {
			t.Fatalf("admin priority: %v %v", updated, err)
		}
		for _, pause := range []bool{true, false} {
			if updated, err := repository.SetAdminGenerationMiningPolicy(ctx, adminID, pause); err != nil || !updated {
				t.Fatalf("admin mining: %v %v", updated, err)
			}
			user, err := repository.UserByID(ctx, adminID)
			if err != nil {
				t.Fatal(err)
			}
			assertPolicy(user, priority, pause)
		}
	}

	t.Run("training priority and aging ignore mining policy", func(t *testing.T) {
		var expected []int64
		for index, policy := range []struct {
			priority, pause bool
			age             time.Duration
		}{{false, true, 0}, {true, false, 0}, {false, false, domain.GPUPriorityHeadStart + time.Minute}} {
			var userID int64
			name := fmt.Sprintf("training-policy-%d", index)
			if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,queue_priority,pause_mining_for_quick_generation) VALUES($1,'hash','user',$2,$3) RETURNING id`, name, policy.priority, policy.pause).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			job, err := repository.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{PublicID: name, RequestID: name, UserID: userID, ProfileID: "test", Family: "krea2", BaseModel: "test.safetensors", Name: name, OutputName: name, TriggerWord: "test", ConceptType: "character", Preset: "quick", Resolution: 768, MaxTrainSteps: 800, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.0001, Seed: 42, SampleCount: 5, DatasetBytes: 1024, DatasetPath: "/test/dataset.zip"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `UPDATE lora_training_jobs SET created_at=now()-($2::bigint*interval '1 second') WHERE id=$1`, job.ID, int64(policy.age.Seconds())); err != nil {
				t.Fatal(err)
			}
			expected = append(expected, job.ID)
		}
		for _, id := range []int64{expected[2], expected[1], expected[0]} {
			job, err := repository.ClaimNextLoraTrainingJob(ctx)
			if err != nil || job.ID != id {
				t.Fatalf("claim id=%d want=%d err=%v", job.ID, id, err)
			}
			if _, err := repository.ClaimNextLoraTrainingJob(ctx); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("active task preempted: %v", err)
			}
			if err := repository.UpdateLoraTrainingJob(ctx, job.ID, store.UpdateLoraTrainingJobParams{State: domain.LoraTrainingCompleted}); err != nil {
				t.Fatal(err)
			}
		}
	})
}
