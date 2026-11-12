package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestLoraRepeatAtomicReceiptAndRecoveryIntegration(t *testing.T) {
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
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	resetIntegrationDatabase(t, db)
	repo := store.New(db)
	var owner int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,can_train_image_lora) VALUES('repeat-owner','disabled','user',true) RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	dataset, err := repo.CreateLoraDataset(ctx, owner, "repeat-dataset-0001", "Dataset", []byte("encrypted-manifest"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.CreateLoraDatasetSnapshot(ctx, owner, dataset.ID, dataset.Revision, "repeat-snapshot-0001", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	source, err := repo.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{PublicID: "repeat-source-0001", RequestID: "repeat-source-0001", UserID: owner,
		ProfileID: "krea-profile", Family: "krea2", BaseModel: "base.safetensors", Name: "Source", OutputName: "source", TriggerWord: "fixture", ConceptType: "object", Preset: "balanced",
		Resolution: 768, MaxTrainSteps: 1234, NetworkDim: 16, NetworkAlpha: 8, LearningRate: 0.00012, Seed: 1<<40 + 19, SampleCount: 5,
		DatasetBytes: 100, DatasetPath: "original.zip", DatasetSnapshotID: snapshot.ID, DatasetSnapshotHash: snapshot.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateLoraTrainingJob(ctx, source.ID, store.UpdateLoraTrainingJobParams{State: domain.LoraTrainingFailed, Stage: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	source, err = repo.LoraTrainingJobByPublicID(ctx, source.PublicID, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := domain.NewLoraTrainingRepeatConstraint(source, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	params := domain.CreateLoraTrainingRepeatParams{SourceJobID: source.PublicID, SourceSHA256: domain.LoraTrainingRepeatSourceHash(source), Constraint: c,
		Job: domain.LoraTrainingRepeatJob(source, c)}
	params.Job.PublicID, params.Job.UserID, params.Job.RequestID = "repeat-child-0001", owner, "repeat-request-0001"
	params.Job.DatasetPath, params.Job.DatasetBytes = "repeat.zip", 100
	for name, mutate := range map[string]func(*domain.CreateLoraTrainingRepeatParams){
		"snapshot":      func(p *domain.CreateLoraTrainingRepeatParams) { p.Job.DatasetSnapshotHash = strings.Repeat("c", 64) },
		"numeric":       func(p *domain.CreateLoraTrainingRepeatParams) { p.Job.MaxTrainSteps++ },
		"source digest": func(p *domain.CreateLoraTrainingRepeatParams) { p.SourceSHA256 = strings.Repeat("d", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := params
			mutate(&changed)
			if _, _, err := repo.CreateLoraTrainingRepeat(ctx, changed); !errors.Is(err, store.ErrLoraTrainingRepeatSource) {
				t.Fatal(err)
			}
		})
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM lora_training_jobs`).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed request created a job", count, err)
	}
	var wg sync.WaitGroup
	type outcome struct {
		receipt domain.LoraTrainingRepeatReceipt
		created bool
		err     error
	}
	results := make(chan outcome, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := params
			p.Job.PublicID = fmt.Sprintf("repeat-child-%04d", i)
			p.Job.DatasetPath = fmt.Sprintf("repeat-%d.zip", i)
			r, created, err := repo.CreateLoraTrainingRepeat(ctx, p)
			results <- outcome{r, created, err}
		}(i)
	}
	wg.Wait()
	close(results)
	createdCount := 0
	var saved domain.LoraTrainingRepeatReceipt
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if saved.JobPublicID != "" && saved.JobPublicID != result.receipt.JobPublicID {
			t.Fatal("retry created another child")
		}
		saved = result.receipt
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatal("non-atomic repeat", createdCount)
	}
	child, err := repo.LoraTrainingJobByPublicID(ctx, saved.JobPublicID, owner, false)
	if err != nil || child.Seed != 19 || child.MaxTrainSteps != 1234 || child.DatasetSnapshotID != snapshot.ID || child.LearningRate != source.LearningRate {
		t.Fatalf("wrong input %+v %v", child, err)
	}
	changed := params
	changed.Constraint = c.Clone()
	changed.Constraint.ProfileSHA256 = strings.Repeat("e", 64)
	if _, _, err := repo.CreateLoraTrainingRepeat(ctx, changed); !errors.Is(err, store.ErrLoraTrainingRepeatConflict) {
		t.Fatal("request reuse not rejected", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET disabled=true WHERE id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.CreateLoraTrainingRepeat(ctx, params); !errors.Is(err, store.ErrLoraTrainingForbidden) {
		t.Fatal("disabled replay", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET disabled=false WHERE id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM lora_training_jobs WHERE id IN ($1,$2)`, source.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	recovered, created, err := repo.CreateLoraTrainingRepeat(ctx, params)
	if err != nil || created || recovered.JobID != nil || recovered.SourceJobID != nil || recovered.JobPublicID != child.PublicID {
		t.Fatalf("deleted receipt lost %+v %v", recovered, err)
	}
	params.Job.RequestID = "repeat-new-request-01"
	if _, _, err := repo.CreateLoraTrainingRepeat(ctx, params); !errors.Is(err, store.ErrLoraTrainingRepeatSource) {
		t.Fatal("deleted source accepted", err)
	}
}
