package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ai-access-gateway/internal/database"
	"ai-access-gateway/internal/domain"
	"ai-access-gateway/internal/store"
)

func TestLoraProvenancePersistenceIntegration(t *testing.T) {
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
	var owner int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role) VALUES('passport-owner','disabled','user') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	repo := store.New(db)
	job, err := repo.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{
		PublicID: "passport-job-0001", RequestID: "passport-job-0001", UserID: owner, ProfileID: "passport-profile",
		Family: "krea2", BaseModel: "base.safetensors", Name: "Passport", OutputName: "passport_output",
		TriggerWord: "fixture", ConceptType: "object", Preset: "quick", Resolution: 512, MaxTrainSteps: 100,
		NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.001, Seed: (1 << 32) + 14, SampleCount: 5,
		DatasetBytes: 100, DatasetPath: "private-server-path"})
	if err != nil || job.Provenance != nil {
		t.Fatal("legacy/default evidence", err)
	}
	p := &domain.LoraTrainingProvenance{Version: 1, GatewayJobID: job.PublicID, CapturedAt: time.Now().UTC(),
		Profile:        domain.LoraExecutionProfile{ID: job.ProfileID, Family: job.Family, BaseModel: job.BaseModel},
		Parameters:     domain.LoraExecutionParameters{Resolution: 512, MaxSteps: 100, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.001, Seed: 14, SampleCount: 5},
		Tuner:          domain.LoraTunerIdentity{SourceScope: "python-and-dependencies-v1", SourceSHA256: strings.Repeat("a", 64)},
		DatasetArchive: domain.LoraFileIdentity{Name: "dataset.zip", Bytes: 100, SHA256: strings.Repeat("b", 64)}}
	p.ProfileSHA256 = p.Profile.Hash()
	for _, role := range []string{"base_source", "base_execution", "vae", "text_encoder"} {
		p.Models = append(p.Models, domain.LoraModelIdentity{Role: role,
			LoraFileIdentity: domain.LoraFileIdentity{Name: role + ".safetensors", Bytes: 10, SHA256: strings.Repeat("c", 64)}})
	}
	update := store.UpdateLoraTrainingJobParams{State: domain.LoraTrainingRunning, Provenance: p}
	if err := repo.UpdateLoraTrainingJob(ctx, job.ID, update); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*domain.LoraTrainingProvenance){
		"different job": func(p *domain.LoraTrainingProvenance) { p.GatewayJobID = "another-job" },
		"changed seed":  func(p *domain.LoraTrainingProvenance) { p.Parameters.Seed++ },
		"changed model": func(p *domain.LoraTrainingProvenance) { p.Models[0].SHA256 = strings.Repeat("e", 64) },
		"changed profile": func(p *domain.LoraTrainingProvenance) {
			p.Profile.ID = "other-profile"
			p.ProfileSHA256 = p.Profile.Hash()
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := p.Clone()
			mutate(changed)
			update.Provenance = changed
			if err := repo.UpdateLoraTrainingJob(ctx, job.ID, update); !errors.Is(err, domain.ErrLoraProvenance) {
				t.Fatal("conflicting execution accepted", err)
			}
		})
	}
	completed := p.Clone()
	verified := completed.CapturedAt.Add(time.Minute)
	completed.VerifiedAt = &verified
	completed.Artifact = &domain.LoraFileIdentity{Name: "passport_output.safetensors", Bytes: 20, SHA256: strings.Repeat("d", 64)}
	update = store.UpdateLoraTrainingJobParams{State: domain.LoraTrainingCompleted, Provenance: completed,
		ArtifactName: completed.Artifact.Name, ArtifactBytes: completed.Artifact.Bytes}
	if err := repo.UpdateLoraTrainingJob(ctx, job.ID, update); err != nil {
		t.Fatal(err)
	}
	update.Provenance = nil
	if err := repo.UpdateLoraTrainingJob(ctx, job.ID, update); err != nil {
		t.Fatal("old-agent update", err)
	}
	stored, err := repo.LoraTrainingJobByPublicID(ctx, job.PublicID, owner, false)
	if err != nil || stored.Provenance == nil || stored.Provenance.Artifact == nil || stored.Provenance.Artifact.SHA256 != completed.Artifact.SHA256 {
		t.Fatal("database roundtrip lost evidence", err)
	}
	if stored.Seed != (1<<32)+14 || stored.Provenance.Parameters.Seed != 14 {
		t.Fatal("requested and actual normalized seeds were conflated")
	}
	if _, err := repo.LoraTrainingJobByPublicID(ctx, job.PublicID, owner+1, false); err == nil {
		t.Fatal("foreign passport became readable")
	}
	update.Provenance = completed.Clone()
	update.ArtifactName = "wrong.safetensors"
	if err := repo.UpdateLoraTrainingJob(ctx, job.ID, update); !errors.Is(err, domain.ErrLoraProvenance) {
		t.Fatal("artifact mismatch accepted", err)
	}
}
