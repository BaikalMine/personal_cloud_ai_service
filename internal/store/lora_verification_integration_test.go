package store_test

import (
	"context"
	"database/sql"
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
	"github.com/lib/pq"
)

func TestLoraVerificationAtomicPersistenceIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var owner, other int64
	for name, id := range map[string]*int64{"verification-owner": &owner, "verification-other": &other} {
		if err := db.QueryRowContext(ctx, `INSERT INTO users(username,password_hash,role,can_use_quick_generation,can_train_image_lora,can_use_advanced_generation_settings,can_generate_text_to_image,generation_daily_limit,generation_total_limit)
  VALUES($1,'disabled','user',true,true,true,true,100,100) RETURNING id`, name).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	repo := store.New(db)
	source, err := repo.CreateLoraTrainingJob(ctx, domain.CreateLoraTrainingJobParams{PublicID: "verification-source-0001", RequestID: "verification-source-0001", UserID: owner, ProfileID: "profile", Family: "krea2", BaseModel: "base.safetensors", Name: "Source training", OutputName: "adapter", TriggerWord: "fixture", ConceptType: "object", Preset: "quick", Resolution: 512, MaxTrainSteps: 100, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.001, Seed: 1, SampleCount: 5, DatasetBytes: 100, DatasetPath: "private-fixture-path"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := &domain.LoraTrainingProvenance{Version: 1, GatewayJobID: source.PublicID, CapturedAt: now.Add(-time.Minute), VerifiedAt: &now,
		Profile:        domain.LoraExecutionProfile{ID: source.ProfileID, Family: source.Family, BaseModel: source.BaseModel},
		Parameters:     domain.LoraExecutionParameters{Resolution: 512, MaxSteps: 100, NetworkDim: 16, NetworkAlpha: 16, LearningRate: 0.001, Seed: 1, SampleCount: 5},
		Tuner:          domain.LoraTunerIdentity{SourceScope: "python-and-dependencies-v1", SourceSHA256: strings.Repeat("a", 64)},
		DatasetArchive: domain.LoraFileIdentity{Name: "dataset.zip", Bytes: 100, SHA256: strings.Repeat("b", 64)},
		Artifact:       &domain.LoraFileIdentity{Name: "adapter.safetensors", Bytes: 100, SHA256: strings.Repeat("d", 64)}}
	p.ProfileSHA256 = p.Profile.Hash()
	for _, role := range []string{"base_source", "base_execution", "vae", "text_encoder"} {
		p.Models = append(p.Models, domain.LoraModelIdentity{Role: role, LoraFileIdentity: domain.LoraFileIdentity{Name: role + ".safetensors", Bytes: 10, SHA256: strings.Repeat("c", 64)}})
	}
	if err := repo.UpdateLoraTrainingJob(ctx, source.ID, store.UpdateLoraTrainingJobParams{State: domain.LoraTrainingCompleted, Provenance: p, ArtifactName: p.Artifact.Name, ArtifactBytes: p.Artifact.Bytes}); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE lora_training_jobs SET agent_job_id='fixture-agent-0001' WHERE id=$1`, source.ID)
	plan := domain.LoraVerificationPlan{Version: 1, Cases: []domain.LoraVerificationCase{{Prompt: "private prompt one", Seed: "42"}, {Prompt: "private prompt two", Seed: "43"}}, Weights: []string{"0.5", "1"}}
	members, err := plan.Members()
	if err != nil {
		t.Fatal(err)
	}
	paramsFor := func(number int) domain.CreateGenerationBatchParams {
		prefix := fmt.Sprintf("verification-%04d", number)
		params := domain.CreateGenerationBatchParams{PublicID: prefix + "-batch", UserID: owner, UsernameSnapshot: "verification-owner", RequestID: prefix + "-request", TemplateID: "text-to-image", WorkflowID: "photoflow-krea2", ModelName: "Krea2/base.safetensors", Mode: domain.GenerationBatchLoraVerification, MaxParallel: 1,
			LoraVerification: &domain.CreateLoraVerificationParams{TrainingJobID: source.ID, TrainingPublicID: source.PublicID, Family: source.Family, ProfileSHA256: p.ProfileSHA256, Artifact: *p.Artifact, ModelID: "krea2:fixture", RequestSHA256: strings.Repeat("e", 64), PlanCipher: []byte("opaque encrypted plan fixture"), Members: append([]domain.LoraVerificationMemberSpec(nil), members...)}}
		for i, member := range members {
			params.Jobs = append(params.Jobs, domain.CreateGenerationBatchJobParams{PublicID: fmt.Sprintf("%s-child-%02d", prefix, i), RequestID: fmt.Sprintf("%s-request-%02d", prefix, i), CorrelationID: "verification-trace-0001", Position: member.Position, ExperimentValue: member.Weight,
				Prepared: domain.PreparedGenerationJob{TemplateID: params.TemplateID, WorkflowID: params.WorkflowID, ModelName: params.ModelName, Seed: member.Seed, PayloadCipher: []byte("opaque generation fixture"), Dependencies: []string{"comfyui"}}})
		}
		return params
	}
	quota := func() int64 {
		t.Helper()
		var used int64
		if err := db.QueryRowContext(ctx, `SELECT generation_total_used FROM users WHERE id=$1`, owner).Scan(&used); err != nil {
			t.Fatal(err)
		}
		return used
	}
	params := paramsFor(1)
	batch, created, err := repo.CreateGenerationBatch(ctx, params)
	if err != nil || !created || batch.TotalCount != 6 || batch.Mode != domain.GenerationBatchLoraVerification || quota() != 6 {
		t.Fatal(batch, created, err, quota())
	}
	restored, err := store.New(db).LoraVerificationByBatch(ctx, owner, batch.PublicID)
	if err != nil || len(restored.Members) != 6 || restored.TrainingJobID == nil || restored.TrainingPublicID != source.PublicID || restored.TrainingProvenance == nil || restored.Artifact != *p.Artifact || string(restored.PlanCipher) != "opaque encrypted plan fixture" {
		t.Fatal(restored, err)
	}
	for i, member := range restored.Members {
		if member.LoraVerificationMemberSpec != members[i] || member.JobID == nil || member.State != "draft" || member.JobPublicID != params.Jobs[i].PublicID {
			t.Fatal(member)
		}
	}
	if _, err := repo.LoraVerificationByBatch(ctx, other, batch.PublicID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign comparison visible", err)
	}
	if ids, err := repo.ListLoraVerificationBatches(ctx, other, source.PublicID, 0, 30); err != nil || len(ids) != 0 {
		t.Fatal("foreign comparison listed", ids, err)
	}
	ids, err := repo.ListLoraVerificationBatches(ctx, owner, source.PublicID, 0, 30)
	if err != nil || len(ids) != 1 || ids[0] != batch.PublicID {
		t.Fatal(ids, err)
	}

	t.Run("idempotent retry and cross-kind conflict", func(t *testing.T) {
		retry := paramsFor(1)
		retry.PublicID = "unused-new-public-batch"
		got, created, err := repo.CreateGenerationBatch(ctx, retry)
		if err != nil || created || got.ID != batch.ID || quota() != 6 {
			t.Fatal(got, created, err)
		}
		retry.LoraVerification.RequestSHA256 = strings.Repeat("f", 64)
		if _, _, err := repo.CreateGenerationBatch(ctx, retry); !errors.Is(err, store.ErrGenerationBatchConflict) {
			t.Fatal("different request accepted", err)
		}
		retry = paramsFor(1)
		retry.Mode = domain.GenerationBatchSeeds
		retry.LoraVerification = nil
		if _, _, err := repo.CreateGenerationBatch(ctx, retry); !errors.Is(err, store.ErrGenerationBatchConflict) {
			t.Fatal("generic request recovered another kind", err)
		}
	})
	t.Run("child failure rolls back metadata and quota", func(t *testing.T) {
		bad := paramsFor(2)
		bad.Jobs[1].PublicID = bad.Jobs[0].PublicID
		if _, _, err := repo.CreateGenerationBatch(ctx, bad); err == nil {
			t.Fatal("duplicate child accepted")
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM generation_batches WHERE request_id=$1`, bad.RequestID).Scan(&count); err != nil || count != 0 || quota() != 6 {
			t.Fatal("partial transaction", count, err)
		}
		if _, err := repo.LoraVerificationByBatch(ctx, owner, bad.PublicID); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("partial verification", err)
		}
	})
	t.Run("invalid prepared layout never reserves quota", func(t *testing.T) {
		for name, mutate := range map[string]func(*domain.CreateGenerationBatchParams){
			"missing metadata": func(p *domain.CreateGenerationBatchParams) { p.LoraVerification = nil },
			"generic mode":     func(p *domain.CreateGenerationBatchParams) { p.Mode = domain.GenerationBatchSeeds },
			"video":            func(p *domain.CreateGenerationBatchParams) { p.TemplateID = "text-to-video" },
			"parallel":         func(p *domain.CreateGenerationBatchParams) { p.MaxParallel = 2 },
			"parameter":        func(p *domain.CreateGenerationBatchParams) { p.ParameterName = "steps" },
			"parameter values": func(p *domain.CreateGenerationBatchParams) { p.ParameterValues = []string{"1"} },
			"parent batch":     func(p *domain.CreateGenerationBatchParams) { p.ParentBatchID = &batch.ID },
			"source job":       func(p *domain.CreateGenerationBatchParams) { p.SourceJobID = restored.Members[0].JobID },
			"child parent":     func(p *domain.CreateGenerationBatchParams) { p.Jobs[0].ParentJobID = restored.Members[0].JobID },
			"child seed":       func(p *domain.CreateGenerationBatchParams) { p.Jobs[0].Prepared.Seed++ },
			"child weight":     func(p *domain.CreateGenerationBatchParams) { p.Jobs[1].ExperimentValue = "2" },
			"child template":   func(p *domain.CreateGenerationBatchParams) { p.Jobs[0].Prepared.TemplateID = "image-to-image" },
			"child workflow":   func(p *domain.CreateGenerationBatchParams) { p.Jobs[0].Prepared.WorkflowID = "other" },
			"child model":      func(p *domain.CreateGenerationBatchParams) { p.Jobs[0].Prepared.ModelName = "other" },
			"member count": func(p *domain.CreateGenerationBatchParams) {
				p.LoraVerification.Members = p.LoraVerification.Members[:3]
			},
		} {
			t.Run(name, func(t *testing.T) {
				bad := paramsFor(2)
				mutate(&bad)
				if _, _, err := repo.CreateGenerationBatch(ctx, bad); !errors.Is(err, domain.ErrLoraVerification) {
					t.Fatal("invalid layout accepted", err)
				}
				if quota() != 6 {
					t.Fatal("invalid layout charged quota")
				}
			})
		}
	})
	t.Run("database constraints reject malformed evidence and members", func(t *testing.T) {
		for _, query := range []string{
			`UPDATE lora_verifications SET artifact_sha256='invalid' WHERE batch_id=$1`,
			`UPDATE lora_verifications SET artifact_bytes=0 WHERE batch_id=$1`,
			`UPDATE lora_verifications SET plan_cipher=''::bytea WHERE batch_id=$1`,
			`UPDATE lora_verifications SET training_provenance='[]'::jsonb WHERE batch_id=$1`,
			`UPDATE lora_verification_members SET weight='0.03' WHERE batch_id=$1 AND position=2`,
			`UPDATE lora_verification_members SET weight='0' WHERE batch_id=$1 AND position=2`,
			`UPDATE lora_verification_members SET weight='1' WHERE batch_id=$1 AND position=1`,
			`UPDATE lora_verification_members SET seed=1125899906842625 WHERE batch_id=$1`,
			`UPDATE lora_verification_members SET case_index=10 WHERE batch_id=$1`,
			`UPDATE lora_verification_members SET variant_index=5 WHERE batch_id=$1`,
			`UPDATE generation_batches SET experiment_mode='unrecognized' WHERE id=$1`,
			`UPDATE generation_batches SET parameter_name='weight' WHERE id=$1`,
		} {
			_, err := db.ExecContext(ctx, query, batch.ID)
			var pg *pq.Error
			if !errors.As(err, &pg) || pg.Code != "23514" {
				t.Fatalf("missing check constraint: %s: %v", query, err)
			}
		}
	})
	t.Run("rights source and quota are checked at reservation", func(t *testing.T) {
		foreign := paramsFor(3)
		foreign.UserID = other
		if _, _, err := repo.CreateGenerationBatch(ctx, foreign); !errors.Is(err, store.ErrLoraVerificationSource) {
			t.Fatal("foreign source accepted", err)
		}
		exec(`UPDATE users SET can_train_image_lora=false WHERE id=$1`, owner)
		if _, _, err := repo.CreateGenerationBatch(ctx, paramsFor(3)); !errors.Is(err, store.ErrQuickGenerationForbidden) {
			t.Fatal("revoked access accepted", err)
		}
		exec(`UPDATE users SET can_train_image_lora=true,disabled=true WHERE id=$1`, owner)
		if _, _, err := repo.CreateGenerationBatch(ctx, paramsFor(3)); !errors.Is(err, store.ErrQuickGenerationForbidden) {
			t.Fatal("disabled account accepted", err)
		}
		exec(`UPDATE users SET disabled=false,account_expires_at=now()-interval '1 hour' WHERE id=$1`, owner)
		if _, _, err := repo.CreateGenerationBatch(ctx, paramsFor(3)); !errors.Is(err, store.ErrQuickGenerationForbidden) {
			t.Fatal("expired account accepted", err)
		}
		exec(`UPDATE users SET account_expires_at=NULL,generation_total_limit=6 WHERE id=$1`, owner)
		if _, _, err := repo.CreateGenerationBatch(ctx, paramsFor(3)); !errors.Is(err, store.ErrQuickGenerationTotalLimit) {
			t.Fatal("quota exceeded", err)
		}
		exec(`UPDATE users SET generation_total_limit=100 WHERE id=$1`, owner)
		changed := paramsFor(3)
		changed.LoraVerification.Artifact.SHA256 = strings.Repeat("f", 64)
		if _, _, err := repo.CreateGenerationBatch(ctx, changed); !errors.Is(err, store.ErrLoraVerificationSource) {
			t.Fatal("changed artifact accepted", err)
		}
		if quota() != 6 {
			t.Fatal("failed requests charged quota")
		}
	})
	t.Run("concurrent retries reserve once", func(t *testing.T) {
		var workers sync.WaitGroup
		for i := 0; i < 4; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for attempt := 0; attempt < 5; attempt++ {
					_, _, err := repo.CreateGenerationBatch(ctx, paramsFor(4))
					if err == nil {
						return
					}
					var pg *pq.Error
					if !errors.As(err, &pg) || (pg.Code != "40001" && pg.Code != "23505") {
						t.Error(err)
						return
					}
				}
				t.Error("retry budget exhausted")
			}()
		}
		workers.Wait()
		if quota() != 12 {
			t.Fatal("duplicate quota reservation", quota())
		}
		ids, err := repo.ListLoraVerificationBatches(ctx, owner, source.PublicID, batch.ID, 30)
		if err != nil || len(ids) != 0 {
			t.Fatal("cursor ordering", ids, err)
		}
	})
	t.Run("deletion keeps exact membership and source evidence", func(t *testing.T) {
		exec(`DELETE FROM generation_jobs WHERE id=$1`, *restored.Members[0].JobID)
		exec(`DELETE FROM lora_training_jobs WHERE id=$1`, source.ID)
		projection, err := repo.GenerationBatchByPublicID(ctx, owner, batch.PublicID)
		if err != nil || projection.MissingCount != 1 {
			t.Fatal("missing batch member not counted", projection, err)
		}
		got, err := store.New(db).LoraVerificationByBatch(ctx, owner, batch.PublicID)
		if err != nil || got.TrainingJobID != nil || got.TrainingProvenance == nil || len(got.Members) != 6 || got.Members[0].JobID != nil || got.Members[0].State != "missing" || got.Members[0].JobPublicID != params.Jobs[0].PublicID {
			t.Fatal("tombstone or evidence lost", got, err)
		}
		retry, created, err := repo.CreateGenerationBatch(ctx, paramsFor(1))
		if err != nil || created || retry.ID != batch.ID || quota() != 12 {
			t.Fatal("committed request lost after source deletion", retry, created, err)
		}
	})
	t.Run("incomplete membership is not a valid comparison", func(t *testing.T) {
		exec(`DELETE FROM lora_verification_members WHERE batch_id=$1 AND position=2`, batch.ID)
		if _, err := repo.LoraVerificationByBatch(ctx, owner, batch.PublicID); !errors.Is(err, domain.ErrLoraVerification) {
			t.Fatal("incomplete comparison accepted", err)
		}
	})
}
